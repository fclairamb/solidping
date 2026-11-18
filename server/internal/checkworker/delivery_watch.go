package checkworker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	checkemailconfig "github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checksmtpconfig "github.com/fclairamb/solidping/server/internal/checkers/checksmtp/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

const (
	// deliveryWatchInterval is how often the watch sweeps. A missed probe is
	// reported at send time + limit, to within this interval (the limit is at
	// least a minute).
	deliveryWatchInterval = 30 * time.Second

	// deliveryWatchMatchFetch bounds how many raw email results are read to
	// find the one carrying a send's sentAt.
	deliveryWatchMatchFetch = 200

	outputKeySentAt         = "sentAt"
	outputKeySourceCheckUID = "sourceCheckUid"
	// outputKeyTimeoutAt marks a row as (a copy of) a watch timeout: when the
	// watch recorded the missed probe. passiveVerdict keeps the check down
	// until a signal newer than this instant arrives.
	outputKeyTimeoutAt = "deliveryTimeoutAt"
)

// DeliveryWatch reports a probe mail that never arrived (spec
// 2026-11-18-03). The email check's own passive verdict only runs once per
// period; with a 1 day period a relay that drops mail would be reported a day
// late although the send time is known exactly. The watch reads the last
// successful send of every send-mode smtp check delivering to an email check
// with max_delivery_delay, and when that send is older than the limit and no
// result of the email check carries its sentAt, writes ONE down evaluation row.
// The row itself carries the sentAt, so the next sweep finds it and writes
// nothing: one row per missed probe, never one per tick.
//
// Mechanism: a sweep on the passive evaluator's loop rather than a shorter
// evaluation cadence, because a shorter cadence would write an evaluation row
// per tick for the ordinary period verdict.
type DeliveryWatch struct {
	db          db.Service
	incidentSvc *incidents.Service
	logger      *slog.Logger
}

// NewDeliveryWatch builds the watch.
func NewDeliveryWatch(dbService db.Service, incidentSvc *incidents.Service) *DeliveryWatch {
	return &DeliveryWatch{
		db:          dbService,
		incidentSvc: incidentSvc,
		logger:      slog.Default().With("component", "delivery_watch"),
	}
}

type watchedEmail struct {
	check *models.Check
	limit time.Duration
	token string
}

// Sweep evaluates every watched email check once. It returns how many rows it
// wrote. workerUID is the evaluator's workers row (evaluation rows carry one,
// which is what keeps them from being read back as signals).
func (w *DeliveryWatch) Sweep(ctx context.Context, workerUID string, now time.Time) (int, error) {
	emails, err := w.db.ListEnabledChecksByType(ctx, []string{string(checkerdef.CheckTypeEmail)})
	if err != nil {
		return 0, err
	}

	watched := make(map[string]*watchedEmail)

	for _, check := range emails {
		cfg := &checkemailconfig.EmailConfig{}
		if cfg.FromMap(check.Config) != nil || cfg.MaxDeliveryDelay <= 0 {
			continue
		}

		watched[check.UID] = &watchedEmail{check: check, limit: cfg.MaxDeliveryDelay, token: cfg.Token}
	}

	if len(watched) == 0 {
		return 0, nil
	}

	smtpChecks, err := w.db.ListEnabledChecksByType(ctx, []string{string(checkerdef.CheckTypeSMTP)})
	if err != nil {
		return 0, err
	}

	written := 0

	for _, smtp := range smtpChecks {
		target := w.targetOf(smtp, watched)
		if target == nil {
			continue
		}

		wrote, err := w.sweepPair(ctx, workerUID, now, smtp, target)
		if err != nil {
			w.logger.WarnContext(ctx, "delivery watch failed for a pair",
				"smtp_check_uid", smtp.UID, "email_check_uid", target.check.UID, "error", err)

			continue
		}

		if wrote {
			written++
		}
	}

	return written, nil
}

