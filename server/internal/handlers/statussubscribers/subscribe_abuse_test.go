package statussubscribers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portSubscribeAbusePG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// internal/db/incident_number_test.go).
const portSubscribeAbusePG = 15674

// abuseFixture is one org + public page behind the real Subscribe handler, on
// either engine (spec 2026-10-28-02).
type abuseFixture struct {
	svc    *statussubscribers.Service
	dbSvc  db.Service
	bunDB  *bun.DB
	router *httpx.Router
	sender *fakeSender
	org    *models.Organization
	page   *models.StatusPage
	// suffix keeps addresses unique across the subtests sharing one database:
	// the per-recipient budget is instance-wide.
	suffix string
	// clientIP is the caller address of every request this fixture sends.
	clientIP string
}

type abuseEngine struct {
	dbSvc db.Service
	bunDB *bun.DB
	seq   atomic.Int64
}

func (e *abuseEngine) fixture(t *testing.T, budget config.StatusSubscribersConfig) *abuseFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	n := e.seq.Add(1)
	suffix := strconv.FormatInt(n, 10) + "-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)

	org := models.NewOrganization("abuse-"+suffix, "Acme "+suffix)
	r.NoError(e.dbSvc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme Status", "acme-status-"+suffix)
	r.NoError(e.dbSvc.CreateStatusPage(ctx, page))

	svc := statussubscribers.NewService(e.dbSvc, nil, statussubscribers.WithConfirmBudget(budget))

	formatter, err := email.NewFormatter()
	r.NoError(err)

	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://status.example.com"
	sender := &fakeSender{}
	handler := statussubscribers.NewHandler(svc, e.dbSvc, sender, formatter, cfg)

	router := httpx.New()
	router.POST("/api/v1/orgs/:org/status-pages/:statusPageUid/subscribers", handler.Subscribe)

	return &abuseFixture{
		svc: svc, dbSvc: e.dbSvc, bunDB: e.bunDB, router: router, sender: sender,
		org: org, page: page, suffix: suffix,
		clientIP: fmt.Sprintf("198.51.%d.%d", (n/250)%250, n%250+1),
	}
}

func (f *abuseFixture) addr(local string) string {
	return local + "-" + f.suffix + "@acme.com"
}

// subscribeHTTP posts to the public endpoint and asserts the uniform 202.
func (f *abuseFixture) subscribeHTTP(t *testing.T, address string) {
	t.Helper()

	body, err := json.Marshal(map[string]any{"email": address})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/status-pages/"+f.page.UID+"/subscribers", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = f.clientIP + ":4321"

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "pending_confirmation")
}

func (f *abuseFixture) row(t *testing.T, address string) *models.StatusPageSubscriber {
	t.Helper()

	sub, err := f.dbSvc.FindAnySubscriber(t.Context(), f.page.UID, address, models.SubscriberScopePage, nil)
	require.NoError(t, err)

	return sub
}

func (f *abuseFixture) mailsTo(address string) int {
	count := 0

	for _, msg := range f.sender.sent() {
		if len(msg.Recipients.To) == 1 && msg.Recipients.To[0] == address {
			count++
		}
	}

	return count
}

// age backdates a row's confirm token (and creation) by `by`.
func (f *abuseFixture) age(t *testing.T, uid string, by time.Duration) {
	t.Helper()

	old := time.Now().UTC().Add(-by)
	_, err := f.bunDB.NewUpdate().
		Model((*models.StatusPageSubscriber)(nil)).
		Set("confirm_sent_at = ?", old).
		Set("created_at = ?", old).
		Where("uid = ?", uid).
		Exec(t.Context())
	require.NoError(t, err)
}

func defaultBudget() config.StatusSubscribersConfig {
	return config.DefaultStatusSubscribersConfig()
}

