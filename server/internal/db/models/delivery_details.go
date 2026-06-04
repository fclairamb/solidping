package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrDeliveryDetailsScanType is returned when DeliveryDetails.Scan receives a
// value of an unexpected SQL type.
var ErrDeliveryDetailsScanType = errors.New("unsupported type for DeliveryDetails.Scan")

// DeliveryDetailsBodyCap is the maximum number of bytes retained for a captured
// request or response body before persistence. Bodies larger than this are
// truncated; the channel sender is responsible for capping before it builds the
// DeliveryDetails so secrets in oversized bodies are never marshaled.
const DeliveryDetailsBodyCap = 16 * 1024

// DeliveryDetails holds structured, per-attempt notification delivery artifacts.
// It is stored as JSON (Postgres jsonb / SQLite text) in
// incident_notifications.delivery_details. Every field is optional: a channel
// populates only what it can produce, and a missing field is simply omitted.
//
// Privacy: callers MUST never place the webhook signing secret, an Authorization
// header, or any other secret into this struct. RequestURL must be stripped of
// its query string and userinfo before being set.
type DeliveryDetails struct {
	// HTTPStatusCode is the HTTP response status (e.g. 200, 503). Zero/omitted
	// for non-HTTP channels or when no response was received.
	HTTPStatusCode int `json:"httpStatusCode,omitempty"`
	// RequestURL is the target URL with the query string and any credentials
	// stripped (host + path only). Never the raw URL.
	RequestURL string `json:"requestUrl,omitempty"`
	// RequestBody is the payload sent to the receiver, capped at
	// DeliveryDetailsBodyCap bytes.
	RequestBody string `json:"requestBody,omitempty"`
	// ResponseBody is the receiver's response body, capped at
	// DeliveryDetailsBodyCap bytes.
	ResponseBody string `json:"responseBody,omitempty"`
	// DurationMs is the wall-clock time of the delivery attempt in milliseconds.
	DurationMs int64 `json:"durationMs,omitempty"`
	// ResponseHeaders is a small allowlisted set of response headers (e.g.
	// Retry-After, Content-Type). Never carries request-side secret headers.
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
}

// CapBody truncates a body to DeliveryDetailsBodyCap bytes, appending a marker
// when truncation occurred so the operator knows the stored copy is partial.
func CapBody(body []byte) string {
	if len(body) <= DeliveryDetailsBodyCap {
		return string(body)
	}

	return string(body[:DeliveryDetailsBodyCap]) + "\n…[truncated]"
}

// Value implements driver.Valuer so DeliveryDetails persists as a JSON string on
// both Postgres (jsonb) and SQLite (text). A nil pointer is handled by the
// driver as NULL before this is called; on a non-nil empty value it stores "{}".
func (d DeliveryDetails) Value() (driver.Value, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal DeliveryDetails: %w", err)
	}

	return string(data), nil
}

// Scan implements sql.Scanner for reading the JSON blob back from either engine.
func (d *DeliveryDetails) Scan(value any) error {
	if value == nil {
		return nil
	}

	var data []byte

	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("%w: %T", ErrDeliveryDetailsScanType, value)
	}

	if len(data) == 0 {
		return nil
	}

	return json.Unmarshal(data, d)
}
