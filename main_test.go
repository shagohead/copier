package main

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestGenerate(t *testing.T) {
	for _, tt := range []struct {
		name string
		meth method
		code string
		wrap map[string]wrapper
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
			meth: method{Rename: map[string]string{"A": "B"}, Except: []string{"C"}, Skip: []string{"D"}},
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
			meth: method{Skip: []string{"X"}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: "skip: unknown field X",
		},
		{
			name: "unknown except",
			meth: method{Except: []string{"X"}},
			code: `
			type Receiver struct{}
			type Argument struct{}
			`,
			fail: "except: unknown field X",
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
			name: "wrapper/dst-valid",
			code: `
			type Receiver struct { A Int32X }
			type Argument struct { A int32 }
			type Int32X struct { Int32Value int32; Valid bool }
			`,
			wrap: map[string]wrapper{"example.Int32X": {
				Value: ".Int32Value",
				Valid: ".Valid",
			}},
			want: `func (d *Receiver) CopyFromArgument(s *Argument) {
				d.A.Valid = true
				d.A.Int32Value = s.A
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
				Wrappers: tt.wrap,
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
			want, err := format.Source([]byte("package example\n\n" + tt.want))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(want), string(files[0].content)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
