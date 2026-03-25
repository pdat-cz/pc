package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	serial "go.bug.st/serial"
)

func runRelease(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("device path required\nUsage: pc release <device-path>\nExample: pc release /dev/ttyUSB0")
	}

	device := args[0]

	fmt.Printf("Attempting to release serial port: %s\n\n", device)

	success := false

	// Step 1: Try to remove lock files
	fmt.Println("1. Checking for lock files...")
	if removed, err := removeLockFiles(device); err != nil {
		fmt.Printf("   ⚠ Error checking lock files: %v\n", err)
	} else if removed > 0 {
		fmt.Printf("   ✓ Removed %d lock file(s)\n", removed)
		success = true
	} else {
		fmt.Println("   • No lock files found")
	}

	// Step 2: Try to open and close the port (clears OS-level locks)
	fmt.Println("\n2. Attempting to open and close port...")
	if err := openAndClose(device); err != nil {
		fmt.Printf("   ✗ Failed: %v\n", err)
	} else {
		fmt.Println("   ✓ Successfully opened and closed port")
		success = true
	}

	// Step 3: Check for processes using the port
	fmt.Println("\n3. Checking for processes using the port...")
	if procs, err := findProcessesUsingPort(device); err != nil {
		fmt.Printf("   ⚠ Error checking processes: %v\n", err)
	} else if len(procs) > 0 {
		fmt.Printf("   ⚠ Found %d process(es) using the port:\n", len(procs))
		for _, proc := range procs {
			fmt.Printf("      %s\n", proc)
		}
		fmt.Println("\n   To forcefully kill these processes, run:")
		fmt.Printf("      sudo fuser -k %s\n", device)
		fmt.Println("   WARNING: This will terminate the process(es) immediately!")
		success = true // We found the culprit
	} else {
		fmt.Println("   • No processes found using the port")

		// If still locked but no processes found, try with sudo
		fmt.Println("\n   Trying with elevated privileges...")
		if procs, err := findProcessesWithSudo(device); err != nil {
			fmt.Printf("   ⚠ Error: %v\n", err)
		} else if len(procs) > 0 {
			fmt.Printf("   ✓ Found %d process(es) with sudo:\n", len(procs))
			for _, proc := range procs {
				fmt.Printf("      %s\n", proc)
			}
			fmt.Println("\n   To forcefully kill these processes, run:")
			fmt.Printf("      sudo fuser -k %s\n", device)
			success = true
		}
	}

	// Step 4: Check for systemd services
	fmt.Println("\n4. Checking systemd services...")
	if services := checkSystemdServices(device); len(services) > 0 {
		fmt.Printf("   ⚠ Found %d active service(s) using the port:\n", len(services))
		for _, svc := range services {
			fmt.Printf("      %s\n", svc)
		}
		fmt.Println("\n   To stop these services, run:")
		for _, svc := range services {
			fmt.Printf("      sudo systemctl stop %s\n", svc)
		}
		fmt.Println("\n   To prevent auto-start on boot:")
		for _, svc := range services {
			fmt.Printf("      sudo systemctl disable %s\n", svc)
		}
		success = true
	} else {
		fmt.Println("   • No systemd services found using the port")
	}

	// Step 5: Show kernel driver info
	fmt.Println("\n5. Kernel driver information...")
	showDriverInfo(device)

	// Summary
	fmt.Println("\n" + strings.Repeat("═", 60))
	if success {
		fmt.Println("✓ Port release successful!")
		fmt.Printf("  %s should now be available for use.\n", device)
	} else {
		fmt.Println("⚠ Port release incomplete")
		fmt.Println("  The port may still be locked by another process.")
		fmt.Println("  Try the commands suggested above.")
	}
	fmt.Println(strings.Repeat("═", 60))

	return nil
}

