package statussubscribers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
	"github.com/fclairamb/solidping/server/internal/httpclientpool"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

// Compile-time assertion that *Notifier satisfies the statusupdates fan-out hook.
var _ statusupdates.SubscriberNotifier = (*Notifier)(nil)

// UpdateEventFor builds a *statusupdates.SubscriberUpdateEvent. Exposed
// primarily for tests that drive NotifyStatusUpdate directly.
func UpdateEventFor(
	statusPageUID string,
	incidentUID *string,
	kind, title, body string,
	linkURL *string,
	pageName string,
	incidentUpdateCount int,
) *statusupdates.SubscriberUpdateEvent {
	return &statusupdates.SubscriberUpdateEvent{
		StatusPageUID:       statusPageUID,
		IncidentUID:         incidentUID,
		Kind:                kind,
		Title:               title,
		BodyMarkdown:        body,
		LinkURL:             linkURL,
		PageName:            pageName,
		IncidentUpdateCount: incidentUpdateCount,
	}
}

// NotifyStatusUpdate adapts the statusupdates.SubscriberUpdateEvent to this
// package's UpdateEvent and performs the fan-out. This method lets *Notifier
// satisfy statusupdates.SubscriberNotifier directly.
func (n *Notifier) NotifyStatusUpdate(ctx context.Context, event *statusupdates.SubscriberUpdateEvent) {
	n.notify(ctx, &UpdateEvent{
		StatusPageUID:       event.StatusPageUID,
		IncidentUID:         event.IncidentUID,
		Kind:                event.Kind,
		Title:               event.Title,
		BodyMarkdown:        event.BodyMarkdown,
		LinkURL:             event.LinkURL,
		PageName:            event.PageName,
		IncidentUpdateCount: event.IncidentUpdateCount,
		PageLogoURL:         event.PageLogoURL,
		PageHideBranding:    event.PageHideBranding,
	})
}

// UpdateEvent describes a freshly-published status update for subscriber fan-out.
// It is intentionally decoupled from the statusupdates response type so the
// statusupdates package depends on this small interface, not the reverse.
type UpdateEvent struct {
	StatusPageUID string
	IncidentUID   *string
	Kind          string
	Title         string
	BodyMarkdown  string
	LinkURL       *string
	PageName      string
	// IncidentUpdateCount is the number of updates already published for the
	// incident (excluding this one). Zero means this is the first update for
	// the incident, which maps to the incident-opened template.
	IncidentUpdateCount int
	// PageLogoURL / PageHideBranding are the status page's own branding. They
	// are the page's, never the organization's: a subscriber opted into a
	// status page, so org branding must not leak into their mail.
	PageLogoURL      *string
	PageHideBranding bool
}

// Notifier fans a published status update out to confirmed subscribers, on
// whichever channel each one registered: email, an HMAC-signed webhook, or a
// Slack incoming webhook.
type Notifier struct {
	db        db.Service
	sender    email.Sender
	formatter email.Formatter
	baseURL   string
	logger    *slog.Logger
	// creds opens the encrypted delivery endpoints. nil is legal (self-hosted
	// with no master key); plaintext envelopes still open.
	creds credentials.Service
	// guard is the process's egress policy for outbound notification
	// deliveries. Every endpoint URL is checked against it right before a
	// delivery, and the default client dials through it, so a redirect or a
	// DNS change cannot steer a delivery into the server's own network. nil
	// means "no policy" (allow everything).
	guard *egress.Guard
	// httpClient delivers webhook/slack payloads. Injectable so tests can
	// point it at an httptest server.
	httpClient *http.Client
}

// NewNotifier builds a subscriber fan-out notifier.
//
// guard is a required parameter, not a setter, so no construction site can
// forget the egress policy: a subscriber endpoint is a customer-supplied URL,
// and an unguarded client following a redirect into 169.254.169.254 or a 10.x
// service is a plain SSRF.
func NewNotifier(
	dbService db.Service, sender email.Sender, formatter email.Formatter, baseURL string,
	logger *slog.Logger, creds credentials.Service, guard *egress.Guard,
) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}

	return &Notifier{
		db: dbService, sender: sender, formatter: formatter, baseURL: baseURL,
		logger: logger, creds: creds, guard: guard,
		httpClient: httpclientpool.NewGuardedClient(deliveryTimeout, guard),
	}
}

// SetHTTPClient overrides the delivery client. Test seam only.
func (n *Notifier) SetHTTPClient(client *http.Client) {
	if client != nil {
		n.httpClient = client
	}
}

// notify sends one mail wave for the update to all confirmed page-scoped
// subscribers plus incident-scoped subscribers matching the incident. Errors
// are logged, never returned — fan-out must never fail the originating status
// update. Safe to call from a goroutine.
func (n *Notifier) notify(ctx context.Context, event *UpdateEvent) {
	if n == nil {
		return
	}

	subs, err := n.db.ListConfirmedSubscribers(ctx, event.StatusPageUID, event.IncidentUID)
	if err != nil {
		n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to list subscribers",
			"statusPageUid", event.StatusPageUID, "error", err)

		return
	}

	if len(subs) == 0 {
		return
	}

	kind := mailKindForUpdate(event)
	localeOf := subscriberLocales(ctx, n.db)

	for _, sub := range subs {
		// A subscriber the circuit breaker disabled is skipped but NOT deleted:
		// the row is the operator's only record that the endpoint broke.
		if sub.DisabledAt != nil {
			continue
		}

		switch sub.Channel {
		case models.SubscriberChannelWebhook, models.SubscriberChannelSlack:
			n.deliverEndpoint(ctx, sub, event)
		case models.SubscriberChannelEmail:
			// An instance with no mailer configured still delivers webhooks;
			// only the email leg is skipped.
			if n.sender == nil || n.formatter == nil {
				continue
			}

			n.sendOne(ctx, sub, event, kind, localeOf(sub))
		default:
			// An unknown channel is a row written by a NEWER version of the
			// server than this process. Skipping is right; silence is not.
			n.logger.WarnContext(ctx, "status subscriber fan-out: unknown channel, skipping",
				"subscriberUid", sub.UID, "channel", string(sub.Channel))
		}
	}
}

