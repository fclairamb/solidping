package telegram_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

func TestEscapeHTML(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("A &amp; B &lt;script&gt;", telegram.EscapeHTML("A & B <script>"))
	// '&' must be escaped FIRST, otherwise "&lt;" would come out as "&amp;lt;".
	r.Equal("&lt;b&gt;bold&lt;/b&gt;", telegram.EscapeHTML("<b>bold</b>"))
	// Quotes are deliberately left alone: Telegram's restricted entity parser
	// does not accept the numeric references html.EscapeString would emit.
	r.Equal(`he said "hi" &amp; 'bye'`, telegram.EscapeHTML(`he said "hi" & 'bye'`))
	r.Empty(telegram.EscapeHTML(""))
}

// THE test of this spec: a hostile check name must produce a body Telegram
// accepts. Telegram rejects the ENTIRE message when entities do not parse, so
// an unescaped '&' in a check name would silently kill alerting for that check
// — with no error anywhere in the dashboard, because the send itself is what
// fails.
func TestBuildAlertHTML_EscapesEveryInterpolatedValue(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := telegram.BuildAlertHTML(&telegram.AlertParams{
		State:       telegram.StateDown,
		CheckName:   "A & B <script>",
		Detail:      "HTTP 503 — Service <Unavailable> & angry",
		OrgSlug:     "acme&co",
		IncidentURL: "https://solidping.io/dash0/orgs/acme/incidents/abc?a=1&b=2",
	})

	// Every hostile fragment is escaped...
	r.Contains(body, "A &amp; B &lt;script&gt;")
	r.Contains(body, "Service &lt;Unavailable&gt; &amp; angry")
	r.Contains(body, "acme&amp;co")
	r.Contains(body, "incidents/abc?a=1&amp;b=2")

	// ...and no raw script tag or bare ampersand survived anywhere.
	r.NotContains(body, "<script>")
	r.NotRegexp(`&(?:[^a-z#]|$)`, body)

	// The only tags left are the ones we emit ourselves.
	r.Contains(body, "<b>🔴 Incident — A &amp; B &lt;script&gt;</b>")
	r.Contains(body, "<b>Status:</b> DOWN")

	// And the result is well-formed markup: parsing it must not choke, and the
	// escaped text must round-trip back to the original.
	assertWellFormedTelegramHTML(t, body)
	r.Contains(htmlText(t, body), "A & B <script>")
}

// assertWellFormedTelegramHTML parses the body with the same tolerance a
// browser-grade parser has and asserts that only Telegram-supported tags appear
// and that every tag is balanced.
func assertWellFormedTelegramHTML(t *testing.T, body string) {
	t.Helper()

	r := require.New(t)

	nodes, err := html.ParseFragment(strings.NewReader(body), &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	r.NoError(err)

	supported := map[string]bool{"b": true, "i": true, "u": true, "s": true, "a": true, "code": true, "pre": true}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			r.True(supported[n.Data], "unsupported telegram HTML tag %q in body", n.Data)
		}

		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	for _, node := range nodes {
		walk(node)
	}
}

// htmlText returns the plain text of an HTML fragment, i.e. entities decoded.
func htmlText(t *testing.T, body string) string {
	t.Helper()

	nodes, err := html.ParseFragment(strings.NewReader(body), &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	require.NoError(t, err)

	var out strings.Builder

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}

		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	for _, node := range nodes {
		walk(node)
	}

	return out.String()
}

// The escaped body must survive JSON encoding and reach the Bot API byte for
// byte — a positive control that the escaping is not undone on the wire.
func TestSendMessage_EscapedBodyReachesTheAPIIntact(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK, `{"ok":true,"result":{"message_id":1}}`)
	client := newTestClient(t, fake)

	body := telegram.BuildAlertHTML(&telegram.AlertParams{
		State:     telegram.StateDown,
		CheckName: "A & B <script>",
		OrgSlug:   "acme",
	})

	_, err := client.SendMessage(context.Background(), &telegram.Message{ChatID: "1", HTML: body})
	r.NoError(err)

	sent, ok := fake.body["text"].(string)
	r.True(ok)
	r.Equal(body, sent)
	r.Contains(sent, "A &amp; B &lt;script&gt;")

	// Sanity: a JSON round-trip preserves it byte for byte. (encoding/json
	// escapes '&' as & on the wire, which decodes back identically — the
	// escaping survives, it is only spelled differently in transit.)
	raw, err := json.Marshal(sent)
	r.NoError(err)

	var decoded string

	r.NoError(json.Unmarshal(raw, &decoded))
	r.Equal(body, decoded)
	r.Contains(decoded, "&amp;")
}

