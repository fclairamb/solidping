package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/activation"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

var (
	// ErrConnectionNotFound is returned when the connection is not found.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrIncidentNotFound is returned when the incident is not found.
	ErrIncidentNotFound = errors.New("incident not found")
	// ErrCheckNotFound is returned when the check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrSenderNotFound is returned when no sender is found for the connection type.
	ErrSenderNotFound = errors.New("sender not found for connection type")
	// ErrMissingConnectionUID is returned when connectionUid is not provided.
	ErrMissingConnectionUID = errors.New("connectionUid is required")
	// ErrMissingIncidentUID is returned when incidentUid is not provided.
	ErrMissingIncidentUID = errors.New("incidentUid is required")
	// ErrMissingEventType is returned when eventType is not provided.
	ErrMissingEventType = errors.New("eventType is required")
	// ErrEncryptionDisabled is returned when a connection has encrypted
	// settings but the credentials service is not available (no master key).
	ErrEncryptionDisabled = errors.New("connection has encrypted settings but encryption is disabled")
)

// NotificationJobConfig configures notification parameters.
type NotificationJobConfig struct {
	ConnectionUID string `json:"connectionUid"`
	IncidentUID   string `json:"incidentUid"`
	EventType     string `json:"eventType"` // "incident.created", "incident.resolved", "incident.escalated"
	// Comment carries the body and author of an `incident.comment` event.
	// Embedded in the job rather than re-read from the event row at send time:
	// the job then renders exactly the text that was commented, even if the
	// event is aggregated away or the incident is deleted before delivery.
	Comment *notifications.CommentInfo `json:"comment,omitempty"`
	// Acknowledgment carries who acknowledged an incident and from where, for
	// `incident.acknowledged`. Embedded for the same reason as Comment: the
	// notice must name the person who actually acked even if the incident is
	// unacked — or re-acked by somebody else — between enqueue and delivery.
	Acknowledgment *notifications.AckInfo `json:"acknowledgment,omitempty"`
	// JobUID is the UID of the job row itself. Populated at Sites 1+2 so that
	// NotificationJobRun.Run can update the matching audit row by job_uid.
	JobUID string `json:"jobUid,omitempty"`
}

// NotificationJobDefinition is the factory for notification jobs.
type NotificationJobDefinition struct{}

// Type returns the job type identifier.
func (d *NotificationJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeNotification
}

// CreateJobRun creates a new notification job run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *NotificationJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg NotificationJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing notification config: %w", err)
	}

	if cfg.ConnectionUID == "" {
		return nil, ErrMissingConnectionUID
	}
	if cfg.IncidentUID == "" {
		return nil, ErrMissingIncidentUID
	}
	if cfg.EventType == "" {
		return nil, ErrMissingEventType
	}

	return &NotificationJobRun{config: cfg}, nil
}

// NotificationJobRun is the executable instance of a notification job.
type NotificationJobRun struct {
	config NotificationJobConfig
}

