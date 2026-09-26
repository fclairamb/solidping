package checkscreenshots_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/checkscreenshots"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// recordingNotifier records the express hints the service publishes.
type recordingNotifier struct {
	mu    sync.Mutex
	sends []string
}

func (n *recordingNotifier) Notify(_ context.Context, eventType, payload string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.sends = append(n.sends, eventType+" "+payload)

	return nil
}

func (n *recordingNotifier) Listen(string) <-chan string { return make(chan string) }

func (n *recordingNotifier) Unlisten(string, <-chan string) {}

func (n *recordingNotifier) Close() error { return nil }

func (n *recordingNotifier) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]string(nil), n.sends...)
}

type fixture struct {
	db       *sqlite.Service
	store    *attachments.Service
	router   *httpx.Router
	clock    *clock.Fake
	notifier *recordingNotifier
	org      *models.Organization
	browser  *models.Check
}

func setup(t *testing.T) *fixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	store := attachments.NewService(files.NewService(dbSvc, cfg), dbSvc, cfg)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	browser := models.NewCheck(org.UID, "home", "browser")
	browser.Regions = []string{"us-east", "eu-west"}
	r.NoError(dbSvc.CreateCheck(ctx, browser))

	fake := clock.NewFake(time.Now())
	hints := &recordingNotifier{}
	handler := checkscreenshots.NewHandler(checkscreenshots.NewService(dbSvc, store, hints, fake), cfg)

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks/:checkUid/screenshots")
	group.GET("", handler.List)
	group.POST("/capture", handler.Capture)

	return &fixture{db: dbSvc, store: store, router: router, clock: fake, notifier: hints, org: org, browser: browser}
}

func (f *fixture) do(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	return rec
}

func (f *fixture) capturePath(org, check string) string {
	return "/api/v1/orgs/" + org + "/checks/" + check + "/screenshots/capture"
}

func (f *fixture) listPath(org, check string) string {
	return "/api/v1/orgs/" + org + "/checks/" + check + "/screenshots"
}

func pngBytes(marker string) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte(marker)...)
}

