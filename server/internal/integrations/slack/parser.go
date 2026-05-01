package slack

import (
	"regexp"
	"strings"
)

// ParsedCommand represents a parsed Slack bot command.
type ParsedCommand struct {
	Command    string            // "checks", "results", "incidents", "help"
	Subcommand string            // "add", "list", "rm" (for checks)
	Args       []string          // Positional arguments (e.g., URL, slug)
	Flags      map[string]string // Named flags (e.g., "slug", "interval", "check")
}

// mentionRegex matches a Slack user mention like <@U123ABC>.
var mentionRegex = regexp.MustCompile(`^<@[A-Z0-9]+>`)

// slackLinkRegex matches Slack auto-formatted links like <https://example.com|example.com> or <https://example.com>.
var slackLinkRegex = regexp.MustCompile(`<(https?://[^|>]+)(?:\|[^>]*)?>`)

// extractSlackLinks replaces Slack-formatted links with just the URL.
// Slack formats links as <https://url|display_text> or <https://url>.
func extractSlackLinks(text string) string {
	return slackLinkRegex.ReplaceAllString(text, "$1")
}

// ParseMentionText extracts a command from a mention message.
// Input: "<@U123ABC> checks add https://example.com -slug mycheck".
// Output: ParsedCommand{Command: "checks", Subcommand: "add", Args: [...], Flags: {...}}.
func ParseMentionText(text string) *ParsedCommand {
	// Strip the bot mention from the start
	text = mentionRegex.ReplaceAllString(text, "")

	// Extract URLs from Slack's auto-formatted links <https://url|display>
	text = extractSlackLinks(text)

	text = strings.TrimSpace(text)

	if text == "" {
		return &ParsedCommand{
			Command: cmdHelp,
			Flags:   make(map[string]string),
		}
	}

	// Tokenize respecting quoted strings
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return &ParsedCommand{
			Command: cmdHelp,
			Flags:   make(map[string]string),
		}
	}

	cmd := &ParsedCommand{
		Command: strings.ToLower(tokens[0]),
		Flags:   make(map[string]string),
	}

	// Process remaining tokens
	tokens = tokens[1:]

	// Commands that have subcommands
	hasSubcommand := map[string]bool{
		cmdChecks:    true,
		cmdConfig:    true,
		cmdIncidents: true,
	}

	// Extract subcommand if applicable
	if hasSubcommand[cmd.Command] && len(tokens) > 0 && !strings.HasPrefix(tokens[0], "-") {
		cmd.Subcommand = strings.ToLower(tokens[0])
		tokens = tokens[1:]
	}

	// Parse remaining tokens into args and flags
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]

		if strings.HasPrefix(token, "-") {
			// This is a flag
			flagName := strings.TrimLeft(token, "-")
			flagName = strings.ToLower(flagName)

			// Check if next token exists and is the value
			if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
				cmd.Flags[flagName] = tokens[i+1]
				i++ // Skip the value token
			} else {
				// Flag without value (boolean flag)
				cmd.Flags[flagName] = "true"
			}
		} else {
			// This is a positional argument
			cmd.Args = append(cmd.Args, token)
		}
	}

	return cmd
}

// tokenize splits a string into tokens, respecting quoted strings.
func tokenize(text string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

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
