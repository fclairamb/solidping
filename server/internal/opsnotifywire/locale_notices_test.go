package opsnotifywire_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/opsnotifywire"
)

// TestEmailJobIsWrittenInTheSuperAdminsLanguage drives the real email closure:
// the queued email job carries the notice written in the recipient's language
// and their locale, like every other email job.
func TestEmailJobIsWrittenInTheSuperAdminsLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	env := newWireEnv(t, &services.Registry{}, &config.Config{})
	r.NoError(env.db.EnsureDefaultEmailRoute(ctx, env.user.UID, env.orgUID, env.user.Email))
	r.NoError(env.db.UpdateUser(ctx, env.user.UID, &models.UserUpdate{
		Preferences: &models.UserPreferences{Locale: "de", Timezone: "Europe/Berlin"},
	}))

	registry := &services.Registry{
		Jobs: jobsvc.NewService(env.db.DB(), env.db, notifier.NewLocalEventNotifier(), nil),
	}
	deps := opsnotifywire.Build(env.db, registry, &config.Config{})

	report := opsnotify.DeliverToUser(ctx, deps, env.log, env.user.UID,
		opsnotify.NewTestNotice(env.user.Email, "https://solidping.example/d/"))
	r.Equal(1, report.Delivered)

	var jobs []*models.Job

	r.NoError(env.db.DB().NewSelect().Model(&jobs).
		Where("type = ?", string(jobdef.JobTypeEmail)).Scan(ctx))
	r.Len(jobs, 1)

	raw, err := json.Marshal(jobs[0].Config)
	r.NoError(err)

	var payload struct {
		To       []string `json:"to"`
		Subject  string   `json:"subject"`
		Text     string   `json:"text"`
		Locale   string   `json:"locale"`
		Timezone string   `json:"timezone"`
	}

	r.NoError(json.Unmarshal(raw, &payload))
	r.Equal([]string{env.user.Email}, payload.To)
	r.Equal("de", payload.Locale)
	r.Equal("Europe/Berlin", payload.Timezone)
	r.Equal("[SolidPing] Test der Betreiber-Benachrichtigungen", payload.Subject)
	r.Contains(payload.Text, "Angefordert von alice@acme.com.")
	r.Contains(payload.Text, "https://solidping.example/d/")
}