//nolint:funlen,maintidx // one contract, run against both engines
func runSubscribeAbuseContract(t *testing.T, engine *abuseEngine) {
	t.Helper()

	t.Run("re-subscribing a confirmed address leaves it confirmed and mails nothing", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f := engine.fixture(t, defaultBudget())
		address := f.addr("ops")

		f.subscribeHTTP(t, address)
		_, err := f.svc.Confirm(t.Context(), f.row(t, address).ConfirmToken)
		r.NoError(err)

		before := f.row(t, address)
		r.NotNil(before.ConfirmedAt)

		f.subscribeHTTP(t, address)

		after := f.row(t, address)
		r.NotNil(after.ConfirmedAt, "a repeat subscribe must not un-confirm the subscriber")
		r.Equal(before.UnsubscribeToken, after.UnsubscribeToken, "the unsubscribe links already mailed must keep working")
		r.Equal(before.ConfirmToken, after.ConfirmToken)
		r.Equal(1, f.mailsTo(address), "only the first subscribe mails a confirm link")

		confirmed, err := f.dbSvc.ListConfirmedSubscribers(t.Context(), f.page.UID, nil)
		r.NoError(err)
		r.Len(confirmed, 1, "the subscriber still receives the fan-out")
	})

	t.Run("ten subscribes of one new address mail at most the per-recipient budget", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f := engine.fixture(t, defaultBudget())
		address := f.addr("victim")

		for range 10 {
			f.subscribeHTTP(t, address)
		}

		r.Equal(config.DefaultStatusSubscriberConfirmPerRecipientPerHour, f.mailsTo(address))

		// A throttled call leaves the row alone, so the last link mailed
		// still confirms.
		lastMail := f.sender.sent()[len(f.sender.sent())-1]
		token := f.row(t, address).ConfirmToken
		r.Contains(lastMail.HTML, token)
		_, err := f.svc.Confirm(t.Context(), token)
		r.NoError(err)
	})

	t.Run("the per-recipient budget spans every page", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		first := engine.fixture(t, defaultBudget())
		second := engine.fixture(t, defaultBudget())
		address := first.addr("spread")

		for range 2 {
			first.subscribeHTTP(t, address)
		}

		for range 3 {
			second.subscribeHTTP(t, address)
		}

		r.Equal(2, first.mailsTo(address))
		r.Equal(1, second.mailsTo(address))
	})

	t.Run("the per-IP budget caps distinct addresses from one client", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f := engine.fixture(t, defaultBudget())

		for i := range config.DefaultStatusSubscriberConfirmPerIPPerHour + 5 {
			f.subscribeHTTP(t, f.addr("ip"+strconv.Itoa(i)))
		}

		r.Len(f.sender.sent(), config.DefaultStatusSubscriberConfirmPerIPPerHour)
	})

	t.Run("the per-page budget caps a page across clients", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		budget := defaultBudget()
		budget.ConfirmPerPagePerHour = 2
		f := engine.fixture(t, budget)

		for i := range 4 {
			f.clientIP = "203.0.113." + strconv.Itoa(i+1) + "0"
			f.subscribeHTTP(t, f.addr("page"+strconv.Itoa(i)))
		}

		r.Len(f.sender.sent(), 2)
	})

	t.Run("a first subscribe sends exactly one confirm mail", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f := engine.fixture(t, defaultBudget())
		address := f.addr("first")

		f.subscribeHTTP(t, address)

		r.Len(f.sender.sent(), 1)
		r.Equal(1, f.mailsTo(address))
		r.Contains(f.sender.sent()[0].HTML, f.row(t, address).ConfirmToken)
	})

	t.Run("an unsubscribed address can subscribe again and gets a new confirm mail", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f := engine.fixture(t, defaultBudget())
		address := f.addr("returning")

		f.subscribeHTTP(t, address)
		_, err := f.svc.Confirm(t.Context(), f.row(t, address).ConfirmToken)
		r.NoError(err)
		_, err = f.svc.Unsubscribe(t.Context(), f.row(t, address).UnsubscribeToken)
		r.NoError(err)

		f.subscribeHTTP(t, address)

		r.Equal(2, f.mailsTo(address))
		revived := f.row(t, address)
		r.Nil(revived.DeletedAt)
		r.Nil(revived.ConfirmedAt)
		r.Contains(f.sender.sent()[1].HTML, revived.ConfirmToken)
	})

	t.Run("an expired unconfirmed row is refused then reaped; a confirmed one is kept", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		ctx := t.Context()
		f := engine.fixture(t, defaultBudget())
		stale, kept, fresh := f.addr("stale"), f.addr("kept"), f.addr("fresh")

		f.subscribeHTTP(t, stale)
		f.subscribeHTTP(t, kept)
		f.subscribeHTTP(t, fresh)
		_, err := f.svc.Confirm(ctx, f.row(t, kept).ConfirmToken)
		r.NoError(err)

		age := models.SubscriberConfirmTTL + 24*time.Hour
		staleRow := f.row(t, stale)
		f.age(t, staleRow.UID, age)
		f.age(t, f.row(t, kept).UID, age)

		// Expired but not yet reaped: the link is already refused.
		_, err = f.svc.Confirm(ctx, staleRow.ConfirmToken)
		r.ErrorIs(err, statussubscribers.ErrInvalidToken)

		runner, err := (&jobtypes.StateCleanupJobDefinition{}).CreateJobRun(nil)
		r.NoError(err)
		r.NoError(runner.Run(ctx, &jobdef.JobContext{DBService: f.dbSvc, Logger: slog.Default()}))

		_, err = f.dbSvc.FindAnySubscriber(ctx, f.page.UID, stale, models.SubscriberScopePage, nil)
		r.Error(err, "the expired unconfirmed row is hard-deleted")

		_, err = f.svc.Confirm(ctx, staleRow.ConfirmToken)
		r.ErrorIs(err, statussubscribers.ErrInvalidToken)

		r.NotNil(f.row(t, kept).ConfirmedAt, "a confirmed row of the same age is kept")
		r.Nil(f.row(t, fresh).ConfirmedAt, "a fresh unconfirmed row is kept")

		_, err = f.svc.Confirm(ctx, f.row(t, fresh).ConfirmToken)
		r.NoError(err)
	})
}

func TestSubscribeAbuse_SQLite(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	runSubscribeAbuseContract(t, &abuseEngine{dbSvc: dbSvc, bunDB: dbSvc.DB()})
}

func TestSubscribeAbuse_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portSubscribeAbusePG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runSubscribeAbuseContract(t, &abuseEngine{dbSvc: dbSvc, bunDB: dbSvc.DB()})
}
