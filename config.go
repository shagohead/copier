package main

import (
	"fmt"
	"os"
	"slices"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// Root config.
type config struct {
	Packages ordered[pkgMethods]          `yaml:"packages"` // Methods of multiple packages.
	Methods  ordered[[]method]            `yaml:"methods"`  // Methods of the current package.
	Getters  map[string]map[string]string `yaml:"getters"`  // Getters of alternative value types.
	Wrappers map[string]wrapper           `yaml:"wrappers"` // Wrappers by full type path.
	Imports  map[string]string            `yaml:"imports"`  // Package paths by explicit import names.
}

// Methods of single package.
type pkgMethods struct {
	Methods ordered[[]method] `yaml:"methods"` // Methods by receiver type name.
}

// Copying method description.
type method struct {
	Argument string            `yaml:"-"`      // Method argument type path.
	Params   []string          `yaml:"-"`      // Additional method arguments: "name path/to/pkg.Type".
	Mode     mode              `yaml:"mode"`   // Generation mode, FullCopy by default.
	Into     bool              `yaml:"into"`   // Copy from receiver into argument.
	Rename   map[string]string `yaml:"rename"` // Map receiver to argument field names.
	Except   fieldSet          `yaml:"except"` // Copy into all destination fields, except these.
	Skip     fieldSet          `yaml:"skip"`   // Copy from all source fields, skipping these.
	Fields   map[string]field  `yaml:"fields"` // Overrides of copying by destination field names.
}

// UnmarshalYAML decodes method, which "arg" is either argument type path,
// or list of argument type path and additional arguments.
func (m *method) UnmarshalYAML(node ast.Node) error {
	type Plain method
	var v struct {
		Plain `yaml:",inline"`
		Arg   any `yaml:"arg"`
	}
	if err := yaml.NodeToValue(node, &v); err != nil {
		return err
	}
	*m = method(v.Plain)
	switch arg := v.Arg.(type) {
	case nil:
	case string:
		m.Argument = arg
	case []any:
		if len(arg) == 0 {
			return fmt.Errorf("arg: at least one item required")
		}
		for i, item := range arg {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("arg: item %d is not a string", i)
			}
			if i == 0 {
				m.Argument = s
			} else {
				m.Params = append(m.Params, s)
			}
		}
	default:
		return fmt.Errorf("arg: expected string or list of strings")
	}
	return nil
}

// Override of field copying.
//
// Expression is a template of the whole assignment statement,
// if it uses destination field {{.D}}, like "someFunc(&{{.D}}, &{{.S}})".
// Otherwise it is a template of value assigned into destination field,
// where {{.}} or {{.S}} is the source field, like "someFunc({{.}})".
type field struct {
	Expr string   `yaml:"expr"` // Expression template.
	Args []string `yaml:"args"` // Additional method arguments, merged with arg.
}

// UnmarshalYAML decodes field from mapping or from expression string.
func (f *field) UnmarshalYAML(node ast.Node) error {
	if _, ok := node.(*ast.StringNode); ok {
		return yaml.NodeToValue(node, &f.Expr)
	}
	type Plain field
	return yaml.NodeToValue(node, (*Plain)(f))
}

// Method generation mode.
type mode string

const (
	// Copy values of source fields into destination fields.
	FullCopy mode = "FullCopy"
	// Set boolean destination fields to true, if source fields are set.
	// Source field is set if wrapper CopyIf expression is true,
	// or slice or map is not nil.
	OnlySet mode = "OnlySet"
)

// List of field names, or all unmatched fields if defined as boolean true.
type fieldSet struct {
	All   bool
	Names []string
}

// has reports if field matched with name is in set.
func (s fieldSet) has(name string) bool {
	return slices.Contains(s.Names, name)
}

// hasUnmatched reports if field without match is in set.
func (s fieldSet) hasUnmatched(name string) bool {
	return s.All || s.has(name)
}

func (s *fieldSet) UnmarshalYAML(node ast.Node) error {
	switch n := node.(type) {
	case *ast.NullNode:
		return nil
	case *ast.BoolNode:
		s.All = n.Value
		return nil
	}
	return yaml.NodeToValue(node, &s.Names)
}

// Wrapper of type with some additional logic:
// - make copying only in some curcumstances
// - respect emptyness represented in different ways
//
// Expressions are text/template templates executed with the field expression as data.
// Expression starting with dot is shorthand for selector on the field: ".Value" equals to "{{.}}.Value".
type wrapper struct {
	Type   string `yaml:"type"`   // Underlying value type.
	Value  string `yaml:"value"`  // Path to field with value.
	Valid  string `yaml:"valid"`  // Expression determining «emptyness» of value.
	CopyIf string `yaml:"copyif"` // Copy if expression evaluates to true.
}

// Mapping which preserves keys order of yaml document.
type ordered[T any] []entry[T]

type entry[T any] struct {
	Key   string
	Value T
}

func (o *ordered[T]) UnmarshalYAML(node ast.Node) error {
	var values []*ast.MappingValueNode
	switch n := node.(type) {
	case *ast.NullNode:
		return nil
	case *ast.MappingNode:
		values = n.Values
	case *ast.MappingValueNode:
		values = []*ast.MappingValueNode{n}
	default:
		return fmt.Errorf("%s: expected mapping", node.GetToken().Position)
	}
	for _, kv := range values {
		var e entry[T]
		if err := yaml.NodeToValue(kv.Key, &e.Key); err != nil {
			return err
		}
		if err := yaml.NodeToValue(kv.Value, &e.Value); err != nil {
			return err
		}
		*o = append(*o, e)
	}
	return nil
}

func configFromFile(fpath string) (*config, error) {
	f, err := os.Open(fpath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg := new(config)
	if err = yaml.NewDecoder(f).Decode(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func configFromArgs(receiver, argument string, into bool) *config {
	return &config{
		Methods: ordered[[]method]{{
			Key:   receiver,
			Value: []method{{Argument: argument, Into: into}},
		}},
	}
}
