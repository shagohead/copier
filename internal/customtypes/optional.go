package customtypes

import (
	"time"
)

type OptInt32 struct {
	Value int32
	Set   bool
}

type OptNilInt32 struct {
	Value int32
	Set   bool
	Null  bool
}

type OptTime struct {
	Value time.Time
	Set   bool
}

type OptNilTime struct {
	Value time.Time
	Set   bool
	Null  bool
}

type StructFields struct {
	EventID OptInt32
	ValueID OptInt32
}
