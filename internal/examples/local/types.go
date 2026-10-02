package local

import (
	"database/sql"
	"time"

	pkgAlias "github.com/shagohead/copier/internal/customtypes"
)

//go:generate go run ../../../ -f copier.yaml

type Receiver struct {
	SomeField string
	IntField  int32
}

type Argument struct {
	SomeField string
	IntField  int32
}

type Destination struct {
	IntField  int32
	SomeField string
}

type StructFields struct {
	EventID sql.NullInt32
	ValueID sql.NullInt32
}

type CustomString string

type ErrorsMap map[string][]string

type ExtendedReceiver struct {
	A int32
	B time.Time
	C string
	D int64
	E CustomString
	F ErrorsMap
	G sql.NullInt32
}

type ExtendedArgument struct {
	A pkgAlias.OptInt32
	R time.Time
	D int
	E string
	F map[string][]string
	G pkgAlias.OptNilInt32
}
