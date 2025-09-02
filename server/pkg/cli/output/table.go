package output

import (
	"errors"
	"fmt"
	"io"

	"github.com/gookit/color"
	"github.com/jedib0t/go-pretty/v6/table"
)

// ErrUnsupportedDataType is returned when an unsupported data type is provided.
var ErrUnsupportedDataType = errors.New("unsupported data type for table output")

// TableOutputter outputs data in human-readable table format.
type TableOutputter struct {
	writer io.Writer
}

// NewTableOutputter creates a new table outputter.
func NewTableOutputter(writer io.Writer) *TableOutputter {
	return &TableOutputter{writer: writer}
}

// Print outputs data as a formatted table.
func (o *TableOutputter) Print(data interface{}) error {
	// For simple string messages
	if str, ok := data.(string); ok {
		_, err := fmt.Fprintln(o.writer, str)
		return err
	}

	// For structured data, caller should handle table creation
	return ErrUnsupportedDataType
}

// PrintError outputs an error message in red.
func (o *TableOutputter) PrintError(err error) error {
	_, writeErr := fmt.Fprintf(o.writer, "%s: %s\n", color.Red.Sprint("Error"), err.Error())
	return writeErr
}

// Success prints a success message in green.
func (o *TableOutputter) Success(message string) error {
	_, err := fmt.Fprintln(o.writer, color.Green.Sprint(message))
	return err
}

// PrintMessage prints a plain message.
func PrintMessage(writer io.Writer, message string) {
	_, _ = fmt.Fprintln(writer, message)
}

// PrintSuccess prints a success message in green.
func PrintSuccess(writer io.Writer, message string) {
	_, _ = fmt.Fprintln(writer, color.Green.Sprint(message))
}

// PrintError prints an error message in red.
func PrintError(writer io.Writer, message string) {
	_, _ = fmt.Fprintf(writer, "%s: %s\n", color.Red.Sprint("Error"), message)
}

// PrintWarning prints a warning message in yellow.
func PrintWarning(writer io.Writer, message string) {
	_, _ = fmt.Fprintf(writer, "%s: %s\n", color.Yellow.Sprint("Warning"), message)
}

// NewTable creates a new table writer with default styling.
func NewTable(writer io.Writer) table.Writer {
	tbl := table.NewWriter()
	tbl.SetOutputMirror(writer)
	tbl.SetStyle(table.StyleLight)
	return tbl
}

// PrintTable prints a table with headers and rows.
func PrintTable(writer io.Writer, headers []string, rows [][]interface{}) {
	tbl := NewTable(writer)

	// Add headers
	headerRow := make(table.Row, len(headers))
	for i, h := range headers {
		headerRow[i] = h
	}
	tbl.AppendHeader(headerRow)

	// Add rows
	for _, row := range rows {
		tbl.AppendRow(row)
	}

	tbl.Render()
}

// PrintKeyValue prints key-value pairs in a formatted way.
func PrintKeyValue(writer io.Writer, pairs map[string]string) {
	for key, value := range pairs {
		_, _ = fmt.Fprintf(writer, "%s: %s\n", color.Bold.Sprint(key), value)
	}
}
