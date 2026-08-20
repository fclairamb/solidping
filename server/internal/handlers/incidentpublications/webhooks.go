package incidentpublications

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// WebhookPayload is the body delivered to outgoing webhook connections for a
// publication lifecycle event.
//
// It is the PUBLIC view on purpose: a webhook consumer receiving
// `statuspage.incident.published` is receiving the same facts the status page
// shows, not the operational incident's diagnostics. The incident UID is
// carried so a consumer that also subscribes to `incident.*` can correlate the
// two, but nothing derived from probe output travels here.
type WebhookPayload struct {
	Event         string     `json:"event"`
	OccurredAt    time.Time  `json:"occurredAt"`
	PublicationID string     `json:"publicationUid"`
	StatusPageUID string     `json:"statusPageUid"`
	IncidentUID   *string    `json:"incidentUid,omitempty"`
	Title         string     `json:"title"`
	State         string     `json:"state"`
	Severity      *string    `json:"severity,omitempty"`
	AutoCreated   bool       `json:"autoCreated"`
	PublishedAt   time.Time  `json:"publishedAt"`
	ResolvedAt    *time.Time `json:"resolvedAt,omitempty"`
}

// webhookJobConfig mirrors jobtypes.WebhookJobConfig on the wire. It is
// duplicated rather than imported because jobtypes imports THIS package (to run
// the auto-publish job), and a cycle is a worse problem than four fields.
type webhookJobConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    map[string]any    `json:"body"`
}

// SetJobsService wires the job queue used to deliver publication webhooks.
// Optional: without it, publication events are recorded but nothing is
// delivered outbound.
func (s *Service) SetJobsService(jobs jobsvc.Service) {
	s.jobs = jobs
}

// dispatchWebhooks fans a publication lifecycle event out to the org's enabled
// webhook connections. Best-effort: a failure to enqueue is logged and never
// fails the publication, exactly like the incident notification fan-out.
//
// Only `webhook` connections are targeted. The chat integrations (Slack,
// Discord, …) intentionally stay on the internal incident.* lifecycle: an
// on-call channel wants to hear that a check went down, not that the marketing
// status page was updated.
func (s *Service) dispatchWebhooks(
	ctx context.Context, orgUID string, eventType models.EventType, pub *models.IncidentPublication,
) {
	if s.jobs == nil {
		return
	}

	webhookType := models.ConnectionTypeWebhook
	enabled := true

	conns, err := s.db.ListChannels(ctx, &models.ListIntegrationsFilter{
		OrganizationUID: orgUID,
		Type:            &webhookType,
		Enabled:         &enabled,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "publication webhook fan-out: failed to list webhook connections",
			"orgUid", orgUID, "error", err)

		return
	}

	if len(conns) == 0 {
		return
	}

	payload := WebhookPayload{
		Event:         string(eventType),
		OccurredAt:    s.clock.Now(),
		PublicationID: pub.UID,
		StatusPageUID: pub.StatusPageUID,
		IncidentUID:   pub.IncidentUID,
		Title:         pub.PublicTitle,
		State:         string(pub.PublicState),
		Severity:      pub.Severity,
		AutoCreated:   pub.AutoCreated,
		PublishedAt:   pub.PublishedAt,
		ResolvedAt:    pub.ResolvedAt,
	}

	body := map[string]any{}

	raw, err := json.Marshal(payload)
	if err == nil {
		_ = json.Unmarshal(raw, &body)
	}

	for _, conn := range conns {
		url, ok := conn.Settings["url"].(string)
		if !ok || url == "" {
			continue
		}

		config, marshalErr := json.Marshal(webhookJobConfig{
			URL:    url,
			Method: http.MethodPost,
			Body:   body,
		})
		if marshalErr != nil {
			continue
		}

		if _, jobErr := s.jobs.CreateJob(
			ctx, orgUID, string(jobdef.JobTypeWebhook), config, nil,
		); jobErr != nil {
			s.logger.WarnContext(ctx, "publication webhook fan-out: failed to enqueue webhook job",
				"connectionUid", conn.UID, "publicationUid", pub.UID, "error", jobErr)
		}
	}
}
