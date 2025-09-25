package tui

import (
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// App runs a minimal dual-pane TUI skeleton.
func App(version string) error {
	app := tview.NewApplication()
	left := tview.NewList()
	left.SetBorder(true).SetTitle("Devices / Bookmarks")
	left.AddItem("Add device (F7)", "", '7', nil)
	left.AddItem("modbus+tcp://127.0.0.1:502?unit=1", "example", 0, nil)

	right := tview.NewTable()
	right.SetBorder(true).SetTitle("Points / Values")
	right.SetCell(0, 0, tview.NewTableCell("Path").SetSelectable(false))
	right.SetCell(0, 1, tview.NewTableCell("Value").SetSelectable(false))

	status := tview.NewTextView().SetDynamicColors(true)
	status.SetBorder(true).SetTitle("Status")
	if version == "" {
		version = "dev"
	}
	_, _ = fmt.Fprintf(status, "[green]p.d.a. commander[-]  v%s  %s", version, time.Now().Format(time.RFC3339))

	log := tview.NewTextView().SetDynamicColors(true)
	log.SetBorder(true).SetTitle("Log (F2)")
	log.SetChangedFunc(func() { app.Draw() })

	main := tview.NewFlex().AddItem(left, 0, 1, true).AddItem(right, 0, 2, false)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(main, 0, 1, true).
		AddItem(log, 7, 0, false).
		AddItem(status, 1, 0, false)

	app.SetRoot(root, true)
	app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case 'q':
			app.Stop()
		default:
			// no-op
		}
		switch ev.Key() {
		case tcell.KeyF10:
			app.Stop()
		default:
			// no-op
		}
		return ev
	})

	if err := app.Run(); err != nil {
		return err
	}
	return nil
}
