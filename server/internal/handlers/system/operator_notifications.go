package system

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// OperatorNotificationsResponse is the whole Server → Notifications tab in one
// document.
//
// It deliberately ships the candidate list next to the configuration. The
// single most likely silent failure is "subscribed, but that person has no
// notification route", and an operator can only see that if the page knows
// which routes each candidate actually has — so the API resolves it rather
// than leaving the UI to make N extra calls it would probably skip.
type OperatorNotificationsResponse struct {
	// Enabled is the master switch.
	Enabled bool `json:"enabled"`
	// Events is the subscribable vocabulary, in render order.
	Events []string `json:"events"`
	// Recipients is the candidate list: every super admin, plus anyone still
	// named in the parameter who has since lost the flag.
	Recipients []OperatorNotificationRecipient `json:"recipients"`
}

// OperatorNotificationRecipient is one row of the tab.
type OperatorNotificationRecipient struct {
	UserUID string `json:"userUid"`
	Email   string `json:"email"`
	Name    string `json:"name,omitempty"`
	// SuperAdmin is false only for a stale subscription — someone who was
	// subscribed and has since lost the flag. Their notices are already being
	// skipped at delivery time; the row exists so the operator can see why.
	SuperAdmin bool `json:"superAdmin"`
	// Events is what this user is currently subscribed to. Empty means "not a
	// recipient".
	Events []string `json:"events"`
	// Routes lists the distinct contact TYPES this user can be reached on,
	// across every org they belong to. Empty is the amber warning case:
	// subscribing them would deliver nothing.
	Routes []string `json:"routes"`
}

// OperatorNotificationsRequest is the PUT body.
type OperatorNotificationsRequest struct {
	Enabled bool `json:"enabled"`
	// Recipients is the FULL desired list. The dashboard sends every row it
	// rendered, including the ones with no event checked; the service drops
	// those (see normalizeRecipients) so unchecking the last box unsubscribes
	// rather than saving an inert row.
	Recipients []opsnotify.Recipient `json:"recipients"`
}

// OperatorNoticeTestResponse reports what the "Send me a test" button actually
// achieved. A bare 200 would be a lie: the transport succeeds as a call even
// when every route was skipped.
type OperatorNoticeTestResponse struct {
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
	Routes    int `json:"routes"`
}

// SetOperatorNoticeDeps wires the synchronous notice transport used by the
// test endpoint. Called from app/server.go; nil leaves the endpoint reporting
// "nothing delivered" rather than panicking.
func (s *Service) SetOperatorNoticeDeps(deps opsnotify.Deps) {
	s.opsNotice = &deps
}

// GetOperatorNotifications reads the configuration and resolves the candidate
// recipient list.
func (s *Service) GetOperatorNotifications(ctx context.Context) (*OperatorNotificationsResponse, error) {
	cfg, err := opsnotify.LoadConfig(ctx, s.db)
	if err != nil {
		return nil, err
	}

	users, err := s.db.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	subscribed := make(map[string][]string, len(cfg.Recipients))
	for i := range cfg.Recipients {
		subscribed[cfg.Recipients[i].UserUID] = cfg.Recipients[i].Events
	}

	rows := make([]OperatorNotificationRecipient, 0, len(users))

	for _, user := range users {
		events, isSubscribed := subscribed[user.UID]
		// Every super admin is a candidate; a non-super-admin appears only
		// when they are a stale subscription that needs cleaning up.
		if !user.SuperAdmin && !isSubscribed {
			continue
		}

		routes, routesErr := s.contactTypesFor(ctx, user.UID)
		if routesErr != nil {
			return nil, routesErr
		}

		rows = append(rows, OperatorNotificationRecipient{
			UserUID:    user.UID,
			Email:      user.Email,
			Name:       user.Name,
			SuperAdmin: user.SuperAdmin,
			Events:     events,
			Routes:     routes,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Email) < strings.ToLower(rows[j].Email)
	})

	return &OperatorNotificationsResponse{
		Enabled:    cfg.Enabled,
		Events:     opsnotify.SubscribableEvents(),
		Recipients: rows,
	}, nil
}

