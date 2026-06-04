package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeliveryDetails_ValueScanRoundTrip verifies the JSON Valuer/Scanner pair
// round-trips losslessly, matching how the column is persisted on Postgres
// (jsonb) and SQLite (text). This is the parity guard: both engines store and
// read the same JSON shape through this one codec.
func TestDeliveryDetails_ValueScanRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	original := &DeliveryDetails{
		HTTPStatusCode: 503,
		RequestURL:     "https://hooks.example.com/path",
		RequestBody:    `{"type":"incident.created"}`,
		ResponseBody:   `{"error":"unavailable"}`,
		DurationMs:     1234,
		ResponseHeaders: map[string]string{
			"Retry-After":  "120",
			"Content-Type": "application/json",
		},
	}

	stored, err := original.Value()
	r.NoError(err)

	// Scan from the string form (SQLite) and the []byte form (Postgres jsonb).
	var fromString DeliveryDetails
	r.NoError(fromString.Scan(stored))
	r.Equal(*original, fromString)

	var fromBytes DeliveryDetails
	r.NoError(fromBytes.Scan([]byte(stored.(string))))
	r.Equal(*original, fromBytes)
}

// TestDeliveryDetails_ScanNullAndEmpty ensures a NULL column or an empty value
// scans into a zero-value struct without error (pre-feature rows).
func TestDeliveryDetails_ScanNullAndEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var d DeliveryDetails
	r.NoError(d.Scan(nil))
	r.Equal(DeliveryDetails{}, d)

	var e DeliveryDetails
	r.NoError(e.Scan(""))
	r.Equal(DeliveryDetails{}, e)
}
