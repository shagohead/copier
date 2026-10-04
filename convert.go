package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

// Expression of some type.
type value struct {
	expr string
	typ  types.Type
}

// Resolved wrapper config.
type wrapperInfo struct {
	wrapper
	typ types.Type // Value type.
}

// Source field prepared for reading.
type source struct {
	guard   string // Copy only if this expression is true.
	valid   string // Optional expression of value non-emptiness.
	wrapped bool   // Validity defined by wrapper.
	deref   bool   // Value can be read only if `valid`.
	val     value
}

// Destination field prepared for writing.
type destination struct {
	marks   []string   // Statements marking value as set.
	valid   string     // Assignable validity expression.
	invalid bool       // Validity expression is negated: assign negated validity.
	elem    types.Type // Element type of pointer value.
	nilable bool       // Value may be reset to nil.
	val     value
}

// copyField writes statements copying src into dst.
func (g *fileGen) copyField(dst, src value) error {
	if v, ok, err := g.convertValue(dst.typ, src); err != nil || ok {
		if ok {
			fmt.Fprintf(&g.body, "%s = %s\n", dst.expr, v.expr)
		}
		return err
	}
	in, err := g.source(src)
	if err != nil {
		return err
	}
	out, err := g.destination(dst)
	if err != nil {
		return err
	}
	assign, ok, err := g.assign(out, in.val)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cannot copy %s (%s) into %s (%s)",
			src.expr, g.typeString(src.typ), dst.expr, g.typeString(dst.typ))
	}

	if in.guard != "" {
		fmt.Fprintf(&g.body, "if %s {\n", in.guard)
	}
	for _, s := range out.marks {
		fmt.Fprintln(&g.body, s)
	}
	if out.valid != "" {
		valid := "true"
		switch {
		case in.valid != "":
			valid = in.valid
		case isString(in.val.typ):
			// Only empty string is always an empty value,
			// unlike zero number or false boolean.
			valid = in.val.expr + ` != ""`
		}
		if out.invalid {
			valid = negate(valid)
		}
		fmt.Fprintf(&g.body, "%s = %s\n", out.valid, valid)
	}
	reset := out.nilable && in.valid != "" && in.wrapped
	if in.deref || reset {
		fmt.Fprintf(&g.body, "if %s {\n%s\n", in.valid, assign)
		if out.nilable {
			fmt.Fprintf(&g.body, "} else {\n%s = nil\n", out.val.expr)
		}
		g.body.WriteString("}\n")
	} else {
		fmt.Fprintln(&g.body, assign)
	}
	if in.guard != "" {
		g.body.WriteString("}\n")
	}
	return nil
}

// copySet writes statement setting boolean dst to true, if src is set:
// wrapper CopyIf expression is true, or slice or map is not nil.
func (g *fileGen) copySet(dst, src value) error {
	if b, ok := dst.typ.Underlying().(*types.Basic); !ok || b.Info()&types.IsBoolean == 0 {
		return fmt.Errorf("destination %s (%s) is not boolean", dst.expr, g.typeString(dst.typ))
	}
	w, err := g.wrapperOf(src.typ)
	if err != nil {
		return err
	}
	var expr string
	switch {
	case w != nil && w.CopyIf != "":
		if expr, err = render(w.CopyIf, src.expr); err != nil {
			return fmt.Errorf("copyif: %v", err)
		}
		// Expression is typed bool, which is not assignable to bool alias.
		if !types.Identical(dst.typ, types.Typ[types.Bool]) {
			expr = g.conversion(dst.typ, expr)
		}
	case w != nil:
		return fmt.Errorf("source %s (%s) wrapper has no copyif", src.expr, g.typeString(src.typ))
	default:
		switch src.typ.Underlying().(type) {
		case *types.Slice, *types.Map:
			expr = src.expr + " != nil"
		default:
			return fmt.Errorf("source %s (%s) is neither wrapper with copyif, slice or map",
				src.expr, g.typeString(src.typ))
		}
	}
	fmt.Fprintf(&g.body, "%s = %s\n", dst.expr, expr)
	return nil
}

