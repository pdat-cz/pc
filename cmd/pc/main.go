package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/pdat-cz/pc/pkg/proto"
	mb "github.com/pdat-cz/pc/pkg/proto/modbus"
	wb "github.com/pdat-cz/pc/pkg/proto/mbus"
)

// splitArgs separates flag args from positional args so that flags can appear
// before or after the URI without Go's flag package stopping at the first
// non-flag argument. Returns (flagArgs, positionalArgs).
func splitArgs(args []string) ([]string, []string) {
	var flags, pos []string
	i := 0
	for i < len(args) {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			// If the flag contains "=" the value is embedded; otherwise consume next arg as value.
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i += 2
				continue
			}
		} else {
			pos = append(pos, a)
		}
		i++
	}
	return flags, pos
}

// These variables are set at build time via -ldflags -X.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func printVersion() {
	fmt.Printf("pc %s\ncommit: %s\ndate:   %s\n", version, commit, date)
}

func main() {
	// Support global --version/-v flags
	if len(os.Args) > 1 {
		if os.Args[1] == "--version" || os.Args[1] == "-v" {
			printVersion()
			return
		}
	}

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	switch sub {
	case "cat":
		if err := runCat(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "set":
		if err := runSet(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "scan":
		if err := runScan(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "diagnose":
		if err := runDiagnose(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "release":
		if err := runRelease(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version":
		// allow: pc version
		printVersion()
		return
	case "help", "-h", "--help":
		usage()
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown subcommand:", sub)
		usage()
		os.Exit(2)
	}
}

// newClientFromURI selects and returns the appropriate proto.Client for the given URI scheme.
func newClientFromURI(uri string) (proto.Client, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "modbus+tcp", "modbus+rtu":
		return mb.NewClient(), nil
	case "mbus+tcp", "mbus+rtu":
		return wb.NewClient(), nil
	default:
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
}

func usage() {
	fmt.Println("p.d.a. commander (pc) - personal digital assistant for fieldbus protocols")
	fmt.Println("Usage:")
	fmt.Println("  pc cat <uri> <addr> [addr...]")
	fmt.Println("  pc set <uri> <addr=value> [addr=value...]")
	fmt.Println("  pc scan <mbus-uri> [--from N] [--to N] [--timeout 500ms]")
	fmt.Println("  pc diagnose <uri> --address <addr> [options]")
	fmt.Println("  pc release <device-path>")
	fmt.Println("  pc version | --version | -v")
	fmt.Println()
	fmt.Println("Serial URI parameters (mbus+rtu, modbus+rtu):")
	fmt.Println("  baud=300|600|1200|2400|4800|9600|19200|38400|57600|115200")
	fmt.Println("  parity=E|O|N   data=7|8   stop=1|2")
	fmt.Println("  M-Bus standard: baud=2400&parity=E&data=8&stop=1")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  pc cat modbus+tcp://127.0.0.1:502?unit=1 holding/1 holding/2@u16")
	fmt.Println("  pc set modbus+tcp://127.0.0.1:502?unit=1 holding/10@u16=1234")
	fmt.Println("  pc cat modbus+rtu:///dev/ttyUSB0?baud=9600&parity=N&data=8&stop=1&unit=1 holding/0@f32be")
	fmt.Println("  pc set modbus+rtu:///dev/ttyUSB0?baud=19200&parity=E&data=8&stop=1&unit=1 holding/10@u16=1234")
	fmt.Println("  pc scan mbus+rtu:///dev/ttyUSB0?baud=2400&parity=E")
	fmt.Println("  pc cat mbus+rtu:///dev/ttyUSB0?baud=2400&parity=E mbus/1")
	fmt.Println("  pc diagnose --address 7 mbus+rtu:///dev/ttyUSB0")
	fmt.Println("  pc diagnose --address 7 --baud 2400 --json mbus+rtu:///dev/ttyUSB0")
	fmt.Println("  pc release /dev/ttyUSB0")
}
