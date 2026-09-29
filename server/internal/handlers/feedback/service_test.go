package feedback

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func TestHTTPPoster_RequestShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var (
		gotPath   string
		gotAuth   string
		gotAccept string
		gotAPIVer string
		gotBody   map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotAuth = req.Header.Get("Authorization")
		gotAccept = req.Header.Get("Accept")
		gotAPIVer = req.Header.Get("X-Github-Api-Version")

		buf, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(buf, &gotBody)

		w.WriteHeader(http.StatusCreated)
	}))

	defer srv.Close()

	poster := &httpGitHubPoster{
		client:             srv.Client(),
		overrideAPIBaseURL: srv.URL,
	}
	err := poster.CreateIssue(
		context.Background(),
		"fclairamb/solidping",
		"abc123",
		"Bug report: x",
		"body",
		[]string{"in-app-report"},
	)
	r.NoError(err)
	r.Equal("/repos/fclairamb/solidping/issues", gotPath)
	r.Equal("Bearer abc123", gotAuth)
	r.Equal("application/vnd.github+json", gotAccept)
	r.Equal("2022-11-28", gotAPIVer)
	r.Equal("Bug report: x", gotBody["title"])
	r.Equal("body", gotBody["body"])
}

func TestHTTPPoster_FailureStatus(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))

	defer srv.Close()

	poster := &httpGitHubPoster{
		client:             srv.Client(),
		overrideAPIBaseURL: srv.URL,
	}
	err := poster.CreateIssue(
		context.Background(),
		"fclairamb/solidping",
		"abc123",
		"t",
		"b",
		nil,
	)
	r.Error(err)
	r.Contains(err.Error(), "401")
}

// TestSubmitReport_DisabledStoresNothing: with the default config (no token,
// no repo) the report is refused before any org lookup, file write or GitHub
// call. A nil db/files would panic if the service got any further.
func TestSubmitReport_DisabledStoresNothing(t *testing.T) {
	t.Parallel()

	poster := &countingGitHubPoster{}
	svc := NewService(nil, nil, &config.Config{}, poster)

	_, err := svc.SubmitReport(t.Context(), &SubmitReportRequest{
		URL:            "https://example.com/page",
		OrgSlug:        "default",
		Screenshot:     strings.NewReader("x"),
		ScreenshotSize: 1,
	})
	require.ErrorIs(t, err, ErrReportDisabled)
	require.Zero(t, poster.calls)
}

// TestHandlerSubmitReport_DisabledIs404 pins the endpoint contract: 404, not
// 201, when the feature is off.
func TestHandlerSubmitReport_DisabledIs404(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	handler := NewHandler(NewService(nil, nil, cfg, &countingGitHubPoster{}), nil, cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/mgmt/report", strings.NewReader(""))

	require.NoError(t, handler.SubmitReport(rec, req))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
