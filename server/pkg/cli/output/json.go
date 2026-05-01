// Package output provides output formatting utilities for CLI commands.
package output

import (
	"encoding/json"
	"io"
)

// JSON output keys used in CLI responses.
const (
	keyError   = "error"
	keyMessage = "message"
	keySuccess = "success"
)

// JSONOutputter outputs data in JSON format.
type JSONOutputter struct {
	writer io.Writer
}

// NewJSONOutputter creates a new JSON outputter.
func NewJSONOutputter(writer io.Writer) *JSONOutputter {
	return &JSONOutputter{writer: writer}
}

// Print outputs data as JSON.
func (o *JSONOutputter) Print(data interface{}) error {
	encoder := json.NewEncoder(o.writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// PrintError outputs an error as JSON.
func (o *JSONOutputter) PrintError(err error) error {
	return o.Print(map[string]interface{}{
		keyError: map[string]string{
			keyMessage: err.Error(),
		},
	})
}

// Success prints a success message as JSON.
func (o *JSONOutputter) Success(message string) error {
	return o.Print(map[string]interface{}{
		keySuccess: true,
		keyMessage: message,
	})
}

// PrintJSON is a helper to print any data as JSON.
func PrintJSON(writer io.Writer, data interface{}) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// FormatError formats an error for JSON output.
func FormatError(err error) map[string]interface{} {
	return map[string]interface{}{
		keyError: map[string]string{
			keyMessage: err.Error(),
		},
	}
}

// FormatSuccess formats a success message for JSON output.
func FormatSuccess(message string, data interface{}) map[string]interface{} {
	result := map[string]interface{}{
		keySuccess: true,
		keyMessage: message,
	}
	if data != nil {
		result["data"] = data
	}
	return result
}