// subscriberLocales returns the language each subscriber of one status page
// reads their mail in (spec 2026-10-21-01, decision 6): their own account's
// preferences when the address has one, else the page's language, else
// English. UTC unless the account sets a zone: a status page has none. The
// page is read once, on the first call, so every subscriber passed must be on
// the same page (a fan-out wave is); the resolver caches each address.
func subscriberLocales(ctx context.Context, dbService db.Service) func(*models.StatusPageSubscriber) i18n.Locale {
	var (
		resolver *recipient.Resolver
		hint     recipient.Hint
	)

	return func(sub *models.StatusPageSubscriber) i18n.Locale {
		if resolver == nil {
			resolver = recipient.NewResolver(dbService)
			hint = recipient.Hint{Locale: pageLanguage(ctx, dbService, sub.OrganizationUID, sub.StatusPageUID)}
		}

		return resolver.ForAddress(ctx, "", sub.EmailAddress(), hint)
	}
}

// pageLanguage is the language column of a status page, "" when it is unset or
// the page cannot be read (the reader then gets English).
func pageLanguage(ctx context.Context, dbService db.Service, orgUID, statusPageUID string) string {
	if dbService == nil {
		return ""
	}

	page, err := dbService.GetStatusPage(ctx, orgUID, statusPageUID)
	if err != nil || page == nil || page.Language == nil {
		if err != nil {
			slog.DebugContext(ctx, "status subscriber locale: page lookup failed",
				"statusPageUid", statusPageUID, "error", err)
		}

		return ""
	}

	return *page.Language
}

// LabelIn is the banner of an update message (its eyebrow, and the start of
// its preheader), in the reader's language. In English it is label(), word
// for word, for every kind but the confirm mail, which shows no banner.
func (k MailKind) LabelIn(loc i18n.Locale) string {
	switch k {
	case MailKindIncidentOpened:
		return loc.T("subscriber.update.kind.incidentOpened")
	case MailKindResolved:
		return loc.T("subscriber.update.kind.resolved")
	case MailKindConfirm, MailKindUpdate:
		return loc.T("subscriber.update.kind.update")
	default:
		return loc.T("subscriber.update.kind.update")
	}
}

// sendOne renders and sends a single subscriber EMAIL message, in loc. Emails are PII, so the
// recipient address is never logged in clear — only the subscriber UID.
func (n *Notifier) sendOne(
	ctx context.Context, sub *models.StatusPageSubscriber, event *UpdateEvent, kind MailKind, loc i18n.Locale,
) {
	data := &MailData{
		PageName:       event.PageName,
		Title:          event.Title,
		BodyMarkdown:   event.BodyMarkdown,
		UnsubscribeURL: unsubscribeURL(n.baseURL, sub.UnsubscribeToken),
	}

	if event.LinkURL != nil {
		data.LinkURL = *event.LinkURL
	}

	viewModel := map[string]any{
		"Label":                    kind.LabelIn(loc),
		"Title":                    data.Title,
		"BodyMarkdown":             data.BodyMarkdown,
		"LinkURL":                  data.LinkURL,
		"PageName":                 data.PageName,
		"SubscriberUnsubscribeURL": data.UnsubscribeURL,
	}
	// STATUS PAGE branding, and it honors hide_branding: no OrgName is set
	// here on purpose, so neither the org's logo nor the "<org> — sent by
	// SolidPing" footer line can reach a subscriber.
	email.ApplyStatusPageBranding(viewModel, event.PageName, event.PageLogoURL, event.PageHideBranding)

	subject, htmlBody, textBody, err := n.formatter.Format(
		"status-subscriber-update.html", loc, viewModel)
	if err != nil {
		n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to format mail",
			"subscriberUid", sub.UID, "statusPageUid", event.StatusPageUID, "error", err)

		return
	}

	msg := &email.Message{
		Recipients:       email.Recipients{To: []string{sub.EmailAddress()}},
		Subject:          subject,
		HTML:             htmlBody,
		Text:             textBody,
		SupportReplyable: email.SupportReplyable("status-subscriber-update.html"),
		Locale:           loc.Lang(),
		Timezone:         loc.Timezone(),
	}

	if _, err := n.sender.Send(ctx, msg); err != nil {
		n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to send mail",
			"subscriberUid", sub.UID, "statusPageUid", event.StatusPageUID, "error", err)
	}
}

// mailKindForUpdate maps a status update to a subscriber message kind.
func mailKindForUpdate(event *UpdateEvent) MailKind {
	if event.Kind == string(models.StatusUpdateKindResolved) {
		return MailKindResolved
	}

	if event.IncidentUID != nil && event.IncidentUpdateCount == 0 {
		return MailKindIncidentOpened
	}

	return MailKindUpdate
}
