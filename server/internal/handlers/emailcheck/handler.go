// Package emailcheck implements the JMAP email handler that turns inbound
// emails into SolidPing check results. Recipient addresses look like
// `<48-hex-token>[+status]@<inbox-domain>`; the token resolves to a
// configured email-type check, and the optional plus-suffix (or a
// X-SolidPing-Status header / [STATUS] subject tag) sets the result status.
package emailcheck

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jmap"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

const (
	statusUp      = "up"
	statusDown    = "down"
	statusError   = "error"
	statusRunning = "running"

	headerSolidPingStatus = "X-SolidPing-Status"

	// headerSourceCheckUID and headerSentAt are the send-mode SMTP attribution
	// headers (spec 2026-08-19-04), set by checksmtp when send_email is on
	// (see checksmtp/checker.go's headerSourceCheckUID/headerSentAt — same
	// literal values, duplicated rather than shared to keep checksmtp and
	// emailcheck decoupled packages). Both are optional: an intermediate MTA
	// stripping them must degrade gracefully to today's plain behavior, never
	// break delivery.
	headerSourceCheckUID = "X-SolidPing-Check"
	headerSentAt         = "X-SolidPing-Sent-At"

	outputKeyMessage        = "message"
	outputKeyFrom           = "from"
	outputKeySubject        = "subject"
	outputKeyMessageID      = "messageId"
	outputKeyReceivedAt     = "receivedAt"
	outputKeySourceCheckUID = "sourceCheckUid"
	outputKeySentAt         = "sentAt"
	outputKeyLatencyMs      = "latencyMs"
)

// maxSaneDeliveryLatency bounds the send→receive latency that is recorded as
// an enrichment. Greylisting can legitimately add minutes; nothing in this
// feature's design implies delays beyond a day, so anything larger reads as a
// clock problem (the worker's clock and the mail path's clock are not the
// same box) rather than a genuinely slow delivery, and is dropped rather than
// charted as if it were real.
const maxSaneDeliveryLatency = 24 * time.Hour

// recipientPattern captures the 48-hex token and an optional +status suffix
// from the local part of the recipient address.
var recipientPattern = regexp.MustCompile(`^([0-9a-f]{48})(?:\+(up|down|error|running))?@`)

// subjectTagPattern captures an optional [STATUS] tag at the start of the
// subject line. Case-insensitive.
var subjectTagPattern = regexp.MustCompile(`(?i)^\s*\[(up|down|error|running)\]`)

// Handler implements jmap.Handler for the email check type.
type Handler struct {
	db          db.Service
	incidentSvc *incidents.Service
	logger      *slog.Logger
}

// NewHandler builds a handler wired to the db service and incident pipeline.
// rt may be nil (realtime disabled) — hint publishing is a nil-safe no-op then.
func NewHandler(dbService db.Service, jobSvc jobsvc.Service, rtPub *realtime.Publisher, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return &Handler{
		db:          dbService,
		incidentSvc: incidents.NewService(dbService, jobSvc, clock.Real{}, rtPub),
		logger:      logger,
	}
}

// HandleEmail tries to resolve the email to a configured check. Returns
// OutcomeIgnored when no token is found in any recipient, OutcomeProcessed
// once a result has been recorded, and OutcomeRejected if the token format
// matches but no check exists (the email cannot ever succeed).
//
//nolint:gocritic // jmap.Handler interface passes Email by value
func (h *Handler) HandleEmail(ctx context.Context, _ *jmap.Mailboxes, email jmap.Email) (jmap.Outcome, error) {
	token, status, recipient := h.extractTokenAndStatus(&email)
	if token == "" {
		return jmap.OutcomeIgnored, nil
	}

	check, err := h.db.GetCheckByEmailToken(ctx, token)
	if err != nil || check == nil {
		// Token-shaped local-part but no matching check — won't ever match.
		// Reject so the email moves to Processed and the handler chain stops.
		h.logger.WarnContext(ctx, "email token did not resolve to a check",
			"token", token, "recipient", recipient, "error", err)

		return jmap.OutcomeRejected, nil
	}

	if err := h.recordResult(ctx, check, &email, status); err != nil {
		return jmap.OutcomeIgnored, err
	}

	return jmap.OutcomeProcessed, nil
}

// extractTokenAndStatus walks the email's recipients (To, Cc, Bcc) looking
// for an address whose local part is a 48-hex token. Returns the token, the
// resolved status, and the matched recipient address (for logging).
//
// Status resolution priority:
//  1. plus-addressing in the matched recipient (`token+down@…`)
//  2. X-SolidPing-Status header
//  3. [STATUS] subject tag
//  4. default "up"
func (h *Handler) extractTokenAndStatus(email *jmap.Email) (string, string, string) {
	var (
		token      string
		plusStatus string
		matched    string
	)

	for _, addrs := range [][]jmap.EmailAddress{email.To, email.Cc, email.Bcc} {
		for i := range addrs {
			groups := recipientPattern.FindStringSubmatch(strings.ToLower(addrs[i].Email))
			if groups == nil {
				continue
			}

			token = groups[1]
			matched = addrs[i].Email

			if len(groups) >= 3 && groups[2] != "" {
				plusStatus = groups[2]
			}

			break
		}

		if token != "" {
			break
		}
	}

	if token == "" {
		return "", "", ""
	}

	if plusStatus != "" {
		return token, plusStatus, matched
	}

	if hdr := findHeader(email.Headers, headerSolidPingStatus); hdr != "" {
		if s, ok := normalizeStatus(hdr); ok {
			return token, s, matched
		}
	}

	if tag := subjectTagPattern.FindStringSubmatch(email.Subject); tag != nil {
		if s, ok := normalizeStatus(tag[1]); ok {
			return token, s, matched
		}
	}

	return token, statusUp, matched
}

