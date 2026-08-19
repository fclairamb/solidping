package checks

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksmtp"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// smtpDeliveryConfigField is the config field name reported on validation
// errors, so the dashboard can attach the message to the email-check picker.
const smtpDeliveryConfigField = "delivery_check_uid"

// smtpMinSendInterval is the floor on a send-mode SMTP check's period (spec
// 2026-08-19-04): every period submits a real email through the monitored
// server, so a too-short period risks flooding the paired inbox.
const smtpMinSendInterval = 60 * time.Second

// validateSMTPDeliveryConfig enforces every rule that makes a send-mode SMTP
// check's config legal, on the EFFECTIVE (post-merge, post-normalize) config.
// It runs on both the create and the PATCH path — PATCH matters most:
// UpdateCheck never calls checker.Validate, so this is the only gate there
// (mirrors validateTunnelConfig exactly, for the same reason).
//
// Rules:
//   - only relevant to smtp checks with send_email set; every other check
//     (and a plain SMTP check) passes trivially.
//   - mail_from and delivery_check_uid must both be non-empty — the checker's
//     own Validate() enforces this shape too, but that only runs on create.
//   - delivery_check_uid must reference a check that exists in the SAME org
//     (the lookup is org-scoped, so a cross-org uid simply reads as "not
//     found") and is a CheckTypeEmail check — this indirection is what stops
//     one org aiming probes at another org's check address.
//   - the instance must have an email_inbox configured — with none, there is
//     no address domain to build a recipient from regardless of the
//     reference's validity.
func (s *Service) validateSMTPDeliveryConfig(
	ctx context.Context, orgUID, checkType string, effective map[string]any,
) error {
	if checkerdef.CheckType(checkType) != checkerdef.CheckTypeSMTP {
		return nil
	}

	sendEmail, _ := effective["send_email"].(bool)
	if !sendEmail {
		return nil
	}

	// Re-validated here (not just at the checker's Validate(), which only runs
	// on create): mail_from is spliced verbatim into the wire MAIL FROM command
	// and the From: header, so a PATCH must be held to the same real-address
	// requirement or it could smuggle a CRLF-terminated extra SMTP command /
	// an injected header past this gate.
	mailFrom, _ := effective["mail_from"].(string)
	if err := checksmtp.ValidateMailFrom(mailFrom); err != nil {
		return err
	}

	deliveryCheckUID, _ := effective[smtpDeliveryConfigField].(string)
	if deliveryCheckUID == "" {
		return checkerdef.NewConfigError(smtpDeliveryConfigField, "is required when send_email is set")
	}

	target, err := s.db.GetCheck(ctx, orgUID, deliveryCheckUID)
	if err != nil || target == nil {
		return checkerdef.NewConfigErrorf(
			smtpDeliveryConfigField, "check %s does not exist in this organization", deliveryCheckUID,
		)
	}

	if target.Type != string(checkerdef.CheckTypeEmail) {
		return checkerdef.NewConfigErrorf(
			smtpDeliveryConfigField, "check %s is a %q check, only email checks can be a delivery target",
			deliveryCheckUID, target.Type,
		)
	}

	param, err := s.db.GetSystemParameter(ctx, jmap.SystemParameterKey)
	if err != nil || param == nil {
		return checkerdef.NewConfigError("send_email", "this instance has no email inbox configured")
	}

	inboxCfg, err := jmap.JSONMapToConfig(param.Value)
	if err != nil || inboxCfg.AddressDomain == "" {
		return checkerdef.NewConfigError("send_email", "this instance has no email inbox configured")
	}

	return nil
}

// validateSMTPSendInterval enforces the send-mode minimum period (spec
// 2026-08-19-04 D — "enforce a minimum interval on send-mode SMTP checks so
// the inbox isn't flooded"). A zero period means "not provided" and is left
// to the normal default-period handling elsewhere, which already exceeds this
// floor. Only relevant to smtp checks with send_email set.
func validateSMTPSendInterval(checkType string, config map[string]any, period time.Duration) error {
	if checkerdef.CheckType(checkType) != checkerdef.CheckTypeSMTP {
		return nil
	}

	sendEmail, _ := config["send_email"].(bool)
	if !sendEmail || period == 0 {
		return nil
	}

	if period < smtpMinSendInterval {
		return checkerdef.NewConfigErrorf(
			"period", "send-mode SMTP checks require a period of at least %s, got %s",
			smtpMinSendInterval, period,
		)
	}

	return nil
}
