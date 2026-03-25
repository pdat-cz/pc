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
	MBus     AddrKind = "mbus"
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

// DiagnosticResult represents a connectivity test result
type DiagnosticResult struct {
	Success      bool          // Overall success
	Duration     time.Duration // Time taken
	Error        error         // Error if any

	// Semantic stages (optional, protocol-specific)
	HandshakeOK  *bool  // Low-level handshake (E5 for M-Bus, TCP connect for Modbus)
	DataReceived *bool  // Application-level data response

	// Additional protocol-specific details
	Details map[string]interface{}
}

// Diagnoser performs protocol connectivity diagnostics
type Diagnoser interface {
	// Diagnose tests connectivity with current client configuration
	// The interpretation of params is protocol-specific
	Diagnose(ctx context.Context, params map[string]interface{}) (DiagnosticResult, error)
}
