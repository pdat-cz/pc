package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/pdat-cz/pc/pkg/addr"
	"github.com/pdat-cz/pc/pkg/proto"
)

type catOutput struct {
	Spec    proto.ReadSpec `json:"spec"`
	Read    readSection    `json:"read"`
	Decoded decodedSection `json:"decoded,omitempty"`
	TS      time.Time      `json:"ts"`
}

type readSection struct {
	Bytes string `json:"bytes"`
	Bits  string `json:"bits,omitempty"`
}

type decodedSection struct {
	Format string `json:"format,omitempty"`
	Bytes  string `json:"bytes,omitempty"`
	Bits   string `json:"bits,omitempty"`
	Value  any    `json:"value,omitempty"`
}

// formatBytes returns a hex string with spaces between bytes, e.g. "01 ab 7f".
func formatBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// Pre-size: 2 chars per byte + 1 space between => n*3-1
	out := make([]byte, 0, len(b)*3-1)
	hexDigits := "0123456789abcdef"
	for i, by := range b {
		out = append(out, hexDigits[by>>4])
		out = append(out, hexDigits[by&0x0f])
		if i < len(b)-1 {
			out = append(out, ' ')
		}
	}
	return string(out)
}

// bitsMSB returns MSB-first bits per byte, spaced between bytes, optionally limited to 'limit' bits.
func bitsMSB(b []byte, limit uint16) string {
	if len(b) == 0 {
		return ""
	}
	maxBits := int(limit)
	if maxBits == 0 {
		maxBits = len(b) * 8
	}
	out := make([]byte, 0, maxBits+len(b)-1)
	bitsEmitted := 0
	for bi, by := range b {
		for i := 7; i >= 0 && bitsEmitted < maxBits; i-- {
			if (by>>uint(i))&0x01 == 1 {
				out = append(out, '1')
			} else {
				out = append(out, '0')
			}
			bitsEmitted++
		}
		if bitsEmitted >= maxBits {
			break
		}
		if bi < len(b)-1 && bitsEmitted < maxBits {
			out = append(out, ' ')
		}
	}
	return string(out)
}

// bitsLSB returns LSB-first bits per byte (as Modbus packs coils), spaced between bytes, limited by 'limit' bits.
func bitsLSB(b []byte, limit uint16) string {
	if len(b) == 0 {
		return ""
	}
	maxBits := int(limit)
	if maxBits == 0 {
		maxBits = len(b) * 8
	}
	out := make([]byte, 0, maxBits+len(b)-1)
	bitsEmitted := 0
	for bi, by := range b {
		for i := 0; i < 8 && bitsEmitted < maxBits; i++ {
			if (by>>uint(i))&0x01 == 1 {
				out = append(out, '1')
			} else {
				out = append(out, '0')
			}
			bitsEmitted++
		}
		if bitsEmitted >= maxBits {
			break
		}
		if bi < len(b)-1 && bitsEmitted < maxBits {
			out = append(out, ' ')
		}
	}
	return string(out)
}

// decodedBytes mirrors the byte/word transformations in decodeFormat and returns the
// exact byte slice that was fed into the numeric conversion.
func decodedBytes(b []byte, format string) ([]byte, error) {
	switch format {
	case "":
		return nil, nil
	case "u16", "s16":
		if len(b) < 2 {
			return nil, fmt.Errorf("need 2 bytes")
		}
		return b[:2], nil
	case "u32", "s32", "f32", "f32be", "f32abcd":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return b[:4], nil
	case "u32le", "s32le", "f32le":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return b[:4], nil
	case "u32bs": // BA DC
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return []byte{b[1], b[0], b[3], b[2]}, nil
	case "u32ws", "s32ws": // CD AB
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return []byte{b[2], b[3], b[0], b[1]}, nil
	case "u32wbs": // DC BA
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return []byte{b[3], b[2], b[1], b[0]}, nil
	case "f32ws", "f32cdab": // CD AB
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return []byte{b[2], b[3], b[0], b[1]}, nil
	case "f32bs", "f32badc": // BA DC
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return []byte{b[1], b[0], b[3], b[2]}, nil
	case "f32wbs", "f32dcba": // DC BA
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return []byte{b[3], b[2], b[1], b[0]}, nil
	default:
		return nil, fmt.Errorf("unknown format: %s", format)
	}
}