// assign returns statement assigning val into destination.
func (g *fileGen) assign(out destination, val value) (string, bool, error) {
	if v, ok, err := g.convertValue(out.val.typ, val); err != nil || ok {
		return out.val.expr + " = " + v.expr, ok, err
	}
	if out.elem == nil {
		return "", false, nil
	}
	v, ok, err := g.convertValue(out.elem, val)
	if err != nil || !ok {
		return "", false, err
	}
	// Type of new(expr) is inferred from expr, which may be only assignable
	// to the element type (like unnamed map into named map type).
	if !types.Identical(out.elem, v.typ) {
		v.expr = g.conversion(out.elem, v.expr)
	}
	if g.newExpr() {
		return fmt.Sprintf("%s = new(%s)", out.val.expr, v.expr), true, nil
	}
	return fmt.Sprintf("{\nv := %s\n%s = &v\n}", v.expr, out.val.expr), true, nil
}

func (g *fileGen) source(src value) (source, error) {
	w, err := g.wrapperOf(src.typ)
	if err != nil {
		return source{}, err
	}
	if w != nil {
		in := source{wrapped: true}
		if w.CopyIf != "" {
			if in.guard, err = render(w.CopyIf, src.expr); err != nil {
				return in, err
			}
		}
		if w.Valid != "" {
			if in.valid, err = render(w.Valid, src.expr); err != nil {
				return in, err
			}
		}
		val, err := render(w.Value, src.expr)
		in.val = value{val, w.typ}
		return in, err
	}
	switch t := src.typ.Underlying().(type) {
	case *types.Pointer:
		return source{
			valid: src.expr + " != nil",
			deref: true,
			val:   value{"*" + src.expr, t.Elem()},
		}, nil
	case *types.Slice, *types.Map, *types.Chan:
		return source{valid: src.expr + " != nil", val: src}, nil
	}
	return source{val: src}, nil
}

func (g *fileGen) destination(dst value) (destination, error) {
	w, err := g.wrapperOf(dst.typ)
	if err != nil {
		return destination{}, err
	}
	if w != nil {
		var out destination
		if w.CopyIf != "" {
			lhs, neg, err := assignable(w.CopyIf, dst.expr)
			if err != nil {
				return out, fmt.Errorf("copyif: %v", err)
			}
			out.marks = append(out.marks, fmt.Sprintf("%s = %t", lhs, !neg))
		}
		if w.Valid != "" {
			if out.valid, out.invalid, err = assignable(w.Valid, dst.expr); err != nil {
				return out, fmt.Errorf("valid: %v", err)
			}
		}
		val, err := render(w.Value, dst.expr)
		out.val = value{val, w.typ}
		return out, err
	}
	switch t := dst.typ.Underlying().(type) {
	case *types.Pointer:
		return destination{elem: t.Elem(), nilable: true, val: dst}, nil
	case *types.Slice, *types.Map, *types.Chan:
		return destination{nilable: true, val: dst}, nil
	}
	return destination{val: dst}, nil
}

// convertValue returns src converted into dst type,
// directly or through one of the source type getters.
func (g *fileGen) convertValue(dst types.Type, src value) (value, bool, error) {
	if v, ok := g.convert(dst, src); ok {
		return v, true, nil
	}
	getters, err := g.gettersOf(src.typ)
	if err != nil {
		return value{}, false, err
	}
	// Getters of exactly matching types are preferred over converted ones.
	for _, exact := range []bool{true, false} {
		for _, gt := range getters {
			if exact != types.Identical(dst, gt.typ) {
				continue
			}
			expr, err := render(gt.expr, src.expr)
			if err != nil {
				return value{}, false, fmt.Errorf("getter %s: %v", gt.expr, err)
			}
			if v, ok := g.convert(dst, value{expr, gt.typ}); ok {
				return v, true, nil
			}
		}
	}
	return value{}, false, nil
}

// convert returns src converted into dst type.
func (g *fileGen) convert(dst types.Type, src value) (value, bool) {
	if types.AssignableTo(src.typ, dst) {
		return src, true
	}
	// Building conversion registers imports, so it is made only when used.
	converted := func() value { return value{g.conversion(dst, src.expr), dst} }
	du, su := dst.Underlying(), src.typ.Underlying()
	if db, ok := du.(*types.Basic); ok {
		if sb, ok := su.(*types.Basic); ok && kind(db) != 0 && kind(db) == kind(sb) {
			return converted(), true
		}
		return value{}, false
	}
	if types.Identical(du, su) {
		return converted(), true
	}
	switch d := du.(type) {
	case *types.Slice:
		if s, ok := su.(*types.Array); ok && types.Identical(d.Elem(), s.Elem()) {
			// Clone avoids aliasing of source array memory.
			v := value{g.importName("slices", "slices") + ".Clone(" + src.expr + "[:])", types.NewSlice(s.Elem())}
			if !types.AssignableTo(v.typ, dst) {
				v = value{g.conversion(dst, v.expr), dst}
			}
			return v, true
		}
	case *types.Array:
		if s, ok := su.(*types.Slice); ok && types.Identical(d.Elem(), s.Elem()) {
			return converted(), true
		}
	}
	return value{}, false
}