// Run executes the notification job.
func (r *NotificationJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	// 1. Load connection
	connection, err := jctx.DBService.GetChannel(ctx, r.config.ConnectionUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionNotFound, err)
	}

	// Decrypt and merge any encrypted settings (slack tokens, webhook
	// URLs, PagerDuty routing keys, etc.) before passing them down to the sender.
	// On decrypt failure we don't ship a half-credential — fail the job.
	if connection.SettingsPrivate != nil && *connection.SettingsPrivate != "" {
		creds := jctx.Services.Credentials
		// A plaintext envelope (no-master-key fallback) opens with no key; only
		// AES-GCM / sealed envelopes need one. Gate the disabled error on that so
		// a self-hosted deployment can still send notifications with its token.
		// A nil service can open nothing, so it always fails here.
		if creds == nil || (credentials.RequiresKey(*connection.SettingsPrivate) && !creds.Enabled()) {
			return fmt.Errorf("%w: %s", ErrEncryptionDisabled, connection.UID)
		}

		private, decErr := creds.DecryptForOrg(
			ctx, connection.OrganizationUID, *connection.SettingsPrivate,
		)
		if decErr != nil {
			return fmt.Errorf("decrypt connection settings: %w", decErr)
		}

		merged := credentials.MergeConfig(connection.Settings, private)
		connection.Settings = models.JSONMap(merged)
	}

	// 2. Load incident
	incident, err := jctx.DBService.GetIncident(ctx, connection.OrganizationUID, r.config.IncidentUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIncidentNotFound, err)
	}

	// 3. Load check
	check, err := jctx.DBService.GetCheck(ctx, connection.OrganizationUID, incident.CheckUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCheckNotFound, err)
	}

	// 4. Get sender for connection type
	sender, ok := notifications.GetSender(connection.Type)
	if !ok {
		return fmt.Errorf("%w: %s", ErrSenderNotFound, connection.Type)
	}

	// Webhook senders may auto-generate / rotate their signing secret on
	// delivery. Inject a persistence callback so the mutated secret is written
	// back (re-encrypted) to the channel row.
	if ws, isWebhook := sender.(*notifications.WebhookSender); isWebhook {
		ws.UpdateChannel = r.webhookChannelUpdater(jctx)
	}

	// Web Push senders prune dead subscriptions; inject the same persistence
	// callback so the pruned list is written back to the channel row.
	if wps, isWebPush := sender.(*notifications.WebPushSender); isWebPush {
		wps.UpdateChannel = r.webhookChannelUpdater(jctx)
	}

	// 5. Build notification payload
	payload := r.buildPayload(ctx, jctx, log, connection, incident, check)

	// 6. Send notification
	log.InfoContext(ctx, "Sending notification",
		"connectionType", connection.Type,
		"connectionUid", connection.UID,
		"incidentUid", incident.UID,
		"eventType", r.config.EventType,
	)

	if sendErr := r.sendAndAudit(ctx, jctx, sender, payload); sendErr != nil {
		return sendErr
	}

	log.InfoContext(ctx, "Notification sent successfully")

	// Activation funnel: idempotent — fires once on the first successful
	// incident notification dispatch for this org. Links the milestone to
	// the incident/check that triggered it so the dashboard can render a
	// named check link even for this historically-payload-thin event type.
	activation.Emit(ctx, jctx.DBService, connection.OrganizationUID,
		models.EventTypeOrgActivationFirstIncidentPaged,
		activation.SourceSystem, "", models.JSONMap{
			"_incident_uid": incident.UID,
			"_check_uid":    check.UID,
			"check_slug":    check.Slug,
			"check_name":    check.Name,
		})

	return nil
}

// buildPayload assembles everything a sender needs for one delivery.
func (r *NotificationJobRun) buildPayload(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	connection *models.Integration, incident *models.Incident, check *models.Check,
) *notifications.Payload {
	org := r.lookupOrgIdentity(ctx, jctx, log, connection.OrganizationUID)

	return &notifications.Payload{
		EventType:   r.config.EventType,
		Incident:    incident,
		Check:       check,
		Integration: connection,
		OrgSlug:     org.Slug,
		OrgName:     org.Name,
		OrgLogoURL:  org.LogoURL,
		AppBaseURL:  appBaseURL(jctx),
		// Resolved at send time, not at incident-open: the on-call rotation may
		// have handed over since. Returns nil for every uncertain case, so a
		// mention is only ever added when we know exactly who to name.
		OnCallMentions: ResolveOnCallMentions(ctx, jctx, log, connection, check, r.config.EventType),
		// Non-nil only for `incident.comment`.
		Comment: r.config.Comment,
		// Non-nil only for `incident.acknowledged`.
		Acknowledgment: r.config.Acknowledgment,
	}
}

// lookupOrgIdentity resolves the organization slug (so senders can build
// user-facing URLs: the email magic-link ack endpoint, Slack dashboard links),
// plus the name and logo the email templates brand with. One lookup, three
// values — the slug already required loading the whole row.
//
// A missing org is logged but does not fail the notification — recipients
// still get notified, just without one-click links or org branding.
func (r *NotificationJobRun) lookupOrgIdentity(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, orgUID string,
) orgIdentity {
	org, err := jctx.DBService.GetOrganization(ctx, orgUID)
	if err != nil || org == nil {
		log.WarnContext(ctx, "Failed to load org identity for notification URLs and branding",
			"orgUid", orgUID, "error", err)

		return orgIdentity{}
	}

	identity := orgIdentity{Slug: org.Slug, Name: org.Name}
	if org.LogoURL != nil {
		identity.LogoURL = *org.LogoURL
	}

	return identity
}

