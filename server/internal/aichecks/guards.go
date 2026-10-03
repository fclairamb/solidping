package aichecks

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Guard refusals. A repair candidate failing any of them is never accepted.
var (
	// ErrNewHost is a candidate contacting a host the previous script did not.
	ErrNewHost = errors.New("the candidate contacts a host the previous version did not")
	// ErrNewTryCatch is a candidate adding a try/catch.
	ErrNewTryCatch = errors.New("the candidate adds a try/catch")
	// ErrDroppedAssertion is a candidate that can no longer report down.
	ErrDroppedAssertion = errors.New("the candidate no longer reports a down status")
	// ErrNewAPI is a candidate using a protocol global the previous script did not.
	ErrNewAPI = errors.New("the candidate uses an API the previous version did not")
)

var (
	urlLiteralRE = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'` + "`" + `<>\\)]+`)
	// hostArgRE catches tcp.connect("host", ...) style literal hosts.
	hostArgRE  = regexp.MustCompile(`\b(?:tcp|udp|rdp|vnc)\s*\.\s*connect\s*\(\s*["']([^"']+)["']`)
	envRefRE   = regexp.MustCompile(`\benv\s*(?:\.\s*([A-Za-z_$][\w$]*)|\[\s*["']([^"']+)["']\s*\])`)
	tryRE      = regexp.MustCompile(`\btry\s*\{`)
	downRE     = regexp.MustCompile(`["']down["']`)
	apiGlobals = []string{"browser", "tcp", "udp", "websocket", "rdp", "vnc", "solidping"}
)

// ScriptHosts lists the hosts a script contacts: URL literals in the script,
// literal hosts given to a socket connect, and the hosts of the env values
// the script reads. Lowercase, sorted, deduplicated.
func ScriptHosts(script string, env map[string]string) []string {
	set := map[string]struct{}{}

	addURL := func(raw string) {
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
			set[strings.ToLower(parsed.Hostname())] = struct{}{}
		}
	}

	code, _ := scanScript(script)

	for _, match := range urlLiteralRE.FindAllString(code, -1) {
		addURL(match)
	}

	for _, match := range hostArgRE.FindAllStringSubmatch(code, -1) {
		set[strings.ToLower(match[1])] = struct{}{}
	}

	for _, match := range envRefRE.FindAllStringSubmatch(code, -1) {
		name := match[1]
		if name == "" {
			name = match[2]
		}

		value, ok := env[name]
		if !ok {
			continue
		}

		for _, inner := range urlLiteralRE.FindAllString(value, -1) {
			addURL(inner)
		}
	}

	out := make([]string, 0, len(set))
	for host := range set {
		out = append(out, host)
	}

	sort.Strings(out)

	return out
}

// codeOnly strips comments and string literals, so a keyword in a string
// ("try {") is not counted as code.
func codeOnly(script string) string {
	_, code := scanScript(script)

	return code
}

// scanScript walks a script once and returns it without comments, and
// without comments nor string contents. A comment marker inside a string
// ("https://...") stays a string.
func scanScript(script string) (string, string) {
	var noComments, code strings.Builder

	runes := []rune(script)

	for i := 0; i < len(runes); i++ {
		char := runes[i]
		next := rune(0)

		if i+1 < len(runes) {
			next = runes[i+1]
		}

		switch {
		case char == '/' && next == '/':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}

			if i < len(runes) {
				noComments.WriteRune('\n')
				code.WriteRune('\n')
			}
		case char == '/' && next == '*':
			i += 2
			for i+1 < len(runes) && (runes[i] != '*' || runes[i+1] != '/') {
				i++
			}

			i++

			noComments.WriteRune(' ')
			code.WriteRune(' ')
		case char == '"' || char == '\'' || char == '`':
			end := stringEnd(runes, i)
			noComments.WriteString(string(runes[i : end+1]))
			code.WriteRune(char)
			code.WriteRune(char)

			i = end
		default:
			noComments.WriteRune(char)
			code.WriteRune(char)
		}
	}

	return noComments.String(), code.String()
}

// stringEnd returns the index of the quote closing the string opened at
// start, or the last index when it is unterminated.
func stringEnd(runes []rune, start int) int {
	quote := runes[start]

	for i := start + 1; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			i++
		case quote:
			return i
		case '\n':
			if quote != '`' {
				return i
			}
		}
	}

	return len(runes) - 1
}

// CheckHosts refuses a candidate contacting a host the previous version did
// not. This is what keeps a prompt injection in page content from sending
// `secrets` somewhere new.
func CheckHosts(previous, candidate string, env map[string]string) error {
	allowed := ScriptHosts(previous, env)

	for _, host := range ScriptHosts(candidate, env) {
		if !slices.Contains(allowed, host) {
			return fmt.Errorf("%w: %s", ErrNewHost, host)
		}
	}

	return nil
}

// CheckNoNewTryCatch refuses a candidate with more try blocks than the
// previous version: a new try/catch is how a repair hides a failing
// assertion.
func CheckNoNewTryCatch(previous, candidate string) error {
	if len(tryRE.FindAllString(codeOnly(candidate), -1)) > len(tryRE.FindAllString(codeOnly(previous), -1)) {
		return ErrNewTryCatch
	}

	return nil
}

// CheckStillReportsDown refuses a candidate that lost every `down` path the
// previous version had: a script that can only say up asserts nothing.
func CheckStillReportsDown(previous, candidate string) error {
	prev, _ := scanScript(previous)
	cand, _ := scanScript(candidate)

	if downRE.MatchString(prev) && !downRE.MatchString(cand) {
		return ErrDroppedAssertion
	}

	return nil
}

// CheckSameAPIs refuses a candidate using a protocol global (browser, tcp,
// websocket...) the previous version did not use.
func CheckSameAPIs(previous, candidate string) error {
	prev, cand := codeOnly(previous), codeOnly(candidate)

	for _, name := range apiGlobals {
		re := regexp.MustCompile(`\b` + name + `\s*\.`)
		if re.MatchString(cand) && !re.MatchString(prev) {
			return fmt.Errorf("%w: %s", ErrNewAPI, name)
		}
	}

	return nil
}

// CheckCandidate runs every mechanical guard of a repair candidate.
func CheckCandidate(previous, candidate string, env map[string]string) error {
	for _, guard := range []func() error{
		func() error { return CheckHosts(previous, candidate, env) },
		func() error { return CheckNoNewTryCatch(previous, candidate) },
		func() error { return CheckStillReportsDown(previous, candidate) },
		func() error { return CheckSameAPIs(previous, candidate) },
	} {
		if err := guard(); err != nil {
			return err
		}
	}

	return nil
}

// hostsDeclared reports whether every host the script contacts appears in the
// user's own words (prompt, contract, env values). A generated script only
// gets the real secret values when it does.
func hostsDeclared(script string, env map[string]string, declared ...string) bool {
	text := strings.ToLower(strings.Join(declared, "\n"))
	for _, value := range env {
		text += "\n" + strings.ToLower(value)
	}

	for _, host := range ScriptHosts(script, env) {
		if !strings.Contains(text, host) {
			return false
		}
	}

	return true
}
