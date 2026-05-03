package feedback_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/feedback"
)

func TestBuildIssueTitle(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("Bug report: hello world", feedback.BuildIssueTitle(&feedback.IssueInput{Comment: "hello world"}))

	long := strings.Repeat("a", 80)
	got := feedback.BuildIssueTitle(&feedback.IssueInput{Comment: long})
	r.True(strings.HasPrefix(got, "Bug report: "))
	r.Less(len(got), len("Bug report: ")+85)
	r.True(strings.HasSuffix(got, "…"))

	// Falls back to URL when no comment
	r.Equal(
		"Bug report: https://x.example/path",
		feedback.BuildIssueTitle(&feedback.IssueInput{URL: "https://x.example/path"}),
	)

	// Defaults when nothing
	r.Equal("Bug report: (no description)", feedback.BuildIssueTitle(&feedback.IssueInput{}))
}

func TestBuildIssueBody_Image(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := feedback.BuildIssueBody(&feedback.IssueInput{
		URL:           "https://x.example",
		Comment:       "stuff broken",
		OrgSlug:       "default",
		UserEmail:     "alice@example.com",
		ServerVersion: "1.2.3",
		FileUID:       "f-123",
		SignedURL:     "https://x.example/pub/files/f-123?exp=1&sig=abcd",
		SignedURLExp:  time.Unix(1, 0),
		MimeType:      "image/png",
		ReportedAt:    time.Unix(0, 0),
		Context: feedback.ContextPayload{
			UserAgent:    "Mozilla/5.0",
			Viewport:     "1920x1080",
			RecentErrors: []string{"oops"},
		},
	})

	r.Contains(body, "## Report")
	r.Contains(body, "stuff broken")
	r.Contains(body, "## Page")
	r.Contains(body, "https://x.example")
	r.Contains(body, "## Screenshot")
	r.Contains(body, "![Screenshot](https://x.example/pub/files/f-123?exp=1&sig=abcd)")
	r.Contains(body, "## Console Errors")
	r.Contains(body, "alice@example.com")
}

func TestBuildIssueBody_Video(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := feedback.BuildIssueBody(&feedback.IssueInput{
		URL:        "https://x.example",
		FileUID:    "f-123",
		SignedURL:  "https://x.example/pub/files/f-123?exp=1&sig=abcd",
		MimeType:   "video/webm",
		ReportedAt: time.Unix(0, 0),
	})

	r.Contains(body, "## Screen recording")
	r.NotContains(body, "## Screenshot")
	r.Contains(body, "[Download](https://x.example/pub/files/f-123?exp=1&sig=abcd)")
}

func TestBuildIssueBody_NoMedia(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := feedback.BuildIssueBody(&feedback.IssueInput{
		URL:        "https://x.example",
		Comment:    "I lost my data",
		ReportedAt: time.Unix(0, 0),
	})

	r.NotContains(body, "## Screenshot")
	r.NotContains(body, "## Screen recording")
	r.Contains(body, "I lost my data")
}

func TestBuildIssueBody_RecentErrorsCannotBreakFence(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := feedback.BuildIssueBody(&feedback.IssueInput{
		URL: "https://x.example",
		Context: feedback.ContextPayload{
			RecentErrors: []string{"hostile ``` payload ```"},
		},
		ReportedAt: time.Unix(0, 0),
	})

	r.NotContains(body, "hostile ``` payload ```")
	r.Contains(body, "ʼʼʼ")
}
