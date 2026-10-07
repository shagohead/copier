package main

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"go/types"
	"go/version"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"unicode"

	"golang.org/x/tools/go/packages"
)

// Name of generated file in target package.
const outputFile = "copier.go"

// Generated file content.
type outFile struct {
	path    string
	content []byte
}

// Package for which methods are generated.
type target struct {
	key     string // Key from config `packages`. Path relative to working dir.
	pattern string // Pattern for packages.Load.
	dir     string // Local package directory, if key is not package import path.
	methods ordered[[]method]
	pkg     *packages.Package
	output  string // Generated file path.
}

// generate loads packages defined in config and generates their files in memory.
//
// Config paths are relative to dir.
// dir is current dir in argument mode or config file location.
func generate(cfg *config, dir string) ([]outFile, error) {
	if len(cfg.Methods) > 0 && len(cfg.Packages) > 0 {
		return nil, fmt.Errorf("define either methods or packages, not both")
	}
	var targets []*target
	if len(cfg.Methods) > 0 {
		targets = append(targets, &target{key: "", methods: cfg.Methods})
	}
	for _, e := range cfg.Packages {
		targets = append(targets, &target{key: e.Key, methods: e.Value.Methods})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("empty methods definition")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var patterns []string
	addPattern := func(p string) {
		if !slices.Contains(patterns, p) {
			patterns = append(patterns, p)
		}
	}
	for _, t := range targets {
		t.pattern = t.key
		if t.key == "" || isDir(filepath.Join(dir, t.key)) {
			t.dir = realpath(filepath.Join(dir, t.key))
			t.pattern = "./" + filepath.ToSlash(filepath.Clean(t.key))
		}
		addPattern(t.pattern)
		for _, rcv := range t.methods {
			for _, m := range rcv.Value {
				if p, _ := splitTypePath(m.Argument); p != "" {
					addPattern(p)
				}
				// Packages of additional arguments are not loaded for performance,
				// see fileGen.paramType.
			}
		}
	}

	// Resolve target packages first, to replace previously generated
	// files with empty ones: they can be outdated and fail type checking.
	match := func(pkgs []*packages.Package) error {
		for _, t := range targets {
			t.pkg = nil
			for _, pkg := range pkgs {
				if t.dir != "" && realpath(pkg.Dir) == t.dir || t.dir == "" && pkg.PkgPath == t.pattern {
					t.pkg = pkg
					break
				}
			}
			if t.pkg == nil || t.pkg.Dir == "" {
				return fmt.Errorf("%s: package not found", t.pattern)
			}
			t.output = filepath.Join(t.pkg.Dir, outputFile)
		}
		return nil
	}

	// Load package files structure.
	pcfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles, Dir: dir}
	pkgs, err := packages.Load(pcfg, patterns...)
	if err != nil {
		return nil, err
	}
	if err := match(pkgs); err != nil {
		return nil, err
	}

	// And overlay already existing copier files content for package types analysys call.
	pcfg.Overlay = make(map[string][]byte)
	for _, t := range targets {
		if slices.Contains(t.pkg.GoFiles, t.output) || slices.Contains(t.pkg.CompiledGoFiles, t.output) {
			pcfg.Overlay[t.output] = []byte("package " + t.pkg.Name + "\n")
		}
	}

	pcfg.Mode = packages.NeedName | packages.NeedFiles | packages.NeedImports |
		packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule
	if pkgs, err = packages.Load(pcfg, patterns...); err != nil {
		return nil, err
	}
	if err := match(pkgs); err != nil {
		return nil, err
	}
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			return nil, fmt.Errorf("%s: %v", pkg.PkgPath, e)
		}
	}

	importNames := make(map[string]string, len(cfg.Imports))
	for name, pkgPath := range cfg.Imports {
		if !token.IsIdentifier(name) {
			return nil, fmt.Errorf("imports: invalid name %q", name)
		}
		if prev, ok := importNames[pkgPath]; ok {
			return nil, fmt.Errorf("imports: %s has names %s and %s", pkgPath, min(prev, name), max(prev, name))
		}
		importNames[pkgPath] = name
	}

	index := make(map[string]*types.Package)
	var walk func(p *types.Package)
	walk = func(p *types.Package) {
		if _, ok := index[p.Path()]; ok {
			return
		}
		index[p.Path()] = p
		for _, imp := range p.Imports() {
			walk(imp)
		}
	}
	for _, pkg := range pkgs {
		walk(pkg.Types)
	}

	files := make([]outFile, 0, len(targets))
	for _, t := range targets {
		g := &fileGen{
			cfg:      cfg,
			pkg:      t.pkg,
			index:    index,
			imports:  make(map[string]string),
			explicit: make(map[string]bool),
			names:    importNames,
			methods:  make(map[string]bool),
			wrappers: make(map[string]*wrapperInfo),
			getters:  make(map[string][]getter),
			reserved: map[string]bool{"d": true, "s": true},
		}
		// Imports should not be shadowed by arguments of any method.
		for _, rcv := range t.methods {
			for _, m := range rcv.Value {
				for _, param := range m.allParams() {
					if name, _, ok := splitParam(param); ok {
						g.reserved[name] = true
					}
				}
			}
		}
		for _, rcv := range t.methods {
			for _, m := range rcv.Value {
				if err := g.method(rcv.Key, m); err != nil {
					return nil, fmt.Errorf("%s: %s(%s): %v", t.pkg.PkgPath, rcv.Key, m.Argument, err)
				}
			}
		}
		content, err := g.content()
		if err != nil {
			return nil, fmt.Errorf("%s: %v", t.output, err)
		}
		files = append(files, outFile{path: t.output, content: content})
	}
	return files, nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// allParams returns additional arguments of method and field overrides.