// targetOf resolves the watched email check a send-mode smtp check delivers to,
// through delivery_check_uid or, failing that, the token in delivery_to.
func (w *DeliveryWatch) targetOf(smtp *models.Check, watched map[string]*watchedEmail) *watchedEmail {
	cfg := &checksmtpconfig.SMTPConfig{}
	if cfg.FromMap(smtp.Config) != nil || !cfg.SendEmail {
		return nil
	}

	if target, ok := watched[cfg.DeliveryCheckUID]; ok && target.check.OrganizationUID == smtp.OrganizationUID {
		return target
	}

	local, _, found := strings.Cut(cfg.DeliveryTo, "@")
	if !found || local == "" {
		return nil
	}

	local, _, _ = strings.Cut(local, "+")

	for _, target := range watched {
		if target.check.OrganizationUID == smtp.OrganizationUID && target.token != "" &&
			strings.EqualFold(target.token, local) {
			return target
		}
	}

	return nil
}

func (w *DeliveryWatch) sweepPair(
	ctx context.Context, workerUID string, now time.Time, smtp *models.Check, target *watchedEmail,
) (bool, error) {
	statusUp := int(checkerdef.StatusUp)

	sends, err := w.db.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: smtp.OrganizationUID,
		CheckUIDs:       []string{smtp.UID},
		PeriodTypes:     []string{models.PeriodTypeRaw},
		Statuses:        []int{statusUp},
		Limit:           1,
	})
	if err != nil {
		return false, fmt.Errorf("list sends: %w", err)
	}

	if len(sends.Results) == 0 {
		return false, nil
	}

	sentRaw, _ := sends.Results[0].Output[outputKeySentAt].(string)

	sentAt, parseErr := time.Parse(time.RFC3339, sentRaw)
	if parseErr != nil || now.Sub(sentAt) <= target.limit {
		return false, nil //nolint:nilerr // a send without a usable sentAt is skipped, not an error
	}

	seen, err := w.alreadyAccounted(ctx, target.check, sentAt)
	if err != nil || seen {
		return false, err
	}

	return true, w.recordTimeout(ctx, workerUID, now, smtp, target, sentAt)
}

// alreadyAccounted reports whether a result of the email check (a received
// probe or an earlier timeout row) carries this sentAt.
func (w *DeliveryWatch) alreadyAccounted(ctx context.Context, email *models.Check, sentAt time.Time) (bool, error) {
	after := sentAt.Add(-time.Minute)

	rows, err := w.db.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID:  email.OrganizationUID,
		CheckUIDs:        []string{email.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &after,
		Limit:            deliveryWatchMatchFetch,
	})
	if err != nil {
		return false, fmt.Errorf("list email results: %w", err)
	}

	for _, row := range rows.Results {
		raw, _ := row.Output[outputKeySentAt].(string)
		if parsed, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil && parsed.Equal(sentAt) {
			return true, nil
		}
	}

	return false, nil
}

func (w *DeliveryWatch) recordTimeout(
	ctx context.Context, workerUID string, now time.Time,
	smtp *models.Check, target *watchedEmail, sentAt time.Time,
) error {
	resultUID, err := uuid.NewV7()
	if err != nil {
		return err
	}

	status := int(checkerdef.StatusDown)
	duration := float32(0)
	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: target.check.OrganizationUID,
		CheckUID:        target.check.UID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     now,
		WorkerUID:       &workerUID,
		Status:          &status,
		Duration:        &duration,
		Metrics:         models.JSONMap{},
		Output: models.JSONMap{
			outputKeyMessage: fmt.Sprintf("sent at %s, not received after %s",
				sentAt.UTC().Format("15:04"), checkemailconfig.FormatDuration(target.limit)),
			outputKeySentAt:         sentAt.Format(time.RFC3339),
			outputKeySourceCheckUID: smtp.UID,
			outputKeyTimeoutAt:      now.UTC().Format(time.RFC3339Nano),
			outputKeyEvaluation:     true,
		},
		CreatedAt: now,
	}

	if inMaintenance, mwErr := w.incidentSvc.IsCheckInActiveMaintenance(ctx, target.check.UID); mwErr == nil {
		result.Maintenance = inMaintenance
	}

	if err := w.db.SaveResultWithStatusTracking(ctx, result); err != nil {
		return err
	}

	if err := w.incidentSvc.ProcessCheckResult(ctx, target.check, result); err != nil {
		w.logger.WarnContext(ctx, "incident processing failed for a missed probe",
			"check_uid", target.check.UID, "error", err)
	}

	return nil
}
