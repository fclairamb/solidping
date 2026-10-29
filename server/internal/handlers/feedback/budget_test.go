package feedback

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

// newBudgetSQLite returns a fresh in-memory SQLite database and n orgs.
func newBudgetSQLite(t *testing.T, slugs ...string) (db.Service, []*models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	orgs := make([]*models.Organization, 0, len(slugs))

	for _, slug := range slugs {
		org := models.NewOrganization(slug, "Acme")
		r.NoError(dbSvc.CreateOrganization(ctx, org))
		orgs = append(orgs, org)
	}

	return dbSvc, orgs
}

// newBudgetService builds a feedback service with a fake GitHub poster, the
// given per-org quota and anonymous screenshot budget, and a counter key of
// its own (tests sharing one Postgres must not share the budget).
func newBudgetService(
	t *testing.T, dbSvc db.Service, quota int64, anonPerHour int,
) (*Service, *countingGitHubPoster) {
	t.Helper()

	cfg := newFeedbackQuotaConfig(t, quota)
	cfg.App.FeedbackAnonScreenshotsPerHour = anonPerHour

	poster := &countingGitHubPoster{}
	svc := NewService(dbSvc, files.NewService(dbSvc, cfg), cfg, poster)
	svc.anonCounterKey = anonScreenshotCounterKey + ":" + t.Name()

	return svc, poster
}

// newMember creates a user and returns its UID, for authenticated reports.
func newMember(t *testing.T, dbSvc db.Service) string {
	t.Helper()

	user := models.NewUser("member@acme.example")
	require.NoError(t, dbSvc.CreateUser(t.Context(), user))

	return user.UID
}

// screenshotReport is a report with a one-byte screenshot.
func screenshotReport(orgSlug, userUID string) *SubmitReportRequest {
	return &SubmitReportRequest{
		URL:            "https://example.com/page",
		OrgSlug:        orgSlug,
		UserUID:        userUID,
		Screenshot:     strings.NewReader("x"),
		ScreenshotSize: 1,
		ScreenshotName: "s.png",
		ScreenshotMIME: "image/png",
	}
}

// reportBytes sums the live `reports` bytes of the orgs; each test screenshot
// is one byte, so this is also the file count.
func reportBytes(ctx context.Context, t *testing.T, dbSvc db.Service, orgs ...*models.Organization) int64 {
	t.Helper()

	var total int64

	for _, org := range orgs {
		used, err := dbSvc.SumFileSizeByGroup(ctx, org.UID, string(filestorage.GroupTypeReports))
		require.NoError(t, err)

		total += used
	}

	return total
}

// assertAnonBudgetIsInstanceWide is the spec 2026-10-29-01 regression: the
// anonymous screenshot budget is spent once for the whole instance, however
// the caller spreads its reports over orgs.
func assertAnonBudgetIsInstanceWide(t *testing.T, dbSvc db.Service, orgA, orgB *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	svc, _ := newBudgetService(t, dbSvc, 1024, 3)

	for i := range 6 {
		org := orgA
		if i%2 == 1 {
			org = orgB
		}

		_, err := svc.SubmitReport(ctx, screenshotReport(org.Slug, ""))
		if i < 3 {
			r.NoError(err, "report %d is within the budget", i+1)
		} else {
			r.ErrorIs(err, ErrReportRateLimited, "report %d is past the instance budget", i+1)
		}
	}

	r.Equal(int64(3), reportBytes(ctx, t, dbSvc, orgA, orgB),
		"exactly 3 screenshots are stored across both orgs")
}

func TestSubmitReport_AnonBudgetIsInstanceWide_SQLite(t *testing.T) {
	t.Parallel()

	dbSvc, orgs := newBudgetSQLite(t, "acme-a", "acme-b")
	assertAnonBudgetIsInstanceWide(t, dbSvc, orgs[0], orgs[1])
}