func (m method) allParams() []string {
	params := slices.Clone(m.Params)
	for _, f := range m.Fields {
		params = append(params, f.Args...)
	}
	return params
}

// splitParam splits additional argument "name path/to/pkg.Type".
func splitParam(s string) (name, typ string, ok bool) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return "", "", false
	}
	return fields[0], fields[1], true
}

// splitTypePath splits "path/to/pkg.Name" into package path and type name.
func splitTypePath(s string) (pkgPath, name string) {
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return "", s
	}
	return s[:i], s[i+1:]
}

// Generator of single file.
type fileGen struct {
	cfg      *config
	pkg      *packages.Package
	index    map[string]*types.Package // Loaded packages by path.
	imports  map[string]string         // Import names by path.
	explicit map[string]bool           // Imports which need explicit name.
	names    map[string]string         // Configured import names by path.
	methods  map[string]bool           // Generated methods.
	wrappers map[string]*wrapperInfo
	getters  map[string][]getter
	reserved map[string]bool // Names which cannot be used for imports.
	body     bytes.Buffer
}

func (g *fileGen) method(rname string, m method) error {
	rnamed, rstruct, err := g.lookupStruct(g.pkg.Types, rname)
	if err != nil {
		return fmt.Errorf("receiver: %v", err)
	}
	apath, aname := splitTypePath(m.Argument)
	apkg := g.pkg.Types
	if apath != "" {
		if apkg = g.index[apath]; apkg == nil {
			return fmt.Errorf("argument: package %s not loaded", apath)
		}
	}
	anamed, astruct, err := g.lookupStruct(apkg, aname)
	if err != nil {
		return fmt.Errorf("argument: %v", err)
	}

	mname := "Copy"
	if m.Mode == OnlySet {
		mname = "Set"
	}
	if m.Into {
		mname += "Into"
	} else {
		mname += "From"
	}
	if aname != rname {
		mname += aname
	}
	if g.methods[rname+"."+mname] {
		return fmt.Errorf("duplicate method %s", mname)
	}
	g.methods[rname+"."+mname] = true
	if obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(rnamed), false, g.pkg.Types, mname); obj != nil {
		return fmt.Errorf("%s already declared at %s", mname, g.pkg.Fset.Position(obj.Pos()))
	}

	rfields := g.fields(rstruct)
	afields := g.fields(astruct)
	afieldsByName := make(map[string]*types.Var, len(afields))
	for _, f := range afields {
		afieldsByName[f.Name()] = f
	}
	// Receiver is the destination by default.
	dstName, srcName := "d", "s"
	dstFields, srcFields := rfields, afields
	if m.Into {
		dstFields, srcFields = srcFields, dstFields
	}
	if err := checkNames("rename", m.Rename, rfields, true); err != nil {
		return err
	}
	if err := checkNames("rename", m.Rename, afields, false); err != nil {
		return err
	}
	switch m.Mode {
	case "", FullCopy, OnlySet:
	default:
		return fmt.Errorf("unknown mode %q, use %s or %s", m.Mode, FullCopy, OnlySet)
	}
	if m.Except.All && m.Skip.All {
		return fmt.Errorf("except and skip cannot be both true")
	}
	if err := checkNames("except", m.Except.Names, dstFields, true); err != nil {
		return err
	}
	if err := checkNames("skip", m.Skip.Names, srcFields, true); err != nil {
		return err
	}
	if err := checkNames("fields", slices.Sorted(maps.Keys(m.Fields)), dstFields, true); err != nil {
		return err
	}

	// Pairs of destination and source fields, either of which may be nil.
	type pair struct{ dst, src *types.Var }
	var pairs []pair
	used := make(map[string]bool, len(rfields))
	for _, rf := range rfields {
		an := rf.Name()
		if r, ok := m.Rename[an]; ok {
			an = r
		}
		af := afieldsByName[an]
		if af != nil {
			if used[an] {
				return fmt.Errorf("argument field %s matched more than once", an)
			}
			used[an] = true
		}
		if m.Into {
			pairs = append(pairs, pair{af, rf})
		} else {
			pairs = append(pairs, pair{rf, af})
		}
	}
	for _, af := range afields {
		if used[af.Name()] {
			continue
		}
		if m.Into {
			pairs = append(pairs, pair{af, nil})
		} else {
			pairs = append(pairs, pair{nil, af})
		}
	}

	// Arguments of overrides are merged in fields order.
	params := m.Params
	for _, p := range pairs {
		if p.dst != nil {
			params = append(params, m.Fields[p.dst.Name()].Args...)
		}
	}
	paramsDecl, err := g.params(params)
	if err != nil {
		return err
	}
	recvName, argName := dstName, srcName
	if m.Into {
		recvName, argName = srcName, dstName
	}
	fmt.Fprintf(&g.body, "\nfunc (%s *%s) %s(%s *%s%s) {\n", recvName, rname, mname, argName, g.typeString(anamed), paramsDecl)

	for _, p := range pairs {
		dst, src := p.dst, p.src
		if dst == nil {
			if !m.Skip.hasUnmatched(src.Name()) {
				return fmt.Errorf("source field %s has no destination field, add it into skip or rename", src.Name())
			}
			continue
		}
		override, overridden := m.Fields[dst.Name()]
		if m.Except.has(dst.Name()) {
			if overridden {
				return fmt.Errorf("field %s is both in except and fields", dst.Name())
			}
			continue
		}
		if src != nil && m.Skip.has(src.Name()) {
			src = nil
			if !overridden {
				continue
			}
		}
		dstExpr := dstName + "." + dst.Name()
		var srcExpr string
		if src != nil {
			srcExpr = srcName + "." + src.Name()
		}
		if overridden {
			if err := g.override(dstExpr, srcExpr, override); err != nil {
				return fmt.Errorf("field %s: %v", dst.Name(), err)
			}
			continue
		}
		if src == nil {
			if !m.Except.hasUnmatched(dst.Name()) {
				return fmt.Errorf("destination field %s has no source field, add it into except or rename", dst.Name())
			}
			continue
		}
		copyField := g.copyField
		if m.Mode == OnlySet {
			copyField = g.copySet
		}
		err := copyField(value{dstExpr, dst.Type()}, value{srcExpr, src.Type()})
		if err != nil {
			return fmt.Errorf("field %s: %v", dst.Name(), err)
		}
	}
	g.body.WriteString("}\n")
	return nil
}

