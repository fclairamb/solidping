//go:build windows

package logsetup

import (
	"io"

	"golang.org/x/sys/windows"
)

// enableANSI turns on virtual terminal processing on a Windows console, so it
// renders the ANSI color sequences instead of printing them.
func enableANSI(out io.Writer) {
	file, ok := out.(fdWriter)
	if !ok {
		return
	}

	handle := windows.Handle(file.Fd())

	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return
	}

	_ = windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
}