// removeLockFiles removes lock files for the given device
func removeLockFiles(device string) (int, error) {
	// Extract device name (e.g., "ttyUSB0" from "/dev/ttyUSB0")
	deviceName := filepath.Base(device)

	// Common lock file locations
	lockDirs := []string{
		"/var/lock",
		"/var/spool/lock",
		"/var/run/lock",
	}

	removed := 0

	for _, dir := range lockDirs {
		// Check common lock file patterns
		patterns := []string{
			filepath.Join(dir, "LCK.."+deviceName),
			filepath.Join(dir, "LK."+deviceName),
			filepath.Join(dir, deviceName+".lock"),
		}

		for _, lockFile := range patterns {
			if _, err := os.Stat(lockFile); err == nil {
				// File exists, try to remove it
				if err := os.Remove(lockFile); err != nil {
					// If permission denied, try with sudo
					cmd := exec.Command("sudo", "rm", "-f", lockFile)
					if err := cmd.Run(); err != nil {
						fmt.Printf("   ⚠ Failed to remove %s: %v\n", lockFile, err)
						continue
					}
				}
				fmt.Printf("   Removed: %s\n", lockFile)
				removed++
			}
		}
	}

	return removed, nil
}

// openAndClose attempts to open and immediately close the port
// This clears OS-level locks
func openAndClose(device string) error {
	// Try to open the port with basic settings
	mode := &serial.Mode{
		BaudRate: 9600,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}

	port, err := serial.Open(device, mode)
	if err != nil {
		return fmt.Errorf("cannot open: %w", err)
	}

	// Give OS a moment to register the open
	time.Sleep(100 * time.Millisecond)

	// Close the port
	if err := port.Close(); err != nil {
		return fmt.Errorf("cannot close: %w", err)
	}

	// Give OS a moment to release the lock
	time.Sleep(100 * time.Millisecond)

	return nil
}

// findProcessesUsingPort finds processes that have the port open
func findProcessesUsingPort(device string) ([]string, error) {
	// Try lsof first (more portable)
	cmd := exec.Command("lsof", device)
	output, err := cmd.CombinedOutput()
	if err == nil && len(output) > 0 {
		lines := strings.Split(string(output), "\n")
		// Skip header and empty lines
		procs := []string{}
		for i, line := range lines {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue
			}
			procs = append(procs, line)
		}
		return procs, nil
	}

	// Try fuser as fallback
	cmd = exec.Command("fuser", device)
	output, err = cmd.CombinedOutput()
	if err == nil && len(output) > 0 {
		return []string{string(output)}, nil
	}

	// No processes found (or tools not available)
	return nil, nil
}

// findProcessesWithSudo tries to find processes using sudo
func findProcessesWithSudo(device string) ([]string, error) {
	// Try sudo lsof
	cmd := exec.Command("sudo", "lsof", device)
	output, err := cmd.CombinedOutput()
	if err == nil && len(output) > 0 {
		lines := strings.Split(string(output), "\n")
		procs := []string{}
		for i, line := range lines {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue
			}
			procs = append(procs, line)
		}
		return procs, nil
	}

	// Try sudo fuser
	cmd = exec.Command("sudo", "fuser", "-v", device)
	output, err = cmd.CombinedOutput()
	if err == nil && len(output) > 0 {
		return []string{string(output)}, nil
	}

	return nil, nil
}

// checkSystemdServices checks for systemd services that might be using the port
func checkSystemdServices(device string) []string {
	deviceName := filepath.Base(device)
	services := []string{}

	// Common service patterns
	serviceNames := []string{
		"serial-getty@" + deviceName + ".service",
		"getty@" + deviceName + ".service",
		"ModemManager.service",
	}

	for _, svc := range serviceNames {
		cmd := exec.Command("systemctl", "is-active", svc)
		output, err := cmd.Output()
		if err == nil && strings.TrimSpace(string(output)) == "active" {
			services = append(services, svc)
		}
	}

	return services
}

// showDriverInfo displays kernel driver information for the device
func showDriverInfo(device string) {
	deviceName := filepath.Base(device)

	// Check dmesg for recent messages about this device
	cmd := exec.Command("dmesg")
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		found := false
		for _, line := range lines {
			if strings.Contains(line, deviceName) {
				if !found {
					fmt.Println("   Recent kernel messages:")
					found = true
				}
				// Show last 3 relevant lines
				fmt.Printf("      %s\n", strings.TrimSpace(line))
			}
		}
		if !found {
			fmt.Println("   • No recent kernel messages")
		}
	}

	// Check sys filesystem for driver info
	sysPath := "/sys/class/tty/" + deviceName + "/device/driver"
	if target, err := os.Readlink(sysPath); err == nil {
		fmt.Printf("   Driver: %s\n", filepath.Base(target))
	}
}
