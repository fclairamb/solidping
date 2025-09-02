//go:build windows

package pretty

import (
	"os"

	"golang.org/x/sys/windows"
)

func terminalSupportsColor() bool {
	handle := windows.Handle(os.Stdout.Fd())
	var mode uint32

	err := windows.GetConsoleMode(handle, &mode)
	if err != nil {
		return false
	}

	// Enable ENABLE_VIRTUAL_TERMINAL_PROCESSING
	mode |= windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	err = windows.SetConsoleMode(handle, mode)
	return err == nil
}