// conversion returns conversion expression of x into type t.
func (g *fileGen) conversion(t types.Type, x string) string {
	s := g.typeString(t)
	if strings.HasPrefix(s, "*") || strings.HasPrefix(s, "<-") || strings.HasPrefix(s, "func") {
		s = "(" + s + ")"
	}
	return s + "(" + x + ")"
}

func isString(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&types.IsString != 0
}

// kind returns category of basic type, which values are convertible
// to each other without changing meaning of the value.
func kind(b *types.Basic) int {
	switch info := b.Info(); {
	case info&types.IsUntyped != 0:
		return 0
	case info&types.IsBoolean != 0:
		return 1
	case info&types.IsInteger != 0:
		return 2
	case info&types.IsFloat != 0:
		return 3
	case info&types.IsComplex != 0:
		return 4
	case info&types.IsString != 0:
		return 5
	}
	return 0
}

var selectorChain = regexp.MustCompile(`^(\.[\pL_][\pL\pN_]*)+$`)

// typeKeys returns config keys which may refer to type t:
// full path ("net/url.URL"), qualified name ("url.URL")
// and name of the generated package type ("Name").
func (g *fileGen) typeKeys(t types.Type) []string {
	switch t := t.(type) {
	case *types.Basic:
		return []string{t.Name()}
	case *types.Named:
		obj := t.Obj()
		if obj.Pkg() == nil {
			return []string{obj.Name()}
		}
		keys := []string{obj.Pkg().Path() + "." + obj.Name()}
		if obj.Pkg().Name() != obj.Pkg().Path() {
			keys = append(keys, obj.Pkg().Name()+"."+obj.Name())
		}
		if obj.Pkg() == g.pkg.Types {
			keys = append(keys, obj.Name())
		}
		return keys
	}
	return nil
}

// configOf returns config value of type t from m.
func configOf[T any](g *fileGen, m map[string]T, t types.Type) (string, T, bool) {
	for _, key := range g.typeKeys(t) {
		if v, ok := m[key]; ok {
			return key, v, true
		}
	}
	var zero T
	return "", zero, false
}

// wrapperOf returns wrapper config of type t or nil.
func (g *fileGen) wrapperOf(t types.Type) (*wrapperInfo, error) {
	key, w, ok := configOf(g, g.cfg.Wrappers, t)
	if !ok {
		return nil, nil
	}
	if info, ok := g.wrappers[key]; ok {
		return info, nil
	}
	info, err := g.resolveWrapper(t, w)
	if err != nil {
		return nil, fmt.Errorf("wrapper %s: %v", key, err)
	}
	g.wrappers[key] = info
	return info, nil
}

// Getter of alternative value type.
type getter struct {
	expr string
	typ  types.Type
}

// gettersOf returns getters of type t sorted by their type names.
func (g *fileGen) gettersOf(t types.Type) ([]getter, error) {
	key, m, ok := configOf(g, g.cfg.Getters, t)
	if !ok {
		return nil, nil
	}
	if getters, ok := g.getters[key]; ok {
		return getters, nil
	}
	getters := make([]getter, 0, len(m))
	for _, name := range slices.Sorted(maps.Keys(m)) {
		typ, err := g.parseType(name)
		if err != nil {
			return nil, fmt.Errorf("getters %s: %v", key, err)
		}
		getters = append(getters, getter{m[name], typ})
	}
	g.getters[key] = getters
	return getters, nil
}

func (g *fileGen) resolveWrapper(t types.Type, w wrapper) (*wrapperInfo, error) {
	if w.Value == "" {
		return nil, fmt.Errorf("value is not defined")
	}
	info := &wrapperInfo{wrapper: w}
	var err error
	if info.typ, err = g.selectorType(t, w.Value); err != nil {
		return nil, fmt.Errorf("value: %v", err)
	}
	for option, expr := range map[string]string{"valid": w.Valid, "copyif": w.CopyIf} {
		if _, err := g.selectorType(t, expr); err != nil {
			return nil, fmt.Errorf("%s: %v", option, err)
		}
	}
	if w.Type != "" {
		typ, err := g.parseType(w.Type)
		if err != nil {
			return nil, fmt.Errorf("type: %v", err)
		}
		if info.typ != nil && !types.Identical(typ, info.typ) {
			return nil, fmt.Errorf("type %s does not match value type %s", w.Type, g.typeString(info.typ))
		}
		info.typ = typ
	}
	if info.typ == nil {
		return nil, fmt.Errorf("type is required for value %q", w.Value)
	}
	return info, nil
}

