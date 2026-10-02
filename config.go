package main

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// Root config.
type config struct {
	Packages ordered[pkgMethods] `yaml:"packages"` // Methods of multiple packages.
	Methods  ordered[[]method]   `yaml:"methods"`  // Methods of the current package.
	Wrappers map[string]wrapper  `yaml:"wrappers"` // Wrappers by full type path.
}

// Methods of single package.
type pkgMethods struct {
	Methods ordered[[]method] `yaml:"methods"` // Methods by receiver type name.
}

// Copying method description.
type method struct {
	Argument string            `yaml:"arg"`    // Method argument type path.
	Into     bool              `yaml:"into"`   // Copy from receiver into argument.
	Rename   map[string]string `yaml:"rename"` // Map receiver to argument field names.
	Except   []string          `yaml:"except"` // Copy into all destination fields, except these.
	Skip     []string          `yaml:"skip"`   // Copy from all source fields, skipping these.
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
	Getter string `yaml:"getter"` // Value getter method.
	Setter string `yaml:"setter"` // Value setter method.
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