// assertParallelQuotaHoldsOneFile: N parallel anonymous reports against an
// org whose quota has room for one file store at most one file.
func assertParallelQuotaHoldsOneFile(t *testing.T, dbSvc db.Service, org *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	const parallel = 10

	svc, _ := newBudgetService(t, dbSvc, 1, 0)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted int
	)

	for range parallel {
		wg.Go(func() {
			_, err := svc.SubmitReport(ctx, screenshotReport(org.Slug, ""))
			if err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()

				return
			}

			if !strings.Contains(err.Error(), ErrStorageQuotaExceeded.Error()) {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}

	wg.Wait()

	r.LessOrEqual(accepted, 1)
	r.LessOrEqual(reportBytes(ctx, t, dbSvc, org), int64(1), "at most one file fits the quota")
}

func TestSubmitReport_ParallelQuota_SQLite(t *testing.T) {
	t.Parallel()

	dbSvc, orgs := newBudgetSQLite(t, "acme-par")
	assertParallelQuotaHoldsOneFile(t, dbSvc, orgs[0])
}

// TestSubmitReport_DispatchCapStoresNoAnonScreenshot: with the issue dispatch
// budget spent, an anonymous screenshot would never be filed, so it is not
// stored. A member's screenshot, and a text-only anonymous report, are still
// accepted.
func TestSubmitReport_DispatchCapStoresNoAnonScreenshot(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-cap")
	svc, poster := newBudgetService(t, dbSvc, 1024, 20)

	svc.dispatchWindowStart = time.Now()
	svc.dispatchCount = maxIssueDispatchesPerHour
	member := newMember(t, dbSvc)

	_, err := svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, ""))
	r.ErrorIs(err, ErrReportRateLimited)
	r.Zero(reportBytes(ctx, t, dbSvc, orgs[0]), "no file is stored for a report that cannot be filed")

	_, err = svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, member))
	r.NoError(err, "a member's report keeps today's behaviour")
	r.Equal(int64(1), reportBytes(ctx, t, dbSvc, orgs[0]))

	_, err = svc.SubmitReport(ctx, &SubmitReportRequest{URL: "https://example.com/page", OrgSlug: orgs[0].Slug})
	r.NoError(err, "a text-only report is accepted")
	r.Zero(poster.count(), "no issue is dispatched past the cap")
}

// TestSubmitReport_MemberAndTextOnlyPastAnonBudget: the anonymous budget does
// not apply to authenticated members, nor to reports without a screenshot.
func TestSubmitReport_MemberAndTextOnlyPastAnonBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-member")
	svc, _ := newBudgetService(t, dbSvc, 1024, 1)
	member := newMember(t, dbSvc)

	_, err := svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, ""))
	r.NoError(err)

	_, err = svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, ""))
	r.ErrorIs(err, ErrReportRateLimited, "the anonymous budget is spent")

	_, err = svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, member))
	r.NoError(err, "an authenticated member's screenshot is still stored")

	_, err = svc.SubmitReport(ctx, &SubmitReportRequest{URL: "https://example.com/page", OrgSlug: orgs[0].Slug})
	r.NoError(err, "a report without a screenshot is accepted")

	r.Equal(int64(2), reportBytes(ctx, t, dbSvc, orgs[0]))
}

// multipartReport builds the multipart body of an anonymous report with a
// screenshot, as the dash0 dialog sends it.
func multipartReport(t *testing.T, orgSlug string) (*bytes.Buffer, string) {
	t.Helper()

	var body bytes.Buffer

	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("url", "https://example.com/page"))
	require.NoError(t, writer.WriteField("org", orgSlug))

	part, err := writer.CreateFormFile("screenshot", "s.png")
	require.NoError(t, err)

	_, err = part.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	return &body, writer.FormDataContentType()
}

// TestHandlerSubmitReport_AnonBudget: the first anonymous report with a
// screenshot answers 201, stores its file and dispatches one issue; past the
// budget the endpoint answers 429 RATE_LIMITED.
func TestHandlerSubmitReport_AnonBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-http")
	svc, poster := newBudgetService(t, dbSvc, 1024, 1)
	handler := NewHandler(svc, nil, svc.cfg)

	post := func() *httptest.ResponseRecorder {
		body, contentType := multipartReport(t, orgs[0].Slug)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/mgmt/report", body)
		req.Header.Set("Content-Type", contentType)

		rec := httptest.NewRecorder()
		r.NoError(handler.SubmitReport(rec, req))

		return rec
	}

	rec := post()
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
	r.Equal(int64(1), reportBytes(ctx, t, dbSvc, orgs[0]))
	r.Eventually(func() bool { return poster.count() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the report dispatches one issue")

	rec = post()
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Contains(rec.Body.String(), "RATE_LIMITED")
	r.Equal(int64(1), reportBytes(ctx, t, dbSvc, orgs[0]), "the refused report stores nothing")
}
