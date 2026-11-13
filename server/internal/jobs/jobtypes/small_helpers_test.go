package jobtypes

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// A discovery timeout that is unset or not a duration resolves to 0, which the
// discovery engine reads as "use your own default": a typo never becomes an
// instant timeout.
func TestDiscoveryResolveTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unset", "", 0},
		{"valid", "45s", 45 * time.Second},
		{"not a duration", "soon", 0},
		{"bare number", "45", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			kubernetes := &KubernetesDiscoveryJobRun{}
			kubernetes.config.Timeout = tt.raw
			require.Equal(t, tt.want, kubernetes.resolveTimeout())

			container := &ContainerDiscoveryJobRun{}
			container.config.Timeout = tt.raw
			require.Equal(t, tt.want, container.resolveTimeout())
		})
	}
}

func TestDiscordEscalationWording(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	title := "  API   is\n down  "
	long := strings.Repeat("x", discordDetailCap+50)

	r.Equal("Incident", discordIncidentRef(nil))
	r.Equal("Incident", discordIncidentRef(&models.Incident{}), "no number yet")
	r.Equal("Incident #42", discordIncidentRef(&models.Incident{Number: 42}))

	r.Empty(discordEscalationDetail(nil))
	r.Equal("API is down", discordEscalationDetail(&models.Incident{Title: &title}), "whitespace is collapsed")
	r.Equal("open for 5m0s",
		discordEscalationDetail(&models.Incident{StartedAt: time.Now().Add(-5 * time.Minute)}),
		"an untitled incident says how long it has been open")

	capped := discordEscalationDetail(&models.Incident{Title: &long})
	r.True(strings.HasSuffix(capped, "…"))
	r.Equal(discordDetailCap-1, strings.Count(capped, "x"))
}

// The anchor's message id comes back as a float64 after a JSON round trip and
// as an integer when read in the process that wrote it.
func TestTelegramAnchorMessageID(t *testing.T) {
	t.Parallel()

	entry := func(value any) *models.StateEntry {
		return &models.StateEntry{Value: &models.JSONMap{telegramThreadMessageIDField: value}}
	}

	tests := []struct {
		name  string
		entry *models.StateEntry
		want  int64
	}{
		{"no entry", nil, 0},
		{"no value", &models.StateEntry{}, 0},
		{"float64", entry(float64(100)), 100},
		{"int64", entry(int64(101)), 101},
		{"int", entry(102), 102},
		{"string", entry("103"), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, telegramAnchorMessageID(tt.entry))
		})
	}
}

// A byte-wise truncation can cut a percent-escape in half; the broken tail is
// dropped so the URL stays valid.
func TestTrimPartialEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"abc", "abc"},
		{"abc%", "abc"},
		{"abc%4", "abc"},
		{"abc%41", "abc%41"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, trimPartialEscape(tt.in))
		})
	}
}
