package mbus

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pdat-cz/pc/pkg/proto"
	serial "go.bug.st/serial"
)

type Client struct {
	netw    string             // "tcp" or "rtu"
	addr    string             // tcp host:port or serial dev path
	conn    net.Conn           // when tcp
	port    io.ReadWriteCloser // when rtu
	timeout time.Duration
	baud    int                // serial baud rate (RTU only); used for inter-frame gap

	// pipeRd is set by StartPipeReader for scan operations.
	// When set, reads go through the pipe instead of the port directly.
	pipeRd     *PipeReader
	pipCancel  func()
}

// PipeReader wraps a serial port in a single background read goroutine.
// This guarantees only one goroutine ever calls Read() on the port, so
// context timeouts work correctly even when SetReadTimeout is unsupported.
type PipeReader struct {
	ch   chan byte
	done chan struct{}
}

// StartPipeReader launches the background goroutine. Call the returned
// cancel func (or Close) to stop it. Must be called after Open().
func (c *Client) StartPipeReader() {
	r := c.reader()
	ch := make(chan byte, 4096)
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 256)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := r.Read(buf)
			for i := 0; i < n; i++ {
				select {
				case ch <- buf[i]:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	c.pipeRd = &PipeReader{ch: ch, done: done}
	c.pipCancel = func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}
}

