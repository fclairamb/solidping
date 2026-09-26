package app

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// reportRateLimitBurst mirrors reportRateLimitConfig's Burst in server.go —
// kept here as a named constant (rather than a magic 10) so this test breaks
// loudly, not silently, if that constant ever changes.
const reportRateLimitBurst = 10

// newReportRequest builds a minimal, valid multipart POST /api/mgmt/report
// body: just the required "url" field, no screenshot. No Authorization
// header and no "org" field — the anonymous case spec 2026-09-25-26 exists
// for.
func newReportRequest(t *testing.T, baseURL string) *http.Request {
	t.Helper()

	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)
	r := require.New(t)
	r.NoError(mw.WriteField("url", "https://example.com/page"))
	r.NoError(mw.Close())

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/mgmt/report", &buf)
	r.NoError(err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return req
}

// TestFeedbackReport_RateLimitedAnonymously boots a real server (real
// NewServer + real SetupRoutes) and drives POST /api/mgmt/report the way an
// anonymous caller would: no Authorization header, no org. It proves the fix
// for spec 2026-09-25-26 — before it, this route sat outside limitedPrefix
// and had no rate limit of any kind — by exhausting the dedicated
// reportRateLimitConfig burst (5 req/min, burst 10) from one IP and checking
// the next request is 429 with the standard error shape, using a single
// http.Client so every request shares the same local address the rate
// limiter buckets on.
func TestFeedbackReport_RateLimitedAnonymously(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "feedback-ratelimit-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	// One seeded org so an admitted request resolves cleanly (201) instead
	// of racing against the "no org configured" fallback path — the rate
	// limiter sits ahead of that logic either way, but a clean 201 makes the
	// "admitted vs blocked" assertion unambiguous.
	org := models.NewOrganization("fb-rl-org", "Feedback RateLimit Org")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	client := ts.Client()

	var lastResp *http.Response
	for i := range reportRateLimitBurst + 1 {
		resp, doErr := client.Do(newReportRequest(t, ts.URL))
		r.NoError(doErr)

		if i < reportRateLimitBurst {
			r.Equal(http.StatusCreated, resp.StatusCode, "request %d (within burst) must be admitted", i+1)
		}

		lastResp = resp
	}

	r.Equal(http.StatusTooManyRequests, lastResp.StatusCode,
		"the request beyond the burst must be rate-limited")

	var body struct {
		Title string `json:"title"`
		Code  string `json:"code"`
	}
	r.NoError(json.NewDecoder(lastResp.Body).Decode(&body))
	_ = lastResp.Body.Close()
	r.Equal("RATE_LIMITED", body.Code, "the standard error shape must be used")
	r.NotEmpty(body.Title)
}
