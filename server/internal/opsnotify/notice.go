// Package opsnotify is the instance-level "tell an operator" transport.
//
// It generalizes what the platform watchdog (spec 2026-08-24-10) already did
// for its hourly digest: deliver one free-text notice to a chosen user through
// that user's OWN enabled notification routes (`user_notification_routes` →
// `user_contacts`), across every organization they belong to, deduplicated by
// destination. No new medium, no hardcoded webhook — operators already
// maintain their contact preferences for incident paging, and instance events
// inherit them.
//
// # Why this package is a leaf, and why DeliverToUser takes Deps
//
// The originating spec sketched `DeliverToUser(ctx, jctx *jobdef.JobContext,
// …)`. That signature is unreachable: `jobs/jobdef` imports `app/services`,
// whose Registry holds a `*support.Service`, and `internal/support` is one of
// the two packages that must be able to RAISE a notice. Importing jobdef here
// would close `support → opsnotify → jobdef → services → support`. The same
// argument rules out importing `integrations/slack` (which imports
// `handlers/auth`, the other raiser).
//
// So this package stays a leaf and takes its media as closures on Deps.
// `internal/opsnotifywire` builds those closures from the service registry.
package opsnotify

// Event vocabulary. These strings are persisted inside the
// `operator_notifications` system parameter and are the `event` metric label,
// so they are part of the contract: renaming one silently unsubscribes every
// operator who had it selected.
const (
	// EventSupportMessage fires when an INBOUND support message is captured.
	// Outbound operator replies deliberately do not fire it: a colleague
	// answering is not an event anyone needs to be paged for.
	EventSupportMessage = "support.message"
	// EventUserRegistered fires once per new `users` row, whatever the signup
	// method — password, any OAuth/OIDC/SAML/LDAP provider, or accepting an
	// invitation.
	EventUserRegistered = "user.registered"
	// EventWatchdogDigest is the platform watchdog's hourly digest. It is NOT
	// subscribable through operator notifications — the watchdog has its own
	// `platform_watchdog` recipient list — but it travels this transport, so
	// it needs a label of its own to stay distinguishable in the metrics.
	EventWatchdogDigest = "watchdog.digest"
	// EventTest is the "Send me a test" button. Same exclusion as the digest:
	// deliverable, never subscribable.
	EventTest = "test"
)

// SubscribableEvents is the set an operator may subscribe a recipient to, in
// the order the dashboard renders them.
//
// The watchdog digest and the test notice are absent on purpose: subscribing
// to them would either duplicate `platform_watchdog` or mean nothing.
func SubscribableEvents() []string {
	return []string{EventSupportMessage, EventUserRegistered}
}

// IsSubscribableEvent reports whether an event may appear in a recipient's
// `events` list.
func IsSubscribableEvent(event string) bool {
	for _, known := range SubscribableEvents() {
		if known == event {
			return true
		}
	}

	return false
}

// Notice is one thing an operator is told about.
//
// It is deliberately medium-agnostic plain text: every renderer in this
// package turns it into the shape its medium accepts (an email body, escaped
// Telegram HTML, a Slack DM, a push title+body, a truncated SMS). Nothing here
// may carry markup — bodies are attacker-influenced (a support message is
// literally typed by a stranger) and must never be interpolated as such.
type Notice struct {
	// Event is one of the constants above. It is the metric label and the
	// subscription key.
	Event string
	// Subject is one line: the email subject, the push title, the SMS lead.
	Subject string
	// Body is multi-line plain text.
	Body string
	// URL deep-links into the dashboard. May be empty.
	URL string
}

// noticeText is the plain-text rendering shared by email, Slack and Telegram:
// the body, then the deep link when there is one.
func (n Notice) noticeText() string {
	if n.URL == "" {
		return n.Body
	}

	return n.Body + "\n\n" + n.URL
}
