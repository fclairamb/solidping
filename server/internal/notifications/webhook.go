package notifications

import (
	"bytes"
	"context"
	"crypto/hmac"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const (
	webhookTimeout = 30 * time.Second
	// webhookSecretPrefix is the Svix-compatible prefix for signing secrets.
	webhookSecretPrefix = "whsec_"
	// webhookSecretBytes is the number of random bytes in a signing secret.
	webhookSecretBytes = 32

	// Settings keys used by the signing-secret lifecycle.
	settingsKeySigningSecret         = "signingSecret"
	settingsKeySigningSecretPrevious = "signingSecretPrevious"
	settingsKeySigningSecretExpiry   = "signingSecretPreviousExpiry"

	// Standard Webhooks header names. HTTP header names are case-insensitive on
	// the wire and Go canonicalizes them via textproto, so receivers using any
	// Standard-Webhooks library read them regardless of this exact casing.
	headerWebhookID        = "Webhook-Id"
	headerWebhookTimestamp = "Webhook-Timestamp"
	headerWebhookSignature = "Webhook-Signature"
)

var (
	// ErrWebhookURLNotConfigured is returned when the webhook URL is not configured.
	ErrWebhookURLNotConfigured = errors.New("webhook url not configured")
	// ErrWebhookRequestFailed is returned when the webhook request returns a non-2xx status code.
	ErrWebhookRequestFailed = errors.New("webhook request failed")
	// ErrInvalidSigningSecret is returned when a signing secret is malformed.
	ErrInvalidSigningSecret = errors.New("invalid signing secret")
)

// WebhookPayload is the Standard Webhooks envelope sent to receivers.
type WebhookPayload struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      WebhookData `json:"data"`
}

// WebhookData carries the typed incident + check data for a webhook event.
type WebhookData struct {
	Incident WebhookIncident `json:"incident"`
	Check    WebhookCheck    `json:"check"`
	// Comment is present only on `incident.comment` deliveries and carries the
	// comment body plus its author. Omitted entirely for every other event, so
	// existing receivers see a byte-identical payload to before.
	Comment *CommentInfo `json:"comment,omitempty"`
	// Acknowledgment is present only on `incident.acknowledged` deliveries and
	// names who took the incident and from where. Omitted entirely for every
	// other event, so existing receivers see a byte-identical payload to
	// before.
	Acknowledgment *AckInfo `json:"acknowledgment,omitempty"`
}

// WebhookIncident is the incident projection inside a webhook payload.
type WebhookIncident struct {
	UID                   string     `json:"uid"`
	StartedAt             time.Time  `json:"startedAt"`
	ResolvedAt            *time.Time `json:"resolvedAt"`
	AcknowledgedAt        *time.Time `json:"acknowledgedAt,omitempty"`
	DurationSeconds       *int       `json:"durationSeconds"`
	Title                 *string    `json:"title"`
	FailureCount          int        `json:"failureCount"`
	RelapseCount          int        `json:"relapseCount"`
	RecoveryPeriodSeconds *int       `json:"recoveryPeriodSeconds"`
}

// WebhookCheck is the check projection inside a webhook payload.
type WebhookCheck struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// WebhookSender sends notifications via HTTP webhooks signed per the Standard
// Webhooks specification (https://www.standardwebhooks.com/).
type WebhookSender struct {
	// UpdateChannel persists a channel after the sender mutates its signing
	// secrets (auto-generation or expired-previous purge). When nil (e.g. in
	// tests that don't need persistence), the sender skips persistence and
	// logs a warning, but still signs with the in-memory secret.
	UpdateChannel func(ctx context.Context, channel *models.Integration) error
}

// generateWebhookSecret returns a fresh `whsec_<base64url-unpadded-32-bytes>`
// signing secret using crypto/rand.
func generateWebhookSecret() (string, error) {
	raw := make([]byte, webhookSecretBytes)
	if _, err := crypto_rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating webhook secret: %w", err)
	}

	return webhookSecretPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// signRequest computes the Standard Webhooks signature header value over the
// signed content `"{id}.{timestamp}.{body}"` for each secret. The returned
// string is a space-separated list of `v1,<base64-hmac>` entries.
func signRequest(secrets []string, id, timestamp string, body []byte) (string, error) {
	signedContent := make([]byte, 0, len(id)+len(timestamp)+len(body)+2)
	signedContent = append(signedContent, id...)
	signedContent = append(signedContent, '.')
	signedContent = append(signedContent, timestamp...)
	signedContent = append(signedContent, '.')
	signedContent = append(signedContent, body...)

	sigs := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		trimmed := strings.TrimPrefix(secret, webhookSecretPrefix)

		secretBytes, err := base64.RawURLEncoding.DecodeString(trimmed)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidSigningSecret, err)
		}

		mac := hmac.New(sha256.New, secretBytes)
		mac.Write(signedContent)
		sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		sigs = append(sigs, "v1,"+sig)
	}

	return strings.Join(sigs, " "), nil
}

