package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestConvertCheckToResponseRedactedConfigKeys: the check response names the
// config keys that carry a credential in clear, so dash0 keeps their values
// out of the session replay (spec 2026-11-16-02).
func TestConvertCheckToResponseRedactedConfigKeys(t *testing.T) {
	t.Parallel()

	svc := &Service{}

	cases := []struct {
		name     string
		typ      string
		config   map[string]any
		expected []string
	}{
		{"heartbeat token", "heartbeat", map[string]any{"token": "abc", "period": "60s"}, []string{"token"}},
		{"email token", "email", map[string]any{"token": "abc"}, []string{"token"}},
		{
			"smtp delivery_to", "smtp",
			map[string]any{"host": "mx.acme.com", "delivery_to": "abc@inbox.acme.com"},
			[]string{"delivery_to"},
		},
		{"smtp without delivery_to", "smtp", map[string]any{"host": "mx.acme.com"}, nil},
		{"http has none", "http", map[string]any{"url": "https://acme.com"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			check := models.NewCheck("org", "c", tc.typ)
			check.Config = tc.config

			resp := svc.convertCheckToResponse(check)
			require.Equal(t, tc.expected, resp.RedactedConfigKeys)
		})
	}
}
