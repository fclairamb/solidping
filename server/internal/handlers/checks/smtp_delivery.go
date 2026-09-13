package checks

import (
	"context"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksmtp"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// smtpDeliveryToField and smtpDeliveryCheckUIDField are the config field
// names reported on validation errors, so the dashboard can attach the
// message to the right input.
const (
	smtpDeliveryToField       = "delivery_to"
	smtpDeliveryCheckUIDField = "delivery_check_uid"
)

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
// Revised design (2026-08-19, superseding the reference-only original):
// `delivery_to` is now the OPERATIVE recipient — stored directly rather than
// resolved from a reference at claim time, which is what makes send mode work
// on every worker/agent (see checksmtp.SMTPConfig's DeliveryTo doc comment).
// `delivery_check_uid` is demoted to OPTIONAL bonus metadata: attribution and
// the dashboard's pairing links only, never a security control.
//
// Rules:
//   - only relevant to smtp checks with send_email set; every other check
//     (and a plain SMTP check) passes trivially.
//   - mail_from and delivery_to must both be single bare RFC 5322 addresses
//     — checksmtp.ValidateMailFrom / ValidateDeliveryTo, re-run here because
//     UpdateCheck never calls checker.Validate.
//   - delivery_to's domain must equal the instance's configured email_inbox
//     domain — the ONE restriction left once the reference indirection is
//     gone; the instance must have an email_inbox configured at all, or
//     send_email is rejected outright.
//   - delivery_check_uid, WHEN SUPPLIED, must reference a check that exists
//     in the SAME org (the lookup is org-scoped, so a cross-org uid simply
//     reads as "not found") and is a CheckTypeEmail check — so the bonus
//     metadata itself can't become a cross-org information leak — but it is
//     never required.
//
// Accepted residual risk (Florent, 2026-08-19, recorded in the spec's
// "Revised design" section): giving up the reference indirection means one
// org that learns another org's tokenized address can aim probes at it. Do
// NOT reinstate a same-org constraint on delivery_to — that trade-off was
// made deliberately, in exchange for send mode actually working on private
// locations.
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
	// on create): mail_from and delivery_to are both spliced verbatim into
	// the wire protocol, so a PATCH must be held to the same real-address
	// requirement or it could smuggle a CRLF-terminated extra SMTP command /
	// an injected header past this gate.
	mailFrom, _ := effective["mail_from"].(string)
	if err := checksmtp.ValidateMailFrom(mailFrom); err != nil {
		return err
	}

	deliveryTo, _ := effective[smtpDeliveryToField].(string)
	deliveryCheckUID, _ := effective[smtpDeliveryCheckUIDField].(string)

	// An ABSENT delivery_to with a delivery_check_uid is the config-as-code
	// shape (spec 2026-09-11-02): delivery_to is export-redacted, so a
	// stripped document carries only the uid and deriveRedactedFields fills
	// the address in before this runs. Reaching here with it still empty means
	// the derivation failed, so report the reference that failed rather than
	// the field the document deliberately omitted.
	if deliveryTo == "" && deliveryCheckUID != "" {
		if refErr := s.validateDeliveryCheckReference(ctx, orgUID, deliveryCheckUID); refErr != nil {
			return refErr
		}

		return checkerdef.NewConfigErrorf(smtpDeliveryToField,
			"could not be derived from %s: this instance has no email inbox configured",
			smtpDeliveryCheckUIDField)
	}

	if err := checksmtp.ValidateDeliveryTo(deliveryTo); err != nil {
		return err
	}

	param, err := s.db.GetSystemParameter(ctx, jmap.SystemParameterKey)
	if err != nil || param == nil {
		return checkerdef.NewConfigError("send_email", "this instance has no email inbox configured")
	}

	inboxCfg, err := jmap.JSONMapToConfig(param.Value)
	if err != nil || inboxCfg.AddressDomain == "" {
		return checkerdef.NewConfigError("send_email", "this instance has no email inbox configured")
	}

	if !strings.EqualFold(deliveryToDomain(deliveryTo), inboxCfg.AddressDomain) {
		return checkerdef.NewConfigErrorf(
			smtpDeliveryToField, "must be an address at this instance's inbox domain (%s)", inboxCfg.AddressDomain,
		)
	}

	// delivery_check_uid is optional bonus metadata now — validated only when
	// supplied, and never required.
	return s.validateDeliveryCheckReference(ctx, orgUID, deliveryCheckUID)
}

// validateDeliveryCheckReference enforces the delivery_check_uid rules: when
// supplied it must name a check that exists in the SAME org (the lookup is
// org-scoped, so a cross-org uid simply reads as "not found") and is a
// CheckTypeEmail check. An empty uid passes — the field is optional.
func (s *Service) validateDeliveryCheckReference(ctx context.Context, orgUID, deliveryCheckUID string) error {
	if deliveryCheckUID == "" {
		return nil
	}

	target, err := s.db.GetCheck(ctx, orgUID, deliveryCheckUID)
	if err != nil || target == nil {
		return checkerdef.NewConfigErrorf(
			smtpDeliveryCheckUIDField, "check %s does not exist in this organization", deliveryCheckUID,
		)
	}

	if target.Type != string(checkerdef.CheckTypeEmail) {
		return checkerdef.NewConfigErrorf(
			smtpDeliveryCheckUIDField, "check %s is a %q check, only email checks can be a delivery target",
			deliveryCheckUID, target.Type,
		)
	}

	return nil
}

// deliveryToDomain returns the domain half of a delivery_to address, or ""
// if it has no '@' (ValidateDeliveryTo already rejected that shape by the
// time this runs, but stay defensive rather than panic-prone).
func deliveryToDomain(deliveryTo string) string {
	at := strings.LastIndex(deliveryTo, "@")
	if at < 0 || at == len(deliveryTo)-1 {
		return ""
	}

	return deliveryTo[at+1:]
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