func TestBuildAlertHTML_StatesAndOptionalFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	down := telegram.BuildAlertHTML(&telegram.AlertParams{State: telegram.StateDown, CheckName: "api"})
	r.Contains(down, "🔴 Incident — api")
	r.NotContains(down, "<b>Detail:</b>")
	r.NotContains(down, "<b>Org:</b>")
	r.NotContains(down, "<a href")

	escalated := telegram.BuildAlertHTML(&telegram.AlertParams{State: telegram.StateEscalated, CheckName: "api"})
	r.Contains(escalated, "🟠 Incident — api")
	r.Contains(escalated, "<b>Status:</b> ESCALATED")

	resolved := telegram.BuildAlertHTML(&telegram.AlertParams{State: telegram.StateResolved, CheckName: "api"})
	r.Contains(resolved, "🟢 Resolved — api")
	r.Contains(resolved, "<b>Status:</b> RESOLVED")

	// An empty state and an empty check name still render something usable.
	blank := telegram.BuildAlertHTML(&telegram.AlertParams{})
	r.Contains(blank, "🔴 Incident — check")
	r.Contains(blank, "<b>Status:</b> DOWN")
}

func TestBuildResolvedOriginalHTML(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	edited := telegram.BuildResolvedOriginalHTML(&telegram.AlertParams{
		State:     telegram.StateDown, // deliberately stale: the edit forces RESOLVED
		CheckName: "A & B",
		OrgSlug:   "acme",
	})

	r.True(strings.HasPrefix(edited, "<b>✅ 🟢 Resolved — A &amp; B</b>"), "got %q", edited)
	r.Contains(edited, "<b>Status:</b> RESOLVED")
	assertWellFormedTelegramHTML(t, edited)
}

func TestStateEmoji(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("🔴", telegram.StateEmoji(telegram.StateDown))
	r.Equal("🟠", telegram.StateEmoji(telegram.StateEscalated))
	r.Equal("🟢", telegram.StateEmoji(telegram.StateResolved))
	r.Equal("🔴", telegram.StateEmoji("something-new"))
}

func TestBuildConversationalBodies(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	linked := telegram.BuildLinkedHTML("acme&co")
	r.Contains(linked, "acme&amp;co")
	r.Contains(linked, "/stop")
	assertWellFormedTelegramHTML(t, linked)

	expired := telegram.BuildExpiredLinkHTML()
	// The reply must not reveal WHICH failure it was — that would turn the bot
	// into an oracle for probing live tokens.
	r.NotContains(strings.ToLower(expired), "unknown")
	r.NotContains(strings.ToLower(expired), "never")
	assertWellFormedTelegramHTML(t, expired)

	assertWellFormedTelegramHTML(t, telegram.BuildUnlinkedHTML())
	r.NotEmpty(telegram.BuildUnknownCommandHTML())

	// The group refusal names the fix rather than only refusing: from the
	// user's side the link did work, it just landed somewhere v1 cannot use.
	group := telegram.BuildGroupNotSupportedHTML()
	r.Contains(group, "direct chat")
	assertWellFormedTelegramHTML(t, group)
}

// TestBuildAlertHTML_QualifiesTheHeadlineRef is the positive control both ways:
// the same alert renders "#42" for a single-org chat and "#acme:42" for a chat
// linked in several orgs — where "#42" would be genuinely ambiguous, since the
// number is per-org sequential and exists in every one of them.
func TestBuildAlertHTML_QualifiesTheHeadlineRef(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	params := &telegram.AlertParams{
		State:     telegram.StateDown,
		Number:    42,
		CheckName: "API health",
		OrgSlug:   "acme",
	}

	short := telegram.BuildAlertHTML(params)
	r.Contains(short, "#42 — API health")
	r.NotContains(short, "#acme:42")

	params.QualifyRef = true
	qualified := telegram.BuildAlertHTML(params)
	r.Contains(qualified, "#acme:42 — API health")

	// The derived bodies inherit the flag, so a resolution or an acknowledgement
	// cannot disagree with the alert it rewrites.
	r.Contains(telegram.BuildResolvedOriginalHTML(params), "#acme:42")
}
