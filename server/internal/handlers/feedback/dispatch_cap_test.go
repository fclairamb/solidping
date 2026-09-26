package feedback

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// countingGitHubPoster counts CreateIssue calls; it never talks to a real
// GitHub API. Used to prove the per-hour dispatch cap actually stops calls
// from reaching the poster, not just that a counter increments somewhere.
type countingGitHubPoster struct {
	calls int
}

func (p *countingGitHubPoster) CreateIssue(
	_ context.Context, _, _, _, _ string, _ []string,
) error {
	p.calls++

	return nil
}

// TestDispatchGitHubIssue_CapsAt20PerHour is the spec 2026-09-25-26 Tests
// bullet: "21st dispatch within an hour is skipped with a WARN log (fake
// GitHub client)". dispatchGitHubIssue is called directly (rather than
// through SubmitReport, which fires it off in a goroutine) so the 21 calls
// are synchronous and deterministic.
func TestDispatchGitHubIssue_CapsAt20PerHour(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	poster := &countingGitHubPoster{}

	var logBuf bytes.Buffer

	svc := &Service{
		cfg: &config.Config{
			App: config.AppConfig{
				GitHub: config.AppGitHubConfig{
					IssuesToken: "test-token",
					Repo:        "acme/acme-repo",
				},
			},
		},
		github: poster,
		logger: slog.New(slog.NewTextHandler(&logBuf, nil)),
		clock:  time.Now,
	}

	req := &SubmitReportRequest{URL: "https://example.com/page"}

	for i := range maxIssueDispatchesPerHour + 1 {
		// fileURI left empty: signedScreenshotURL short-circuits on it, so
		// this never needs a JWT secret to run.
		svc.dispatchGitHubIssue(context.Background(), req, "acme", fmt.Sprintf("file-%d", i), "", "")
	}

	r.Equal(maxIssueDispatchesPerHour, poster.calls,
		"the 21st dispatch within the hour must not reach GitHub")
	r.Contains(logBuf.String(), "bug_report: github issue dispatch capped",
		"the capped dispatch must log a WARN naming the cap")

	// The window resets after an hour: the next dispatch after it must go
	// through again rather than staying capped forever.
	svc.clock = func() time.Time { return time.Now().Add(time.Hour + time.Minute) }
	svc.dispatchGitHubIssue(context.Background(), req, "acme", "file-after-window", "", "")
	r.Equal(maxIssueDispatchesPerHour+1, poster.calls,
		"a dispatch in a fresh hourly window must not stay capped")
}

// TestAllowIssueDispatch_WindowResets is a narrower unit test of the counter
// itself, independent of the GitHub plumbing above.
func TestAllowIssueDispatch_WindowResets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	svc := &Service{clock: func() time.Time { return now }}

	for i := range maxIssueDispatchesPerHour {
		r.True(svc.allowIssueDispatch(), "dispatch %d of the hourly allowance must be admitted", i+1)
	}

	r.False(svc.allowIssueDispatch(), "the 21st dispatch within the hour must be refused")

	now = now.Add(time.Hour)
	r.True(svc.allowIssueDispatch(), "a dispatch exactly one hour after the window start must be admitted")
}
