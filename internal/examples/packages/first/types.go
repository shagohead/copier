package first

import (
	"database/sql"
	"time"

	pkgAlias "github.com/shagohead/copier/internal/customtypes"
)

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

type ExtendedReceiver struct {
	A int32
	B time.Time
	C string
	D int64
}

type ExtendedArgument struct {
	A pkgAlias.OptInt32
	R time.Time
	D int
}
