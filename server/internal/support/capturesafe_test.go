package support_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/support"
)

// panickingDB is a db.Service that blows up on the first query.
//
// The embedded nil interface is the point: every method it does not override
// panics with a nil dereference, and DB() panics outright. It stands in for the
// class of bug CaptureSafe exists to contain — a nil map, an out-of-range slice,
// a parser that trips on a malformed provider payload — none of which a
// returned error would ever represent.
type panickingDB struct {
	db.Service
}

func (panickingDB) DB() *bun.DB {
	panic("capture blew up inside the support service")
}

// recordingHandler captures slog records so a test can prove a specific branch
// ran, without depending on a metric another parallel test may also touch.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
	buf     bytes.Buffer
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, rec slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.records = append(h.records, rec)
	h.buf.WriteString(rec.Message)
	h.buf.WriteString("\n")

	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) messages() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.buf.String()
}

// TestCaptureSafe_ContainsAPanic is the test that actually exercises the
// recover() in CaptureSafe.
//
// The contract is not "capture returns an error tidily" — Capture already does
// that. It is that A CAPTURE BUG MUST NEVER ESCAPE INTO WEBHOOK HANDLING. A
// panic escaping here unwinds the webhook handler, the provider sees a 5xx,
// retries, and eventually disables the subscription: one bug costs the whole
// channel rather than one message.
//
// Asserting NotPanics against a DB that merely returns errors would prove
// nothing — that test passes with the recover() deleted. This one forces a real
// panic on the first query.
func TestCaptureSafe_ContainsAPanic(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	handler := &recordingHandler{}
	svc := support.NewService(panickingDB{}, support.Options{
		Logger: slog.New(handler),
	})

	before := testutil.ToFloat64(
		prommetrics.SupportCapture.WithLabelValues(models.SupportChannelWhatsApp, "failed"))

	r.NotPanics(func() {
		svc.CaptureSafe(t.Context(), &support.Inbound{
			Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
			ExternalID: "wamid.PANIC", Body: "hello",
		})
	})

	// The recover branch specifically — not merely "no panic reached the
	// caller", which a service that never touched the DB would also satisfy.
	r.Contains(handler.messages(), "support capture panicked")

	// And the failure is COUNTED, so a silent capture outage is visible in
	// solidping_support_capture_total rather than looking like nobody writing
	// in. Strictly greater rather than exactly +1: other tests in this package
	// run in parallel and only ever increment.
	after := testutil.ToFloat64(
		prommetrics.SupportCapture.WithLabelValues(models.SupportChannelWhatsApp, "failed"))
	r.Greater(after, before)

	// POSITIVE CONTROL: the panic really did come from the capture path, so a
	// direct Capture on the same service propagates it. If this did NOT panic,
	// the assertions above would be measuring a service that simply never
	// reached the database.
	r.Panics(func() {
		_, _, _ = svc.Capture(t.Context(), &support.Inbound{
			Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
			ExternalID: "wamid.PANIC2", Body: "hello",
		})
	})
}

// TestCaptureSafe_IgnoresNilInputs covers the two guard clauses, which are the
// cheapest way for a channel adapter to crash the webhook.
func TestCaptureSafe_IgnoresNilInputs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var nilService *support.Service

	r.NotPanics(func() { nilService.CaptureSafe(t.Context(), &support.Inbound{}) })
	r.NotPanics(func() {
		support.NewService(panickingDB{}, support.Options{Logger: slog.New(&recordingHandler{})}).
			CaptureSafe(t.Context(), nil)
	})
}

// TestCaptureSafe_LogsAPlainFailureWithoutPanicking keeps the ordinary error
// path honest alongside the panic path: a DB error is logged at WARN and
// swallowed, and is NOT reported as a panic.
func TestCaptureSafe_LogsAPlainFailureWithoutPanicking(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	handler := &recordingHandler{}
	svc := support.NewService(h.dbSvc, support.Options{Logger: slog.New(handler)})

	r.NoError(h.dbSvc.Close())

	r.NotPanics(func() {
		svc.CaptureSafe(t.Context(), &support.Inbound{
			Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
			ExternalID: "wamid.DEADDB", Body: "hello",
		})
	})

	logged := handler.messages()
	r.Contains(logged, "support capture failed")
	r.NotContains(logged, "support capture panicked",
		"a returned error must not be reported as a panic")
}

// TestRecordDelivery_ContainsAPanic covers the sibling of CaptureSafe.
//
// RecordDelivery runs inside the very same Meta and Twilio delivery callbacks,
// so it needs the same containment for the same reason: a panic would unwind
// the webhook handler, the provider would see a 5xx, retry, and eventually
// disable the subscription. A support reply's delivery status is never worth
// the channel.
//
// This gap was real — RecordDelivery shipped without the recover, and the
// webhook-level test in whatsappcb found it.
func TestRecordDelivery_ContainsAPanic(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	handler := &recordingHandler{}
	svc := support.NewService(panickingDB{}, support.Options{Logger: slog.New(handler)})

	r.NotPanics(func() {
		svc.RecordDelivery(t.Context(), models.SupportChannelWhatsApp, "wamid.OUT", "delivered")
	})

	r.Contains(handler.messages(), "support delivery update panicked")

	// The guard clauses short-circuit BEFORE the database, so they must not log
	// a panic — otherwise every no-op callback would look like a fault.
	quiet := &recordingHandler{}
	quietSvc := support.NewService(panickingDB{}, support.Options{Logger: slog.New(quiet)})

	r.NotPanics(func() {
		quietSvc.RecordDelivery(t.Context(), models.SupportChannelWhatsApp, "", "delivered")
		quietSvc.RecordDelivery(t.Context(), models.SupportChannelWhatsApp, "wamid.OUT", "")
	})
	r.Empty(quiet.messages())
}
