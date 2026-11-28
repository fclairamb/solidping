package notifications

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEscapeSlackText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a &amp; b &lt;x|y&gt;", escapeSlackText("a & b <x|y>"))
	require.Equal(t, "plain *bold* name", escapeSlackText("plain *bold* name"))
}

func TestEscapeChatMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"API health", "API health"},
		{"relapse #2 (eu-west)", "relapse #2 (eu-west)"},
		{"#3", "#3"},
		{"# Big", `\# Big`},
		{"line\n## sub", "line\n\\## sub"},
		{"[x](https://evil.example)", `\[x\](https://evil.example)`},
		{"*a* _b_ ~c~ `d` |e|", "\\*a\\* \\_b\\_ \\~c\\~ \\`d\\` \\|e\\|"},
		{"@channel <@123> > quote", `\@channel \<\@123\> \> quote`},
		{`back\slash`, `back\\slash`},
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, escapeChatMarkdown(tt.in), "input %q", tt.in)
		require.Equal(t, tt.want, escapeMattermostMarkdown(tt.in))
		require.Equal(t, tt.want, escapeDiscordMarkdown(tt.in))
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	require.Equal(t, "short", truncateRunes("short", 10))
	require.Equal(t, "éééé…", truncateRunes(strings.Repeat("é", 20), 5))
}

// TestFailureReasonIsCapped pins the length cap: a long target-controlled
// failure reason cannot fill a card, and the stored details keep it all.
func TestFailureReasonIsCapped(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 2000)
	payload := regionsTestPayload(eventTypeIncidentCreated, false)
	payload.Incident.Details["failure_reason"] = long

	got := getFailureReason(payload.Incident)
	require.Len(t, []rune(got), maxInsertedFailureReason)
	require.True(t, strings.HasSuffix(got, "…"))
	require.Equal(t, long, payload.Incident.Details["failure_reason"])
	require.Equal(t, "connection refused",
		getFailureReason(regionsTestPayload(eventTypeIncidentCreated, false).Incident))
}

// TestGoogleChatEscapesDecoratedText: decoratedText renders an HTML subset,
// so a check name or comment cannot add a link.
func TestGoogleChatEscapesDecoratedText(t *testing.T) {
	t.Parallel()

	payload := regionsTestPayload(eventTypeIncidentComment, false)
	evil := `API <a href="https://evil.example">login</a>`
	payload.Check.Name = &evil
	payload.Comment = &CommentInfo{Text: "<b>urgent</b>", AuthorName: "bob"}

	widgets := (&GoogleChatSender{}).buildWidgets(payload, evil)

	r := require.New(t)
	r.Equal(`API &lt;a href=&#34;https://evil.example&#34;&gt;login&lt;/a&gt; (http)`, widgets[0].DecoratedText.Text)
	r.Equal("&lt;b&gt;urgent&lt;/b&gt;", widgets[1].DecoratedText.Text)

	plain := regionsTestPayload(eventTypeIncidentCreated, false)
	r.Equal("API health (http)", (&GoogleChatSender{}).buildWidgets(plain, "API health")[0].DecoratedText.Text)
}