// Data of field override template.
type overrideData struct {
	dst, src string
	usedDst  bool
	noSrc    bool
}

// D returns destination field expression.
func (d *overrideData) D() string {
	d.usedDst = true
	return d.dst
}

// S returns source field expression.
func (d *overrideData) S() string {
	return d.String()
}

// String returns source field expression for {{.}}.
func (d *overrideData) String() string {
	if d.src == "" {
		d.noSrc = true
	}
	return d.src
}

// override writes field copying statement from override template.
func (g *fileGen) override(dst, src string, f field) error {
	if f.Expr == "" {
		return fmt.Errorf("fields: expr is required")
	}
	t, err := template.New("").Parse(f.Expr)
	if err != nil {
		return fmt.Errorf("fields: %v", err)
	}
	data := &overrideData{dst: dst, src: src}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return fmt.Errorf("fields: %v", err)
	}
	if data.noSrc {
		return fmt.Errorf("fields: expr uses source field, but there is no source field")
	}
	if data.usedDst {
		fmt.Fprintln(&g.body, b.String())
	} else {
		fmt.Fprintf(&g.body, "%s = %s\n", dst, b.String())
	}
	return nil
}

// params returns additional arguments declaration, prefixed with comma.
func (g *fileGen) params(list []string) (string, error) {
	var b strings.Builder
	// Same arguments of method and field overrides are merged.
	names := map[string]string{"d": "", "s": ""}
	for _, param := range list {
		name, typ, ok := splitParam(param)
		if !ok {
			return "", fmt.Errorf("arg %q: expected format \"name path/to/package.Type\"", param)
		}
		if !token.IsIdentifier(name) {
			return "", fmt.Errorf("arg %q: invalid name %s", param, name)
		}
		if prev, ok := names[name]; ok {
			if prev != "" && prev == typ {
				continue
			}
			return "", fmt.Errorf("arg %q: name %s is already used", param, name)
		}
		names[name] = typ
		t, err := g.paramType(typ)
		if err != nil {
			return "", fmt.Errorf("arg %q: %v", param, err)
		}
		fmt.Fprintf(&b, ", %s %s", name, t)
	}
	return b.String(), nil
}

