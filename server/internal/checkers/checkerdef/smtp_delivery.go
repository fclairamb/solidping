package checkerdef

import "context"

// SMTPResolvedRecipientConfigKey is the raw check-config key an SMTP send-mode
// job carries its server-resolved delivery recipient under. It follows the
// `timeout`/`tunnelCheckUid` precedent: the worker reads it generically off
// the raw config map (never through SMTPConfig.FromMap, which does not know
// this key at all), so it can be threaded onto the execution context without
// growing a checker config field that a caller could set directly.
//
// This split is deliberate, not incidental: the address must never be
// reachable via JSON config decoding, because SMTPConfig.FromMap is also what
// a JS check's `check()` sub-check helper uses to build a config from a
// user-authored script (checkjs/checker.go), entirely bypassing the
// org-scoped, DB-backed resolution that produces this value. If the address
// were a normal config field, that path could set it directly and send mail
// to an arbitrary recipient with none of the reference-not-address
// guarantees. Context-only delivery closes that off structurally: nothing
// populates this key for a JS sub-check's context, so SMTPDeliveryRecipientFrom
// always misses there regardless of what the script's config map contains.
const SMTPResolvedRecipientConfigKey = "smtpResolvedRecipient"

// SMTPDeliveryInfo is the server-resolved information a send-mode SMTP check
// needs to submit its probe email: the concrete recipient address (resolved
// from delivery_check_uid) and the sending check's own UID (for the
// X-SolidPing-Check header — attribution, not authentication).
type SMTPDeliveryInfo struct {
	Recipient      string
	SourceCheckUID string
}

// smtpDeliveryCtxKeyType makes the context key unique within the binary.
type smtpDeliveryCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for a context key
var smtpDeliveryCtxKey = smtpDeliveryCtxKeyType{}

// WithSMTPDeliveryRecipient returns a context carrying the concrete delivery
// info a send-mode SMTP check should submit its probe email with. The worker
// sets it once per execution (read off the raw job config, server-resolved
// ahead of dispatch); checkers only consume it.
func WithSMTPDeliveryRecipient(ctx context.Context, info SMTPDeliveryInfo) context.Context {
	return context.WithValue(ctx, smtpDeliveryCtxKey, info)
}

// SMTPDeliveryRecipientFrom returns the resolved delivery info carried by ctx,
// or (zero, false) when absent — the overwhelmingly common case (send mode
// off), and also the safety net for any Execute call that reaches a send-mode
// config outside real job dispatch (e.g. a JS check's sub-check helper): no
// context value means no address, which the checker must treat as a loud
// error rather than silently skipping the send.
func SMTPDeliveryRecipientFrom(ctx context.Context) (SMTPDeliveryInfo, bool) {
	info, ok := ctx.Value(smtpDeliveryCtxKey).(SMTPDeliveryInfo)
	if !ok || info.Recipient == "" {
		return SMTPDeliveryInfo{}, false
	}

	return info, true
}
