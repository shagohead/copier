package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"regexp"
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
	guard string // Copy only if this expression is true.
	// FIXME: «empty means always valid»?
	valid   string // Expression of value non-emptiness; empty means always valid.
	wrapped bool   // Validity defined by wrapper.
	deref   bool   // Value can be read only if `valid`.
	val     value
}

// Destination field prepared for writing.
type destination struct {
	// FIXME: Возможно такое не нужно для получателя значения. А только для источника.
	// Потому как CopyIf нужен только для проверки выполнять ли копирование из.
	// Пустое/null значение же уже определяется параметром Valid.
	// Еще один флаг заполненного значения для destination кажется не нужен.
	marks   []string   // Statements marking value as set.
	valid   string     // Assignable validity expression.
	invalid bool       // Validity expression is negated: assign negated validity.
	elem    types.Type // Element type of pointer value.
	nilable bool       // Value may be reset to nil.
	val     value
}

// copyField writes statements copying src into dst.
func (g *fileGen) copyField(dst, src value) error {
	if expr, ok := g.convert(dst.typ, src.typ, src.expr); ok {
		fmt.Fprintf(&g.body, "%s = %s\n", dst.expr, expr)
		return nil
	}
	in, err := g.source(src)
	if err != nil {
		return err
	}
	out, err := g.destination(dst)
	if err != nil {
		return err
	}
	assign, err := g.assign(out, in.val)
	if err != nil {
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
		if in.valid != "" {
			valid = in.valid
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

// assign returns statement assigning val into destination.
func (g *fileGen) assign(out destination, val value) (string, error) {
	if expr, ok := g.convert(out.val.typ, val.typ, val.expr); ok {
		return out.val.expr + " = " + expr, nil
	}
	if out.elem == nil {
		return "", fmt.Errorf("not convertible")
	}
	expr, ok := g.convert(out.elem, val.typ, val.expr)
	if !ok {
		return "", fmt.Errorf("not convertible")
	}
	// FIXME: Выглядит лишним после вызова convert четырьмя строками выше.
	if expr == val.expr && !types.Identical(out.elem, val.typ) {
		expr = g.conversion(out.elem, expr)
	}
	// FIXME: В тестах поймать следующие кейсы.
	if g.newExpr() {
		return fmt.Sprintf("%s = new(%s)", out.val.expr, expr), nil
	}
	return fmt.Sprintf("{\nv := %s\n%s = &v\n}", expr, out.val.expr), nil
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

// convert returns expression of src converted into dst type.
func (g *fileGen) convert(dst, src types.Type, x string) (string, bool) {
	if types.AssignableTo(src, dst) {
		return x, true
	}
	du, su := dst.Underlying(), src.Underlying()
	if db, ok := du.(*types.Basic); ok {
		if sb, ok := su.(*types.Basic); ok && kind(db) != 0 && kind(db) == kind(sb) {
			return g.conversion(dst, x), true
		}
		return "", false
	}
	if types.Identical(du, su) {
		return g.conversion(dst, x), true
	}
	switch d := du.(type) {
	case *types.Slice:
		if s, ok := su.(*types.Array); ok && types.Identical(d.Elem(), s.Elem()) {
			// Clone avoids aliasing of source array memory.
			x = g.importName("slices", "slices") + ".Clone(" + x + "[:])"
			if !types.AssignableTo(types.NewSlice(s.Elem()), dst) {
				x = g.conversion(dst, x)
			}
			return x, true
		}
	case *types.Array:
		if s, ok := su.(*types.Slice); ok && types.Identical(d.Elem(), s.Elem()) {
			return g.conversion(dst, x), true
		}
	}
	return "", false
}

// conversion returns conversion expression of x into type t.
func (g *fileGen) conversion(t types.Type, x string) string {
	s := g.typeString(t)
	if strings.HasPrefix(s, "*") || strings.HasPrefix(s, "<-") || strings.HasPrefix(s, "func") {
		s = "(" + s + ")"
	}
	return s + "(" + x + ")"
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

// wrapperOf returns wrapper config of type t or nil.
func (g *fileGen) wrapperOf(t types.Type) (*wrapperInfo, error) {
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return nil, nil
	}
	key := named.Obj().Pkg().Path() + "." + named.Obj().Name()
	if w, ok := g.wrappers[key]; ok {
		return w, nil
	}
	w, ok := g.cfg.Wrappers[key]
	if !ok {
		return nil, nil
	}
	info, err := g.resolveWrapper(t, w)
	if err != nil {
		return nil, fmt.Errorf("wrapper %s: %v", key, err)
	}
	g.wrappers[key] = info
	return info, nil
}

func (g *fileGen) resolveWrapper(t types.Type, w wrapper) (*wrapperInfo, error) {
	if w.Getter != "" || w.Setter != "" {
		return nil, fmt.Errorf("getter and setter are not supported")
	}
	if w.Value == "" {
		return nil, fmt.Errorf("value is not defined")
	}
	info := &wrapperInfo{wrapper: w}
	if selectorChain.MatchString(w.Value) {
		info.typ = t
		for name := range strings.SplitSeq(w.Value[1:], ".") {
			obj, _, _ := types.LookupFieldOrMethod(info.typ, true, g.pkg.Types, name)
			f, ok := obj.(*types.Var)
			if !ok {
				return nil, fmt.Errorf("value: field %s not found in %s", name, g.typeString(info.typ))
			}
			info.typ = f.Type()
		}
	}
	// FIXME: Возможно это лишнее и достаточно автоопредленного типа.
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
	} else if p := g.index[pkgPath]; p != nil {
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

// render executes expression template with x as data.
// Expression starting with dot is a shorthand for selector of x.
func render(tmpl, x string) (string, error) {
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
