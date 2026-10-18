// Package protocol holds the message bodies a SolidPing server exchanges with
// an executable plugin (spec 2026-10-17-05 §15). The exec transport carries
// them inside a JSON-RPC 2.0 envelope over stdin/stdout; the HTTP transport
// (spec 2026-10-17-06) reuses them unchanged.
package protocol

import (
	"encoding/json"
	"time"
)

// Method names.
const (
	MethodExecute  = "execute"
	MethodValidate = "validate"
)

// JSONRPCVersion is the only envelope version spoken.
const JSONRPCVersion = "2.0"

// ExecuteParams asks the plugin to run one check.
type ExecuteParams struct {
	// Type is the plugin check type, e.g. "x-gopher".
	Type string `json:"type"`
	// Config is the validated check config, secrets included.
	Config map[string]any `json:"config"`
	// Deadline is when the server stops waiting (RFC 3339).
	Deadline time.Time `json:"deadline"`
}

// ExecuteResult is a plugin's answer to execute.
type ExecuteResult struct {
	// Status is up, down, timeout, error or warning.
	Status string `json:"status"`
	// Output is diagnostic data; Output["message"] is the summary line.
	Output map[string]any `json:"output,omitempty"`
	// Metrics are numeric values only.
	Metrics map[string]any `json:"metrics,omitempty"`
}

// ValidateParams asks the plugin to validate a config the schema accepted.
type ValidateParams struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// ValidateResult is a plugin's answer to validate. An empty Errors list means
// the config is valid.
type ValidateResult struct {
	Errors []ValidationError `json:"errors,omitempty"`
}

// ValidationError names one invalid config key.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Request is the JSON-RPC 2.0 request envelope.
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// Response is the JSON-RPC 2.0 response envelope.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// TypeManifest is one plugin check type as a private-location agent reports
// it on connect (spec 2026-10-17-05 §18). The server stores it per org, so
// the form and the validation of that type work while the agent is offline.
// Schema is the raw schema file of the type, not the published document.
type TypeManifest struct {
	Type          string          `json:"type"`
	Plugin        string          `json:"plugin"`
	Version       string          `json:"version"`
	Protocol      string          `json:"protocol"`
	Description   string          `json:"description,omitempty"`
	Labels        []string        `json:"labels,omitempty"`
	MinPeriod     string          `json:"minPeriod,omitempty"`
	DefaultPeriod string          `json:"defaultPeriod,omitempty"`
	MaxPeriod     string          `json:"maxPeriod,omitempty"`
	Schema        json.RawMessage `json:"schema"`
}