// paramType returns type name of additional argument type string,
// like "*[]path/to/pkg.Name", registering import of its package.
//
// Packages of argument types are not loaded.
// Loaded package (generated, argument types and their imports) is used by its name.
// Otherwise package name is guessed from its path and imported with explicit name,
// since actual package name may differ from the path.
func (g *fileGen) paramType(s string) (string, error) {
	for _, prefix := range []string{"*", "[]"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			t, err := g.paramType(rest)
			return prefix + t, err
		}
	}
	pkgPath, name := splitTypePath(s)
	if !token.IsIdentifier(name) {
		return "", fmt.Errorf("invalid type %s", s)
	}
	switch pkgPath {
	case "":
		if _, ok := types.Universe.Lookup(name).(*types.TypeName); ok {
			return name, nil
		}
		if _, ok := g.pkg.Types.Scope().Lookup(name).(*types.TypeName); ok {
			return name, nil
		}
		return "", fmt.Errorf("type %s not found", s)
	case g.pkg.PkgPath:
		if _, ok := g.pkg.Types.Scope().Lookup(name).(*types.TypeName); !ok {
			return "", fmt.Errorf("type %s not found", s)
		}
		return name, nil
	}
	if p := g.index[pkgPath]; p != nil {
		if _, ok := p.Scope().Lookup(name).(*types.TypeName); !ok {
			return "", fmt.Errorf("type %s not found", s)
		}
		return g.qualifier(p) + "." + name, nil
	}
	n := g.importName(pkgPath, guessPkgName(pkgPath))
	if !g.isStd(pkgPath) {
		g.explicit[pkgPath] = true
	}
	return n + "." + name, nil
}

