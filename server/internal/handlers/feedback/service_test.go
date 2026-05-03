package feedback

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
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
		gotAPIVer = req.Header.Get("X-GitHub-Api-Version")

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