// selectorType returns type of fields selector shorthand (like ".A.B")
// applied to t, or nil if expr is not a fields selector.
func (g *fileGen) selectorType(t types.Type, expr string) (types.Type, error) {
	if !selectorChain.MatchString(expr) {
		return nil, nil
	}
	for name := range strings.SplitSeq(expr[1:], ".") {
		obj, _, _ := types.LookupFieldOrMethod(t, true, g.pkg.Types, name)
		f, ok := obj.(*types.Var)
		if !ok {
			return nil, fmt.Errorf("field %s not found in %s", name, g.typeString(t))
		}
		t = f.Type()
	}
	return t, nil
}

// parseType parses type string, like "*[]path/to/pkg.Name".
func (g *fileGen) parseType(s string) (types.Type, error) {
	switch {
	case strings.HasPrefix(s, "*"):
		t, err := g.parseType(s[1:])
		if err != nil {
			return nil, err
		}
		return types.NewPointer(t), nil
	case strings.HasPrefix(s, "[]"):
		t, err := g.parseType(s[2:])
		if err != nil {
			return nil, err
		}
		return types.NewSlice(t), nil
	}
	pkgPath, name := splitTypePath(s)
	var obj types.Object
	if pkgPath == "" {
		if obj = types.Universe.Lookup(name); obj == nil {
			obj = g.pkg.Types.Scope().Lookup(name)
		}
	} else if p := g.lookupPkg(pkgPath); p != nil {
		obj = p.Scope().Lookup(name)
	} else {
		return nil, fmt.Errorf("package %s not loaded", pkgPath)
	}
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("type %s not found", s)
	}
	return tn.Type(), nil
}

// lookupPkg returns loaded package by path or by unique name.
func (g *fileGen) lookupPkg(pathOrName string) *types.Package {
	if p := g.index[pathOrName]; p != nil {
		return p
	}
	var found *types.Package
	for _, p := range g.index {
		if p.Name() == pathOrName {
			if found != nil {
				return nil
			}
			found = p
		}
	}
	return found
}

// render executes expression template with x as data.
// Expression starting with dot is a shorthand for selector of x.
func render(tmpl, x string) (string, error) {
	if strings.HasPrefix(x, "*") {
		x = "(" + x + ")"
	}
	if strings.HasPrefix(tmpl, ".") {
		return x + tmpl, nil
	}
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, x); err != nil {
		return "", err
	}
	return b.String(), nil
}

// assignable renders boolean expression template and returns its assignable operand.
// Negated expression (like "!{{.}}.Null") returns its operand with neg flag.
func assignable(tmpl, x string) (lhs string, neg bool, err error) {
	s, err := render(tmpl, x)
	if err != nil {
		return "", false, err
	}
	e, err := parser.ParseExpr(s)
	if err != nil {
		return "", false, err
	}
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.NOT {
		e, neg = unparen(u.X), true
	}
	switch e.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.StarExpr:
		return exprString(s, e), neg, nil
	}
	return "", false, fmt.Errorf("expression %q is not assignable", s)
}

// negate returns negation of boolean expression.
func negate(s string) string {
	e, err := parser.ParseExpr(s)
	if err != nil {
		return "!(" + s + ")"
	}
	switch e := e.(type) {
	case *ast.Ident:
		switch e.Name {
		case "true":
			return "false"
		case "false":
			return "true"
		}
		return "!" + s
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return exprString(s, unparen(e.X))
		}
	case *ast.BinaryExpr:
		op := map[token.Token]string{token.EQL: "!=", token.NEQ: "=="}[e.Op]
		if op != "" {
			return exprString(s, e.X) + " " + op + " " + exprString(s, e.Y)
		}
	case *ast.SelectorExpr, *ast.CallExpr, *ast.IndexExpr, *ast.ParenExpr:
		return "!" + s
	}
	return "!(" + s + ")"
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// exprString returns source of e parsed by parser.ParseExpr from src.
func exprString(src string, e ast.Expr) string {
	return src[e.Pos()-1 : e.End()-1]
}
