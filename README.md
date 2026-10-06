# Copier

Struct fields copying methods generator.

Makes struct methods with copying all of the fields except explicitly excluded.
Supports converting diffirent types with custom templates.

- [Quick start](#quick-start)
- [Running](#running)
- [Config](#config)
- [Methods](#methods)
- [Fields matching](#fields-matching)
- [Types conversion](#types-conversion)
- [Wrappers](#wrappers)
- [Getters](#getters)
- [OnlySet mode](#onlyset-mode)
- [Fields override](#fields-override)
- [Imports](#imports)

## Quick start

Types of the package:

```go
package app

//go:generate go run github.com/shagohead/copier@latest -f copier.yaml

type User struct {
	ID    int64
	Name  string
	Email string
}

type UserRow struct {
	ID    int64
	Name  string
	Email string
}
```

`copier.yaml` in the same directory:

```yaml
methods:
  UserRow:          # Receiver type.
    - arg: User     # Copy from User into UserRow.
    - arg: User
      into: true    # Copy from UserRow into User.
```

`go generate ./...` writes `copier.go` into the package:

```go
package app

func (d *UserRow) CopyFromUser(s *User) {
	d.ID = s.ID
	d.Name = s.Name
	d.Email = s.Email
}

func (s *UserRow) CopyIntoUser(d *User) {
	d.ID = s.ID
	d.Name = s.Name
	d.Email = s.Email
}
```

## Running

```sh
# Generate methods described in config file.
copier -f copier.yaml

# Generate single method in the current directory package:
# receiver type, argument type and optional -i for copying into argument.
copier -r UserRow -a User
copier -r UserRow -a example.com/app/models.User -i
```

Generated file is always `copier.go` in the package of receiver types.
It is replaced completely, so methods defined with `-r`/`-a` replace all methods defined with config file.
Files are written only after all of them are generated successfully,
and outdated `copier.go` (which does not compile anymore) does not break generation.

## Config

Methods of the package containing config file:

```yaml
methods:
  ReceiverType:
    - arg: ArgumentType
```

Methods of multiple packages: keys are subdirectories relative to config file,
or fully qualified package paths.

```yaml
packages:
  first:
    methods:
      Receiver:
        - arg: Argument
        - arg: example.com/app/second.Receiver
  second:
    methods:
      Receiver:
        - arg: Argument
```

Config can have only one of `methods` or `packages`. Other sections
(`wrappers`, `getters`, `imports`) are shared by all packages.
See [internal/examples](internal/examples) for complete examples.

## Methods

Each receiver has list of methods, each method has argument type.
Receiver types are always from the generated package. Argument type is either
type of the same package (`User`) or fully qualified (`example.com/app/models.User`).

| Property | Description |
|----------|-------------|
| `arg`    | Argument type, or list of argument type and [additional arguments](#fields-override). |
| `into`   | Copy from receiver into argument, instead of from argument into receiver. |
| `mode`   | `FullCopy` (default) or [`OnlySet`](#onlyset-mode). |
| `rename` | Map of receiver field names into argument field names. |
| `except` | List of destination fields not copied, or `true` for all unmatched. |
| `skip`   | List of source fields not copied, or `true` for all unmatched. |
| `fields` | [Overrides](#fields-override) of fields copying by destination field names. |

Method name is `CopyFrom` or `CopyInto` (`SetFrom`/`SetInto` in `OnlySet` mode)
followed by argument type name. Type name is omitted if it is the same as receiver
type name: `func (d *User) CopyFrom(s *models.User)`.
Destination is always named `d`, and source is `s`.

## Fields matching

Fields are matched by name, in the order of receiver fields.
**Matching is strict**: each destination field should have a source field and each
source field should be copied, otherwise generation fails with an error, like:

```
destination field UpdatedAt has no source field, add it into except or rename
```

```go
type Profile struct {
	FullName  string
	Email     string
	UpdatedAt time.Time
}

type ProfileRequest struct {
	Name     string
	Email    string
	Password string
}
```

```yaml
methods:
  Profile:
    - arg: ProfileRequest
      rename:
        FullName: Name    # Receiver field: argument field.
      except: [UpdatedAt] # Destination fields which are not copied.
      skip: [Password]    # Source fields which are not copied.
```

```go
func (d *Profile) CopyFromProfileRequest(s *ProfileRequest) {
	d.FullName = s.Name
	d.Email = s.Email
}
```

`except: true` ignores all destination fields without source,
but still requires copying of all source fields.
`skip: true` ignores all source fields without destination,
but still requires all destination fields to have source.
They cannot be both `true`.

## Types conversion

Values are assigned directly when possible, otherwise they are converted:

- basic types of the same kind (integers, floats, strings, booleans) and their aliases;
- types with the same underlying type;
- slices and arrays with the same element type.

```go
type UserID int64

type Account struct {
	ID    UserID
	Age   int64
	Tags  []string
	Token [16]byte
}

type AccountDTO struct {
	ID    int64
	Age   int32
	Tags  []string
	Token []byte
}
```

```go
func (d *Account) CopyFromAccountDTO(s *AccountDTO) {
	d.ID = UserID(s.ID)
	d.Age = int64(s.Age)
	d.Tags = s.Tags
	d.Token = [16]byte(s.Token)
}
```

Conversion of integer into string, or float into integer is not allowed.
Note that conversion of slice into array panics if slice is shorter than array.
Array is copied into slice with `slices.Clone(s.Token[:])`.

Slices with different element types are copied with converting each element:

```go
if s.Values != nil {
	d.Values = make([]int64, len(s.Values))
	for i := range s.Values {
		d.Values[i] = int64(s.Values[i])
	}
} else {
	d.Values = nil
}
```

## Wrappers

Wrappers are types holding a value, which cannot be assigned directly,
like `sql.NullString` or optional types of generated API clients.

```yaml
wrappers:
  OptString:               # Type of the generated package.
    value: .Value          # Field with value.
    copyif: .Set           # Copy value only if this is true.
  OptNilInt64:
    value: .Value
    valid: "!{{.}}.Null"   # Value is not empty if this is true.
    copyif: .Set
  database/sql.NullString: # Type of another package.
    value: .String
    valid: .Valid
  database/sql.NullInt64:
    value: .Int64
    valid: .Valid
```

| Property | Description |
|----------|-------------|
| `value`  | Expression of the wrapped value. |
| `type`   | Type of value. Required only if `value` is not a fields selector (like a method call). |
| `copyif` | Boolean expression: source is copied only if it is true; destination gets it as `true`. |
| `valid`  | Boolean expression of value non-emptiness. |

Expressions are [text/template](https://pkg.go.dev/text/template) templates, where `{{.}}` is the field.
Expression starting with dot is a shorthand: `.Value` is the same as `{{.}}.Value`.
Destination `copyif` and `valid` should be assignable, possibly negated: `.Valid`, `!{{.}}.Null`.

```go
type PatchRequest struct {
	Name  OptString
	Age   OptNilInt64
	Score *int64
}

type PatchRow struct {
	Name  sql.NullString
	Age   sql.NullInt64
	Score sql.NullInt64
}
```

```go
func (d *PatchRow) CopyFromPatchRequest(s *PatchRequest) {
	if s.Name.Set {
		d.Name.Valid = s.Name.Value != ""
		d.Name.String = s.Name.Value
	}
	if s.Age.Set {
		d.Age.Valid = !s.Age.Null
		d.Age.Int64 = s.Age.Value
	}
	d.Score.Valid = s.Score != nil
	if s.Score != nil {
		d.Score.Int64 = *s.Score
	}
}
```

Validity of destination wrapper is taken from:

- `valid` of source wrapper;
- `!= nil` for pointers, slices and maps;
- `!= ""` for strings, since empty string is always empty value;
- `true` for other types, since zero number or `false` is not always empty value.

Source wrapper with `valid` copied into pointer, slice or map sets it to `nil` for invalid values.
Wrapper keys may be fully qualified (`example.com/app/types.OptString`),
qualified with package name (`types.OptString`), or type names of the generated package.

## Getters

Getters are expressions of alternative types of source values.
They are used, when source value cannot be assigned or converted into destination type.

```yaml
getters:
  time.Time:                    # Source type.
    int64: .Unix()              # Alternative type: expression.
    string: .Format("2006-01-02")
  net/url.URL:
    string: .String()
  "[]time.Time":                # Slice types are supported too.
    "[]string": formatTimes({{.}})
```

```go
type Event struct {
	At      int64
	Link    string
	History []int64
}

type EventDTO struct {
	At      time.Time
	Link    url.URL
	History []time.Time
}
```

```go
func (d *Event) CopyFromEventDTO(s *EventDTO) {
	d.At = s.At.Unix()
	d.Link = s.Link.String()
	if s.History != nil {
		d.History = make([]int64, len(s.History))
		for i := range s.History {
			d.History[i] = s.History[i].Unix()
		}
	} else {
		d.History = nil
	}
}
```

Getters are applied to values of wrappers too: `s.Link.Value.String()`.
Getter of exactly matching type is preferred over converted ones,
and getter of slice type is preferred over converting each element.

## OnlySet mode

Generates method setting boolean destination fields, if source fields are *set*:
wrappers with `copyif`, or not nil slices and maps.
Useful to know which fields of a patch request should be updated.

```go
type Patch struct {
	Name OptString
	Age  OptNilInt64
	Tags []string
}

type PatchFields struct {
	Name bool
	Age  bool
	Tags bool
}
```

```yaml
methods:
  PatchFields:
    - arg: Patch
      mode: OnlySet
```

```go
func (d *PatchFields) SetFromPatch(s *Patch) {
	d.Name = s.Name.Set
	d.Age = s.Age.Set
	d.Tags = s.Tags != nil
}
```

## Fields override

`fields` overrides copying of destination fields with expression templates:

- template of the value, where `{{.}}` (or `{{.S}}`) is the source field:
  `normalizeStatus({{.}})`;
- template of the whole statement, if it uses destination field `{{.D}}`:
  `parse(&{{.D}}, {{.S}})`.

Overridden field does not need source field, if template does not use it.
Templates may use additional method arguments, defined in `arg` list or in field
`args`, in format `name path/to/package.Type`. Same arguments are merged.

```go
type Order struct {
	Total     int64
	Status    string
	CreatedAt time.Time
}

type OrderRequest struct {
	Total  int64
	Status string
}
```

```yaml
methods:
  Order:
    - arg: [OrderRequest, "now time.Time"]
      fields:
        Status: normalizeStatus({{.}})
        CreatedAt:
          expr: "{{.D}} = now.In(loc)"
          args: ["loc *time.Location"]
```

```go
func (d *Order) CopyFromOrderRequest(s *OrderRequest, now time.Time, loc *time.Location) {
	d.Total = s.Total
	d.Status = normalizeStatus(s.Status)
	d.CreatedAt = now.In(loc)
}
```

Packages used only in expressions are not imported into generated file.
Packages of additional arguments types are imported, but not loaded,
so their types are not checked.

## Imports

Import names are package names.
Packages, which are not loaded (like packages of additional arguments),
are imported with names guessed from their paths.
Actual package name may differ from its path, so names of such packages
can be defined explicitly:

```yaml
methods:
  Order:
    - arg: [OrderRequest, "db *example.com/app/internal/firestore/apiv1.Client"]

imports:
  firestore: example.com/app/internal/firestore/apiv1
```

```go
import firestore "example.com/app/internal/firestore/apiv1"

func (d *Order) CopyFromOrderRequest(s *OrderRequest, db *firestore.Client) {
	d.Total = s.Total
}
```

## TODO

- Profile.
- Use inmemory FS for testing & benchmarks.
- Add benchmarks for each feature. Commit stats.
