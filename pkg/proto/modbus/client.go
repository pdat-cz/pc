package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go/ast"
	"go/parser"
	"go/token"

	"github.com/pdat-cz/pc/pkg/addr"
	"github.com/pdat-cz/pc/pkg/proto"
	serial "go.bug.st/serial"
)

const (
	fcReadHolding   = 0x03
	fcReadInput     = 0x04
	fcWriteSingle   = 0x06
	fcWriteMultiple = 0x10
	mbapHeaderLen = 7
	modbusProtoID = 0
)

type Client struct {
	mu      sync.Mutex
	netw    string
	addr    string
	unitID  uint8
	conn    net.Conn           // TCP connection (when netw=="tcp")
	port    io.ReadWriteCloser // RTU serial port (when netw=="rtu")
	timeout time.Duration
	transID uint16
}

// debugf writes debug logs to stderr when PC_DEBUG environment variable is set (non-empty).
func debugf(format string, args ...any) {
	if os.Getenv("PC_DEBUG") == "" {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[pc][modbus] "+format+"\n", args...)
}

func NewClient() *Client { return &Client{} }

func (c *Client) Open(ctx context.Context, uri string) error {
	netw, addrStr, unit, timeout, err := addr.ParseURI(uri)
	//fmt.Println("netw", netw, "addrStr", addrStr, "unit", unit, "timeout", timeout)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.netw = netw
	c.addr = addrStr
	c.unitID = unit
	c.timeout = timeout
	c.transID = uint16(time.Now().UnixNano())
	if netw == "tcp" {
		debugf("open tcp addr=%s unit=%d timeout=%s", addrStr, unit, timeout)
		var d net.Dialer
		conn, err := d.DialContext(ctx, netw, addrStr)
		if err != nil {
			return fmt.Errorf("modbus tcp dial %s: %w", addrStr, err)
		}
		c.conn = conn
		c.port = nil
		return nil
	}
	if netw == "rtu" {
		// Parse device and query
		//u, err := url.Parse("dummy://" + strings.TrimPrefix(addrStr, "/"))
		//if err != nil {
		//	return err
		//}
		//dev := "/" + u.Path // restore leading slash
		u, err := url.Parse("dummy:" + addrStr)
		if err != nil {
			return err
		}
		dev := u.Path
		//fmt.Printf("[pc][modbus] rtu uri path=%q rawQuery=%q\n", u.Path, u.RawQuery)
		q := u.Query()
		//fmt.Printf("[pc][modbus] rtu params baud=%q data=%q parity=%q stop=%q unit=%d timeout=%s\n",
		//	q.Get("baud"), q.Get("data"), q.Get("parity"), q.Get("stop"), unit, timeout)
		baud := 9600
		if v := q.Get("baud"); v != "" {
			if n, e := strconv.Atoi(v); e == nil {
				baud = n
			}
		}
		dataBits := 8
		if v := q.Get("data"); v != "" {
			if n, e := strconv.Atoi(v); e == nil {
				dataBits = n
			}
		}
		parity := serial.NoParity
		switch strings.ToUpper(q.Get("parity")) {
		case "E", "EVEN":
			parity = serial.EvenParity
		case "O", "ODD":
			parity = serial.OddParity
		default:
			parity = serial.NoParity
		}
		stopBits := serial.OneStopBit
		switch q.Get("stop") {
		case "1":
			stopBits = serial.OneStopBit
		case "2":
			stopBits = serial.TwoStopBits
		}
		mode := &serial.Mode{BaudRate: baud, DataBits: dataBits, Parity: parity, StopBits: stopBits}

		debugf("open rtu dev=%s baud=%d data=%d parity=%v stop=%v unit=%d timeout=%s", dev, baud, dataBits, parity, stopBits, unit, timeout)
		p, err := serial.Open(dev, mode)
		if err != nil {
			// Provide more context and common hints, and list available ports for guidance.
			ports, _ := serial.GetPortsList()
			var hint string
			if len(ports) > 0 {
				hint = fmt.Sprintf("; available ports: %s", strings.Join(ports, ", "))
			} else {
				hint = "; no serial ports detected on this system"
			}
			// Try to suggest a close match if user likely mistyped (e.g., 'O' vs '0').
			suggest := closestDevice(dev, ports)
			if suggest != "" && suggest != dev {
				hint = hint + fmt.Sprintf("; did you mean %s?", suggest)
			}
			if os.IsNotExist(err) {
				return fmt.Errorf("open serial %s: device not found%s: %w", dev, hint, err)
			}
			if os.IsPermission(err) {
				return fmt.Errorf("open serial %s: permission denied (try adding your user to 'dialout' group and re-login, or use sudo)%s: %w", dev, hint, err)
			}
			return fmt.Errorf("open serial %s (baud=%d data=%d parity=%v stop=%v)%s: %w", dev, baud, dataBits, parity, stopBits, hint, err)
		}
		if timeout > 0 {
			if pt, ok := any(p).(interface{ SetReadTimeout(time.Duration) error }); ok {
				_ = pt.SetReadTimeout(timeout)
				//fmt.Printf("[pc][modbus] serial read timeout set to %s\n", timeout)
			}
		}
		c.port = p
		c.conn = nil
		return nil
	}
	return fmt.Errorf("unsupported network: %s", netw)
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close()
	}
	if c.port != nil {
		return c.port.Close()
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
		// If count is not provided, infer needed registers from format
		if count == 0 {
			count = 1
			switch s.Format {
			case "u32", "u32le", "u32ws", "u32bs", "u32wbs", "s32", "s32le", "s32ws", "f32", "f32be", "f32le", "f32ws", "f32bs", "f32wbs", "f32abcd", "f32cdab", "f32badc", "f32dcba":
				count = 2
			}
		}
		switch s.Kind {
		case proto.Holding:
			fc = fcReadHolding
		case proto.Input:
			fc = fcReadInput
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
		// Check if format requires multiple registers
		if isMultiRegisterFormat(spec.Format) {
			regs, err := encodeMultiRegister(val, spec.Format)
			if err != nil {
				return err
			}
			return c.writeMultipleRegisters(ctx, spec.Addr, regs)
		}
		// Single register write (FC06)
		u16, err := toU16(val)
		if err != nil {
			return err
		}
		return c.writeSingleRegister(ctx, spec.Addr, u16)
	default:
		return fmt.Errorf("unsupported kind for write: %s", spec.Kind)
	}
}

// isMultiRegisterFormat returns true if the format requires more than one register
func isMultiRegisterFormat(format string) bool {
	switch format {
	case "u32", "u32le", "u32ws", "u32bs", "u32wbs",
		"s32", "s32le", "s32ws",
		"f32", "f32be", "f32le", "f32ws", "f32bs", "f32wbs",
		"f32abcd", "f32cdab", "f32badc", "f32dcba":
		return true
	}
	return false
}

// encodeMultiRegister encodes a value into multiple registers based on format
func encodeMultiRegister(val any, format string) ([]uint16, error) {
	// Parse the value to float64 first
	var f64 float64
	switch v := val.(type) {
	case float64:
		f64 = v
	case float32:
		f64 = float64(v)
	case int:
		f64 = float64(v)
	case int64:
		f64 = float64(v)
	case uint32:
		f64 = float64(v)
	case string:
		parsed, err := evalNumber(v)
		if err != nil {
			return nil, fmt.Errorf("invalid value %q: %w", v, err)
		}
		f64 = parsed
	default:
		return nil, fmt.Errorf("unsupported value type %T", val)
	}

	// Check for non-finite values
	if math.IsNaN(f64) || math.IsInf(f64, 0) {
		return nil, fmt.Errorf("invalid numeric value")
	}

	var bytes [4]byte

	switch format {
	// Unsigned 32-bit formats
	case "u32": // AB CD (big-endian)
		if f64 < 0 || f64 > math.MaxUint32 {
			return nil, fmt.Errorf("value out of range for u32")
		}
		binary.BigEndian.PutUint32(bytes[:], uint32(f64))
	case "u32le": // DC BA (little-endian)
		if f64 < 0 || f64 > math.MaxUint32 {
			return nil, fmt.Errorf("value out of range for u32")
		}
		binary.LittleEndian.PutUint32(bytes[:], uint32(f64))
	case "u32ws": // CD AB (word-swapped)
		if f64 < 0 || f64 > math.MaxUint32 {
			return nil, fmt.Errorf("value out of range for u32")
		}
		u := uint32(f64)
		bytes[0] = byte(u >> 8)
		bytes[1] = byte(u)
		bytes[2] = byte(u >> 24)
		bytes[3] = byte(u >> 16)
	case "u32bs": // BA DC (byte-swapped per word)
		if f64 < 0 || f64 > math.MaxUint32 {
			return nil, fmt.Errorf("value out of range for u32")
		}
		u := uint32(f64)
		bytes[0] = byte(u >> 16)
		bytes[1] = byte(u >> 24)
		bytes[2] = byte(u)
		bytes[3] = byte(u >> 8)
	case "u32wbs": // DC BA (word+byte swapped)
		if f64 < 0 || f64 > math.MaxUint32 {
			return nil, fmt.Errorf("value out of range for u32")
		}
		binary.LittleEndian.PutUint32(bytes[:], uint32(f64))

	// Signed 32-bit formats
	case "s32": // big-endian
		if f64 < math.MinInt32 || f64 > math.MaxInt32 {
			return nil, fmt.Errorf("value out of range for s32")
		}
		binary.BigEndian.PutUint32(bytes[:], uint32(int32(f64)))
	case "s32le":
		if f64 < math.MinInt32 || f64 > math.MaxInt32 {
			return nil, fmt.Errorf("value out of range for s32")
		}
		binary.LittleEndian.PutUint32(bytes[:], uint32(int32(f64)))
	case "s32ws": // word-swapped
		if f64 < math.MinInt32 || f64 > math.MaxInt32 {
			return nil, fmt.Errorf("value out of range for s32")
		}
		u := uint32(int32(f64))
		bytes[0] = byte(u >> 8)
		bytes[1] = byte(u)
		bytes[2] = byte(u >> 24)
		bytes[3] = byte(u >> 16)

	// Float32 formats
	case "f32", "f32be", "f32abcd": // AB CD (big-endian)
		bits := math.Float32bits(float32(f64))
		binary.BigEndian.PutUint32(bytes[:], bits)
	case "f32le", "f32dcba": // DC BA (little-endian)
		bits := math.Float32bits(float32(f64))
		binary.LittleEndian.PutUint32(bytes[:], bits)
	case "f32ws", "f32cdab": // CD AB (word-swapped)
		bits := math.Float32bits(float32(f64))
		bytes[0] = byte(bits >> 8)
		bytes[1] = byte(bits)
		bytes[2] = byte(bits >> 24)
		bytes[3] = byte(bits >> 16)
	case "f32bs", "f32badc": // BA DC (byte-swapped per word)
		bits := math.Float32bits(float32(f64))
		bytes[0] = byte(bits >> 16)
		bytes[1] = byte(bits >> 24)
		bytes[2] = byte(bits)
		bytes[3] = byte(bits >> 8)
	case "f32wbs": // DC BA (word+byte swapped, same as little-endian)
		bits := math.Float32bits(float32(f64))
		binary.LittleEndian.PutUint32(bytes[:], bits)

	default:
		return nil, fmt.Errorf("unsupported multi-register format: %s", format)
	}

	// Convert 4 bytes to 2 registers (big-endian register order)
	return []uint16{
		binary.BigEndian.Uint16(bytes[0:2]),
		binary.BigEndian.Uint16(bytes[2:4]),
	}, nil
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
	pdu[0] = fcWriteSingle
	binary.BigEndian.PutUint16(pdu[1:], addr)
	binary.BigEndian.PutUint16(pdu[3:], value)
	resp, err := c.exchange(ctx, pdu)
	if err != nil {
		return err
	}
	debugf("write response: len=%d data=%x", len(resp), resp)
	if len(resp) < 5 {
		return fmt.Errorf("unexpected write response: too short (got %d bytes, expected 5): %x", len(resp), resp)
	}
	if resp[0] != fcWriteSingle {
		// Check for exception response
		if resp[0] == fcWriteSingle|0x80 && len(resp) >= 2 {
			return fmt.Errorf("write exception code: %d", resp[1])
		}
		return fmt.Errorf("unexpected write response: wrong function code 0x%02x (expected 0x%02x): %x", resp[0], fcWriteSingle, resp)
	}
	// Verify echo of addr+value
	respAddr := binary.BigEndian.Uint16(resp[1:3])
	respVal := binary.BigEndian.Uint16(resp[3:5])
	if respAddr != addr || respVal != value {
		debugf("write echo mismatch: sent addr=%d val=%d, got addr=%d val=%d", addr, value, respAddr, respVal)
	}
	return nil
}

func (c *Client) writeMultipleRegisters(ctx context.Context, addr uint16, values []uint16) error {
	count := uint16(len(values))
	if count == 0 || count > 123 {
		return fmt.Errorf("invalid register count: %d (must be 1-123)", count)
	}
	byteCount := byte(count * 2)
	// PDU: FC(1) + addr(2) + count(2) + byteCount(1) + data(n*2)
	pdu := make([]byte, 6+int(byteCount))
	pdu[0] = fcWriteMultiple
	binary.BigEndian.PutUint16(pdu[1:], addr)
	binary.BigEndian.PutUint16(pdu[3:], count)
	pdu[5] = byteCount
	for i, v := range values {
		binary.BigEndian.PutUint16(pdu[6+i*2:], v)
	}
	resp, err := c.exchange(ctx, pdu)
	if err != nil {
		return err
	}
	debugf("write multiple response: len=%d data=%x", len(resp), resp)
	if len(resp) < 5 {
		return fmt.Errorf("unexpected write multiple response: too short (got %d bytes, expected 5): %x", len(resp), resp)
	}
	if resp[0] != fcWriteMultiple {
		if resp[0] == fcWriteMultiple|0x80 && len(resp) >= 2 {
			return fmt.Errorf("write multiple exception code: %d", resp[1])
		}
		return fmt.Errorf("unexpected write multiple response: wrong function code 0x%02x (expected 0x%02x): %x", resp[0], fcWriteMultiple, resp)
	}
	// Response should echo addr and count
	respAddr := binary.BigEndian.Uint16(resp[1:3])
	respCount := binary.BigEndian.Uint16(resp[3:5])
	if respAddr != addr || respCount != count {
		debugf("write multiple echo mismatch: sent addr=%d count=%d, got addr=%d count=%d", addr, count, respAddr, respCount)
	}
	return nil
}

func (c *Client) exchange(ctx context.Context, pdu []byte) ([]byte, error) {
	c.mu.Lock()
	netw := c.netw
	c.mu.Unlock()
	if netw == "tcp" {
		return c.exchangeTCP(ctx, pdu)
	}
	if netw == "rtu" {
		return c.exchangeRTU(ctx, pdu)
	}
	return nil, fmt.Errorf("unsupported network: %s", netw)
}

func (c *Client) exchangeTCP(ctx context.Context, pdu []byte) ([]byte, error) {
	debugf("tcp send fc=0x%02x len=%d", pdu[0], len(pdu))
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
	head := make([]byte, mbapHeaderLen)
	binary.BigEndian.PutUint16(head[0:], tid)
	binary.BigEndian.PutUint16(head[2:], modbusProtoID) // protocol id
	// length = unit id + pdu length
	binary.BigEndian.PutUint16(head[4:], uint16(1+len(pdu)))
	head[6] = unit
	adu := append(head, pdu...)
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(adu); err != nil {
		return nil, err
	}
	// Read header
	headBuf := make([]byte, mbapHeaderLen)
	if _, err := readFullWithCtx(ctx, conn, headBuf); err != nil {
		return nil, err
	}
	// check protocol id
	if binary.BigEndian.Uint16(headBuf[2:4]) != modbusProtoID {
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
	debugf("tcp recv fc=0x%02x len=%d", pduResp[0], len(pduResp))
	return pduResp, nil
}

func (c *Client) exchangeRTU(ctx context.Context, pdu []byte) ([]byte, error) {
	c.mu.Lock()
	port := c.port
	unit := c.unitID
	// timeout := c.timeout // ReadTimeout is set on port config
	c.mu.Unlock()
	if port == nil {
		return nil, errors.New("not open")
	}
	// Build RTU frame: [unit][pdu][crc16le]
	adu := make([]byte, 1+len(pdu)+2)
	adu[0] = unit
	copy(adu[1:], pdu)
	crc := crc16Modbus(adu[:1+len(pdu)])
	binary.LittleEndian.PutUint16(adu[1+len(pdu):], crc)
	debugf("rtu send fc=0x%02x len=%d", pdu[0], len(pdu))
	if _, err := port.Write(adu); err != nil {
		return nil, fmt.Errorf("serial write: %w", err)
	}
	// Read response header: unit + function
	h := make([]byte, 2)
	if _, err := readFullFromPort(ctx, port, h); err != nil {
		return nil, fmt.Errorf("serial read header: %w", err)
	}
	debugf("rtu recv hdr unit=%d fn=0x%02x", h[0], h[1])
	if h[0] != unit {
		return nil, fmt.Errorf("unexpected unit id: %d", h[0])
	}
	fn := h[1]
	// Exception response
	if fn&0x80 != 0 {
		b := make([]byte, 1+2)
		if _, err := readFullFromPort(ctx, port, b); err != nil {
			return nil, fmt.Errorf("serial read exception body: %w", err)
		}
		// verify CRC
		frame := append(h, b...)
		if !verifyCRC(frame) {
			return nil, errors.New("bad crc")
		}
		debugf("rtu recv exception code=%d", b[0])
		// return PDU [fn, ex]
		return []byte{fn, b[0]}, nil
	}
	switch fn {
	case fcReadHolding, fcReadInput:
		//fmt.Println("ReadHolding&ReadInput")
		// next: byteCount
		bc := make([]byte, 1)
		if _, err := readFullFromPort(ctx, port, bc); err != nil {
			return nil, fmt.Errorf("serial read byte count: %w", err)
		}
		data := make([]byte, int(bc[0])+2) // data + crc
		if _, err := readFullFromPort(ctx, port, data); err != nil {
			return nil, fmt.Errorf("serial read data: %w", err)
		}
		frame := append(append([]byte{}, h...), append(bc, data...)...)
		if !verifyCRC(frame) {
			return nil, errors.New("bad crc")
		}
		pduResp := make([]byte, 2+int(bc[0]))
		pduResp[0] = fn
		pduResp[1] = bc[0]
		copy(pduResp[2:], data[:int(bc[0])])
		debugf("rtu recv fc=0x%02x byteCount=%d", fn, bc[0])
		return pduResp, nil
	case fcWriteSingle, fcWriteMultiple:
		// FC06 response: addr(2)+val(2)+crc(2)
		// FC16 response: addr(2)+count(2)+crc(2)
		// Both have the same format: 4 data bytes + 2 CRC bytes
		rest := make([]byte, 4+2)
		if _, err := readFullFromPort(ctx, port, rest); err != nil {
			return nil, err
		}
		frame := append(append([]byte{}, h...), rest...)
		if !verifyCRC(frame) {
			return nil, errors.New("bad crc")
		}
		pduResp := make([]byte, 5)
		pduResp[0] = fn
		copy(pduResp[1:], rest[:4])
		debugf("rtu recv fc=0x%02x addr=%d val/count=%d", fn,
			binary.BigEndian.Uint16(rest[0:2]),
			binary.BigEndian.Uint16(rest[2:4]))
		return pduResp, nil
	default:
		return nil, fmt.Errorf("unsupported fn 0x%02x for rtu", fn)
	}
}

func verifyCRC(frame []byte) bool {
	if len(frame) < 3 {
		return false
	}
	data := frame[:len(frame)-2]
	crc := binary.LittleEndian.Uint16(frame[len(frame)-2:])
	return crc16Modbus(data) == crc
}

func crc16Modbus(data []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if (crc & 0x0001) != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc = crc >> 1
			}
		}
	}
	return crc
}

