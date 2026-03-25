package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pdat-cz/pc/pkg/proto"
	wb "github.com/pdat-cz/pc/pkg/proto/mbus"
	serial "go.bug.st/serial"
)

// Serial parameter test configuration
type serialConfig struct {
	baud   int
	parity string
	data   int
	stop   int
}

type testResult struct {
	Config       serialConfig
	Success      bool
	HandshakeOK  bool
	DataReceived bool
	DataLength   int
	Duration     time.Duration
	Error        string
}

func runDiagnose(args []string) error {
	fs := flag.NewFlagSet("diagnose", flag.ExitOnError)
	address := fs.Int("address", 0, "M-Bus primary address (1-250, required)")
	baudFlag := fs.Int("baud", 0, "Test only specific baud rate (optional, tests all if not specified)")
	parityFlag := fs.String("parity", "", "Test only specific parity: N, E, or O (optional, tests all if not specified)")
	dataFlag := fs.Int("data", 0, "Test only specific data bits: 7 or 8 (optional, tests all if not specified)")
	stopFlag := fs.Int("stop", 0, "Test only specific stop bits: 1 or 2 (optional, tests all if not specified)")
	timeoutFlag := fs.Duration("timeout", 500*time.Millisecond, "Timeout per test attempt (e.g. 1.5s, 500ms)")
	jsonOutput := fs.Bool("json", false, "Output results as JSON")
	quiet := fs.Bool("quiet", false, "Suppress progress output (stderr)")
	fs.BoolVar(quiet, "q", false, "Suppress progress output (stderr) - alias for --quiet")

	flagArgs, posArgs := splitArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	// Validate required flags
	if *address < 1 || *address > 250 {
		return fmt.Errorf("--address must be between 1 and 250")
	}

	if len(posArgs) < 1 {
		return fmt.Errorf("URI required\nUsage: pc diagnose <mbus-uri> --address <addr> [options]")
	}
	baseURI := posArgs[0]

	// Parse and validate URI
	u, err := url.Parse(baseURI)
	if err != nil {
		return fmt.Errorf("invalid URI: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "mbus+rtu" {
		return fmt.Errorf("diagnose currently only supports mbus+rtu (got %s)", scheme)
	}

	device := u.Path
	if device == "" {
		return fmt.Errorf("serial device path required in URI (e.g. mbus+rtu:///dev/ttyUSB0)")
	}

	// Build test configurations
	configs := buildTestConfigs(*baudFlag, *parityFlag, *dataFlag, *stopFlag)

	if !*quiet {
		fmt.Fprintf(os.Stderr, "Testing M-Bus device at primary address %d on %s...\n", *address, device)
		fmt.Fprintf(os.Stderr, "Timeout: %v per attempt\n", *timeoutFlag)
		fmt.Fprintf(os.Stderr, "Testing %d configuration(s)\n", len(configs))
		fmt.Fprintf(os.Stderr, "\n⚠ The serial port will be LOCKED for the entire test duration.\n")
		fmt.Fprintf(os.Stderr, "  Other applications cannot access %s until diagnostics complete.\n", device)
		fmt.Fprintf(os.Stderr, "  Press Ctrl+C to abort and release the port.\n\n")
	}

	// Run tests with port locked for entire duration
	results, successCount, err := runTestsWithLockedPort(baseURI, device, configs, byte(*address), *timeoutFlag, *quiet)
	if err != nil {
		return fmt.Errorf("diagnostic tests failed: %w", err)
	}

	// Output results
	if *jsonOutput {
		return outputJSON(results, *address, device, successCount)
	}

	return outputHuman(results, *address, device, successCount)
}

func buildTestConfigs(baudFlag int, parityFlag string, dataFlag, stopFlag int) []serialConfig {
	// Standard M-Bus baud rates (ordered by likelihood)
	bauds := []int{300, 600, 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200}
	if baudFlag > 0 {
		bauds = []int{baudFlag}
	}

	parities := []string{"E", "N", "O"}
	if parityFlag != "" {
		parities = []string{strings.ToUpper(parityFlag)}
	}

	dataBits := []int{8, 7}
	if dataFlag > 0 {
		dataBits = []int{dataFlag}
	}

	stopBits := []int{1, 2}
	if stopFlag > 0 {
		stopBits = []int{stopFlag}
	}

	// Generate all combinations
	configs := make([]serialConfig, 0, len(bauds)*len(parities)*len(dataBits)*len(stopBits))
	for _, b := range bauds {
		for _, p := range parities {
			for _, d := range dataBits {
				for _, s := range stopBits {
					configs = append(configs, serialConfig{
						baud:   b,
						parity: p,
						data:   d,
						stop:   s,
					})
				}
			}
		}
	}

	return configs
}

// runTestsWithLockedPort opens the serial port once and reconfigures it for each test.
// This is more efficient and prevents other processes from interfering.
func runTestsWithLockedPort(baseURI, device string, configs []serialConfig, primary byte, timeout time.Duration, quiet bool) ([]testResult, int, error) {
	results := make([]testResult, 0, len(configs))
	successCount := 0

	// Open port once with first configuration
	firstCfg := configs[0]
	initialURI := fmt.Sprintf("mbus+rtu://%s?baud=%d&parity=%s&data=%d&stop=%d&timeout=%s",
		device, firstCfg.baud, firstCfg.parity, firstCfg.data, firstCfg.stop, timeout.String())

	client := wb.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), timeout+500*time.Millisecond)
	defer cancel()

	if err := client.Open(ctx, initialURI); err != nil {
		return nil, 0, fmt.Errorf("failed to open serial port: %w", err)
	}
	defer func() {
		client.Close()
		if !quiet {
			fmt.Fprintf(os.Stderr, "\n✓ Serial port %s released.\n", device)
		}
	}()

	// Setup signal handler for clean shutdown on Ctrl+C or SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer func() {
		signal.Stop(sigChan)
		close(sigChan)
	}()
	go func() {
		sig, ok := <-sigChan
		if !ok {
			return // channel closed — normal exit, do nothing
		}
		_ = sig
		fmt.Fprintf(os.Stderr, "\n\n⚠ Interrupted! Releasing port %s...\n", device)
		client.Close()
		fmt.Fprintf(os.Stderr, "✓ Port released.\n")
		os.Exit(130) // Standard exit code for SIGINT
	}()

	if !quiet {
		fmt.Fprintf(os.Stderr, "✓ Serial port %s locked for testing.\n\n", device)
	}

	// Start pipe reader so Diagnose never leaks goroutines or hangs
	// on drivers that don't support SetReadTimeout (e.g. ttymxc1).
	client.StartPipeReader()

	// Type-assert to Diagnoser once
	diagnoser, ok := interface{}(client).(proto.Diagnoser)
	if !ok {
		return nil, 0, fmt.Errorf("client does not implement Diagnoser")
	}

	// Test each configuration
	for i, cfg := range configs {
		if !quiet {
			fmt.Fprintf(os.Stderr, "[%3d/%3d] Testing baud=%d parity=%s data=%d stop=%d ... ",
				i+1, len(configs), cfg.baud, cfg.parity, cfg.data, cfg.stop)
		}

		// Reconfigure port for this test (except first which is already configured)
		if i > 0 {
			parity := parityStringToEnum(cfg.parity)
			stopBits := stopBitsIntToEnum(cfg.stop)

			if err := client.ReconfigureSerial(cfg.baud, cfg.data, parity, stopBits); err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "✗ FAIL (reconfigure error: %v)\n", err)
				}
				results = append(results, testResult{
					Config:   cfg,
					Success:  false,
					Duration: 0,
					Error:    fmt.Sprintf("reconfigure failed: %v", err),
				})
				continue
			}

			// Restart pipe reader after reconfiguration so it sees the new baud rate.
			client.RestartPipeReader()
		}

		// Run diagnostic test with hard timeout
		resultChan := make(chan testResult, 1)
		go func() {
			resultChan <- testConfigWithClient(diagnoser, cfg, primary, timeout)
		}()

		// Wait for result or hard timeout (timeout + 1 second grace period)
		var result testResult
		select {
		case result = <-resultChan:
			// Got result normally
		case <-time.After(timeout + 1*time.Second):
			// Hard timeout - test hung
			result = testResult{
				Config:   cfg,
				Success:  false,
				Duration: timeout + 1*time.Second,
				Error:    "test timeout - hung indefinitely",
			}
		}

		results = append(results, result)

		if !quiet {
			if result.Success {
				fmt.Fprintf(os.Stderr, "✓ SUCCESS (E5 ack + %d bytes in %v)\n",
					result.DataLength, result.Duration.Round(time.Millisecond))
				successCount++
			} else if result.HandshakeOK && !result.DataReceived {
				fmt.Fprintf(os.Stderr, "⚠ PARTIAL (E5 ack but no data, %v)\n",
					result.Duration.Round(time.Millisecond))
			} else {
				errMsg := "no response"
				if result.Error != "" {
					errMsg = result.Error
				}
				fmt.Fprintf(os.Stderr, "✗ FAIL (%s, %v)\n",
					errMsg, result.Duration.Round(time.Millisecond))
			}
		}
	}

	return results, successCount, nil
}