// TestListScreenshots covers the listing endpoint's shapes: `{ "data": [...] }`
// newest first, a check addressed by slug, `limit`, the empty `[]`, and 404
// for a check of another org.
func TestListScreenshots(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)
	ctx := t.Context()

	for i := range 3 {
		_, err := f.store.PutCheckScreenshot(ctx, f.org.UID, f.browser.UID, pngBytes(strconv.Itoa(i)), models.JSONMap{
			attachments.DetailKeyCheckUID: f.browser.UID,
			attachments.DetailKeyTrigger:  attachments.TriggerCheckFailure,
			attachments.DetailKeyRegion:   "eu-west",
		})
		r.NoError(err)
		time.Sleep(2 * time.Millisecond)
	}

	rec := f.do(t, http.MethodGet, f.listPath(f.org.Slug, *f.browser.Slug))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Data []attachments.CheckScreenshot `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Len(body.Data, 3)
	r.False(body.Data[0].CapturedAt.Before(body.Data[1].CapturedAt), "newest first")
	r.Equal("eu-west", body.Data[0].Region)
	r.Contains(body.Data[0].DownloadURL, "/pub/files/")

	rec = f.do(t, http.MethodGet, f.listPath(f.org.Slug, f.browser.UID)+"?limit=2")
	r.Equal(http.StatusOK, rec.Code)
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Len(body.Data, 2)

	rec = f.do(t, http.MethodGet, f.listPath(f.org.Slug, f.browser.UID)+"?limit=zero")
	r.Equal(http.StatusUnprocessableEntity, rec.Code)

	httpCheck := models.NewCheck(f.org.UID, "api", "http")
	r.NoError(f.db.CreateCheck(ctx, httpCheck))

	rec = f.do(t, http.MethodGet, f.listPath(f.org.Slug, httpCheck.UID))
	r.Equal(http.StatusOK, rec.Code)
	r.JSONEq(`{"data":[]}`, rec.Body.String(), "a check with none answers an empty list, never null")

	other := models.NewOrganization("globex", "Globex")
	r.NoError(f.db.CreateOrganization(ctx, other))

	rec = f.do(t, http.MethodGet, f.listPath(other.Slug, f.browser.UID))
	r.Equal(http.StatusNotFound, rec.Code, "another org cannot read this check's captures")

	rec = f.do(t, http.MethodGet, f.listPath("nope", f.browser.UID))
	r.Equal(http.StatusNotFound, rec.Code)
}

// TestCaptureNowSchedulesTheRun is the happy path: 202 with the region, ONE job
// row flagged and made due, and the express hint published.
func TestCaptureNowSchedulesTheRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)
	ctx := t.Context()

	rec := f.do(t, http.MethodPost, f.capturePath(f.org.Slug, f.browser.UID))
	r.Equal(http.StatusAccepted, rec.Code, rec.Body.String())

	var resp checkscreenshots.CaptureResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("eu-west", resp.Region, "an unleased job, in region order")
	r.False(resp.RequestedAt.IsZero())

	jobs, err := f.db.ListCheckJobsByCheckUID(ctx, f.browser.UID)
	r.NoError(err)

	flagged := 0

	for _, job := range jobs {
		if job.CaptureRequestedAt == nil {
			continue
		}

		flagged++

		r.Equal("eu-west", *job.Region)
		r.False(job.ScheduledAt.After(f.clock.Now()), "the flagged job is due now")
	}

	r.Equal(1, flagged, "exactly one region runs the capture")
	r.Equal([]string{`check.created {"check_uid":"` + f.browser.UID + `"}`}, f.notifier.snapshot())
}

// TestCaptureNowRefusals covers the refusals that spend no budget: a check type
// that cannot capture (400), a disabled check with no job (409), a missing
// check (404).
func TestCaptureNowRefusals(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)
	ctx := t.Context()

	httpCheck := models.NewCheck(f.org.UID, "api", "http")
	r.NoError(f.db.CreateCheck(ctx, httpCheck))

	rec := f.do(t, http.MethodPost, f.capturePath(f.org.Slug, httpCheck.UID))
	r.Equal(http.StatusBadRequest, rec.Code)

	disabled := models.NewCheck(f.org.UID, "off", "js")
	disabled.Enabled = false
	r.NoError(f.db.CreateCheck(ctx, disabled))

	rec = f.do(t, http.MethodPost, f.capturePath(f.org.Slug, disabled.UID))
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())

	rec = f.do(t, http.MethodPost, f.capturePath(f.org.Slug, "no-such-check"))
	r.Equal(http.StatusNotFound, rec.Code)

	// None of the refusals spent the check's window: a real request still
	// goes through.
	rec = f.do(t, http.MethodPost, f.capturePath(f.org.Slug, f.browser.UID))
	r.Equal(http.StatusAccepted, rec.Code, rec.Body.String())
}

// TestCaptureNowIsRateLimitedPerCheck: one capture per check per minute, a 429
// with Retry-After inside the window, open again once it has passed.
func TestCaptureNowIsRateLimitedPerCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)

	r.Equal(http.StatusAccepted, f.do(t, http.MethodPost, f.capturePath(f.org.Slug, f.browser.UID)).Code)

	f.clock.Advance(20 * time.Second)

	rec := f.do(t, http.MethodPost, f.capturePath(f.org.Slug, f.browser.UID))
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Equal("40", rec.Header().Get("Retry-After"))

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("RATE_LIMITED", body["code"])
	r.Contains(body["detail"], "per check")

	f.clock.Advance(41 * time.Second)

	r.Equal(http.StatusAccepted, f.do(t, http.MethodPost, f.capturePath(f.org.Slug, f.browser.UID)).Code,
		"the window reopens")
}

// TestCaptureNowIsRateLimitedPerOrg: twenty captures per org per hour, whatever
// the checks, and a refusal by the org cap does not burn the check's minute.
func TestCaptureNowIsRateLimitedPerOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)
	ctx := t.Context()

	checks := make([]*models.Check, 0, checkscreenshots.CaptureOrgLimit+1)

	for i := range checkscreenshots.CaptureOrgLimit + 1 {
		check := models.NewCheck(f.org.UID, "page-"+strconv.Itoa(i), "browser")
		r.NoError(f.db.CreateCheck(ctx, check))
		checks = append(checks, check)
	}

	for _, check := range checks[:checkscreenshots.CaptureOrgLimit] {
		r.Equal(http.StatusAccepted, f.do(t, http.MethodPost, f.capturePath(f.org.Slug, check.UID)).Code)
	}

	last := checks[checkscreenshots.CaptureOrgLimit]

	rec := f.do(t, http.MethodPost, f.capturePath(f.org.Slug, last.UID))
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Contains(rec.Body.String(), "per organization")

	f.clock.Advance(checkscreenshots.CaptureOrgWindow)

	r.Equal(http.StatusAccepted, f.do(t, http.MethodPost, f.capturePath(f.org.Slug, last.UID)).Code,
		"the org refusal did not spend this check's own window")
}
