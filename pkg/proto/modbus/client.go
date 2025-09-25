package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pdat-cz/pc/pkg/addr"
	"github.com/pdat-cz/pc/pkg/proto"
)

type Client struct {
	mu      sync.Mutex
	netw    string
	addr    string
	unitID  uint8
	conn    net.Conn
	timeout time.Duration
	transID uint16
}

func NewClient() *Client { return &Client{} }

func (c *Client) Open(ctx context.Context, uri string) error {
	netw, addrStr, unit, timeout, err := addr.ParseURI(uri)
	if err != nil {
		return err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, netw, addrStr)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.netw = netw
	c.addr = addrStr
	c.unitID = unit
	c.conn = conn
	c.timeout = timeout
	c.transID = uint16(time.Now().UnixNano())
	c.mu.Unlock()
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *Client) nextTID() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transID++
	return c.transID
}

// Read implements holding and input registers for now; coils/discretes can be added later.
func (c *Client) Read(ctx context.Context, specs []proto.ReadSpec) ([]proto.Value, error) {
	vals := make([]proto.Value, 0, len(specs))
	for _, s := range specs {
		var (
			fc    byte
			count = s.Count
		)
		switch s.Kind {
		case proto.Holding:
			fc = 0x03
			if count == 0 {
				count = 1
			}
		case proto.Input:
			fc = 0x04
			if count == 0 {
				count = 1
			}
		default:
			return nil, fmt.Errorf("unsupported kind for read: %s", s.Kind)
		}
		data, err := c.readRegisters(ctx, fc, s.Addr, count)
		if err != nil {
			return nil, err
		}
		v := proto.Value{Spec: s, Bytes: data, TS: time.Now()}
		if s.Format != "" {
			if dec, err := decodeFormat(data, s.Format); err == nil {
				v.Decoded = dec
			}
		}
		vals = append(vals, v)
	}
	return vals, nil
}

func (c *Client) Write(ctx context.Context, spec proto.ReadSpec, val any) error {
	switch spec.Kind {
	case proto.Holding:
		// only single register for now
		u16, err := toU16(val)
		if err != nil {
			return err
		}
		return c.writeSingleRegister(ctx, spec.Addr, u16)
	default:
		return fmt.Errorf("unsupported kind for write: %s", spec.Kind)
	}
}

func (c *Client) readRegisters(ctx context.Context, function byte, addr uint16, count uint16) ([]byte, error) {
	if count == 0 || count > 125 {
		return nil, fmt.Errorf("invalid count: %d", count)
	}
	pdu := make([]byte, 5)
	pdu[0] = function
	binary.BigEndian.PutUint16(pdu[1:], addr)
	binary.BigEndian.PutUint16(pdu[3:], count)
	resp, err := c.exchange(ctx, pdu)
	if err != nil {
		return nil, err
	}
	if len(resp) < 2 {
		return nil, errors.New("short response")
	}
	if resp[0] != function {
		if resp[0] == function|0x80 && len(resp) >= 2 {
			return nil, fmt.Errorf("exception code: %d", resp[1])
		}
		return nil, fmt.Errorf("unexpected function in resp: 0x%02x", resp[0])
	}
	byteCount := int(resp[1])
	if len(resp) < 2+byteCount {
		return nil, errors.New("short data")
	}
	data := make([]byte, byteCount)
	copy(data, resp[2:2+byteCount])
	return data, nil
}

func (c *Client) writeSingleRegister(ctx context.Context, addr uint16, value uint16) error {
	pdu := make([]byte, 5)
	pdu[0] = 0x06
	binary.BigEndian.PutUint16(pdu[1:], addr)
	binary.BigEndian.PutUint16(pdu[3:], value)
	resp, err := c.exchange(ctx, pdu)
	if err != nil {
		return err
	}
	if len(resp) < 5 || resp[0] != 0x06 {
		return fmt.Errorf("unexpected write response")
	}
	// echo of addr+value expected
	return nil
}

func (c *Client) exchange(ctx context.Context, pdu []byte) ([]byte, error) {
	c.mu.Lock()
	conn := c.conn
	unit := c.unitID
	timeout := c.timeout
	c.mu.Unlock()
	if conn == nil {
		return nil, errors.New("not open")
	}

	// Build MBAP + PDU
	tid := c.nextTID()
	head := make([]byte, 7)
	binary.BigEndian.PutUint16(head[0:], tid)
	binary.BigEndian.PutUint16(head[2:], 0) // protocol id
	// length = unit id + pdu length
	binary.BigEndian.PutUint16(head[4:], uint16(1+len(pdu)))
	head[6] = unit
	adu := append(head, pdu...)
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(adu); err != nil {
		return nil, err
	}
	// Read header
	headBuf := make([]byte, 7)
	if _, err := readFullWithCtx(ctx, conn, headBuf); err != nil {
		return nil, err
	}
	// check protocol id
	if binary.BigEndian.Uint16(headBuf[2:4]) != 0 {
		return nil, errors.New("invalid protocol id")
	}
	ln := binary.BigEndian.Uint16(headBuf[4:6])
	if ln == 0 {
		return nil, errors.New("zero length")
	}
	buf := make([]byte, ln)
	if _, err := readFullWithCtx(ctx, conn, buf); err != nil {
		return nil, err
	}
	// buf[0] is unit; rest is PDU
	if len(buf) < 2 {
		return nil, errors.New("short adu body")
	}
	pduResp := buf[1:]
	return pduResp, nil
}

func readFullWithCtx(ctx context.Context, conn net.Conn, b []byte) (int, error) {
	deadline := time.Now().Add(5 * time.Second)
	_ = conn.SetReadDeadline(deadline)
	total := 0
	for total < len(b) {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		n, err := conn.Read(b[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func decodeFormat(b []byte, format string) (any, error) {
	switch format {
	case "u16":
		if len(b) < 2 {
			return nil, fmt.Errorf("need 2 bytes")
		}
		return binary.BigEndian.Uint16(b[:2]), nil
	case "s16":
		if len(b) < 2 {
			return nil, fmt.Errorf("need 2 bytes")
		}
		return int16(binary.BigEndian.Uint16(b[:2])), nil
	case "u32":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return binary.BigEndian.Uint32(b[:4]), nil
	case "s32":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return int32(binary.BigEndian.Uint32(b[:4])), nil
	case "f32be":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bits := binary.BigEndian.Uint32(b[:4])
		return math.Float32frombits(bits), nil
	case "f32le":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bits := binary.LittleEndian.Uint32(b[:4])
		return math.Float32frombits(bits), nil
	default:
		return nil, fmt.Errorf("unknown format: %s", format)
	}
}

func toU16(v any) (uint16, error) {
	switch t := v.(type) {
	case uint8:
		return uint16(t), nil
	case uint16:
		return t, nil
	case int:
		if t < 0 || t > 0xFFFF {
			return 0, fmt.Errorf("out of range")
		}
		return uint16(t), nil
	case int64:
		if t < 0 || t > 0xFFFF {
			return 0, fmt.Errorf("out of range")
		}
		return uint16(t), nil
	case float64:
		if t < 0 || t > 0xFFFF {
			return 0, fmt.Errorf("out of range")
		}
		return uint16(t), nil
	case string:
		// try parse decimal or hex 0x
		if strings.HasPrefix(t, "0x") || strings.HasPrefix(t, "0X") {
			u, err := strconv.ParseUint(t[2:], 16, 16)
			if err != nil {
				return 0, err
			}
			return uint16(u), nil
		}
		u, err := strconv.ParseUint(t, 10, 16)
		if err != nil {
			return 0, err
		}
		return uint16(u), nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}
