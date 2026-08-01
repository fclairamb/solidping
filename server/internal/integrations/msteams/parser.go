package msteams

import (
	"html"
	"regexp"
	"strings"
)

// ParsedCommand represents a parsed Teams bot command. The grammar is
// deliberately identical to the Slack one (internal/integrations/slack:
// ParsedCommand) so `@solidping checks add …` behaves the same in both
// products — only the mention-stripping step differs.
type ParsedCommand struct {
	Command    string            // "checks", "results", "incidents", "config", "help"
	Subcommand string            // "add", "list", "rm" (for checks)
	Args       []string          // Positional arguments (e.g. URL, slug)
	Flags      map[string]string // Named flags (e.g. "check")
}

// mentionTagRegex matches a Teams inline mention entity, e.g.
// `<at>SolidPing</at>` or `<at id="0">SolidPing (dev)</at>`.
//
// Stripping the *tag* rather than matching a display name is what makes the
// trigger word independent of the app's name: a self-hosted operator who
// renames the app in their own manifest keeps working commands, which is the
// resolution of the spec's "confirm the display name works for both SaaS and
// self-hosted manifests" open question.
var mentionTagRegex = regexp.MustCompile(`(?is)<at\b[^>]*>.*?</at>`)

// anchorRegex rewrites `<a href="https://x">label</a>` down to the bare URL,
// the Teams analog of Slack's `<https://x|label>` link form.
var anchorRegex = regexp.MustCompile(`(?is)<a\b[^>]*href="([^"]+)"[^>]*>.*?</a>`)

// htmlTagRegex strips whatever markup is left once mentions and anchors are
// handled — Teams wraps rich-text messages in `<p>`, `<div>`, `<br>`, `<span>`.
var htmlTagRegex = regexp.MustCompile(`(?is)</?[a-z][^>]*>`)

// whitespaceRegex collapses the runs of spaces and non-breaking spaces Teams
// leaves behind after a mention.
var whitespaceRegex = regexp.MustCompile(`\s+`)

// StripMention removes the bot mention (and surrounding Teams markup) from an
// activity's text, leaving the bare command line.
func StripMention(text string) string {
	text = mentionTagRegex.ReplaceAllString(text, " ")
	text = anchorRegex.ReplaceAllString(text, " $1 ")
	text = htmlTagRegex.ReplaceAllString(text, " ")

	// Unescape after tag removal so an escaped `&lt;at&gt;` in a user's own
	// text can never be re-interpreted as markup.
	text = html.UnescapeString(text)

	// html.UnescapeString turns &nbsp; into U+00A0, which is not matched by
	// \s in Go's regexp — normalize it to a plain space first.
	text = strings.ReplaceAll(text, "\u00a0", " ")
	text = whitespaceRegex.ReplaceAllString(text, " ")

	return strings.TrimSpace(text)
}

// ParseMentionText extracts a command from a Teams message activity's text.
//
// Input:  "<at>SolidPing</at> checks add https://example.com -slug mycheck"
// Output: ParsedCommand{Command: "checks", Subcommand: "add", Args: [...], Flags: {...}}.
func ParseMentionText(text string) *ParsedCommand {
	text = StripMention(text)

	if text == "" {
		return &ParsedCommand{Command: cmdHelp, Flags: make(map[string]string)}
	}

	tokens := tokenize(text)
	if len(tokens) == 0 {
		return &ParsedCommand{Command: cmdHelp, Flags: make(map[string]string)}
	}

	cmd := &ParsedCommand{
		Command: strings.ToLower(tokens[0]),
		Flags:   make(map[string]string),
	}

	tokens = tokens[1:]

	// Commands that take a subcommand.
	hasSubcommand := map[string]bool{
		cmdChecks:    true,
		cmdConfig:    true,
		cmdIncidents: true,
	}

	if hasSubcommand[cmd.Command] && len(tokens) > 0 && !strings.HasPrefix(tokens[0], "-") {
		cmd.Subcommand = strings.ToLower(tokens[0])
		tokens = tokens[1:]
	}

	parseArgsAndFlags(cmd, tokens)

	return cmd
}

// parseArgsAndFlags splits the remaining tokens into positional args and
// `-flag value` pairs (a flag with no following value is a boolean "true").
func parseArgsAndFlags(cmd *ParsedCommand, tokens []string) {
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]

		if !strings.HasPrefix(token, "-") {
			cmd.Args = append(cmd.Args, token)

			continue
		}

		flagName := strings.ToLower(strings.TrimLeft(token, "-"))

		if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
			cmd.Flags[flagName] = tokens[i+1]
			i++

			continue
		}

		cmd.Flags[flagName] = "true"
	}
}

// tokenize splits a string into tokens, respecting quoted strings.
func tokenize(text string) []string {
	var (
		tokens    []string
		current   strings.Builder
		inQuote   bool
		quoteChar = rune(0)
	)

	for _, char := range text {
		switch {
		case (char == '"' || char == '\'') && !inQuote:
			inQuote = true
			quoteChar = char
		case char == quoteChar && inQuote:
			inQuote = false
			quoteChar = 0
		case char == ' ' && !inQuote:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}

	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens
}
