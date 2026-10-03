package main

import (
	"bytes"
	"fmt"
	"go/format"
	"go/types"
	"go/version"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

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
			}
		}
	}

	// Resolve target packages first, to replace previously generated
	// files with empty ones: they can be outdated and fail type checking.
	match := func(pkgs []*packages.Package) error {
		for _, t := range targets {
			t.pkg = nil
			for _, pkg := range pkgs {
				// TODO: Проверить как будет работать с package import path в качестве ключа (t.dir).
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

	index := make(map[string]*types.Package)
	var walk func(p *types.Package)
	walk = func(p *types.Package) {
		if _, ok := index[p.Path()]; ok {
			return
		}
		index[p.Path()] = p
		// FIXME: Возможно ходить по импортам лишнее.
		// Все нужные для анализа пакеты уже должны быть в pkgs.
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
			methods:  make(map[string]bool),
			wrappers: make(map[string]*wrapperInfo),
			getters:  make(map[string][]getter),
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
	methods  map[string]bool           // Generated methods.
	wrappers map[string]*wrapperInfo
	getters  map[string][]getter
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

	mname := "CopyFrom"
	if m.Into {
		mname = "CopyInto"
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

	recvName, argName := dstName, srcName
	if m.Into {
		recvName, argName = srcName, dstName
	}
	fmt.Fprintf(&g.body, "\nfunc (%s *%s) %s(%s *%s) {\n", recvName, rname, mname, argName, g.typeString(anamed))
	used := make(map[string]bool, len(rfields))
	for _, rf := range rfields {
		an := rf.Name()
		if r, ok := m.Rename[an]; ok {
			an = r
		}
		af := afieldsByName[an]
		dst, src := rf, af
		if m.Into {
			dst, src = src, dst
		}
		if af != nil {
			if used[an] {
				return fmt.Errorf("argument field %s matched more than once", an)
			}
			used[an] = true
		}
		if dst != nil && m.Except.has(dst.Name()) || src != nil && m.Skip.has(src.Name()) {
			continue
		}
		if af == nil {
			if m.Into {
				if m.Skip.All {
					continue
				}
				return fmt.Errorf("source field %s has no destination field, add it into skip or rename", rf.Name())
			}
			if m.Except.All {
				continue
			}
			return fmt.Errorf("destination field %s has no source field, add it into except or rename", rf.Name())
		}
		copyField := g.copyField
		if m.Mode == OnlySet {
			copyField = g.copySet
		}
		err := copyField(
			value{dstName + "." + dst.Name(), dst.Type()},
			value{srcName + "." + src.Name(), src.Type()},
		)
		if err != nil {
			return fmt.Errorf("field %s: %v", dst.Name(), err)
		}
	}
	for _, af := range afields {
		if used[af.Name()] {
			continue
		}
		if m.Into {
			if !m.Except.hasUnmatched(af.Name()) {
				return fmt.Errorf("destination field %s has no source field, add it into except or rename", af.Name())
			}
		} else if !m.Skip.hasUnmatched(af.Name()) {
			return fmt.Errorf("source field %s has no destination field, add it into skip or rename", af.Name())
		}
	}
	g.body.WriteString("}\n")
	return nil
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

// importName returns package name with optional suffix number, if name already in use.
func (g *fileGen) importName(pkgPath, name string) string {
	if n, ok := g.imports[pkgPath]; ok {
		return n
	}
	taken := func(n string) bool {
		if n == "d" || n == "s" || g.pkg.Types.Scope().Lookup(n) != nil {
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
	fmt.Fprintf(&b, "package %s\n\n", g.pkg.Name)
	paths := make([]string, 0, len(g.imports))
	for p := range g.imports {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	spec := func(p string) string {
		if n := g.imports[p]; n != path.Base(p) {
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