// perSpecTimeout returns the per-address read timeout from the URI.
// Explicit ?timeout= takes priority; otherwise defaults by baud rate
// per EN 13757-2 recommendations (2 s at 2400 baud, 800 ms at ≥9600 baud).
func perSpecTimeout(uri string) time.Duration {
	u, err := url.Parse(uri)
	if err != nil {
		return 2 * time.Second
	}
	q := u.Query()
	if v := q.Get("timeout"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	baud, _ := strconv.Atoi(q.Get("baud"))
	if baud >= 9600 {
		return 800 * time.Millisecond
	}
	return 2 * time.Second
}

func runCat(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: pc cat <uri> <addr> [addr...]")
	}
	uri := args[0]
	specs := make([]proto.ReadSpec, 0, len(args)-1)
	for _, a := range args[1:] {
		rs, err := addr.ParseReadSpec(a)
		if err != nil {
			return fmt.Errorf("invalid addr %q: %w", a, err)
		}
		specs = append(specs, rs)
	}
	c, err := newClientFromURI(uri)
	if err != nil {
		return err
	}
	// Scale timeout: 3 s for connection open + per-spec timeout × number of addresses.
	totalTimeout := 3*time.Second + perSpecTimeout(uri)*time.Duration(len(specs))
	ctx, cancel := context.WithTimeout(context.Background(), totalTimeout)
	defer cancel()
	if err := c.Open(ctx, uri); err != nil {
		return fmt.Errorf("open %s: %w", uri, err)
	}
	defer func() { _ = c.Close() }()
	vals, err := c.Read(ctx, specs)
	if err != nil {
		return fmt.Errorf("read %v: %w", args[1:], err)
	}
	enc := json.NewEncoder(os.Stdout)
	for _, v := range vals {
		out := catOutput{Spec: v.Spec, TS: v.TS}

		// Read section: raw bytes/bits
		out.Read.Bytes = formatBytes(v.Bytes)
		if v.Spec.Kind == proto.Coil || v.Spec.Kind == proto.Discrete {
			out.Read.Bits = bitsLSB(v.Bytes, v.Spec.Count)
		} else {
			out.Read.Bits = bitsMSB(v.Bytes, 0)
		}

		// Decoded section: format, value, bytes/bits after transformation
		out.Decoded.Format = v.Spec.Format
		out.Decoded.Value = v.Decoded
		if v.Spec.Format != "" {
			if db, derr := decodedBytes(v.Bytes, v.Spec.Format); derr == nil && len(db) > 0 {
				out.Decoded.Bytes = formatBytes(db)
				out.Decoded.Bits = bitsMSB(db, 0)
			}
		}

		// Prevent JSON encoding errors on NaN/Inf by returning a clear error instead
		switch fv := out.Decoded.Value.(type) {
		case float32:
			if math.IsNaN(float64(fv)) || math.IsInf(float64(fv), 0) {
				return fmt.Errorf("decoded value is %v (non-finite) for %v; format=%s, raw-bytes=%s", fv, v.Spec, v.Spec.Format, formatBytes(v.Bytes))
			}
		case float64:
			if math.IsNaN(fv) || math.IsInf(fv, 0) {
				return fmt.Errorf("decoded value is %v (non-finite) for %v; format=%s, raw-bytes=%s", fv, v.Spec, v.Spec.Format, formatBytes(v.Bytes))
			}
		}

		if err := enc.Encode(out); err != nil {
			return err
		}
	}
	return nil
}