func readFullFromPort(ctx context.Context, port io.ReadWriter, b []byte) (int, error) {
	total := 0
	deadline := time.Now().Add(10 * time.Second)
	for total < len(b) {
		if time.Now().After(deadline) {
			return total, context.DeadlineExceeded
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		n, err := port.Read(b[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
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
	case "u32bs": // byte-swap within each word: BA DC
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bb := []byte{b[1], b[0], b[3], b[2]}
		return binary.BigEndian.Uint32(bb), nil

	case "u32wbs": // word+byte swap: DC BA
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bb := []byte{b[3], b[2], b[1], b[0]}
		return binary.BigEndian.Uint32(bb), nil
	case "u32le":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return binary.LittleEndian.Uint32(b[:4]), nil
	case "s32":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return int32(binary.BigEndian.Uint32(b[:4])), nil
	case "s32le":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		return int32(binary.LittleEndian.Uint32(b[:4])), nil
	case "f32": // default to big-endian (Modbus register order AB CD)
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bits := binary.BigEndian.Uint32(b[:4])
		return math.Float32frombits(bits), nil
	case "f32be", "f32abcd": // AB CD
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bits := binary.BigEndian.Uint32(b[:4])
		return math.Float32frombits(bits), nil
	case "f32le": // DC BA in Modbus two-register context
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bits := binary.LittleEndian.Uint32(b[:4])
		return math.Float32frombits(bits), nil
	case "f32ws", "f32cdab": // CD AB word-swapped
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bb := []byte{b[2], b[3], b[0], b[1]}
		bits := binary.BigEndian.Uint32(bb)
		return math.Float32frombits(bits), nil
	case "f32bs", "f32badc": // BA DC byte-swapped per word
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bb := []byte{b[1], b[0], b[3], b[2]}
		bits := binary.BigEndian.Uint32(bb)
		return math.Float32frombits(bits), nil
	case "f32wbs", "f32dcba": // DC BA word+byte swapped (equiv. little-endian bytes)
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bb := []byte{b[3], b[2], b[1], b[0]}
		bits := binary.BigEndian.Uint32(bb)
		return math.Float32frombits(bits), nil
	case "u32ws": // word-swapped
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bb := []byte{b[2], b[3], b[0], b[1]} // CD AB
		return binary.BigEndian.Uint32(bb), nil
	case "s32ws":
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes")
		}
		bb := []byte{b[2], b[3], b[0], b[1]}
		return int32(binary.BigEndian.Uint32(bb)), nil
	default:
		return nil, fmt.Errorf("unknown format: %s", format)
	}
}

// evalNumber parses a numeric literal or a simple arithmetic expression and returns its value.
// Allowed: integer/float literals (supports 0x..), parentheses, unary +/-, and binary + - * /.
func evalNumber(s string) (float64, error) {
	// Fast paths: plain int/uint/float
	if i, err := strconv.ParseInt(s, 0, 64); err == nil {
		return float64(i), nil
	}
	if u, err := strconv.ParseUint(s, 0, 64); err == nil {
		return float64(u), nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	// Fallback: parse expression via go/parser
	expr, err := parser.ParseExpr(s)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric expression: %w", err)
	}
	return evalExpr(expr)
}

func evalExpr(e ast.Expr) (float64, error) {
	switch v := e.(type) {
	case *ast.BasicLit:
		switch v.Kind {
		case token.INT:
			// base 0 handles 0x, 0o, 0b
			if i, err := strconv.ParseInt(v.Value, 0, 64); err == nil {
				return float64(i), nil
			}
			if u, err := strconv.ParseUint(v.Value, 0, 64); err == nil {
				return float64(u), nil
			}
			return 0, fmt.Errorf("invalid int literal: %s", v.Value)
		case token.FLOAT:
			f, err := strconv.ParseFloat(v.Value, 64)
			return f, err
		default:
			return 0, fmt.Errorf("unsupported literal: %s", v.Value)
		}
	case *ast.ParenExpr:
		return evalExpr(v.X)
	case *ast.UnaryExpr:
		val, err := evalExpr(v.X)
		if err != nil {
			return 0, err
		}
		switch v.Op {
		case token.ADD:
			return +val, nil
		case token.SUB:
			return -val, nil
		default:
			return 0, fmt.Errorf("unsupported unary operator: %s", v.Op)
		}
	case *ast.BinaryExpr:
		l, err := evalExpr(v.X)
		if err != nil {
			return 0, err
		}
		r, err := evalExpr(v.Y)
		if err != nil {
			return 0, err
		}
		switch v.Op {
		case token.ADD:
			return l + r, nil
		case token.SUB:
			return l - r, nil
		case token.MUL:
			return l * r, nil
		case token.QUO:
			return l / r, nil
		default:
			return 0, fmt.Errorf("unsupported operator: %s", v.Op)
		}
	default:
		return 0, fmt.Errorf("unsupported expression element: %T", e)
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
		if err == nil {
			return uint16(u), nil
		}
		// Try expression evaluation
		valf, err2 := evalNumber(t)
		if err2 != nil {
			return 0, err // keep original parse error context
		}
		if math.IsNaN(valf) || math.IsInf(valf, 0) {
			return 0, fmt.Errorf("invalid numeric result")
		}
		v := math.Round(valf)
		if v < 0 || v > 0xFFFF {
			return 0, fmt.Errorf("out of range")
		}
		return uint16(v), nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}

// closestDevice returns a port path from ports that best matches dev.
// It uses simple Levenshtein distance on base names and a special case for 'O' vs '0'.
func closestDevice(dev string, ports []string) string {
	if len(ports) == 0 || dev == "" {
		return ""
	}
	best := ""
	bestScore := 999
	base := filepath.Base(dev)
	baseLower := strings.ToLower(base)
	swapped := swapO0(base)
	swappedLower := strings.ToLower(swapped)
	for _, p := range ports {
		b := filepath.Base(p)
		bLower := strings.ToLower(b)
		d := levenshteinLimited(baseLower, bLower, 4)
		if d < bestScore {
			bestScore = d
			best = p
		}
		// Try swapped variant too
		if swapped != base {
			d2 := levenshteinLimited(swappedLower, bLower, 3)
			if d2 < bestScore {
				bestScore = d2
				best = p
			}
		}
	}
	if bestScore <= 2 { // only suggest if quite close
		return best
	}
	return ""
}

func swapO0(s string) string {
	b := []rune(s)
	for i, r := range b {
		switch r {
		case 'O':
			b[i] = '0'
		case 'o':
			b[i] = '0'
		case '0':
			b[i] = 'O'
		}
	}
	return string(b)
}

// levenshteinLimited computes Levenshtein distance up to a max threshold for efficiency.
func levenshteinLimited(a, b string, max int) int {
	la, lb := len(a), len(b)
	if la == 0 {
		if lb > max {
			return max + 1
		}
		return lb
	}
	if lb == 0 {
		if la > max {
			return max + 1
		}
		return la
	}
	// initialize previous row
	prev := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		rowMin := i
		aChar := a[i-1]
		for j := 1; j <= lb; j++ {
			cost := 0
			if aChar != b[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
			if m < rowMin {
				rowMin = m
			}
		}
		prev = curr
		// Early exit if no path through this row can beat max
		if rowMin > max {
			return max + 1
		}
	}
	d := prev[lb]
	if d > max {
		return max + 1
	}
	return d
}
