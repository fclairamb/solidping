//go:build !windows

package logsetup

import "io"

// enableANSI is a no-op outside Windows: terminals render ANSI natively.
func enableANSI(io.Writer) {}