// Send sends a notification via webhook, signed per the Standard Webhooks spec.
func (s *WebhookSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	url, ok := payload.Integration.Settings["url"].(string)
	if !ok || url == "" {
		return ErrWebhookURLNotConfigured
	}

	body, err := json.Marshal(s.buildPayload(payload))
	if err != nil {
		return fmt.Errorf("marshaling webhook payload: %w", err)
	}

	secrets, err := s.ensureSecrets(ctx, jctx, payload.Integration)
	if err != nil {
		return err
	}

	webhookID := uuid.New().String()
	webhookTimestamp := strconv.FormatInt(time.Now().Unix(), 10)

	signature, err := signRequest(secrets, webhookID, webhookTimestamp, body)
	if err != nil {
		return err
	}

	// Expose the webhook-id as the message id so the audit layer can record it.
	payload.MessageID = webhookID

	req, err := buildWebhookRequest(ctx, url, body, payload.Integration.Settings,
		webhookHeaders{id: webhookID, timestamp: webhookTimestamp, signature: signature})
	if err != nil {
		return err
	}

	return s.sendAndCapture(req, url, body, payload)
}

// webhookHeaders carries the three Standard Webhooks signing header values.
type webhookHeaders struct {
	id        string
	timestamp string
	signature string
}

// buildWebhookRequest constructs the signed POST request and applies the
// Standard Webhooks headers plus any non-reserved custom headers. Custom headers
// may override User-Agent but never the three signing headers.
func buildWebhookRequest(
	ctx context.Context, url string, body []byte, settings models.JSONMap, hdr webhookHeaders,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating webhook request: %w", err)
	}

	req.Header.Set(headerWebhookID, hdr.id)
	req.Header.Set(headerWebhookTimestamp, hdr.timestamp)
	req.Header.Set(headerWebhookSignature, hdr.signature)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SolidPing/1.0")

	if headers, ok := settings["headers"].(map[string]any); ok {
		for k, v := range headers {
			if str, ok := v.(string); ok {
				if isStandardWebhookHeader(k) {
					continue
				}
				req.Header.Set(k, str)
			}
		}
	}

	return req, nil
}

