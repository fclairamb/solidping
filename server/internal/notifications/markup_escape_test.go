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

func TestEscapeZulipMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"API health", "API health"},
		{"relapse #2 (eu-west)", "relapse #2 (eu-west)"},
		{"# Big", `\# Big`},
		{"[x](https://evil.example)", "\\[x\\]\u200b(https://evil.example)"},
		{"@**all** #**ops**", `@\*\*all\*\* #\*\*ops\*\*`},
		{"_a_ `b` back\\slash", "\\_a\\_ \\`b\\` back\\\\slash"},
		{"a@b.c ~x~ |y| <z>", "a@b.c ~x~ |y| <z>"},
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, escapeZulipMarkdown(tt.in), "input %q", tt.in)
	}
}

func TestEscapeMSTeamsText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "[x]\u200b(https://evil.example)", escapeMSTeamsText("[x](https://evil.example)"))
	require.Equal(t, "API health (http) [DOWN] *a*", escapeMSTeamsText("API health (http) [DOWN] *a*"))
}

func TestSplitEmojiPrefix(t *testing.T) {
	t.Parallel()

	emoji, rest := splitEmojiPrefix(":red_circle: [DOWN] API")
	require.Equal(t, ":red_circle: ", emoji)
	require.Equal(t, "[DOWN] API", rest)

	emoji, rest = splitEmojiPrefix("[UPDATE] API")
	require.Empty(t, emoji)
	require.Equal(t, "[UPDATE] API", rest)

	emoji, rest = splitEmojiPrefix(":not an emoji: x")
	require.Empty(t, emoji)
	require.Equal(t, ":not an emoji: x", rest)
}

// TestZulipEscapesUserValues: a check name, comment or actor cannot add a
// link or a mention to a Zulip message; SolidPing's emoji stays an emoji.
func TestZulipEscapesUserValues(t *testing.T) {
	t.Parallel()

	evil := "API [login](https://evil.example) @**all**"
	payload := regionsTestPayload(eventTypeIncidentCreated, false)
	payload.Check.Name = &evil
	payload.Incident.Details["failure_reason"] = "[x](https://evil.example)"

	content := (&ZulipSender{}).buildContent(payload)

	r := require.New(t)
	r.NotContains(content, "](https://evil.example)")
	r.NotContains(content, "@**all**")
	r.Contains(content, "**:red_circle: \\[DOWN\\] API \\[login\\]\u200b(https://evil.example) @\\*\\*all\\*\\***")
	r.Contains(content, "\\[x\\]\u200b(https://evil.example)")

	comment := regionsTestPayload(eventTypeIncidentComment, false)
	comment.Comment = &CommentInfo{Text: "see [here](https://evil.example)", AuthorName: "[bob](https://evil.example)"}
	r.NotContains((&ZulipSender{}).buildContent(comment), "](https://evil.example)")
}

// TestMSTeamsEscapesUserValues: the title TextBlock and the FactSet values
// cannot carry a markdown link built by a user value.
func TestMSTeamsEscapesUserValues(t *testing.T) {
	t.Parallel()

	evil := "API [login](https://evil.example)"
	payload := regionsTestPayload(eventTypeIncidentComment, false)
	payload.Check.Name = &evil
	payload.Comment = &CommentInfo{Text: "see [here](https://evil.example)", AuthorName: "[bob](https://evil.example)"}

	msg := (&MSTeamsSender{}).buildMessage(payload)
	body := msg.Attachments[0].Content.Body

	r := require.New(t)
	r.NotContains(body[0].Text, "](")
	r.Contains(body[0].Text, "[login]\u200b(https://evil.example)")

	for _, fact := range body[1].Facts {
		r.NotContains(fact.Value, "](", "fact %q", fact.Title)
	}

	failure := regionsTestPayload(eventTypeIncidentCreated, false)
	failure.Incident.Details["failure_reason"] = "[x](https://evil.example)"

	for _, fact := range (&MSTeamsSender{}).buildMessage(failure).Attachments[0].Content.Body[1].Facts {
		r.NotContains(fact.Value, "](", "fact %q", fact.Title)
	}
}

// TestMattermostTitleFallbackEscapes: without a check name suffix to link,
// the title is still escaped, past SolidPing's emoji shortcode.
func TestMattermostTitleFallbackEscapes(t *testing.T) {
	t.Parallel()

	payload := regionsTestPayload(eventTypeIncidentCreated, false)

	got := mattermostTitleWithCheckLink(payload, ":red_circle: [DOWN] [x](https://evil.example) by @bob", "other")
	require.Equal(t, "**:red_circle: \\[DOWN\\] \\[x\\](https://evil.example) by \\@bob**", got)
	require.Equal(t, "**:red_circle: \\[DOWN\\] API**",
		mattermostTitleWithCheckLink(payload, ":red_circle: [DOWN] API", ""))
}