// contactTypesFor lists the distinct contact types a user has an ENABLED route
// for, across every organization they belong to — the same walk delivery does,
// so what the page shows is what the transport would use.
func (s *Service) contactTypesFor(ctx context.Context, userUID string) ([]string, error) {
	members, err := s.db.ListMembersByUser(ctx, userUID)
	if err != nil {
		return nil, fmt.Errorf("list memberships of %s: %w", userUID, err)
	}

	seen := make(map[string]bool)
	out := make([]string, 0, len(members))

	for _, member := range members {
		routes, routesErr := s.db.ListUserContactsWithRoutes(ctx, userUID, member.OrganizationUID)
		if routesErr != nil {
			return nil, fmt.Errorf("list routes of %s: %w", userUID, routesErr)
		}

		for _, route := range routes {
			if !route.Enabled || route.Contact == nil || seen[route.Contact.Type] {
				continue
			}

			seen[route.Contact.Type] = true

			out = append(out, route.Contact.Type)
		}
	}

	return out, nil
}

// SetOperatorNotifications validates and persists the configuration.
//
// It goes through the same validators as the raw parameter CRUD — including
// the super-admin check — so the dashboard cannot save a document the delivery
// path would then refuse to act on.
func (s *Service) SetOperatorNotifications(
	ctx context.Context, req *OperatorNotificationsRequest,
) (*OperatorNotificationsResponse, error) {
	value := map[string]any{
		"enabled":    req.Enabled,
		"recipients": normalizeRecipients(req.Recipients),
	}

	if err := opsnotify.ValidateParameterWithDB(ctx, s.db, value); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidParameter, err)
	}

	if err := s.db.SetSystemParameter(ctx, opsnotify.ParamOperatorNotifications, value, false); err != nil {
		return nil, fmt.Errorf("save operator notifications: %w", err)
	}

	return s.GetOperatorNotifications(ctx)
}

// normalizeRecipients drops rows that subscribe to nothing and trims uids.
//
// An unchecked row is how the dashboard says "not a recipient", so it arrives
// as an empty event list rather than as an absent row. Dropping it here is
// what makes the validator's "must subscribe to at least one event" rule a
// real guard against a typo instead of a false alarm on every save.
func normalizeRecipients(in []opsnotify.Recipient) []opsnotify.Recipient {
	out := make([]opsnotify.Recipient, 0, len(in))

	for i := range in {
		recipient := &in[i]

		events := make([]string, 0, len(recipient.Events))

		for _, event := range recipient.Events {
			trimmed := strings.TrimSpace(event)
			if trimmed != "" {
				events = append(events, trimmed)
			}
		}

		if len(events) == 0 {
			continue
		}

		out = append(out, opsnotify.Recipient{
			UserUID: strings.TrimSpace(recipient.UserUID),
			Events:  events,
		})
	}

	return out
}

// SendOperatorNoticeTest delivers a `test` notice to one user through the real
// transport — the one button an operator presses to confirm the setup works.
//
// It runs SYNCHRONOUSLY, unlike the event hooks, precisely because its whole
// value is the answer: enqueueing it would report "accepted" and tell the
// operator nothing about whether Telegram actually carried it.
func (s *Service) SendOperatorNoticeTest(
	ctx context.Context, user *models.User, baseURL string,
) (*OperatorNoticeTestResponse, error) {
	if s.opsNotice == nil {
		return nil, ErrOperatorNoticeUnavailable
	}

	notice := opsnotify.Notice{
		Event:   opsnotify.EventTest,
		Subject: "[SolidPing] Operator notification test",
		Body: "This is a test of the operator notifications on this SolidPing instance.\n" +
			"If you are reading it, the route it arrived on works.\n\n" +
			"Requested by " + user.Email + ".",
		URL: strings.TrimRight(baseURL, "/") + "/dash0/",
	}

	report := opsnotify.DeliverToUser(ctx, *s.opsNotice, slog.Default(), user.UID, &notice)

	return &OperatorNoticeTestResponse{
		Delivered: report.Delivered,
		Failed:    report.Failed,
		Skipped:   report.Skipped,
		Routes:    report.Routes,
	}, nil
}
