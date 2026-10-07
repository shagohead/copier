package main

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/google/go-cmp/cmp"
)

// Generated files of examples should be equal to already existing ones.
func TestExamples(t *testing.T) {
	for _, dir := range []string{"internal/examples/local", "internal/examples/packages"} {
		t.Run(dir, func(t *testing.T) {
			cfg, err := configFromFile(filepath.Join(dir, "copier.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			files, err := generate(cfg, dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) == 0 {
				t.Fatal("no files generated")
			}
			for _, f := range files {
				want, err := os.ReadFile(f.path)
				if err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(string(want), string(f.content)); diff != "" {
					t.Errorf("%s mismatch (-want +got):\n%s", f.path, diff)
				}
			}
		})
	}
}

func BenchmarkGenerate(b *testing.B) {
	dir := "internal/examples/local"
	cfg, err := configFromFile(filepath.Join(dir, "copier.yaml"))
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		files, err := generate(cfg, dir)
		if err != nil {
			b.Fatal(err)
		}
		if len(files) == 0 {
			b.Fatal("no files generated")
		}
	}
}

func TestGenerate(t *testing.T) {
	for _, tt := range []struct {
		name string
		meth method
		code string
		gett map[string]map[string]string
		wrap map[string]wrapper
		imps map[string]string
		fail string
		want string
	}{
		{
			name: "copy string",
			code: `
			type Receiver struct { SomeString string }
			type Argument struct { SomeString string }
			`,
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.SomeString = s.SomeString
			}`,
		},
		{
			name: "into",
			meth: method{Into: true},
			code: `
			type Receiver struct { A string }
			type Argument struct { A string }
			`,
			want: `func (s *Receiver) CopyIntoArgument(d *Argument) {
				d.A = s.A
			}`,
		},
		{
			name: "basic conversions",
			code: `
			type Str string
			type Receiver struct {
				A int64
				B Str
				C float64
				D uint8
			}
			type Argument struct {
				A int
				B string
				C float32
				D int
			}
			`,
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A = int64(s.A)
				d.B = Str(s.B)
				d.C = float64(s.C)
				d.D = uint8(s.D)
			}`,
		},
		{
			name: "int to string",
			code: `
			type Receiver struct { A string }
			type Argument struct { A int }
			`,
			fail: "cannot copy s.A (int) into d.A (string)",
		},
		{
			name: "same underlying",
			code: `
			type Receiver struct {
				U UUID
				B []byte
				T Inner
				X [16]byte
			}
			type Inner struct{ X int }
			type Argument struct {
				U []byte
				B UUID
				T Other
				X UUID
			}
			type UUID [16]byte
			type Other struct{ X int }
			`,
			want: `import "slices"

			func (d *Receiver) CopyFromArgument(s *Argument) {
				d.U = UUID(s.U)
				d.B = slices.Clone(s.B[:])
				d.T = Inner(s.T)
				d.X = s.X
			}`,
		},
		{
			name: "deref",
			code: `
			type Receiver struct {
		 		P *int64
			}
			type Argument struct {
		 		P *int32
			}
			`,
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				if s.P != nil {
					d.P = new(int64(*s.P))
				} else {
					d.P = nil
				}
			}`,
		},
		{
			name: "rename except skip",
			meth: method{Rename: map[string]string{"A": "B"}, Except: fieldSet{Names: []string{"C"}}, Skip: fieldSet{Names: []string{"D"}}},
			code: `
			type Receiver struct {
				A int
				C int
			}
			type Argument struct {
				B int
				D int
			}
			`,
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A = s.B
			}`,
		},
		{
			name: "missing source",
			code: `
			type Receiver struct { A int }
			type Argument struct{}
			`,
			fail: "destination field A has no source field",
		},
		{
			name: "unused source",
			code: `
			type Receiver struct{}
			type Argument struct { A int }
			`,
			fail: "source field A has no destination field",
		},
		{
			name: "unknown skip",
			meth: method{Skip: fieldSet{Names: []string{"X"}}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: "skip: unknown field X",
		},
		{
			name: "unknown except",
			meth: method{Except: fieldSet{Names: []string{"X"}}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: "except: unknown field X",
		},
		{
			name: "except all",
			meth: method{Except: fieldSet{All: true}},
			code: `
			type Receiver struct { A, B, C int }
			type Argument struct { B int }
			`,
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.B = s.B
			}`,
		},
		{
			name: "except all into",
			meth: method{Into: true, Except: fieldSet{All: true}},
			code: `
			type Receiver struct { B int }
			type Argument struct { A, B, C int }
			`,
			want: `func (s *Receiver) CopyIntoArgument(d *Argument) {
				d.B = s.B
			}`,
		},
		{
			name: "except all with unused source",
			meth: method{Except: fieldSet{All: true}},
			code: `
			type Receiver struct { A int }
			type Argument struct { A, B int }
			`,
			fail: "source field B has no destination field",
		},
		{
			name: "except all with names",
			meth: method{Except: fieldSet{All: true}, Skip: fieldSet{Names: []string{"B"}}},
			code: `
			type Receiver struct { A, C int }
			type Argument struct { A, B int }
			`,
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A = s.A
			}`,
		},
		{
			name: "skip all",
			meth: method{Skip: fieldSet{All: true}},
			code: `
			type Receiver struct { B int }
			type Argument struct { A, B, C int }
			`,
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.B = s.B
			}`,
		},
		{
			name: "skip all into",
			meth: method{Into: true, Skip: fieldSet{All: true}},
			code: `
			type Receiver struct { A, B, C int }
			type Argument struct { B int }
			`,
			want: `func (s *Receiver) CopyIntoArgument(d *Argument) {
				d.B = s.B
			}`,
		},
		{
			name: "skip all with missing source",
			meth: method{Skip: fieldSet{All: true}},
			code: `
			type Receiver struct { A, B int }
			type Argument struct { A int }
			`,
			fail: "destination field B has no source field",
		},
		{
			name: "skip all into with missing source",
			meth: method{Into: true, Skip: fieldSet{All: true}},
			code: `
			type Receiver struct { A int }
			type Argument struct { A, B int }
			`,
			fail: "destination field B has no source field",
		},
		{
			name: "except and skip all",
			meth: method{Except: fieldSet{All: true}, Skip: fieldSet{All: true}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: "except and skip cannot be both true",
		},
		{
			name: "only set",
			meth: method{Mode: OnlySet},
			code: `
			type Receiver struct {
				A bool
				B Flag
				C bool
				D Flag
				E bool
				F bool
			}
			type Argument struct {
				A OptInt32
				B OptInt32
				C []int
				D map[string]int
				E OptNil
				F Bytes
			}
			type Flag bool
			type Bytes []byte
			type OptInt32 struct { Value int32; Set bool }
			type OptNil struct { Value int32; Null bool }
			`,
			wrap: map[string]wrapper{
				"OptInt32": {Value: ".Value", CopyIf: ".Set"},
				"OptNil":   {Value: ".Value", CopyIf: "!{{.}}.Null"},
			},
			want: `func (d *Receiver) SetFromArgument(s *Argument) {
				d.A = s.A.Set
				d.B = Flag(s.B.Set)
				d.C = s.C != nil
				d.D = s.D != nil
				d.E = !s.E.Null
				d.F = s.F != nil
			}`,
		},
		{
			name: "only set into",
			meth: method{Mode: OnlySet, Into: true, Except: fieldSet{All: true}},
			code: `
			type Receiver struct { A []int }
			type Argument struct { A, B bool }
			`,
			want: `func (s *Receiver) SetIntoArgument(d *Argument) {
				d.A = s.A != nil
			}`,
		},
		{
			name: "only set/not boolean",
			meth: method{Mode: OnlySet},
			code: `
			type Receiver struct { A int }
			type Argument struct { A []int }
			`,
			fail: "destination d.A (int) is not boolean",
		},
		{
			name: "only set/not set-able",
			meth: method{Mode: OnlySet},
			code: `
			type Receiver struct { A bool }
			type Argument struct { A *int }
			`,
			fail: "source s.A (*int) is neither wrapper with copyif, slice or map",
		},
		{
			name: "only set/wrapper without copyif",
			meth: method{Mode: OnlySet},
			code: `
			type Receiver struct { A bool }
			type Argument struct { A Null }
			type Null struct { Value int; Valid bool }
			`,
			wrap: map[string]wrapper{"Null": {Value: ".Value", Valid: ".Valid"}},
			fail: "source s.A (Null) wrapper has no copyif",
		},
		{
			name: "unknown mode",
			meth: method{Mode: "Partial"},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: `unknown mode "Partial"`,
		},
		{
			name: "params",
			meth: method{Params: []string{
				"ctx context.Context",
				"loc *time.Location",
				"n int",
				"ids []example.ID",
				"local ID",
			}},
			code: `
			type Receiver struct { A int }
			type Argument struct { A int }
			type ID int
			`,
			want: `import (
				"context"
				"time"
			)

			func (d *Receiver) CopyFromArgument(s *Argument, ctx context.Context, loc *time.Location, n int, ids []ID, local ID) {
				d.A = s.A
			}`,
		},
		{
			name: "params/not loaded packages",
			meth: method{
				Params: []string{
					"c *example.com/nonexistent/go-client/v3.Client",
					"n []gopkg.in/yaml.v3.Node",
					"r *math/rand/v2.Rand",
					"t time.Time",
				},
				Fields: map[string]field{"A": {Expr: "c.Get(n, r)", Args: []string{"n []gopkg.in/yaml.v3.Node"}}},
				Skip:   fieldSet{Names: []string{"T"}},
			},
			code: `
			import "time"

			type Receiver struct { A int }
			type Argument struct { T time.Time }
			`,
			want: `import (
				client "example.com/nonexistent/go-client/v3"
				yaml "gopkg.in/yaml.v3"
				rand "math/rand/v2"
				"time"
			)

			func (d *Receiver) CopyFromArgument(s *Argument, c *client.Client, n []yaml.Node, r *rand.Rand, t time.Time) {
				d.A = c.Get(n, r)
			}`,
		},
		{
			name: "params/current module import",
			meth: method{Params: []string{"c *example/firestore/apiv1.Client"}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			want: `import apiv1 "example/firestore/apiv1"

			func (d *Receiver) CopyFromArgument(s *Argument, c *apiv1.Client) {
			}`,
		},
		{
			name: "params/current module import renamed",
			meth: method{
				Params: []string{"c *project/client/apiv2.Client"},
				Fields: map[string]field{"A": {Expr: "c.Get({{.}}, n)", Args: []string{"n []project/client/apiv2.Node"}}},
			},
			code: `
			type Receiver struct { A int }
			type Argument struct { A int }
			`,
			imps: map[string]string{"client": "project/client/apiv2"},
			want: `import client "project/client/apiv2"

			func (d *Receiver) CopyFromArgument(s *Argument, c *client.Client, n []client.Node) {
				d.A = c.Get(s.A, n)
			}`,
		},
		{
			name: "imports/loaded package",
			code: `
			import "time"

			type Receiver struct { A time.Duration }
			type Argument struct { A int64 }
			`,
			imps: map[string]string{"tm": "time"},
			want: `import tm "time"

			func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A = tm.Duration(s.A)
			}`,
		},
		{
			name: "imports/explicit same name",
			meth: method{Params: []string{"c *example.com/x/apiv1.Client"}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			imps: map[string]string{"firestore": "example.com/x/apiv1"},
			want: `import firestore "example.com/x/apiv1"

			func (d *Receiver) CopyFromArgument(s *Argument, c *firestore.Client) {
			}`,
		},
		{
			name: "imports/invalid name",
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			imps: map[string]string{"go-client": "example.com/x"},
			fail: `imports: invalid name "go-client"`,
		},
		{
			name: "imports/duplicate path",
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			imps: map[string]string{"a": "example.com/x", "b": "example.com/x"},
			fail: "imports: example.com/x has names a and b",
		},
		{
			name: "params/import name shadowed",
			meth: method{Params: []string{"time string"}},
			code: `
			import "time"

			type Receiver struct { A time.Duration }
			type Argument struct { A int64 }
			`,
			want: `import time2 "time"

			func (d *Receiver) CopyFromArgument(s *Argument, time string) {
				d.A = time2.Duration(s.A)
			}`,
		},
		{
			name: "params/format",
			meth: method{Params: []string{"ctx"}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: `arg "ctx": expected format "name path/to/package.Type"`,
		},
		{
			name: "params/reserved",
			meth: method{Params: []string{"s string"}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: `arg "s string": name s is already used`,
		},
		{
			name: "params/duplicate",
			meth: method{Params: []string{"a string", "a int"}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: `arg "a int": name a is already used`,
		},
		{
			name: "params/unknown type",
			meth: method{Params: []string{"a Unknown"}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: `arg "a Unknown": type Unknown not found`,
		},
		{
			name: "fields",
			meth: method{
				Params: []string{"loc *time.Location"},
				Fields: map[string]field{
					"Value":   {Expr: "upper({{.}})"},
					"Source":  {Expr: "{{.S}}.In(loc)", Args: []string{"loc *time.Location"}},
					"Whole":   {Expr: "parse(&{{.D}}, &{{.S}}, strict)", Args: []string{"strict bool"}},
					"Renamed": {Expr: "{{.}} + 1"},
					"Created": {Expr: "now", Args: []string{"now time.Time"}},
				},
				Rename: map[string]string{"Renamed": "Other"},
			},
			code: `
			import "time"

			type Receiver struct {
				Plain   int
				Value   string
				Source  time.Time
				Whole   int
				Renamed int
				Created time.Time
			}
			type Argument struct {
				Plain  int
				Value  string
				Source time.Time
				Whole  string
				Other  int
			}

			func upper(s string) string { return s }
			func parse(d *int, s *string, strict bool) {}
			`,
			want: `import "time"

			func (d *Receiver) CopyFromArgument(s *Argument, loc *time.Location, strict bool, now time.Time) {
				d.Plain = s.Plain
				d.Value = upper(s.Value)
				d.Source = s.Source.In(loc)
				parse(&d.Whole, &s.Whole, strict)
				d.Renamed = s.Other + 1
				d.Created = now
			}`,
		},
		{
			name: "fields/into",
			meth: method{Into: true, Fields: map[string]field{
				"A": {Expr: "{{.}} * 2"},
				"B": {Expr: "id", Args: []string{"id int"}},
			}},
			code: `
			type Receiver struct { A int }
			type Argument struct { A, B int }
			`,
			want: `func (s *Receiver) CopyIntoArgument(d *Argument, id int) {
				d.A = s.A * 2
				d.B = id
			}`,
		},
		{
			name: "fields/skipped source",
			meth: method{
				Skip:   fieldSet{Names: []string{"A"}},
				Fields: map[string]field{"A": {Expr: "0"}},
			},
			code: `
			type Receiver struct { A int }
			type Argument struct { A int }
			`,
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A = 0
			}`,
		},
		{
			name: "fields/no source",
			meth: method{Fields: map[string]field{"A": {Expr: "{{.}}"}}},
			code: `
			type Receiver struct { A int }
			type Argument struct{}
			`,
			fail: "field A: fields: expr uses source field, but there is no source field",
		},
		{
			name: "fields/except",
			meth: method{
				Except: fieldSet{Names: []string{"A"}},
				Fields: map[string]field{"A": {Expr: "1"}},
			},
			code: `
			type Receiver struct { A int }
			type Argument struct { A int }
			`,
			fail: "field A is both in except and fields",
		},
		{
			name: "fields/unknown",
			meth: method{Fields: map[string]field{"X": {Expr: "1"}}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: "fields: unknown field X",
		},
		{
			name: "fields/args conflict",
			meth: method{
				Params: []string{"n int"},
				Fields: map[string]field{"A": {Expr: "n", Args: []string{"n string"}}},
			},
			code: `
			type Receiver struct { A string }
			type Argument struct{}
			`,
			fail: `arg "n string": name n is already used`,
		},
		{
			name: "wrapper/dst",
			code: `
			type Receiver struct { A Int32Struct }
			type Argument struct { A int32 }
			type Int32Struct struct { Int32Value int32 }
			`,
			wrap: map[string]wrapper{"example.Int32Struct": {Value: ".Int32Value"}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A.Int32Value = s.A
			}`,
		},
		{
			name: "wrapper/src",
			code: `
			type Receiver struct { A int32 }
			type Argument struct { A Int32Struct }
			type Int32Struct struct { Int32Value int32 }
			`,
			wrap: map[string]wrapper{"example.Int32Struct": {Value: ".Int32Value"}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A = s.A.Int32Value
			}`,
		},
		{
			name: "wrapper/dst-copyif",
			code: `
			type Receiver struct { A OptInt32 }
			type Argument struct { A int32 }
			type OptInt32 struct { Int32Value int32; Set bool }
			`,
			wrap: map[string]wrapper{"example.OptInt32": {
				Value:  ".Int32Value",
				CopyIf: ".Set",
			}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A.Set = true
				d.A.Int32Value = s.A
			}`,
		},
		{
			name: "wrapper/src-copyif",
			code: `
			type Receiver struct { A int32 }
			type Argument struct { A OptInt32 }
			type OptInt32 struct { Int32Value int32; Set bool }
			`,
			wrap: map[string]wrapper{"example.OptInt32": {
				Value:  ".Int32Value",
				CopyIf: ".Set",
			}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				if s.A.Set {
					d.A = s.A.Int32Value
				}
			}`,
		},
		{
			name: "wrapper/dst-valid-empty",
			code: `
			type Receiver struct {
				Alias NullString
				Opt   NullString
				Int   NullInt
				Bool  NullBool
				Ptr   NullString
			}
			type Argument struct {
				Alias Str
				Opt   OptString
				Int   int
				Bool  bool
				Ptr   *string
			}
			type Str string
			type NullString struct { String string; Valid bool }
			type NullInt struct { Int int; Valid bool }
			type NullBool struct { Bool bool; Valid bool }
			type OptString struct { Value string; Set bool }
			`,
			wrap: map[string]wrapper{
				"NullString": {Value: ".String", Valid: ".Valid"},
				"NullInt":    {Value: ".Int", Valid: ".Valid"},
				"NullBool":   {Value: ".Bool", Valid: ".Valid"},
				"OptString":  {Value: ".Value", CopyIf: ".Set"},
			},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.Alias.Valid = s.Alias != ""
				d.Alias.String = string(s.Alias)
				if s.Opt.Set {
					d.Opt.Valid = s.Opt.Value != ""
					d.Opt.String = s.Opt.Value
				}
				d.Int.Valid = true
				d.Int.Int = s.Int
				d.Bool.Valid = true
				d.Bool.Bool = s.Bool
				d.Ptr.Valid = s.Ptr != nil
				if s.Ptr != nil {
					d.Ptr.String = *s.Ptr
				}
			}`,
		},
		{
			name: "wrapper/valid",
			code: `
			type Receiver struct { A Int32X }
			type Argument struct { A Int32Y }
			type Int32X struct { Int32 int32; Has bool }
			type Int32Y struct { Value int32; Valid bool }
			`,
			wrap: map[string]wrapper{
				"example.Int32X": {Value: ".Int32", Valid: ".Has"},
				"example.Int32Y": {Value: ".Value", Valid: ".Valid"},
			},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A.Has = s.A.Valid
				d.A.Int32 = s.A.Value
			}`,
		},
		{
			name: "wrapper/valid-negate",
			code: `
			type Receiver struct { A Int32X }
			type Argument struct { A Int32Y }
			type Int32X struct { Int32 int32; Valid bool }
			type Int32Y struct { Value int32; Null bool }
			`,
			wrap: map[string]wrapper{
				"example.Int32X": {Value: ".Int32", Valid: ".Valid"},
				"example.Int32Y": {Value: ".Value", Valid: "!{{.}}.Null"},
			},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A.Valid = !s.A.Null
				d.A.Int32 = s.A.Value
			}`,
		},
		{
			name: "wrapper/full",
			code: `
			type Receiver struct { A IntX }
			type Argument struct { A Int32Y }
			type IntX struct { Int int; Valid bool; Set bool }
			type Int32Y struct { Value int32; Null bool; Set bool }
			`,
			wrap: map[string]wrapper{
				"example.IntX":   {Value: ".Int", Valid: ".Valid", CopyIf: ".Set"},
				"example.Int32Y": {Value: ".Value", Valid: "!{{.}}.Null", CopyIf: ".Set"},
			},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				if s.A.Set {
					d.A.Set = true
					d.A.Valid = !s.A.Null
					d.A.Int = int(s.A.Value)
				}
			}`,
		},
		{
			name: "wrapper -> conversion",
			code: `
			type Receiver struct { A int64 }
			type Argument struct { A Int32Struct }
			type Int32Struct struct { Int32Value int32 }
			`,
			wrap: map[string]wrapper{"example.Int32Struct": {Value: ".Int32Value"}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A = int64(s.A.Int32Value)
			}`,
		},
		{
			name: "wrapper -> ptr",
			code: `
			type Receiver struct { A *int32 }
			type Argument struct { A OptNilInt32 }
			type OptNilInt32 struct { Value int32; Null bool; Set bool }
			`,
			wrap: map[string]wrapper{"example.OptNilInt32": {
				Value: ".Value", Valid: "!{{.}}.Null", CopyIf: ".Set",
			}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				if s.A.Set {
					if !s.A.Null {
						d.A = new(s.A.Value)
					} else {
						d.A = nil
					}
				}
			}`,
		},
		{
			name: "ptr -> wrapper",
			code: `
			type Receiver struct { A OptNilInt32; B OptInt32 }
			type Argument struct { A *int32; B *int32 }
			type OptInt32 struct { Value int32; Valid bool; Set bool }
			type OptNilInt32 struct { Value int32; Null bool; Set bool }
			`,
			wrap: map[string]wrapper{
				"example.OptInt32":    {Value: ".Value", Valid: ".Valid", CopyIf: ".Set"},
				"example.OptNilInt32": {Value: ".Value", Valid: "!{{.}}.Null", CopyIf: ".Set"},
			},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A.Set = true
				d.A.Null = s.A == nil
				if s.A != nil {
					d.A.Value = *s.A
				}
				d.B.Set = true
				d.B.Valid = s.B != nil
				if s.B != nil {
					d.B.Value = *s.B
				}
			}`,
		},
		{
			name: "getters",
			code: `
			import "time"

			type S struct {
				Value string
			}

			func FromS(s S) string {
				return s.Value
			}

			func FromSlice(s []time.Time) []string {
				d := make([]string, len(s))
				for i, s := range s {
					d[i] = s.String()
				}
				return d
			}

			type Receiver struct {
				Epoch     int64
				Repr      string
				FuncS     string
				FuncSlice []string
			}
			type Argument struct {
				Epoch     time.Time
				Repr      time.Time
				FuncS     S
				FuncSlice []time.Time
			}
			`,
			gett: map[string]map[string]string{
				"time.Time": {
					"int64":  ".Unix()",
					"string": `.Format("2006-01-02T15:04:05 -07:00:00")`,
				},
				"S": {
					"string": "FromS({{.}})",
				},
				"[]time.Time": {
					"[]string": "FromSlice({{.}})",
				},
			},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.Epoch = s.Epoch.Unix()
				d.Repr = s.Repr.Format("2006-01-02T15:04:05 -07:00:00")
				d.FuncS = FromS(s.FuncS)
				d.FuncSlice = FromSlice(s.FuncSlice)
			}`,
		},
		{
			name: "getters/ptr",
			code: `
			import "time"

			type Receiver struct {
				FromPtr int64
				IntoPtr *int64
			}
			type Argument struct {
				FromPtr *time.Time
				IntoPtr time.Time
			}
			`,
			gett: map[string]map[string]string{"time.Time": {"int64": ".Unix()"}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				if s.FromPtr != nil {
					d.FromPtr = (*s.FromPtr).Unix()
				}
				d.IntoPtr = new(s.IntoPtr.Unix())
			}`,
		},
		{
			name: "getters/slice-elements",
			code: `
			import (
				"net/url"
				"time"
			)

			type Receiver struct {
				Epochs  []int64
				Reprs   Strings
				URLs    []string
				Opt     []string
				Ptr     []int64
				Numbers []int64
			}
			type Argument struct {
				Epochs  []time.Time
				Reprs   Times
				URLs    []url.URL
				Opt     OptURLs
				Ptr     *[]time.Time
				Numbers []int32
			}
			type Strings []string
			type Times []time.Time
			type OptURLs struct { Value []url.URL; Set bool }
			`,
			gett: map[string]map[string]string{
				"time.Time": {
					"int64":  ".Unix()",
					"string": ".String()",
				},
				"url.URL": {"string": ".String()"},
			},
			wrap: map[string]wrapper{"OptURLs": {Value: ".Value", CopyIf: ".Set"}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				if s.Epochs != nil {
					d.Epochs = make([]int64, len(s.Epochs))
					for i := range s.Epochs {
						d.Epochs[i] = s.Epochs[i].Unix()
					}
				} else {
					d.Epochs = nil
				}
				if s.Reprs != nil {
					d.Reprs = make(Strings, len(s.Reprs))
					for i := range s.Reprs {
						d.Reprs[i] = s.Reprs[i].String()
					}
				} else {
					d.Reprs = nil
				}
				if s.URLs != nil {
					d.URLs = make([]string, len(s.URLs))
					for i := range s.URLs {
						d.URLs[i] = s.URLs[i].String()
					}
				} else {
					d.URLs = nil
				}
				if s.Opt.Set {
					if s.Opt.Value != nil {
						d.Opt = make([]string, len(s.Opt.Value))
						for i := range s.Opt.Value {
							d.Opt[i] = s.Opt.Value[i].String()
						}
					} else {
						d.Opt = nil
					}
				}
				if s.Ptr != nil {
					if *s.Ptr != nil {
						d.Ptr = make([]int64, len(*s.Ptr))
						for i := range *s.Ptr {
							d.Ptr[i] = (*s.Ptr)[i].Unix()
						}
					} else {
						d.Ptr = nil
					}
				} else {
					d.Ptr = nil
				}
				if s.Numbers != nil {
					d.Numbers = make([]int64, len(s.Numbers))
					for i := range s.Numbers {
						d.Numbers[i] = int64(s.Numbers[i])
					}
				} else {
					d.Numbers = nil
				}
			}`,
		},
		{
			name: "getters/slice-key-priority",
			code: `
			import "time"

			type Receiver struct { A []string }
			type Argument struct { A []time.Time }

			func FromSlice(s []time.Time) []string { return nil }
			`,
			gett: map[string]map[string]string{
				"time.Time":   {"string": ".String()"},
				"[]time.Time": {"[]string": "FromSlice({{.}})"},
			},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A = FromSlice(s.A)
			}`,
		},
		{
			name: "wrapper -> getter",
			code: `
			import "net/url"

			type Receiver struct {
		 		X string
				Y string
			}
			type Argument struct {
		 		X url.URL
				Y OptURL
			}
			type OptURL struct {
				Value url.URL
				Set bool
			}
			`,
			gett: map[string]map[string]string{"net/url.URL": {"string": ".String()"}},
			wrap: map[string]wrapper{"OptURL": {Value: ".Value", CopyIf: ".Set"}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.X = s.X.String()
				if s.Y.Set {
					d.Y = s.Y.Value.String()
				}
			}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("-- %s --", tt.name) // Usefull in vim errors.
			dir := t.TempDir()
			write := func(name, content string) {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("go.mod", "module example\n\ngo 1.26\n")
			write("types.go", "package example\n"+tt.code+"\n")
			tt.meth.Argument = "Argument"
			cfg := &config{
				Methods:  ordered[[]method]{{Key: "Receiver", Value: []method{tt.meth}}},
				Getters:  tt.gett,
				Wrappers: tt.wrap,
				Imports:  tt.imps,
			}
			files, err := generate(cfg, dir)
			if tt.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tt.fail) {
					t.Fatalf("want error %q, got %v", tt.fail, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := format.Source([]byte("// Code generated by copier. DO NOT EDIT.\n\npackage example\n\n" + tt.want))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(want), string(files[0].content)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFieldSetYAML(t *testing.T) {
	var cfg config
	err := yaml.Unmarshal([]byte(`
