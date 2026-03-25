package addr

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pdat-cz/pc/pkg/proto"
)

// ParseReadSpec parses strings like:
//   - holding/1
//   - holding/2:4
//   - holding/10@u16
//   - input/1
func ParseReadSpec(s string) (proto.ReadSpec, error) {
	var rs proto.ReadSpec
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return rs, fmt.Errorf("invalid spec: %s", s)
	}
	kindStr, rest := parts[0], parts[1]
	switch kindStr {
	case string(proto.Coil):
		rs.Kind = proto.Coil
	case string(proto.Discrete):
		rs.Kind = proto.Discrete
	case string(proto.Holding):
		rs.Kind = proto.Holding
	case string(proto.Input):
		rs.Kind = proto.Input
	case string(proto.MBus):
		rs.Kind = proto.MBus
	default:
		return rs, fmt.Errorf("unknown kind: %s", kindStr)
	}
	// split format suffix - accept '@' or '$' as separator
	fmtIdx := strings.Index(rest, "@")
	if fmtIdx < 0 {
		fmtIdx = strings.Index(rest, "$")
	}
	if fmtIdx >= 0 {
		rs.Format = rest[fmtIdx+1:]
		rest = rest[:fmtIdx]
	}
	// optional count: addr or addr:count
	addrStr := rest
	count := uint16(0)
	if strings.Contains(rest, ":") {
		ac := strings.SplitN(rest, ":", 2)
		addrStr = ac[0]
		c64, err := strconv.ParseUint(ac[1], 10, 16)
		if err != nil {
			return rs, fmt.Errorf("invalid count: %w", err)
		}
		count = uint16(c64)
	}
	a64, err := strconv.ParseUint(addrStr, 10, 16)
	if err != nil {
		return rs, fmt.Errorf("invalid addr: %w", err)
	}
	rs.Addr = uint16(a64)
	rs.Count = count
	return rs, nil
}

// ParseURI parses modbus URIs and returns network ("tcp" or "rtu"), address, unit id, timeout
// Supported examples:
//
//	modbus+tcp://host:port?unit=1&timeout=3s
//	modbus+rtu:///dev/ttyUSB0?baud=9600&parity=N&data=8&stop=1&unit=1&timeout=3s
func ParseURI(uri string) (network, address string, unitID uint8, timeout time.Duration, err error) {
	u, err := url.Parse(uri)
	if err != nil {
		return
	}
	switch u.Scheme {
	case "modbus+tcp":
		network = "tcp"
		address = u.Host
	case "modbus+rtu":
		// For RTU, we pass device path along with query so the caller can parse serial params
		network = "rtu"
		address = u.Path
		if q := u.RawQuery; q != "" {
			address = address + "?" + q
		}
	default:
		err = fmt.Errorf("unsupported scheme: %s", u.Scheme)
		return
	}
	q := u.Query()
	unitID = 1
	if v := q.Get("unit"); v != "" {
		if n, e := strconv.ParseUint(v, 10, 8); e == nil {
			unitID = uint8(n)
		}
	}
	timeout = 3 * time.Second
	if v := q.Get("timeout"); v != "" {
		if d, e := time.ParseDuration(v); e == nil {
			timeout = d
		}
	}
	return
}
