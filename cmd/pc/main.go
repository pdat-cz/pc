package main

import (
	"fmt"
	"os"
)

func main() {
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
	case "tui":
		if err := runTUI(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		usage()
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown subcommand:", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("p.d.a. commander (pc) - personal digital assistant for fieldbus protocols")
	fmt.Println("Usage:")
	fmt.Println("  pc cat <uri> <addr> [addr...]")
	fmt.Println("  pc set <uri> <addr=value> [addr=value...]")
	fmt.Println("  pc tui")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  pc cat modbus+tcp://127.0.0.1:502?unit=1 holding/1 holding/2@u16")
	fmt.Println("  pc set modbus+tcp://127.0.0.1:502?unit=1 holding/10@u16=1234")
}