methods:
  Receiver:
    - arg: A
      except: true
    - arg: B
      skip: [X, Y]
    - arg: C
      except:
      mode: OnlySet
    - arg: [D]
    - arg:
        - E
        - ctx context.Context
        - n int
`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []method{
		{Argument: "A", Except: fieldSet{All: true}},
		{Argument: "B", Skip: fieldSet{Names: []string{"X", "Y"}}},
		{Argument: "C", Mode: OnlySet},
		{Argument: "D"},
		{Argument: "E", Params: []string{"ctx context.Context", "n int"}},
	}
	if diff := cmp.Diff(want, cfg.Methods[0].Value, cmp.AllowUnexported(method{}, fieldSet{})); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestArgYAMLErrors(t *testing.T) {
	for _, tt := range []struct{ yaml, fail string }{
		{"arg: []", "arg: at least one item required"},
		{"arg: [A, 1]", "arg: item 1 is not a string"},
		{"arg: {A: B}", "arg: expected string or list of strings"},
	} {
		var m method
		err := yaml.Unmarshal([]byte(tt.yaml), &m)
		if err == nil || !strings.Contains(err.Error(), tt.fail) {
			t.Errorf("%s: want error %q, got %v", tt.yaml, tt.fail, err)
		}
	}
}

func TestFieldsYAML(t *testing.T) {
	var m method
	err := yaml.Unmarshal([]byte(`
arg: A
fields:
  X: "fn({{.}})"
  Y:
    expr: "set(&{{.D}}, {{.S}}, n)"
    args: [n int]
`), &m)
	if err != nil {
		t.Fatal(err)
	}
	want := method{Argument: "A", Fields: map[string]field{
		"X": {Expr: "fn({{.}})"},
		"Y": {Expr: "set(&{{.D}}, {{.S}}, n)", Args: []string{"n int"}},
	}}
	if diff := cmp.Diff(want, m, cmp.AllowUnexported(method{}, fieldSet{})); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
