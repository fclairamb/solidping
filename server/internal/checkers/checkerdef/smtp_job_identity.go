package checkerdef

import "context"

// SMTPJobIdentity is the context-only marker that distinguishes a real,
// dispatched SMTP check job from any other way SMTPChecker.Execute can be
// reached (revised design, 2026-08-19).
//
// Before this revision, send mode stored a delivery_check_uid REFERENCE and
// resolved it to a concrete recipient server-side at claim time, threading
// the resolved address through this same context-only seam — which
// structurally stopped a `js` check's sub-check helper
// (checkjs/checker.go:307, a verified second caller of Checker.Execute
// alongside worker.go's real job dispatch) from ever reaching a resolved
// recipient, since nothing populates the context for that path.
//
// The revised design stores the recipient directly as a plain `delivery_to`
// config field instead (so send mode works on every worker/agent with no
// claim-time resolution or sealing question — see checksmtp.SMTPConfig's
// DeliveryTo doc comment), which removes that structural protection: a JS
// sub-check config can now supply `send_email: true` and a valid
// inbox-domain `delivery_to` directly. This marker replaces the old
// recipient-carrying context value with a narrower one that exists for
// exactly this gate: only worker.go's real job-dispatch path sets it (see
// applySMTPDeliveryContext), so a JS sub-check's execCtx — inherited from
// the OUTER job's context, which is never of type smtp for a JS check's own
// job — never carries it, regardless of what the sub-check's config map
// requests. CheckUID additionally carries the sending check's own UID for
// the X-SolidPing-Check attribution header (never itself security-sensitive
// — it is not authentication, just attribution).
type SMTPJobIdentity struct {
	CheckUID string
}

// smtpJobIdentityCtxKeyType makes the context key unique within the binary.
type smtpJobIdentityCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for a context key
var smtpJobIdentityCtxKey = smtpJobIdentityCtxKeyType{}

// WithSMTPJobIdentity returns a context marking a real, dispatched SMTP check
// job. The worker sets it once per execution for every SMTP job (send mode or
// not — see applySMTPDeliveryContext); checkers only consume it to decide
// whether send mode is allowed to run at all.
func WithSMTPJobIdentity(ctx context.Context, identity SMTPJobIdentity) context.Context {
	return context.WithValue(ctx, smtpJobIdentityCtxKey, identity)
}

// SMTPJobIdentityFrom returns the job identity carried by ctx, or (zero,
// false) when absent. Absence means this Execute call did not come from
// worker.go's real job dispatch — the checkjs sub-check path being the
// verified example — which send mode must treat as a loud error rather than
// silently sending anyway.
func SMTPJobIdentityFrom(ctx context.Context) (SMTPJobIdentity, bool) {
	identity, ok := ctx.Value(smtpJobIdentityCtxKey).(SMTPJobIdentity)
	if !ok || identity.CheckUID == "" {
		return SMTPJobIdentity{}, false
	}

	return identity, true
}
