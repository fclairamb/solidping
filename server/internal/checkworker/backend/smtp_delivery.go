package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// ErrSMTPDeliveryUnresolved is the reason attached to a send-mode SMTP job
// whose delivery_check_uid could not be resolved to a live, same-org,
// CheckTypeEmail check. It is user-visible (it lands in the error result's
// output), so the specific cause is appended rather than logged separately.
var ErrSMTPDeliveryUnresolved = errors.New(
	"paired email check could not be resolved for this send-mode SMTP check",
)

// ErrSMTPEmailInboxNotConfigured is the reason attached to a send-mode SMTP
// job when the instance has no email_inbox configured at all — there is no
// address domain to build a recipient from, regardless of whether the
// referenced check itself is fine.
var ErrSMTPEmailInboxNotConfigured = errors.New(
	"send-mode SMTP checks require a configured email inbox (system parameter email.inbox)",
)

// resolveSMTPDelivery is the send-mode counterpart of mergeClaimedSecrets: for
// every claimed SMTP job with send_email set, it resolves delivery_check_uid
// to the paired email check's concrete tokenized address and threads it onto
// job.Config under the internal checkerdef.SMTPResolvedRecipientConfigKey —
// ALWAYS recomputed here, never trusted from storage, so nothing a user could
// have PATCHed directly into their own check's public config survives to
// dispatch.
//
// This has to happen here (server-side, at the claim boundary) rather than in
// the shared worker.go execution path: that code also runs verbatim on a
// deported agent, which has no DB access to do this lookup with. A job that
// cannot be resolved (deleted/renamed reference, wrong org, wrong type, or no
// email inbox configured) is dropped from the batch and reported as an
// explicit error result — exactly like mergeClaimedSecrets does for an
// unopenable credentials envelope — rather than silently skipping the send.
//
// Known scope limit: this runs on the DirectBackend (in-process) claim path
// only. The deported-agent claim path (WSBackend / ClaimJobsForAgent) is not
// wired for delivery resolution in this pass; a send-mode SMTP check scheduled
// onto an agent-only region gets no resolved recipient and therefore surfaces
// checksmtp's "no delivery recipient resolved" StatusError instead of a
// working send. See the SMTP check docs page.
func (b *DirectBackend) resolveSMTPDelivery(
	ctx context.Context, workerUID string, jobs []*models.CheckJob,
) []*models.CheckJob {
	if len(jobs) == 0 {
		return jobs
	}

	out := make([]*models.CheckJob, 0, len(jobs))

	for _, job := range jobs {
		if job.Type != string(checkerdef.CheckTypeSMTP) {
			out = append(out, job)

			continue
		}

		sendEmail, _ := job.Config["send_email"].(bool)
		if !sendEmail {
			out = append(out, job)

			continue
		}

		recipient, err := b.resolveSMTPRecipient(ctx, job)
		if err != nil {
			slog.WarnContext(ctx, "Cannot resolve SMTP delivery recipient; reporting error result",
				"error", err, "check_uid", job.CheckUID, "job_uid", job.UID,
				"organization_uid", job.OrganizationUID)

			b.submitSMTPDeliveryError(ctx, job, workerUID, err)

			continue
		}

		if job.Config == nil {
			job.Config = models.JSONMap{}
		}

		job.Config[checkerdef.SMTPResolvedRecipientConfigKey] = recipient
		out = append(out, job)
	}

	return out
}

// resolveSMTPRecipient looks up the delivery_check_uid reference (org-scoped:
// a cross-org uid reads as not-found here exactly like it does at write time
// — this is what makes the reference-not-address indirection enforceable at
// dispatch, not only at save time), requires it to be a CheckTypeEmail check,
// and renders its token against the instance's configured email inbox domain.
func (b *DirectBackend) resolveSMTPRecipient(ctx context.Context, job *models.CheckJob) (string, error) {
	deliveryCheckUID, _ := job.Config["delivery_check_uid"].(string)
	if deliveryCheckUID == "" {
		return "", fmt.Errorf("%w: delivery_check_uid is not set", ErrSMTPDeliveryUnresolved)
	}

	target, err := b.dbService.GetCheck(ctx, job.OrganizationUID, deliveryCheckUID)
	if err != nil || target == nil {
		return "", fmt.Errorf("%w: %s does not exist in this organization", ErrSMTPDeliveryUnresolved, deliveryCheckUID)
	}

	if target.Type != string(checkerdef.CheckTypeEmail) {
		return "", fmt.Errorf(
			"%w: %s is a %q check, not an email check", ErrSMTPDeliveryUnresolved, deliveryCheckUID, target.Type,
		)
	}

	token, _ := target.Config["token"].(string)
	if token == "" {
		return "", fmt.Errorf("%w: paired email check %s has no token", ErrSMTPDeliveryUnresolved, deliveryCheckUID)
	}

	param, err := b.dbService.GetSystemParameter(ctx, jmap.SystemParameterKey)
	if err != nil || param == nil {
		return "", ErrSMTPEmailInboxNotConfigured
	}

	inboxCfg, err := jmap.JSONMapToConfig(param.Value)
	if err != nil || inboxCfg.AddressDomain == "" {
		return "", ErrSMTPEmailInboxNotConfigured
	}

	return token + "@" + inboxCfg.AddressDomain, nil
}

// submitSMTPDeliveryError reports an unresolvable delivery reference as an
// error result so the check's history shows the actionable message instead of
// the check silently never sending — mirrors submitSecretsError.
func (b *DirectBackend) submitSMTPDeliveryError(
	ctx context.Context, job *models.CheckJob, workerUID string, reason error,
) {
	if err := b.SubmitResult(ctx, job, workerUID, &SubmitResultRequest{
		Status:          int(models.ResultStatusError),
		Duration:        0,
		Metrics:         map[string]any{},
		Output:          map[string]any{checkerdef.OutputKeyError: reason.Error()},
		Region:          job.Region,
		NextScheduledAt: nextScheduledAt(job),
	}); err != nil {
		slog.ErrorContext(ctx, "Failed to submit SMTP delivery error result",
			"error", err, "check_uid", job.CheckUID, "job_uid", job.UID)
	}
}
