package statussubscribers_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
)

// errSendFailed is a static sentinel used by the failing fake sender.
var errSendFailed = errors.New("smtp boom")

// fakeSender captures sent messages and optionally fails.
type fakeSender struct {
	mu       sync.Mutex
	messages []*email.Message
	fail     bool
}

func (f *fakeSender) Send(_ context.Context, msg *email.Message) (*email.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.messages = append(f.messages, msg)

	if f.fail {
		return nil, errSendFailed
	}

	return &email.SendResult{Sent: true}, nil
}

func (f *fakeSender) sent() []*email.Message {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]*email.Message, len(f.messages))
	copy(out, f.messages)

	return out
}

// testFormatter builds a real formatter for notifier tests — the notifier now
// renders through the shared template pipeline instead of hand-rolled
// strings, so tests need a working *email.TemplateFormatter, not a stub.
func testFormatter(t *testing.T) email.Formatter {
	t.Helper()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	return formatter
}

// confirmedSubscriber creates and confirms a page-scoped subscriber, returning it.
func confirmedSubscriber(t *testing.T, s *subSetup, email string) {
	t.Helper()

	res := s.subscribe(t, email)
	_, err := s.svc.Confirm(t.Context(), res.Subscriber.ConfirmToken)
	require.NoError(t, err)
}

func TestNotifierFanOutToConfirmedOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	confirmedSubscriber(t, s, "confirmed@example.com")
	_ = s.subscribe(t, "unconfirmed@example.com") // not confirmed

	sender := &fakeSender{}
	notifier := statussubscribers.NewNotifier(s.dbSvc, sender, testFormatter(t), "https://status.example.com", nil)

	notifier.NotifyStatusUpdate(ctx, statussubscribers.UpdateEventFor(
		s.page.UID, nil, string(models.StatusUpdateKindInfo), "Heads up", "Body", nil, "Acme Status", 0))

	msgs := sender.sent()
	r.Len(msgs, 1)
	r.Equal([]string{"confirmed@example.com"}, msgs[0].Recipients.To)
	r.Contains(msgs[0].Text, "Unsubscribe")
	r.Contains(msgs[0].HTML, "Unsubscribe")
}

func TestNotifierResolvedTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	confirmedSubscriber(t, s, "ops@example.com")

	sender := &fakeSender{}
	notifier := statussubscribers.NewNotifier(s.dbSvc, sender, testFormatter(t), "https://status.example.com", nil)

	notifier.NotifyStatusUpdate(ctx, statussubscribers.UpdateEventFor(
		s.page.UID, nil, string(models.StatusUpdateKindResolved),
		"All clear", "Service restored", nil, "Acme Status", 1))

	msgs := sender.sent()
	r.Len(msgs, 1)
	r.Contains(msgs[0].Subject, "Resolved")
	r.Contains(msgs[0].Text, "Resolved")
}

func TestNotifierIncidentOpenedTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	confirmedSubscriber(t, s, "ops@example.com")

	incidentUID := "incident-123"
	sender := &fakeSender{}
	notifier := statussubscribers.NewNotifier(s.dbSvc, sender, testFormatter(t), "https://status.example.com", nil)

	// First update for the incident (count 0) -> incident-opened.
	notifier.NotifyStatusUpdate(ctx, statussubscribers.UpdateEventFor(
		s.page.UID, &incidentUID, string(models.StatusUpdateKindInvestigating),
		"Outage", "We are investigating", nil, "Acme Status", 0))

	msgs := sender.sent()
	r.Len(msgs, 1)
	r.Contains(msgs[0].Subject, "New incident")
}

func TestNotifierMailFailureDoesNotPanic(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	confirmedSubscriber(t, s, "ops@example.com")

	sender := &fakeSender{fail: true}
	notifier := statussubscribers.NewNotifier(s.dbSvc, sender, testFormatter(t), "https://status.example.com", nil)

	// Must not panic or block despite the send error.
	notifier.NotifyStatusUpdate(ctx, statussubscribers.UpdateEventFor(
		s.page.UID, nil, string(models.StatusUpdateKindInfo), "x", "y", nil, "Acme Status", 0))

	r.Len(sender.sent(), 1)
}

func TestNotifierNilSenderNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)

	notifier := statussubscribers.NewNotifier(s.dbSvc, nil, testFormatter(t), "https://status.example.com", nil)
	r.NotPanics(func() {
		notifier.NotifyStatusUpdate(t.Context(), statussubscribers.UpdateEventFor(
			s.page.UID, nil, "info", "x", "y", nil, "Acme Status", 0))
	})
}
