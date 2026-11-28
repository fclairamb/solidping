package notifications

import (
	"strings"
	"unicode/utf8"
)

// Values a user or a monitored endpoint controls (check names, comments,
// actor names, failure reasons) reach chat messages built by string
// concatenation into each platform's markup. Each escaper below makes such a
// value render literally, so it cannot add a link, a mention or formatting
// next to the markup SolidPing adds on purpose. Escape the value where it is
// inserted, never the URLs SolidPing builds itself, and never twice.

// escapeSlackText makes a value render literally in Slack mrkdwn. It is
// Slack's documented control-character escaping: `&`, `<` and `>` must be
// sent as entities, otherwise `<url|text>` links and `<!channel>` / `<@U…>`
// mentions can be formed by a value.
func escapeSlackText(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

// commonMarkInline is the set of characters that start or end inline markup
// in Mattermost and Discord markdown: emphasis (`*`, `_`, `~`), code, links
// and images (`[`, `]`: with both escaped no `[text](url)` can form, so `(`
// and `)` stay readable), tables (`|`), mentions (`@`) and autolinks / `<@id>`
// mentions (`<`, `>`). All are ASCII punctuation, so a backslash before them
// renders the character itself on both platforms.
const commonMarkInline = "\\`*_~[]|@<>"

// escapeChatMarkdown backslash-escapes the inline metacharacters everywhere
// and a heading marker (`#` followed by a space or another `#`) at the start
// of a line, leaving everything else alone so "#3" or "relapse #2" read as
// they always did.
func escapeChatMarkdown(text string) string {
	runes := []rune(text)

	var builder strings.Builder

	builder.Grow(len(text) + len(text)/8)

	lineStart := true

	for i, char := range runes {
		if strings.ContainsRune(commonMarkInline, char) || (char == '#' && lineStart && headingMarkerAt(runes, i)) {
			builder.WriteByte('\\')
		}

		builder.WriteRune(char)

		switch char {
		case '\n':
			lineStart = true
		case ' ', '\t':
			// Leading indentation still precedes a heading marker.
		default:
			lineStart = false
		}
	}

	return builder.String()
}

// headingMarkerAt reports whether the `#` at runes[i] opens a heading: it is
// followed by more `#`, then a space, a tab or the end of the line.
func headingMarkerAt(runes []rune, i int) bool {
	j := i
	for j < len(runes) && runes[j] == '#' {
		j++
	}

	return j == len(runes) || runes[j] == ' ' || runes[j] == '\t' || runes[j] == '\n'
}

// escapeMattermostMarkdown makes a value render literally in Mattermost
// markdown (message attachment fields and titles, Blocks text).
func escapeMattermostMarkdown(text string) string {
	return escapeChatMarkdown(text)
}

// EscapeMattermostMarkdown is escapeMattermostMarkdown for the inbound
// Mattermost action handler, which writes its own ephemeral text.
func EscapeMattermostMarkdown(text string) string {
	return escapeMattermostMarkdown(text)
}

// escapeDiscordMarkdown makes a value render literally in Discord markdown
// (embed descriptions, field values and titles). Discord also honors a
// backslash before `<`, so `<@id>` mentions and `<https://…>` cannot form.
func escapeDiscordMarkdown(text string) string {
	return escapeChatMarkdown(text)
}

// maxInsertedFailureReason caps a failure reason in a notification. The text
// can come from the monitored endpoint (a TLS error quotes the certificate
// names), so a long target-controlled string must not fill the card. The
// stored incident details keep the full text.
const maxInsertedFailureReason = 500

// truncateRunes cuts text to max runes, ending with an ellipsis when cut.
func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}

	runes := []rune(text)

	return string(runes[:limit-1]) + "…"
}
