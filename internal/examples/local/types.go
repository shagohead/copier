package local

import (
	"database/sql"
	"net/url"
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

type OptURL struct {
	Value url.URL
	Set   bool
}

type ExtendedReceiver struct {
	BaseFromWrapper   int32
	Target            time.Time
	Unmodified        string
	IntToInt64        int64
	BaseToAlias       CustomString
	MapToAlias        ErrorsMap
	Wrapper2Wrapper   sql.NullInt32
	FromWrappedGetter string
	Int64FromTime     int64
	StringFromTime    string
}

type ExtendedArgument struct {
	BaseFromWrapper   pkgAlias.OptInt32
	Source            time.Time
	IntToInt64        int
	BaseToAlias       string
	MapToAlias        map[string][]string
	Wrapper2Wrapper   pkgAlias.OptNilInt32
	FromWrappedGetter OptURL
	Int64FromTime     time.Time
	StringFromTime    time.Time
}