// sendAndCapture performs the HTTP request and records structured delivery
// artifacts onto the payload (on both success and failure). The request URL is
// stripped of its query string and credentials before being recorded; the
// signing secret and any auth/custom headers are never stored.
func (s *WebhookSender) sendAndCapture(req *http.Request, url string, body []byte, payload *Payload) error {
	client := newHTTPClient(webhookTimeout)

	details := &models.DeliveryDetails{
		RequestURL:  redactURL(url),
		RequestBody: models.CapBody(body),
	}

	start := time.Now()
	resp, err := client.Do(req)
	details.DurationMs = time.Since(start).Milliseconds()

	if err != nil {
		// Transport error (no response): keep the partial details (url, body,
		// duration) so the failure is still debuggable.
		payload.DeliveryDetails = details

		return fmt.Errorf("sending webhook: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, models.DeliveryDetailsBodyCap+1))

	details.HTTPStatusCode = resp.StatusCode
	details.ResponseBody = models.CapBody(respBody)
	details.ResponseHeaders = allowlistedResponseHeaders(resp.Header)
	payload.DeliveryDetails = details

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d: %s", ErrWebhookRequestFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

// allowlistedResponseHeaders extracts a small, fixed set of safe response
// headers worth surfacing for debugging. Request-side secret headers are never
// read here — only the receiver's response headers, and only the allowlist.
func allowlistedResponseHeaders(h http.Header) map[string]string {
	allow := []string{"Retry-After", "Content-Type"}

	out := make(map[string]string, len(allow))
	for _, name := range allow {
		if v := h.Get(name); v != "" {
			out[name] = v
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// redactURL strips the query string and any userinfo (credentials) from a URL,
// returning scheme://host/path. A URL that fails to parse falls back to its host
// portion only, never the raw string, so credentials in a malformed URL still
// cannot leak.
func redactURL(raw string) string {
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed.Host == "" {
		// Fall back to the host:port prefix at best; never echo the raw URL.
		if i := strings.Index(raw, "?"); i >= 0 {
			raw = raw[:i]
		}
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			raw = raw[at+1:]
		}

		return raw
	}

	redacted := neturl.URL{
		Scheme: parsed.Scheme,
		Host:   parsed.Host, // host:port only — User (credentials) is dropped
		Path:   parsed.Path,
	}

	return redacted.String()
}

// isStandardWebhookHeader reports whether a custom header would clobber one of
// the three reserved Standard Webhooks signing headers (case-insensitive).
func isStandardWebhookHeader(name string) bool {
	switch strings.ToLower(name) {
	case "webhook-id", "webhook-timestamp", "webhook-signature":
		return true
	default:
		return false
	}
}

// ensureSecrets resolves the active signing secrets for a channel. It
// auto-generates a secret when none exists, purges an expired previous secret,
// and persists any mutation through the injected UpdateChannel callback.
func (s *WebhookSender) ensureSecrets(
	ctx context.Context, jctx *jobdef.JobContext, channel *models.Integration,
) ([]string, error) {
	log := s.logger(jctx)

	active, _ := channel.Settings[settingsKeySigningSecret].(string)
	previous, _ := channel.Settings[settingsKeySigningSecretPrevious].(string)

	dirty := false

	// Purge the previous secret once its grace window has elapsed.
	if previous != "" && s.previousExpired(channel) {
		previous = ""
		delete(channel.Settings, settingsKeySigningSecretPrevious)
		delete(channel.Settings, settingsKeySigningSecretExpiry)
		dirty = true
	}

	// Auto-generate on first delivery for legacy channels without a secret.
	if active == "" {
		generated, err := generateWebhookSecret()
		if err != nil {
			return nil, err
		}

		active = generated
		channel.Settings[settingsKeySigningSecret] = active
		dirty = true

		log.InfoContext(ctx, "auto-generated signing secret for webhook channel "+channel.UID)
	}

	if dirty {
		if s.UpdateChannel != nil {
			if err := s.UpdateChannel(ctx, channel); err != nil {
				return nil, fmt.Errorf("persisting webhook signing secret: %w", err)
			}
		} else {
			log.WarnContext(ctx,
				"webhook signing secret changed but no persistence callback is configured; "+
					"signing with in-memory secret only", "channelUid", channel.UID)
		}
	}

	secrets := make([]string, 0, 2)
	secrets = append(secrets, active)

	if previous != "" {
		secrets = append(secrets, previous)
	}

	return secrets, nil
}

// previousExpired reports whether the rotation grace window for the previous
// secret has elapsed. A missing or unparseable expiry is treated as expired.
func (s *WebhookSender) previousExpired(channel *models.Integration) bool {
	expiryRaw, ok := channel.Settings[settingsKeySigningSecretExpiry].(string)
	if !ok || expiryRaw == "" {
		return true
	}

	expiry, err := time.Parse(time.RFC3339, expiryRaw)
	if err != nil {
		return true
	}

	return time.Now().After(expiry)
}

// logger returns the job logger when available, falling back to the default.
func (s *WebhookSender) logger(jctx *jobdef.JobContext) *slog.Logger {
	if jctx != nil && jctx.Logger != nil {
		return jctx.Logger
	}

	return slog.Default()
}

// buildPayload builds the typed Standard Webhooks envelope for a notification.
func (s *WebhookSender) buildPayload(payload *Payload) WebhookPayload {
	checkName := ""
	if payload.Check.Name != nil {
		checkName = *payload.Check.Name
	} else if payload.Check.Slug != nil {
		checkName = *payload.Check.Slug
	}

	incident := WebhookIncident{
		UID:            payload.Incident.UID,
		StartedAt:      payload.Incident.StartedAt,
		ResolvedAt:     payload.Incident.ResolvedAt,
		AcknowledgedAt: payload.Incident.AcknowledgedAt,
		Title:          payload.Incident.Title,
		FailureCount:   payload.Incident.FailureCount,
		RelapseCount:   payload.Incident.RelapseCount,
	}

	if payload.Incident.ResolvedAt != nil {
		duration := int(payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt).Seconds())
		incident.DurationSeconds = &duration
	}

	// RecoveryPeriodSeconds is omitted (null) when no relapse has occurred.
	if payload.Incident.RelapseCount > 0 {
		recovery := payload.Check.RecoveryPeriodSeconds
		incident.RecoveryPeriodSeconds = &recovery
	}

	return WebhookPayload{
		Type:      payload.EventType,
		Timestamp: time.Now().UTC(),
		Data: WebhookData{
			Incident: incident,
			Check: WebhookCheck{
				UID:  payload.Check.UID,
				Name: checkName,
				Type: payload.Check.Type,
			},
			Comment:        payload.Comment,
			Acknowledgment: payload.Acknowledgment,
		},
	}
}
