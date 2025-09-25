package main

import (
	"fmt"
	"github.com/pdat-cz/pc/internal/tui"
)

func runTUI(args []string) error {
	_ = args // reserved for future TUI options
	if err := tui.App(version); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
