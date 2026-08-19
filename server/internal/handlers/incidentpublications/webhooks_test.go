package incidentpublications_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// webhookJob is one enqueued webhook delivery, decoded far enough to assert on.
type webhookJob struct {
	URL  string         `json:"url"`
	Body map[string]any `json:"body"`
}

// enqueuedWebhooks reads the webhook jobs the publication service created,
// oldest first. Going through the real jobs table (rather than a mock) is the
// point: it proves the events reach the DELIVERY path, not merely a function
// call inside the service.
func enqueuedWebhooks(t *testing.T, dbSvc *sqlite.Service, orgUID string) []webhookJob {
	t.Helper()

	jobs, err := dbSvc.ListJobs(t.Context(), &orgUID, 100)
	require.NoError(t, err)

	out := make([]webhookJob, 0, len(jobs))

	// ListJobs returns newest first; the lifecycle reads oldest first.
	for i := len(jobs) - 1; i >= 0; i-- {
		if jobs[i].Type != string(jobdef.JobTypeWebhook) {
			continue
		}

		raw, marshalErr := json.Marshal(jobs[i].Config)
		require.NoError(t, marshalErr)

		var decoded webhookJob
		require.NoError(t, json.Unmarshal(raw, &decoded))

		out = append(out, decoded)
	}

	return out
}

func eventNames(jobs []webhookJob) []string {
	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if name, ok := job.Body["event"].(string); ok {
			names = append(names, name)
		}
	}

	return names
}

// TestPublicationWebhooksFireOnEveryLifecycleStep proves the three forked
// `statuspage.incident.*` events actually reach the outgoing-webhook delivery
// path on publish, update and resolve — and that the payload carries the
// PUBLIC view of the incident, never the operational one.
func TestPublicationWebhooksFireOnEveryLifecycleStep(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	clk := clock.NewFake(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	page := models.NewStatusPage(org.UID, "Acme status", "public")
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	// An enabled webhook connection is the delivery target.
	conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "Ops webhook")
	conn.Settings["url"] = "https://hooks.acme.com/solidping"
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	// A DISABLED one, and a non-webhook one, must both be ignored: chat
	// integrations stay on the internal incident lifecycle, because an on-call
	// channel wants to hear that a check went down, not that the marketing
	// status page was edited.
	disabled := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "Retired webhook")
	disabled.Settings["url"] = "https://hooks.acme.com/retired"
	r.NoError(dbSvc.CreateChannel(ctx, disabled))

	// Disabling has to go through UpdateChannel, not through the struct before
	// the insert: `enabled` is tagged `notnull,default:true`, so bun OMITS a
	// false from the INSERT and the column lands on its DB default of true. A
	// row "disabled" that way would silently still be enabled, and this test
	// would prove nothing.
	off := false
	r.NoError(dbSvc.UpdateChannel(ctx, disabled.UID, &models.IntegrationUpdate{Enabled: &off}))

	reloaded, err := dbSvc.GetChannel(ctx, disabled.UID)
	r.NoError(err)
	r.False(reloaded.Enabled, "the second webhook really is disabled")

	slack := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Ops channel")
	r.NoError(dbSvc.CreateChannel(ctx, slack))

	svc := incidentpublications.NewService(dbSvc, clk, nil)
	svc.SetJobsService(jobs)

	// --- published ---
	created, err := svc.CreatePublication(ctx, org.Slug, page.UID, user.UID,
		&incidentpublications.CreatePublicationRequest{Title: "Payments API is degraded"})
	r.NoError(err)

	sent := enqueuedWebhooks(t, dbSvc, org.UID)
	r.Len(sent, 1, "exactly one delivery: the enabled webhook connection only")
	r.Equal("https://hooks.acme.com/solidping", sent[0].URL)
	r.Equal(string(models.EventTypeStatusPageIncidentPublished), sent[0].Body["event"])
	r.Equal(created.UID, sent[0].Body["publicationUid"])
	r.Equal(page.UID, sent[0].Body["statusPageUid"])
	r.Equal("Payments API is degraded", sent[0].Body["title"])
	r.Equal("investigating", sent[0].Body["state"])

	// --- updated ---
	severity := "major"
	_, err = svc.UpdatePublication(ctx, org.Slug, page.UID, created.UID, user.UID,
		&incidentpublications.UpdatePublicationRequest{Severity: &severity})
	r.NoError(err)

	// --- resolved ---
	resolved := "resolved"
	_, err = svc.UpdatePublication(ctx, org.Slug, page.UID, created.UID, user.UID,
		&incidentpublications.UpdatePublicationRequest{State: &resolved})
	r.NoError(err)

	sent = enqueuedWebhooks(t, dbSvc, org.UID)
	r.Equal([]string{
		string(models.EventTypeStatusPageIncidentPublished),
		string(models.EventTypeStatusPageIncidentUpdated),
		string(models.EventTypeStatusPageIncidentResolved),
	}, eventNames(sent), "the full publication lifecycle reaches the webhook path")

	r.Equal("major", sent[2].Body["severity"])
	r.NotEmpty(sent[2].Body["resolvedAt"])
}

// TestPublicationWebhooksAreSilentWithoutAConnection is the negative half: an
// org with no webhook connection enqueues nothing at all, so the events cannot
// be an unconditional job-table write that only looks correct above.
func TestPublicationWebhooksAreSilentWithoutAConnection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	clk := clock.NewFake(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	page := models.NewStatusPage(org.UID, "Acme status", "public")
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	svc := incidentpublications.NewService(dbSvc, clk, nil)
	svc.SetJobsService(jobs)

	_, err = svc.CreatePublication(ctx, org.Slug, page.UID, user.UID,
		&incidentpublications.CreatePublicationRequest{Title: "Planned maintenance"})
	r.NoError(err)

	r.Empty(enqueuedWebhooks(t, dbSvc, org.UID))
}