// testConfigWithClient runs a diagnostic test on an already-open client
func testConfigWithClient(diagnoser proto.Diagnoser, cfg serialConfig, primary byte, timeout time.Duration) testResult {
	// Run diagnostic
	diagCtx, diagCancel := context.WithTimeout(context.Background(), timeout)
	defer diagCancel()

	result, err := diagnoser.Diagnose(diagCtx, map[string]interface{}{
		"primary": primary,
	})

	var errStr string
	if err != nil {
		errStr = err.Error()
	} else if result.Error != nil {
		errStr = result.Error.Error()
	}

	handshakeOK := false
	dataReceived := false
	dataLength := 0

	if result.HandshakeOK != nil {
		handshakeOK = *result.HandshakeOK
	}
	if result.DataReceived != nil {
		dataReceived = *result.DataReceived
	}
	if dl, ok := result.Details["data_length"].(int); ok {
		dataLength = dl
	}

	return testResult{
		Config:       cfg,
		Success:      result.Success,
		HandshakeOK:  handshakeOK,
		DataReceived: dataReceived,
		DataLength:   dataLength,
		Duration:     result.Duration,
		Error:        errStr,
	}
}

// Helper functions to convert between string/int and serial enums
func parityStringToEnum(parity string) serial.Parity {
	switch strings.ToUpper(parity) {
	case "E", "EVEN":
		return serial.EvenParity
	case "O", "ODD":
		return serial.OddParity
	case "N", "NONE":
		return serial.NoParity
	default:
		return serial.EvenParity
	}
}