func findHeader(headers []jmap.EmailHeader, name string) string {
	for i := range headers {
		if strings.EqualFold(strings.TrimSpace(headers[i].Name), name) {
			return strings.TrimSpace(headers[i].Value)
		}
	}

	return ""
}

// enrichFromSMTPHeaders parses the optional send-mode attribution headers
// (spec 2026-08-19-04) and, when present, adds sourceCheckUid/sentAt/latencyMs
// to output and records the latency metric. Messages without the headers (or
// with an unparsable Sent-At) are left byte-for-byte as they were before this
// feature — full backward compatibility with human/heartbeat-style uses of
// the email check.
//
// latencyMs is dropped (attribution kept) when it would be negative — the
// worker clock and the mail path's clock are not the same box, so a
// non-monotonic reading is a clock artifact, not a real "email arrived before
// it was sent" — or larger than maxSaneDeliveryLatency.
func enrichFromSMTPHeaders(
	output, metrics models.JSONMap, email *jmap.Email, orgUID string,
) {
	sourceCheckUID := findHeader(email.Headers, headerSourceCheckUID)
	sentAtRaw := findHeader(email.Headers, headerSentAt)

	if sourceCheckUID == "" || sentAtRaw == "" {
		return
	}

	sentAt, err := time.Parse(time.RFC3339, sentAtRaw)
	if err != nil {
		return
	}

	output[outputKeySourceCheckUID] = sourceCheckUID
	output[outputKeySentAt] = sentAt.Format(time.RFC3339)

	latency := email.ReceivedAt.Sub(sentAt)
	if latency < 0 || latency > maxSaneDeliveryLatency {
		return
	}

	latencyMs := float64(latency.Microseconds()) / 1000.0
	output[outputKeyLatencyMs] = latencyMs
	metrics[outputKeyLatencyMs] = latencyMs

	prommetrics.EmailDeliveryLatency.WithLabelValues(orgUID).Observe(latency.Seconds())
}

func normalizeStatus(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case statusUp:
		return statusUp, true
	case statusDown:
		return statusDown, true
	case statusError:
		return statusError, true
	case statusRunning:
		return statusRunning, true
	default:
		return "", false
	}
}

func toCheckerStatus(s string) checkerdef.Status {
	switch s {
	case statusUp:
		return checkerdef.StatusUp
	case statusDown:
		return checkerdef.StatusDown
	case statusError:
		return checkerdef.StatusError
	case statusRunning:
		return checkerdef.StatusRunning
	default:
		return checkerdef.StatusUp
	}
}

func defaultMessage(s string) string {
	switch s {
	case statusDown:
		return "Email reported failure"
	case statusError:
		return "Email reported error"
	case statusRunning:
		return "Run started via email"
	default:
		return "Email received"
	}
}

// ErrCheckMissing is returned when recordResult is called without a check.
// Callers shouldn't hit this — it's a defensive guard for tests.
var ErrCheckMissing = errors.New("check is required")

func (h *Handler) recordResult(ctx context.Context, check *models.Check, email *jmap.Email, status string) error {
	if check == nil {
		return ErrCheckMissing
	}

	resultUID, err := uuid.NewV7()
	if err != nil {
		return err
	}

	checkerStatus := toCheckerStatus(status)
	statusInt := int(checkerStatus)
	durationMs := float32(0)

	from := ""
	if len(email.From) > 0 {
		from = email.From[0].Email
	}

	messageID := ""
	if len(email.MessageID) > 0 {
		messageID = email.MessageID[0]
	}

	output := models.JSONMap{
		outputKeyMessage:    defaultMessage(status),
		outputKeyFrom:       from,
		outputKeySubject:    email.Subject,
		outputKeyMessageID:  messageID,
		outputKeyReceivedAt: email.ReceivedAt.Format(time.RFC3339),
	}

	metrics := make(models.JSONMap)
	enrichFromSMTPHeaders(output, metrics, email, check.OrganizationUID)

	now := time.Now()
	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: check.OrganizationUID,
		CheckUID:        check.UID,
		PeriodType:      "raw",
		PeriodStart:     now,
		Status:          &statusInt,
		Duration:        &durationMs,
		Metrics:         metrics,
		Output:          output,
		CreatedAt:       now,
	}

	if err := h.db.SaveResultWithStatusTracking(ctx, result); err != nil {
		return err
	}

	// Skip incident processing for non-terminal statuses
	if checkerStatus == checkerdef.StatusRunning {
		return nil
	}

	if err := h.incidentSvc.ProcessCheckResult(ctx, check, result); err != nil {
		h.logger.WarnContext(ctx, "incident processing failed for email check",
			"check_uid", check.UID, "error", err)
	}

	return nil
}
