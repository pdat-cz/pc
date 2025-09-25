package proto

import (
	"context"
	"time"
)

type AddrKind string

const (
	Coil     AddrKind = "coil"
	Discrete AddrKind = "discrete"
	Holding  AddrKind = "holding"
	Input    AddrKind = "input"
)

type ReadSpec struct {
	Kind   AddrKind `json:"kind"`
	Addr   uint16   `json:"addr"`
	Count  uint16   `json:"count,omitempty"`
	Format string   `json:"format,omitempty"`
}

type Value struct {
	Spec    ReadSpec  `json:"spec"`
	Bytes   []byte    `json:"bytes"`
	Decoded any       `json:"decoded,omitempty"`
	TS      time.Time `json:"ts"`
}

type Client interface {
	Open(ctx context.Context, uri string) error
	Close() error

	Read(ctx context.Context, specs []ReadSpec) ([]Value, error)
	Write(ctx context.Context, spec ReadSpec, val any) error
}