func (pr *PipeReader) readByte(ctx context.Context) (byte, error) {
	select {
	case b, ok := <-pr.ch:
		if !ok {
			return 0, io.EOF
		}
		return b, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// drain discards any bytes already buffered (stale data from previous probe).
func (pr *PipeReader) drain() {
	for len(pr.ch) > 0 {
		<-pr.ch
	}
}

// readLongPipe reads a complete M-Bus long frame byte-by-byte from the pipe.
func (pr *PipeReader) readLong(ctx context.Context) ([]byte, error) {
	rb := func() (byte, error) { return pr.readByte(ctx) }
	for {
		b, err := rb()
		if err != nil {
			return nil, err
		}
		if b != 0x68 {
			continue
		}
		l1, err := rb(); if err != nil { return nil, err }
		l2, err := rb(); if err != nil { return nil, err }
		b2, err := rb(); if err != nil { return nil, err }
		if b2 != 0x68 || l1 != l2 {
			continue
		}
		L := int(l1)
		payload := make([]byte, L)
		for i := range payload {
			payload[i], err = rb()
			if err != nil { return nil, err }
		}
		cs, err := rb(); if err != nil { return nil, err }
		end, err := rb(); if err != nil { return nil, err }
		if end != 0x16 {
			return nil, errors.New("mbus: missing 0x16 end")
		}
		if lrc(payload) != cs {
			return nil, fmt.Errorf("mbus: bad checksum got %02X want %02X", cs, lrc(payload))
		}
		out := make([]byte, 0, 6+L)
		out = append(out, 0x68, byte(L), byte(L), 0x68)
		out = append(out, payload...)
		out = append(out, cs, 0x16)
		return out, nil
	}
}

// ProbeAddress probes a single primary address using the pipe reader.
// Requires StartPipeReader to have been called first.
// Safe to call sequentially — no goroutines are leaked.
func (c *Client) ProbeAddress(ctx context.Context, primary byte) (handshakeOK bool, frame []byte) {
	pr := c.pipeRd
	pr.drain()

	// SND_NKE
	if err := c.sendShort(ctx, 0x40, primary); err != nil {
		return false, nil
	}
	b, err := pr.readByte(ctx)
	if err != nil || b != 0xE5 {
		return false, nil
	}

	// REQ_UD2
	if err := c.sendShort(ctx, 0x7B, primary); err != nil {
		return true, nil
	}
	f, err := pr.readLong(ctx)
	if err != nil {
		return true, nil
	}
	return true, f
}

func NewClient() *Client { return &Client{} }

func (c *Client) Open(ctx context.Context, uri string) error {
	u, err := url.Parse(uri)
	if err != nil {
		return err
	}
	c.timeout = 3 * time.Second
	if v := u.Query().Get("timeout"); v != "" {
		if d, e := time.ParseDuration(v); e == nil {
			c.timeout = d
		}
	}
	switch strings.ToLower(u.Scheme) {
	case "mbus+tcp":
		c.netw = "tcp"
		c.addr = u.Host
		var d net.Dialer
		if c.timeout > 0 {
			d.Timeout = c.timeout
		}
		conn, err := d.DialContext(ctx, "tcp", c.addr)
		if err != nil {
			return fmt.Errorf("mbus tcp dial %s: %w", c.addr, err)
		}
		c.conn = conn
		c.port = nil
		return nil
	case "mbus+rtu":
		c.netw = "rtu"
		dev := u.Path
		q := u.Query()
		baud := atoiDefault(q.Get("baud"), 2400)
		dataBits := atoiDefault(q.Get("data"), 8)
		parity := serial.EvenParity
		switch strings.ToUpper(q.Get("parity")) {
		case "E", "EVEN":
			parity = serial.EvenParity
		case "O", "ODD":
			parity = serial.OddParity
		case "N", "NONE":
			parity = serial.NoParity
		}
		stopBits := serial.OneStopBit
		if q.Get("stop") == "2" {
			stopBits = serial.TwoStopBits
		}
		c.baud = baud
		mode := &serial.Mode{BaudRate: baud, DataBits: dataBits, Parity: parity, StopBits: stopBits}
		p, err := serial.Open(dev, mode)
		if err != nil {
			return fmt.Errorf("open serial %s: %w", dev, err)
		}
		if c.timeout > 0 {
			if pt, ok := any(p).(interface{ SetReadTimeout(time.Duration) error }); ok {
				_ = pt.SetReadTimeout(c.timeout)
			}
		}
		c.port = p
		c.conn = nil
		c.StartPipeReader()
		return nil
	default:
		return fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
}

func (c *Client) Close() error {
	if c.pipCancel != nil {
		c.pipCancel()
		c.pipeRd = nil
		c.pipCancel = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
	if c.port != nil {
		_ = c.port.Close()
	}
	return nil
}

// ReconfigureSerial reconfigures the serial port parameters without closing the port.
// This is useful for diagnostic operations that test multiple configurations.
// Only works for RTU connections; returns error for TCP.
func (c *Client) ReconfigureSerial(baud, dataBits int, parity serial.Parity, stopBits serial.StopBits) error {
	if c.netw != "rtu" {
		return errors.New("reconfigure only supported for RTU connections")
	}
	if c.port == nil {
		return errors.New("serial port not open")
	}

	// Type assert to get access to SetMode
	if sp, ok := c.port.(interface{ SetMode(*serial.Mode) error }); ok {
		mode := &serial.Mode{
			BaudRate: baud,
			DataBits: dataBits,
			Parity:   parity,
			StopBits: stopBits,
		}
		return sp.SetMode(mode)
	}

	return errors.New("serial port does not support reconfiguration")
}

// FlushBuffers clears any pending data from the serial port receive buffer.
// This prevents stale data from previous tests from interfering with current tests.
// Only works for RTU connections; does nothing for TCP.
func (c *Client) FlushBuffers() error {
	if c.netw != "rtu" {
		return nil // TCP doesn't need flushing
	}
	if c.port == nil {
		return errors.New("serial port not open")
	}

	// Try to reset input/output buffers if supported
	if sp, ok := c.port.(interface{ ResetInputBuffer() error }); ok {
		_ = sp.ResetInputBuffer()
	}
	if sp, ok := c.port.(interface{ ResetOutputBuffer() error }); ok {
		_ = sp.ResetOutputBuffer()
	}

	// Also do a read with very short timeout to drain any pending bytes
	if rdr, ok := c.port.(io.Reader); ok {
		// Set very short read timeout (10ms)
		if rt, ok := c.port.(interface{ SetReadTimeout(time.Duration) error }); ok {
			oldTimeout := c.timeout
			_ = rt.SetReadTimeout(10 * time.Millisecond)
			defer rt.SetReadTimeout(oldTimeout)
		}

		// Drain up to 1KB of stale data
		buf := make([]byte, 1024)
		for {
			n, err := rdr.Read(buf)
			if n == 0 || err != nil {
				break
			}
		}
	}

	return nil
}

func (c *Client) Read(ctx context.Context, specs []proto.ReadSpec) ([]proto.Value, error) {
	vals := make([]proto.Value, 0, len(specs))
	for _, rs := range specs {
		if rs.Kind != proto.MBus && rs.Kind != proto.AddrKind("mbus") {
			return nil, fmt.Errorf("mbus client: unsupported kind %q", rs.Kind)
		}
		primary := byte(rs.Addr)
		mode := strings.ToLower(rs.Format)
		if mode == "" {
			mode = "ud2"
		}
		// Inter-frame gap between exchanges (EN 13757-2: min 33 bit times).
		if len(vals) > 0 {
			time.Sleep(c.InterFrameGap())
		}
		// Drain any stale bytes from a previous exchange before starting a new one.
		if c.pipeRd != nil {
			c.pipeRd.drain()
		}
		// Best-effort link reset — wait for E5 ack before sending REQ_UD2
		// to avoid bus collision (slave transmits E5 while master sends REQ_UD2).
		if err := c.sendShort(ctx, 0x40, primary); err == nil && c.pipeRd != nil { // SND_NKE
			nkeCtx, nkeCancel := context.WithTimeout(ctx, 500*time.Millisecond)
			b, err := c.pipeRd.readByte(nkeCtx)
			nkeCancel()
			if err != nil || b != 0xE5 {
				// drain any stale byte so readLong starts clean
				if err == nil {
					c.pipeRd.drain()
				}
			}
		}
		switch mode {
		case "nke":
			// Only reset, return empty bytes
			vals = append(vals, proto.Value{Spec: rs, TS: time.Now()})
			continue
		case "ud2":
			if err := c.sendShort(ctx, 0x7B, primary); err != nil { // REQ_UD2
				return nil, err
			}
		case "ud1":
			if err := c.sendShort(ctx, 0x5B, primary); err != nil { // REQ_UD1
				return nil, err
			}
		default:
			if strings.HasPrefix(mode, "raw:") {
				return nil, errors.New("mbus raw send not implemented")
			}
			return nil, fmt.Errorf("unknown mbus format: %s", mode)
		}
		frame, err := c.readLong(ctx)
		if err != nil {
			return nil, err
		}
		decoded, _ := DecodeFrame(frame)
		var decodedAny any
		if decoded != nil {
			decodedAny = decoded
		}
		vals = append(vals, proto.Value{Spec: rs, Bytes: frame, Decoded: decodedAny, TS: time.Now()})
	}
	return vals, nil
}

func (c *Client) Write(ctx context.Context, spec proto.ReadSpec, val any) error {
	return errors.New("mbus write not implemented")
}

// --- helpers ---

func (c *Client) sendShort(ctx context.Context, control, addr byte) error {
	// short frame: 10 C A CS 16
	cs := byte(uint16(control+addr) & 0xFF)
	frm := []byte{0x10, control, addr, cs, 0x16}
	return c.writeAll(ctx, frm)
}

func (c *Client) writeAll(ctx context.Context, b []byte) error {
	var w io.Writer
	if c.conn != nil {
		w = c.conn
	} else if c.port != nil {
		w = c.port
	} else {
		return errors.New("connection not open")
	}
	if dl, ok := ctx.Deadline(); ok && !dl.IsZero() {
		deadline := dl
		if c.conn != nil {
			_ = c.conn.SetWriteDeadline(deadline)
		}
		if cw, ok := w.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = cw.SetWriteDeadline(deadline)
		}
	}
	_, err := w.Write(b)
	return err
}

func (c *Client) readLong(ctx context.Context) ([]byte, error) {
	if c.pipeRd != nil {
		return c.pipeRd.readLong(ctx)
	}
	r := c.reader()
	if r == nil {
		return nil, errors.New("connection not open")
	}
	setDeadline(c, ctx)
	br := bufio.NewReader(r)
	// read until start 0x68
	for {
		b, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == 0x68 {
			// read L1, L2, 0x68
			l1, err := br.ReadByte()
			if err != nil { return nil, err }
			l2, err := br.ReadByte()
			if err != nil { return nil, err }
			b2, err := br.ReadByte()
			if err != nil { return nil, err }
			if b2 != 0x68 || l1 != l2 {
				// desync, continue scanning
				continue
			}
			L := int(l1)
			payload := make([]byte, L)
			if _, err := io.ReadFull(br, payload); err != nil {
				return nil, err
			}
			cs, err := br.ReadByte()
			if err != nil { return nil, err }
			end, err := br.ReadByte()
			if err != nil { return nil, err }
			if end != 0x16 {
				return nil, errors.New("mbus: missing 0x16 end")
			}
			if lrc(payload) != cs {
				return nil, fmt.Errorf("mbus: bad checksum got %02X want %02X", cs, lrc(payload))
			}
			// Reconstruct full frame: 68 L L 68 [payload] CS 16
			out := make([]byte, 0, 6+L)
			out = append(out, 0x68, byte(L), byte(L), 0x68)
			out = append(out, payload...)
			out = append(out, cs, 0x16)
			return out, nil
		}
	}
}

func (c *Client) reader() io.Reader {
	if c.conn != nil {
		return c.conn
	}
	return c.port
}

func setDeadline(c *Client, ctx context.Context) {
	if dl, ok := ctx.Deadline(); ok && !dl.IsZero() {
		if c.conn != nil {
			_ = c.conn.SetReadDeadline(dl)
		}
		// For serial ports, use SetReadTimeout with duration instead of deadline
		if c.port != nil {
			timeout := time.Until(dl)
			if timeout > 0 {
				if pt, ok := any(c.port).(interface{ SetReadTimeout(time.Duration) error }); ok {
					_ = pt.SetReadTimeout(timeout)
				}
			}
		}
	}
}

// InterFrameGap returns the minimum delay required between M-Bus exchanges
// per EN 13757-2 (33 bit times), rounded up generously for converter turnaround.
func (c *Client) InterFrameGap() time.Duration {
	if c.netw != "rtu" || c.baud <= 0 {
		return 0
	}
	// 33 bit times per spec; multiply by 3 to cover slow converter turnaround.
	gap := time.Duration(33*3*1e9/int64(c.baud)) * time.Nanosecond
	if gap < 20*time.Millisecond {
		gap = 20 * time.Millisecond
	}
	return gap
}

func lrc(b []byte) byte {
	var sum byte
	for _, v := range b {
		sum += v
	}
	return sum
}

func atoiDefault(s string, def int) int {
	if s == "" { return def }
	n, err := strconv.Atoi(s)
	if err != nil { return def }
	return n
}

// RestartPipeReader stops the current pipe reader (if any) and starts a fresh
// one. Call this after ReconfigureSerial so the reader sees the new baud rate.
func (c *Client) RestartPipeReader() {
	if c.pipCancel != nil {
		c.pipCancel()
		c.pipeRd = nil
		c.pipCancel = nil
	}
	c.StartPipeReader()
}

// Diagnose implements proto.Diagnoser for M-Bus.
// When a pipe reader is active it uses that (no goroutine leaks, proper ctx
// cancellation even when SetReadTimeout is unsupported by the driver).
func (c *Client) Diagnose(ctx context.Context, params map[string]interface{}) (proto.DiagnosticResult, error) {
	start := time.Now()

	// Extract M-Bus-specific params
	primary, ok := params["primary"].(byte)
	if !ok {
		return proto.DiagnosticResult{
			Success:  false,
			Duration: time.Since(start),
			Error:    errors.New("mbus: primary address required"),
		}, nil
	}

	// Use pipe reader path when available (no goroutine leaks).
	if c.pipeRd != nil {
		handshakeOK, frame := c.ProbeAddress(ctx, primary)
		dataReceived := len(frame) > 0
		result := proto.DiagnosticResult{
			Success:      handshakeOK && dataReceived,
			Duration:     time.Since(start),
			HandshakeOK:  &handshakeOK,
			DataReceived: &dataReceived,
			Details: map[string]interface{}{
				"primary_address": primary,
				"data_length":     len(frame),
			},
		}
		return result, nil
	}

	// Test SND_NKE and check for E5 acknowledgment
	nkeSuccess := c.trySendNKE(ctx, primary)

	// Test REQ_UD2 and check for data
	frame, dataErr := c.tryRequestUD2(ctx, primary)
	dataReceived := dataErr == nil && len(frame) > 0

	result := proto.DiagnosticResult{
		Success:      nkeSuccess && dataReceived,
		Duration:     time.Since(start),
		Error:        dataErr,
		HandshakeOK:  &nkeSuccess,
		DataReceived: &dataReceived,
		Details: map[string]interface{}{
			"primary_address": primary,
			"data_length":     len(frame),
		},
	}

	if dataReceived {
		result.Details["frame_hex"] = fmt.Sprintf("%x", frame)
	}

	return result, nil
}

// trySendNKE sends SND_NKE and checks for E5 acknowledgment.
// Uses a goroutine + select to honour ctx even when SetReadTimeout is unsupported.
func (c *Client) trySendNKE(ctx context.Context, primary byte) bool {
	if err := c.sendShort(ctx, 0x40, primary); err != nil {
		return false
	}
	r := c.reader()
	if r == nil {
		return false
	}
	setDeadline(c, ctx)

	type result struct{ ok bool }
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := io.ReadFull(r, buf)
		ch <- result{err == nil && buf[0] == 0xE5}
	}()
	select {
	case res := <-ch:
		return res.ok
	case <-ctx.Done():
		return false
	}
}

// tryRequestUD2 sends REQ_UD2 and tries to read the response.
// Uses a goroutine + select to honour ctx even when SetReadTimeout is unsupported.
func (c *Client) tryRequestUD2(ctx context.Context, primary byte) ([]byte, error) {
	if err := c.sendShort(ctx, 0x7B, primary); err != nil {
		return nil, err
	}

	type result struct {
		frame []byte
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		f, e := c.readLong(ctx)
		ch <- result{f, e}
	}()
	select {
	case res := <-ch:
		return res.frame, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