func stopBitsIntToEnum(stop int) serial.StopBits {
	if stop == 2 {
		return serial.TwoStopBits
	}
	return serial.OneStopBit
}

func outputJSON(results []testResult, address int, device string, successCount int) error {
	type jsonConfig struct {
		Baud   int    `json:"baud"`
		Parity string `json:"parity"`
		Data   int    `json:"data"`
		Stop   int    `json:"stop"`
	}

	type jsonResult struct {
		Config       jsonConfig `json:"config"`
		Success      bool       `json:"success"`
		HandshakeOK  bool       `json:"handshake_ok"`
		DataReceived bool       `json:"data_received"`
		DataLength   int        `json:"data_length,omitempty"`
		DurationMs   int64      `json:"duration_ms"`
		Error        string     `json:"error,omitempty"`
	}

	type jsonOutput struct {
		Device       string       `json:"device"`
		Address      int          `json:"primary_address"`
		TestCount    int          `json:"test_count"`
		SuccessCount int          `json:"success_count"`
		Results      []jsonResult `json:"results"`
	}

	jsonResults := make([]jsonResult, len(results))
	for i, r := range results {
		jsonResults[i] = jsonResult{
			Config: jsonConfig{
				Baud:   r.Config.baud,
				Parity: r.Config.parity,
				Data:   r.Config.data,
				Stop:   r.Config.stop,
			},
			Success:      r.Success,
			HandshakeOK:  r.HandshakeOK,
			DataReceived: r.DataReceived,
			DataLength:   r.DataLength,
			DurationMs:   r.Duration.Milliseconds(),
			Error:        r.Error,
		}
	}

	output := jsonOutput{
		Device:       device,
		Address:      address,
		TestCount:    len(results),
		SuccessCount: successCount,
		Results:      jsonResults,
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func outputHuman(results []testResult, address int, device string, successCount int) error {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println("                    DIAGNOSTIC RESULTS                         ")
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("Device:          %s\n", device)
	fmt.Printf("Primary Address: %d\n", address)
	fmt.Printf("Tests Run:       %d\n", len(results))
	fmt.Printf("Successful:      %d\n", successCount)
	fmt.Println()

	// Find successful configurations
	successful := make([]testResult, 0)
	partial := make([]testResult, 0)
	for _, r := range results {
		if r.Success {
			successful = append(successful, r)
		} else if r.HandshakeOK && !r.DataReceived {
			partial = append(partial, r)
		}
	}

	if len(successful) > 0 {
		fmt.Println("✓ WORKING CONFIGURATIONS:")
		fmt.Println()

		// Sort by baud rate (highest first) then by standard-ness
		sort.Slice(successful, func(i, j int) bool {
			if successful[i].Config.baud != successful[j].Config.baud {
				return successful[i].Config.baud > successful[j].Config.baud
			}
			// Prefer 8E1 (EN 13757-2 standard)
			iStd := successful[i].Config.data == 8 && successful[i].Config.parity == "E" && successful[i].Config.stop == 1
			jStd := successful[j].Config.data == 8 && successful[j].Config.parity == "E" && successful[j].Config.stop == 1
			if iStd != jStd {
				return iStd
			}
			return false
		})

		// Display working configs in a table
		fmt.Println("┌───────┬────────┬──────┬──────┬──────────────┬──────────┐")
		fmt.Println("│ Baud  │ Parity │ Data │ Stop │ Data Length  │ Duration │")
		fmt.Println("├───────┼────────┼──────┼──────┼──────────────┼──────────┤")
		for _, r := range successful {
			std := ""
			if r.Config.data == 8 && r.Config.parity == "E" && r.Config.stop == 1 {
				std = " ★" // Mark EN 13757-2 standard config
			}
			fmt.Printf("│ %5d │   %1s    │   %1d  │   %1d  │ %5d bytes  │ %7s  │%s\n",
				r.Config.baud, r.Config.parity, r.Config.data, r.Config.stop,
				r.DataLength, r.Duration.Round(time.Millisecond), std)
		}
		fmt.Println("└───────┴────────┴──────┴──────┴──────────────┴──────────┘")
		fmt.Println("★ = EN 13757-2 standard configuration (8E1)")
		fmt.Println()

		// Recommend the best configuration
		best := successful[0]
		fmt.Println("RECOMMENDED CONFIGURATION:")
		fmt.Printf("  baud=%d parity=%s data=%d stop=%d\n",
			best.Config.baud, best.Config.parity, best.Config.data, best.Config.stop)
		fmt.Println()

		// Generate ready-to-use URI and command
		readyURI := fmt.Sprintf("mbus+rtu://%s?baud=%d&parity=%s&data=%d&stop=%d&address=%d",
			device, best.Config.baud, best.Config.parity, best.Config.data, best.Config.stop, address)

		fmt.Println("READY-TO-USE URI:")
		fmt.Printf("  %s\n", readyURI)
		fmt.Println()

		fmt.Println("NEXT STEPS:")
		fmt.Printf("  pc cat '%s' mbus/%d@ud2\n", readyURI, address)
		fmt.Println()

	} else if len(partial) > 0 {
		fmt.Println("⚠ PARTIAL SUCCESS CONFIGURATIONS (E5 ack received, but no data):")
		fmt.Println()
		for _, r := range partial {
			fmt.Printf("  baud=%d parity=%s data=%d stop=%d (duration: %v)\n",
				r.Config.baud, r.Config.parity, r.Config.data, r.Config.stop,
				r.Duration.Round(time.Millisecond))
		}
		fmt.Println()
		fmt.Println("This indicates the device responded to link reset but not to data request.")
		fmt.Println("Possible causes:")
		fmt.Println("  - Device may not be configured to respond to REQ_UD2")
		fmt.Println("  - Device may require REQ_UD1 instead")
		fmt.Println("  - Device may be in sleep mode or needs initialization")
		fmt.Println()

	} else {
		fmt.Println("✗ NO WORKING CONFIGURATIONS FOUND")
		fmt.Println()
		fmt.Println("No device responded at the specified address.")
		fmt.Println()
		fmt.Println("Troubleshooting suggestions:")
		fmt.Println("  1. Verify device is powered and connected")
		fmt.Println("  2. Check primary address is correct (try different addresses)")
		fmt.Println("  3. Check serial cable wiring (TX/RX, GND)")
		fmt.Println("  4. Try increasing timeout: --timeout 3s")
		fmt.Println("  5. Verify device supports M-Bus protocol")
		fmt.Println()
	}

	// Show detailed matrix for analysis
	fmt.Println("DETAILED TEST MATRIX:")
	fmt.Println()
	fmt.Println("Legend: ✓ = success, ⚠ = partial (E5 only), ✗ = fail, · = timeout/error")
	fmt.Println()

	// Create matrix grouped by baud rate
	bauds := []int{9600, 4800, 2400, 1200, 600, 300}
	parities := []string{"N", "E", "O"}

	for _, baud := range bauds {
		fmt.Printf("Baud %d:\n", baud)

		// Header
		fmt.Print("       ")
		for _, p := range parities {
			fmt.Printf("│  %s   ", p)
		}
		fmt.Println("│")

		// Data/Stop combinations
		for _, data := range []int{8, 7} {
			for _, stop := range []int{1, 2} {
				fmt.Printf("  %d%d   ", data, stop)

				for _, p := range parities {
					cfg := serialConfig{baud: baud, parity: p, data: data, stop: stop}
					symbol := "·"
					for _, r := range results {
						if r.Config == cfg {
							if r.Success {
								symbol = "✓"
							} else if r.HandshakeOK && !r.DataReceived {
								symbol = "⚠"
							} else {
								symbol = "✗"
							}
							break
						}
					}
					fmt.Printf("│  %s   ", symbol)
				}
				fmt.Println("│")
			}
		}
		fmt.Println()
	}

	fmt.Println("═══════════════════════════════════════════════════════════════")

	return nil
}