// orgIdentity is what one org lookup yields for a notification: the slug that
// builds user-facing URLs, plus the name and logo the email templates brand
// with. The zero value is the "org could not be loaded" case — senders treat
// every field as optional.
type orgIdentity struct {
	Slug    string
	Name    string
	LogoURL string
}

// appBaseURL returns the application base URL from the job context's app
// config, or "" when no config is present. Senders treat an empty base URL as
// "no dashboard links", so this keeps notification dispatch nil-safe.
func appBaseURL(jctx *jobdef.JobContext) string {
	if jctx.AppConfig == nil {
		return ""
	}

	return jctx.AppConfig.Server.BaseURL
}

// sendAndAudit delivers the notification and updates the audit row by job UID.
func (r *NotificationJobRun) sendAndAudit(
	ctx context.Context,
	jctx *jobdef.JobContext,
	sender notifications.Sender,
	payload *notifications.Payload,
) error {
	if err := sender.Send(ctx, jctx, payload); err != nil {
		// Network errors should be retryable — leave the audit row at pending
		// so a subsequent retry can update it.
		if notifications.IsNetworkError(err) {
			_ = jctx.DBService.MarkIncidentNotificationFailedByJob(
				ctx, jctx.Job.UID, time.Now(), err.Error(), true, payload.DeliveryDetails,
			)

			return jobdef.NewRetryableError(err)
		}

		_ = jctx.DBService.MarkIncidentNotificationFailedByJob(
			ctx, jctx.Job.UID, time.Now(), err.Error(), false, payload.DeliveryDetails,
		)

		return err
	}

	// Most senders have no message_id. Webhook senders set the Standard
	// Webhooks `webhook-id` on the payload; surface it on the audit row, along
	// with any captured delivery artifacts.
	_ = jctx.DBService.MarkIncidentNotificationSentByJob(
		ctx, jctx.Job.UID, time.Now(), payload.MessageID, payload.DeliveryDetails,
	)

	return nil
}

// webhookChannelUpdater returns a callback that re-encrypts a webhook
// channel's (now-decrypted, mutated) Settings and persists them. Used by the
// WebhookSender when it auto-generates or purges a signing secret. When
// encryption is disabled, secrets fall back to plaintext storage (V1 behavior).
func (r *NotificationJobRun) webhookChannelUpdater(
	jctx *jobdef.JobContext,
) func(ctx context.Context, channel *models.Integration) error {
	return func(ctx context.Context, channel *models.Integration) error {
		secrets := credentials.ConnectionSecretFields(channel.Type)
		effective := credentials.MergeConfig(channel.Settings, nil)
		public, private := credentials.SplitConfig(effective, secrets)

		update := &models.IntegrationUpdate{}

		creds := jctx.Services.Credentials
		if creds == nil || !creds.Enabled() || len(private) == 0 {
			// Plaintext fallback: secrets stay on the public Settings map so
			// delivery keeps working without a master key.
			merged := credentials.MergeConfig(public, private)
			settings := models.JSONMap(merged)
			update.Settings = &settings
			update.ClearSettingsPrivate = true
		} else {
			envelope, err := creds.EncryptForOrg(ctx, channel.OrganizationUID, private)
			if err != nil {
				return fmt.Errorf("encrypt webhook channel settings: %w", err)
			}

			keysJSON, err := json.Marshal(credentials.SortedKeys(private))
			if err != nil {
				return fmt.Errorf("marshal webhook settings private keys: %w", err)
			}

			keysStr := string(keysJSON)
			settings := models.JSONMap(public)
			update.Settings = &settings
			update.SettingsPrivate = &envelope
			update.SettingsPrivateKeys = &keysStr
		}

		return jctx.DBService.UpdateChannel(ctx, channel.UID, update)
	}
}
