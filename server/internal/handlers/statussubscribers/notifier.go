package statussubscribers

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
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
}

// Notifier fans a published status update out to confirmed subscribers by email.
type Notifier struct {
	db        db.Service
	sender    email.Sender
	formatter email.Formatter
	baseURL   string
	logger    *slog.Logger
}

// NewNotifier builds a subscriber fan-out notifier.
func NewNotifier(
	dbService db.Service, sender email.Sender, formatter email.Formatter, baseURL string, logger *slog.Logger,
) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}

	return &Notifier{db: dbService, sender: sender, formatter: formatter, baseURL: baseURL, logger: logger}
}

// notify sends one mail wave for the update to all confirmed page-scoped
// subscribers plus incident-scoped subscribers matching the incident. Errors
// are logged, never returned — fan-out must never fail the originating status
// update. Safe to call from a goroutine.
func (n *Notifier) notify(ctx context.Context, event *UpdateEvent) {
	if n == nil || n.sender == nil || n.formatter == nil {
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

	for _, sub := range subs {
		n.sendOne(ctx, sub, event, kind)
	}
}

// sendOne renders and sends a single subscriber message. Emails are PII, so the
// recipient address is never logged in clear — only the subscriber UID.
func (n *Notifier) sendOne(
	ctx context.Context, sub *models.StatusPageSubscriber, event *UpdateEvent, kind MailKind,
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

	subject, htmlBody, textBody, err := n.formatter.Format("status-subscriber-update.html", map[string]any{
		"Subject":                  kind.subject(data),
		"Label":                    kind.label(),
		"Title":                    data.Title,
		"BodyMarkdown":             data.BodyMarkdown,
		"LinkURL":                  data.LinkURL,
		"PageName":                 data.PageName,
		"SubscriberUnsubscribeURL": data.UnsubscribeURL,
	})
	if err != nil {
		n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to format mail",
			"subscriberUid", sub.UID, "statusPageUid", event.StatusPageUID, "error", err)

		return
	}

	msg := &email.Message{
		Recipients: email.Recipients{To: []string{sub.Email}},
		Subject:    subject,
		HTML:       htmlBody,
		Text:       textBody,
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
