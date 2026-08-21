package discord

import (
	"regexp"
	"strings"
)

// mentionRegex matches a leading Discord user mention: `<@123>` or `<@!123>`.
var mentionRegex = regexp.MustCompile(`^<@!?\d+>`)

// MentionsBot reports whether a message text starts by mentioning this bot.
// Discord delivers the mention as `<@bot_id>` (or `<@!bot_id>` for a nickname
// mention), so both shapes must match or half the invocations look like plain
// chatter.
func MentionsBot(text, botUserID string) bool {
	if botUserID == "" {
		return false
	}

	trimmed := strings.TrimSpace(text)

	return strings.HasPrefix(trimmed, "<@"+botUserID+">") ||
		strings.HasPrefix(trimmed, "<@!"+botUserID+">")
}

// ParseMentionText turns `@SolidPing checks add https://acme.com -slug acme`
// into a Command. Mirrors the Slack parser so a user who knows one knows the
// other.
func ParseMentionText(text string) *Command {
	text = mentionRegex.ReplaceAllString(strings.TrimSpace(text), "")
	text = strings.TrimSpace(text)

	cmd := &Command{Flags: make(map[string]string)}

	tokens := tokenize(text)
	if len(tokens) == 0 {
		cmd.Command = cmdHelp

		return cmd
	}

	cmd.Command = strings.ToLower(tokens[0])
	tokens = tokens[1:]

	hasSubcommand := map[string]bool{cmdChecks: true, cmdConfig: true, cmdIncidents: true}

	if hasSubcommand[cmd.Command] && len(tokens) > 0 && !strings.HasPrefix(tokens[0], "-") {
		cmd.Subcommand = strings.ToLower(tokens[0])
		tokens = tokens[1:]
	}

	parseArgsAndFlags(cmd, tokens)

	return cmd
}

// parseArgsAndFlags splits the remaining tokens into positional arguments and
// `-flag value` pairs.
func parseArgsAndFlags(cmd *Command, tokens []string) {
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]

		if !strings.HasPrefix(token, "-") {
			cmd.Args = append(cmd.Args, token)

			continue
		}

		name := strings.ToLower(strings.TrimLeft(token, "-"))

		if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
			cmd.Flags[name] = tokens[i+1]
			i++

			continue
		}

		cmd.Flags[name] = "true"
	}
}

// tokenize splits a string into tokens, respecting quoted strings.
func tokenize(text string) []string {
	var (
		tokens    []string
		current   strings.Builder
		inQuote   bool
		quoteChar rune
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
