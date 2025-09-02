package output

import (
	"io"
	"os"
)

// Format represents the output format for CLI commands.
type Format string

const (
	// FormatText outputs human-readable tables and key-value pairs.
	FormatText Format = "text"
	// FormatJSON outputs pretty-printed JSON.
	FormatJSON Format = "json"
	// FormatJSONL outputs one JSON object per line (newline-delimited JSON).
	FormatJSONL Format = "jsonl"
)

// Outputter defines the interface for different output formats.
type Outputter interface {
	// Print outputs data in the appropriate format
	Print(data interface{}) error
	// PrintError outputs an error message
	PrintError(err error) error
	// Success prints a success message
	Success(message string) error
}

// NewOutputter creates an outputter based on the format.
func NewOutputter(format Format, writer io.Writer) Outputter {
	if writer == nil {
		writer = os.Stdout
	}

	switch format {
	case FormatJSON:
		return NewJSONOutputter(writer)
	case FormatJSONL:
		return NewJSONLOutputter(writer)
	case FormatText:
		return NewTableOutputter(writer)
	default:
		return NewTableOutputter(writer)
	}
}
