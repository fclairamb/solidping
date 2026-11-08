package cli

import (
	"os"
	"strings"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
)

// StarPromptEnv is the operator switch silencing every GitHub star nudge.
// The CLI does not load the server config, so it reads the variable directly.
const StarPromptEnv = "SP_APP_STARPROMPT"

// StarPromptLine is the one-line GitHub star invitation.
const StarPromptLine = "If SolidPing is useful to you, a star helps: https://github.com/fclairamb/solidping"

// StarPromptEnabled reports whether the star nudges are on: "false" or "0"
// (as koanf parses booleans) turn them off, anything else or unset leaves them on.
func StarPromptEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(StarPromptEnv))) {
	case "false", "0", "f", "no", "off":
		return false
	default:
		return true
	}
}

// StarPromptText returns StarPromptLine, or "" when the switch is off.
func StarPromptText() string {
	if !StarPromptEnabled() {
		return ""
	}

	return StarPromptLine
}

// printStarPrompt prints the star line (text mode callers only).
func printStarPrompt() {
	if line := StarPromptText(); line != "" {
		output.PrintMessage(os.Stdout, line)
	}
}
