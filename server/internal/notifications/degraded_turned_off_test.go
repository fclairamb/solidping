package notifications

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// A degraded incident closed because degraded detection was turned off (spec
// 2026-09-24-08) must not be announced as "steady again": the check did not
// necessarily recover, the evaluator simply stopped looking. Each test has the
// ordinary auto-resolved degraded incident as its positive control.

func degradedResolvedPayload(turnedOff bool) *Payload {
	name, slug := "acme api", "acme-api"
	startedAt := time.Now().Add(-time.Hour)
	resolvedAt := time.Now()

	details := models.JSONMap{
		"degraded_failures_fired":  true,
		"degraded_failure_count":   float64(7),
		"degraded_failure_slots":   float64(60),
		"degraded_failures":        float64(5),
		"degraded_failures_window": float64(60),
	}

	resolutionType := models.ResolutionTypeAuto
	if turnedOff {
		details["degraded_turned_off"] = true
		resolutionType = models.ResolutionTypeDisabled
	}

	return &Payload{
		EventType: eventTypeIncidentResolved,
		Check:     &models.Check{UID: "check-1", Name: &name, Slug: &slug, Type: "http"},
		Incident: &models.Incident{
			UID: "inc-7", Number: 7, Kind: models.IncidentKindDegraded, StartedAt: startedAt,
			ResolvedAt: &resolvedAt, ResolutionType: &resolutionType, Details: details,
		},
		Integration: &models.Integration{Settings: models.JSONMap{"to": []any{"a@acme.com"}}},
		OrgSlug:     "acme",
		AppBaseURL:  "https://solidping.example",
	}
}

func TestDegradedInfoReadsTurnedOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(DegradedInfoFor(degradedResolvedPayload(true).Incident).TurnedOff)
	r.False(DegradedInfoFor(degradedResolvedPayload(false).Incident).TurnedOff)
}

func TestSlackDegradedTurnedOffIsNotSteadyAgain(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	sender := &SlackSender{}

	off := messageText(flatten(t, sender, degradedResolvedPayload(true)))
	r.Contains(off, "degraded detection was turned off")
	r.NotContains(off, "steady again")

	cleared := messageText(flatten(t, sender, degradedResolvedPayload(false)))
	r.Contains(cleared, "steady again")
	r.NotContains(cleared, "turned off")
}

func TestEmailDegradedTurnedOffIsNotSteadyAgain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		turnedOff   bool
		wantSubject string
		wantBody    string
		notInBody   string
	}{
		{"turned off", true, "[DEGRADED CLOSED]", "Degraded detection was turned off", "steady again"},
		{"cleared", false, "[DEGRADED CLEARED]", "steady again", "turned off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			sender := &fakeEmailSender{}
			jctx := &jobdef.JobContext{
				Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
				AppConfig: &config.Config{
					Auth:   config.AuthConfig{JWTSecret: "test-secret"},
					Server: config.ServerConfig{BaseURL: "https://solidping.example"},
				},
				Logger: slog.Default(),
			}

			r.NoError((&EmailSender{}).Send(context.Background(), jctx, degradedResolvedPayload(tc.turnedOff)))
			r.Len(sender.sent, 1)

			msg := sender.sent[0]
			r.Contains(msg.Subject, tc.wantSubject)
			r.Contains(msg.HTML, tc.wantBody)
			r.Contains(msg.Text, tc.wantBody)
			r.NotContains(msg.HTML, tc.notInBody)
			r.NotContains(msg.Text, tc.notInBody)
			r.NotContains(msg.HTML, "{{")
		})
	}
}
