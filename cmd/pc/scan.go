package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	wb "github.com/pdat-cz/pc/pkg/proto/mbus"
)

type scanHit struct {
	Address     int   `json:"address"`
	HandshakeOK bool  `json:"handshake_ok"`
	DataLength  int   `json:"data_length"`
	DurationMs  int64 `json:"duration_ms"`
}

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	fromFlag := fs.Int("from", 1, "Start of primary address range (1-250)")
	toFlag := fs.Int("to", 250, "End of primary address range (1-250)")
	timeoutFlag := fs.Duration("timeout", 500*time.Millisecond, "Timeout per address")
	jsonOutput := fs.Bool("json", false, "Output results as JSON array")
	quiet := fs.Bool("quiet", false, "Suppress progress output")
	fs.BoolVar(quiet, "q", false, "Alias for --quiet")

	flagArgs, posArgs := splitArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if *fromFlag < 1 || *fromFlag > 250 || *toFlag < 1 || *toFlag > 250 || *fromFlag > *toFlag {
		return fmt.Errorf("--from and --to must be 1-250 with --from <= --to")
	}

	if len(posArgs) < 1 {
		return fmt.Errorf("URI required\nUsage: pc scan <mbus+rtu://...> [--from N] [--to N] [--timeout 500ms]")
	}
	uri := posArgs[0]

	client := wb.NewClient()
	openCtx, openCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := client.Open(openCtx, uri); err != nil {
		openCancel()
		return fmt.Errorf("open: %w", err)
	}
	openCancel()
	defer client.Close()

	total := *toFlag - *fromFlag + 1
	if !*quiet {
		fmt.Fprintf(os.Stderr, "Scanning M-Bus primary addresses %d-%d on %s\n", *fromFlag, *toFlag, uri)
		fmt.Fprintf(os.Stderr, "Timeout: %v/addr  |  Addresses: %d  |  Est. max: %v\n\n",
			*timeoutFlag, total, time.Duration(total)**timeoutFlag)
	}

	var hits []scanHit

	gap := client.InterFrameGap()
	for addr := *fromFlag; addr <= *toFlag; addr++ {
		if addr > *fromFlag {
			time.Sleep(gap)
		}
		if !*quiet {
			fmt.Fprintf(os.Stderr, "\r  Probing %3d / %3d (%3d-%3d) ...", addr, *toFlag, *fromFlag, *toFlag)
		}

		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
		handshakeOK, frame := client.ProbeAddress(ctx, byte(addr))
		cancel()
		dur := time.Since(start)

		if handshakeOK {
			hit := scanHit{
				Address:     addr,
				HandshakeOK: true,
				DataLength:  len(frame),
				DurationMs:  dur.Milliseconds(),
			}
			hits = append(hits, hit)

			if !*quiet {
				status := "E5 ack only"
				if len(frame) > 0 {
					status = fmt.Sprintf("OK, %d bytes", len(frame))
				}
				fmt.Fprintf(os.Stderr, "\r  [FOUND] address %3d — %s\n", addr, status)
			}
		}
	}

	if !*quiet {
		fmt.Fprintf(os.Stderr, "\r  Scan complete. Found %d device(s).\n\n", len(hits))
	}

	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(hits)
	}

	if len(hits) == 0 {
		fmt.Println("No M-Bus devices found.")
		fmt.Println()
		fmt.Println("Troubleshooting:")
		fmt.Println("  - Check wiring and power")
		fmt.Println("  - Verify serial settings (baud/parity) with: pc diagnose --address <addr> <uri>")
		fmt.Println("  - Try --timeout 2s for slow devices")
		return nil
	}

	fmt.Printf("Found %d M-Bus device(s):\n\n", len(hits))
	fmt.Println("┌─────────┬─────────────┬─────────────┬──────────┐")
	fmt.Println("│ Address │ Handshake   │ Data        │ Duration │")
	fmt.Println("├─────────┼─────────────┼─────────────┼──────────┤")
	for _, h := range hits {
		handshake := "E5 ack"
		if !h.HandshakeOK {
			handshake = "-"
		}
		data := "-"
		if h.DataLength > 0 {
			data = fmt.Sprintf("%d bytes", h.DataLength)
		}
		fmt.Printf("│  %3d    │ %-11s │ %-11s │ %5dms  │\n",
			h.Address, handshake, data, h.DurationMs)
	}
	fmt.Println("└─────────┴─────────────┴─────────────┴──────────┘")
	fmt.Println()
	fmt.Println("Next: read a device with")
	canonicalURI := mbusCanonicalURI(uri)
	for _, h := range hits {
		fmt.Printf("  pc cat '%s' mbus/%d\n", canonicalURI, h.Address)
	}

	return nil
}

// mbusCanonicalURI returns the URI with all serial parameters made explicit,
// applying M-Bus standard defaults (2400/E/8/1) for any missing values.
// This ensures the suggested pc cat command is fully reproducible.
func mbusCanonicalURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || strings.ToLower(u.Scheme) != "mbus+rtu" {
		return uri
	}
	q := u.Query()
	if q.Get("baud") == "" {
		q.Set("baud", "2400")
	}
	if q.Get("parity") == "" {
		q.Set("parity", "E")
	}
	if q.Get("data") == "" {
		q.Set("data", "8")
	}
	if q.Get("stop") == "" {
		q.Set("stop", "1")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
