package notifications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestRegionInfoFor(t *testing.T) {
	t.Parallel()

	snapshot := map[string]any{
		"failingRegions": []any{"eu-west", "us-east"},
		"regionCount":    float64(3),
		"failQuorum":     float64(2),
	}

	cases := []struct {
		name         string
		incident     *models.Incident
		wantFailing  []string
		wantCount    int
		wantQuorum   int
		wantNilInfo  bool
		wantText     string
		wantTextPlan string
	}{
		{
			name:        "snapshot with several regions",
			incident:    &models.Incident{Details: models.JSONMap{"regions": snapshot}},
			wantFailing: []string{"eu-west", "us-east"}, wantCount: 3, wantQuorum: 2,
			wantText: "🇪🇺 Europe West, 🇺🇸 US East (2 of 3 regions)",
		},
		{
			name: "snapshot written in memory keeps Go types",
			incident: &models.Incident{Details: models.JSONMap{"regions": models.JSONMap{
				"failingRegions": []string{"eu-west"}, "regionCount": 3, "failQuorum": 1,
			}}},
			wantFailing: []string{"eu-west"}, wantCount: 3, wantQuorum: 1,
			wantText: "🇪🇺 Europe West (1 of 3 regions)",
		},
		{
			name: "first_result region only",
			incident: &models.Incident{Details: models.JSONMap{
				"first_result": map[string]any{"region": "eu-west"},
			}},
			wantFailing: []string{"eu-west"}, wantCount: 1, wantQuorum: 1,
			wantText: "🇪🇺 Europe West",
		},
		{
			name: "a relapse uses last_failure",
			incident: &models.Incident{Details: models.JSONMap{
				"first_result": map[string]any{"region": "eu-west"},
				"last_failure": map[string]any{"region": "us-east"},
			}},
			wantFailing: []string{"us-east"}, wantCount: 1, wantQuorum: 1,
			wantText: "🇺🇸 US East",
		},
		{
			name: "unknown slug falls back to the slug",
			incident: &models.Incident{Details: models.JSONMap{
				"first_result": map[string]any{"region": "mars-1"},
			}},
			wantFailing: []string{"mars-1"}, wantCount: 1, wantQuorum: 1,
			wantText: "mars-1",
		},
		{
			name:        "no region",
			incident:    &models.Incident{Details: models.JSONMap{"first_result": map[string]any{"status": "down"}}},
			wantNilInfo: true,
		},
		{
			name:        "no details",
			incident:    &models.Incident{},
			wantNilInfo: true,
		},
		{
			name: "burn incident",
			incident: &models.Incident{Kind: models.IncidentKindSLOBurn, Details: models.JSONMap{
				"first_result": map[string]any{"region": "eu-west"},
			}},
			wantNilInfo: true,
		},
		{
			name: "degraded incident",
			incident: &models.Incident{Kind: models.IncidentKindDegraded, Details: models.JSONMap{
				"regions": snapshot,
			}},
			wantNilInfo: true,
		},
	}

	labels := map[string]RegionLabel{
		"eu-west": {Emoji: "🇪🇺", Name: "Europe West"},
		"us-east": {Emoji: "🇺🇸", Name: "US East"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			info := RegionInfoFor(tc.incident)
			if tc.wantNilInfo {
				r.Nil(info)

				return
			}

			r.NotNil(info)
			r.Equal(tc.wantFailing, info.Failing)
			r.Equal(tc.wantCount, info.RegionCount)
			r.Equal(tc.wantQuorum, info.FailQuorum)

			info.Labels = labels
			r.Equal(tc.wantText, info.Text())
		})
	}
}

func TestRegionInfoPlainTextHasNoEmoji(t *testing.T) {
	t.Parallel()

	info := &RegionInfo{
		Failing: []string{"eu-west", "us-east"}, RegionCount: 3, FailQuorum: 2,
		Labels: map[string]RegionLabel{
			"eu-west": {Emoji: "🇪🇺", Name: "Europe West"},
			"us-east": {Emoji: "🇺🇸", Name: "US East"},
		},
	}

	require.Equal(t, "Europe West, US East (2 of 3 regions)", info.PlainText())
}

func TestRegionLineByEvent(t *testing.T) {
	t.Parallel()

	info := &RegionInfo{Failing: []string{"eu-west"}, RegionCount: 1, FailQuorum: 1}

	cases := map[string]string{
		eventTypeIncidentCreated:        "Failing from: eu-west",
		eventTypeIncidentReopened:       "Failing from: eu-west",
		eventTypeIncidentEscalated:      "Failing from: eu-west",
		"incident.reminder":             "Failing from: eu-west",
		eventTypeIncidentResolved:       "Failed from: eu-west",
		eventTypeIncidentComment:        "",
		eventTypeIncidentAcknowledged:   "",
		eventTypeIncidentUnacknowledged: "",
	}

	for event, want := range cases {
		got := RegionLine(&Payload{EventType: event, Incident: &models.Incident{}, Regions: info})
		require.Equal(t, want, got, event)
	}

	require.Empty(t, RegionLine(&Payload{EventType: eventTypeIncidentCreated, Incident: &models.Incident{}}))
}