// guessPkgName returns package name guessed from its import path,
// like "yaml" for "gopkg.in/yaml.v3" or "foo" for "github.com/x/go-foo/v2".
func guessPkgName(pkgPath string) string {
	elems := strings.Split(pkgPath, "/")
	name := elems[len(elems)-1]
	if len(elems) > 1 && isMajorVersion(name) {
		name = elems[len(elems)-2]
	}
	name, _, _ = strings.Cut(name, ".")
	name = strings.TrimPrefix(name, "go-")
	name = strings.Map(func(r rune) rune {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, name)
	if !token.IsIdentifier(name) {
		return "pkg"
	}
	return name
}

func isMajorVersion(s string) bool {
	v, ok := strings.CutPrefix(s, "v")
	return ok && v != "" && strings.Trim(v, "0123456789") == ""
}

// isStd reports if package path belongs to standard library,
// which package names are equal to the last path element.
// Path of current module may also be without dot.
func (g *fileGen) isStd(pkgPath string) bool {
	if m := g.pkg.Module; m != nil && (pkgPath == m.Path || strings.HasPrefix(pkgPath, m.Path+"/")) {
		return false
	}
	first, _, _ := strings.Cut(pkgPath, "/")
	return !strings.Contains(first, ".")
}

// checkNames checks that all names (or map keys/values) are fields.
func checkNames[T []string | map[string]string](option string, names T, fields []*types.Var, keys bool) error {
	var list []string
	switch v := any(names).(type) {
	case []string:
		list = v
	case map[string]string:
		for k, val := range v {
			if keys {
				list = append(list, k)
			} else {
				list = append(list, val)
			}
		}
	}
	for _, name := range list {
		if !slices.ContainsFunc(fields, func(f *types.Var) bool { return f.Name() == name }) {
			return fmt.Errorf("%s: unknown field %s", option, name)
		}
	}
	return nil
}

func (g *fileGen) lookupStruct(pkg *types.Package, name string) (*types.Named, *types.Struct, error) {
	obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
	if !ok {
		return nil, nil, fmt.Errorf("type %s not found in %s", name, pkg.Path())
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return nil, nil, fmt.Errorf("%s is not a named type", name)
	}
	if named.TypeParams().Len() > 0 {
		return nil, nil, fmt.Errorf("generic type %s is not supported", name)
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, nil, fmt.Errorf("%s is not a struct", name)
	}
	return named, st, nil
}

// fields returns struct fields accessible from generated package.
func (g *fileGen) fields(st *types.Struct) []*types.Var {
	var fields []*types.Var
	for f := range st.Fields() {
		if f.Exported() || f.Pkg() == g.pkg.Types {
			fields = append(fields, f)
		}
	}
	return fields
}

// qualifier registers import of package and returns its name.
func (g *fileGen) qualifier(p *types.Package) string {
	if p == g.pkg.Types {
		return ""
	}
	return g.importName(p.Path(), p.Name())
}

// importName returns package name with optional suffix number,
// if name already planned for using in generating file imports.
func (g *fileGen) importName(pkgPath, name string) string {
	if n, ok := g.imports[pkgPath]; ok {
		return n
	}
	if n, ok := g.names[pkgPath]; ok {
		name = n
		g.explicit[pkgPath] = true
	}
	taken := func(n string) bool {
		if g.reserved[n] || g.pkg.Types.Scope().Lookup(n) != nil {
			return true
		}
		for _, v := range g.imports {
			if v == n {
				return true
			}
		}
		return false
	}
	n := name
	for i := 2; taken(n); i++ {
		n = fmt.Sprintf("%s%d", name, i)
	}
	g.imports[pkgPath] = n
	return n
}

func (g *fileGen) typeString(t types.Type) string {
	return types.TypeString(t, g.qualifier)
}

// newExpr reports if new(expr) is supported by package Go version.
func (g *fileGen) newExpr() bool {
	v := g.pkg.Types.GoVersion()
	return v != "" && version.Compare(v, "go1.26") >= 0
}

func (g *fileGen) content() ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprint(&b, "// Code generated by copier. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", g.pkg.Name)
	paths := make([]string, 0, len(g.imports))
	for p := range g.imports {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	spec := func(p string) string {
		if n := g.imports[p]; n != path.Base(p) || g.explicit[p] {
			return n + " " + fmt.Sprintf("%q", p)
		}
		return fmt.Sprintf("%q", p)
	}
	switch len(paths) {
	case 0:
	case 1:
		fmt.Fprintf(&b, "import %s\n", spec(paths[0]))
	default:
		b.WriteString("import (\n")
		for _, p := range paths {
			fmt.Fprintf(&b, "\t%s\n", spec(p))
		}
		b.WriteString(")\n")
	}
	b.Write(g.body.Bytes())
	src, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%v\n%s", err, b.Bytes())
	}
	return src, nil
}
