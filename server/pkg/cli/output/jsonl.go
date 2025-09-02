package output

import (
	"encoding/json"
	"io"
)

// JSONLOutputter outputs data as newline-delimited JSON (one object per line).
type JSONLOutputter struct {
	writer io.Writer
}

// NewJSONLOutputter creates a new JSONL outputter.
func NewJSONLOutputter(writer io.Writer) *JSONLOutputter {
	return &JSONLOutputter{writer: writer}
}

// Print outputs data as a single JSON line.
func (o *JSONLOutputter) Print(data interface{}) error {
	return json.NewEncoder(o.writer).Encode(data)
}

// PrintError outputs an error as a single JSON line.
func (o *JSONLOutputter) PrintError(err error) error {
	return o.Print(map[string]interface{}{
		"error": map[string]string{
			"message": err.Error(),
		},
	})
}

// Success prints a success message as a single JSON line.
func (o *JSONLOutputter) Success(message string) error {
	return o.Print(map[string]interface{}{
		"success": true,
		"message": message,
	})
}
