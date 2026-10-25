package aichecks

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/secretref"
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
	hostArgRE = regexp.MustCompile(`\b(?:tcp|udp|rdp|vnc)\s*\.\s*connect\s*\(\s*["']([^"']+)["']`)
	envRefRE  = regexp.MustCompile(`\benv\s*(?:\.\s*([A-Za-z_$][\w$]*)|\[\s*["']([^"']+)["']\s*\])`)
	secretRE  = regexp.MustCompile(`\bsecrets\s*(?:\.\s*([A-Za-z_$][\w$]*)|\[\s*["']([^"']+)["']\s*\])`)
	tryRE     = regexp.MustCompile(`\btry\s*\{`)
	// hostTokenRE catches a bare host name or IPv4 address in prose
	// ("monitor acme.com", "the API on 10.0.0.5").
	hostTokenRE = regexp.MustCompile(
		`(?i)\b(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\b|\blocalhost\b`)
	downRE = regexp.MustCompile(`["']down["']`)
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

// SecretRefs lists the secret names (secrets.NAME, secrets["NAME"]) the texts
// refer to. Sorted, deduplicated.
func SecretRefs(texts ...string) []string {
	set := map[string]struct{}{}

	for _, text := range texts {
		for _, match := range secretRE.FindAllStringSubmatch(text, -1) {
			name := match[1]
			if name == "" {
				name = match[2]
			}

			set[name] = struct{}{}
		}
	}

	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
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
		switch {
		case startsComment(runes, i, '/'):
			i = lineCommentEnd(runes, i)
			if i < len(runes) {
				noComments.WriteRune('\n')
				code.WriteRune('\n')
			}
		case startsComment(runes, i, '*'):
			i = blockCommentEnd(runes, i)

			noComments.WriteRune(' ')
			code.WriteRune(' ')
		case runes[i] == '"' || runes[i] == '\'' || runes[i] == '`':
			end := stringEnd(runes, i)
			noComments.WriteString(string(runes[i : end+1]))
			code.WriteRune(runes[i])
			code.WriteRune(runes[i])

			i = end
		default:
			noComments.WriteRune(runes[i])
			code.WriteRune(runes[i])
		}
	}

	return noComments.String(), code.String()
}

// startsComment reports a "/" followed by marker ("/" or "*") at i.
func startsComment(runes []rune, i int, marker rune) bool {
	return runes[i] == '/' && i+1 < len(runes) && runes[i+1] == marker
}

// lineCommentEnd returns the index of the newline ending the comment at i.
func lineCommentEnd(runes []rune, i int) int {
	for i < len(runes) && runes[i] != '\n' {
		i++
	}

	return i
}

// blockCommentEnd returns the index of the "/" closing the comment at i.
func blockCommentEnd(runes []rune, i int) int {
	i += 2
	for i+1 < len(runes) && (runes[i] != '*' || runes[i+1] != '/') {
		i++
	}

	return i + 1
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
// not. It only reads the script text, so a URL built at runtime escapes it:
// it is an early refusal with a clear message, not the security boundary.
// What keeps a prompt injection in page content from sending `secrets`
// somewhere new is the runtime host allowlist of Runner.Run (repairHosts).
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

	for _, name := range []string{"browser", "tcp", "udp", "websocket", "rdp", "vnc", "solidping"} {
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
// gets the real secret values when it does. Like CheckHosts it reads the
// text only and is an early refusal: the run itself is held to
// declaredHosts at runtime.
func hostsDeclared(script string, env map[string]string, declared ...string) bool {
	parts := append([]string(nil), declared...)
	for _, value := range env {
		parts = append(parts, value)
	}

	text := strings.ToLower(strings.Join(parts, "\n"))

	for _, host := range ScriptHosts(script, env) {
		if !strings.Contains(text, host) {
			return false
		}
	}

	return true
}

// repairHosts is the host allowlist of a repair run holding the check's real
// secrets: the hosts the previous version contacts, its URL literals and the
// env values it reads. An env value the previous version never read is not
// in it, so a candidate cannot reach one through a dynamic key.
func repairHosts(previous string, env map[string]string) []string {
	return ScriptHosts(previous, env)
}

// declaredHosts is the host allowlist of a generation run holding real
// secrets: every URL host and bare host name the user wrote (prompt,
// contract, env values).
func declaredHosts(env map[string]string, declared ...string) []string {
	texts := append([]string(nil), declared...)
	for _, value := range env {
		texts = append(texts, value)
	}

	var hosts []string

	for _, text := range texts {
		hosts = append(hosts, urlHosts(text)...)

		for _, token := range hostTokenRE.FindAllString(text, -1) {
			hosts = append(hosts, strings.ToLower(token))
		}
	}

	return dedupHosts(hosts)
}

// urlHosts lists the hosts of the URL literals in text.
func urlHosts(text string) []string {
	var hosts []string

	for _, match := range urlLiteralRE.FindAllString(text, -1) {
		if parsed, err := url.Parse(match); err == nil && parsed.Hostname() != "" {
			hosts = append(hosts, strings.ToLower(parsed.Hostname()))
		}
	}

	return hosts
}

func dedupHosts(hosts []string) []string {
	sort.Strings(hosts)

	return slices.Compact(hosts)
}

// netCallRE finds a call that can open a connection: a request verb, a
// connect or open, a page navigation, a sub-check, or a call the text cannot
// name (eval, Function, a computed member). Matched on maskStrings output.
var netCallRE = regexp.MustCompile(
	`(?:\.\s*(?:get|post|put|patch|delete|head|connect|open|goto)|\bsolidping\s*\.\s*[A-Za-z_$][\w$]*|` +
		`\b(?:fetch|eval|Function)|\])\s*\(`)

var (
	placeholderRE    = regexp.MustCompile(`^"(\d+)"`)
	placeholderAnyRE = regexp.MustCompile(`"\d+"`)
	envReadRE        = regexp.MustCompile(`^env\s*(?:\.\s*([A-Za-z_$][\w$]*)|\[\s*"(\d+)"\s*\])`)
	urlStartRE       = regexp.MustCompile(`(?i)^(?:https?|wss?)://`)
	hostPortRE       = regexp.MustCompile(`^[A-Za-z0-9.\-\[\]:]+$`)
)

// hiddenSnippetMax caps the call site quoted in a refusal.
const hiddenSnippetMax = 80

// hiddenHost reports a candidate that can reach a host its text does not
// show, quoting the call site, or "" (spec 2026-10-24-01). It is the static
// half of the `repair: auto` rule, and fails closed: a candidate with more
// network call sites than the previous version, or a call site whose target
// is neither a string literal nor an env read, is hidden. The boundary is
// the repair grant every scheduled run of an applied version is held to.
func hiddenHost(previous, candidate string, env map[string]string) string {
	prevCode, _ := maskStrings(previous)
	candCode, literals := maskStrings(candidate)
	prevSites := netCallSites(prevCode)
	candSites := netCallSites(candCode)

	for _, site := range candSites {
		if !visibleTarget(candCode[site:], literals, env) {
			return callSnippet(candCode[:site], candCode[site:], literals)
		}
	}

	if len(candSites) > len(prevSites) {
		return fmt.Sprintf("%d network calls where the previous version had %d", len(candSites), len(prevSites))
	}

	return ""
}

// addedReference is the first `${…}` reference the candidate holds that the
// previous version did not, or "" (spec 2026-10-25-01). The repair grant is
// decided on the stored text, so a reference does not lift it; an applied
// candidate still never brings a new one.
func addedReference(previous, candidate string) string {
	known := secretref.Pattern.FindAllString(previous, -1)

	for _, ref := range secretref.Pattern.FindAllString(candidate, -1) {
		if !slices.Contains(known, ref) {
			return ref
		}
	}

	return ""
}

// netCallSites lists the offsets of the first argument of every network call
// that has one. A call without argument (browser.open()) reaches no host.
func netCallSites(code string) []int {
	var sites []int

	for _, match := range netCallRE.FindAllStringIndex(code, -1) {
		arg := strings.TrimLeft(code[match[1]:], " \t\r\n")
		if strings.HasPrefix(arg, ")") {
			continue
		}

		sites = append(sites, len(code)-len(arg))
	}

	return sites
}

// visibleTarget reports a call argument whose host the text shows: a URL or
// host literal, or an env read, ending the argument or followed by a path.
func visibleTarget(arg string, literals []string, env map[string]string) bool {
	if match := placeholderRE.FindStringSubmatch(arg); match != nil {
		lit := literalAt(literals, match[1])
		if strings.Contains(lit, "${") {
			return false
		}

		switch {
		case urlStartRE.MatchString(lit):
			return argEnds(arg[len(match[0]):], authorityClosed(lit), literals)
		case hostPortRE.MatchString(lit):
			// A socket address: nothing may be appended ("http:" + ...).
			rest := strings.TrimLeft(arg[len(match[0]):], " \t\r\n")

			return strings.HasPrefix(rest, ")") || strings.HasPrefix(rest, ",")
		default:
			return false
		}
	}

	if match := envReadRE.FindStringSubmatch(arg); match != nil {
		name := match[1]
		if name == "" {
			name = literalAt(literals, match[2])
		}

		return argEnds(arg[len(match[0]):], authorityClosed(env[name]), literals)
	}

	return false
}

// argEnds reports the rest of an argument that cannot change its host: it
// ends there, or (closed or not) appends a literal starting with a path.
func argEnds(rest string, closed bool, literals []string) bool {
	rest = strings.TrimLeft(rest, " \t\r\n")

	switch {
	case strings.HasPrefix(rest, ")"), strings.HasPrefix(rest, ","):
		return true
	case strings.HasPrefix(rest, "+"):
		if closed {
			return true
		}

		next := strings.TrimLeft(rest[1:], " \t\r\n")
		if match := placeholderRE.FindStringSubmatch(next); match != nil {
			lit := literalAt(literals, match[1])

			return lit != "" && strings.ContainsRune("/?#", rune(lit[0]))
		}

		return false
	default:
		return false
	}
}

// authorityClosed reports a URL whose host is followed by a path, query or
// fragment, so nothing appended can change the host.
func authorityClosed(raw string) bool {
	_, rest, ok := strings.Cut(raw, "://")

	return ok && strings.ContainsAny(rest, "/?#")
}

// literalAt is the content of the masked literal idx, quotes removed.
func literalAt(literals []string, idx string) string {
	n, err := strconv.Atoi(idx)
	if err != nil || n < 0 || n >= len(literals) {
		return ""
	}

	lit := literals[n]
	if len(lit) >= 2 && lit[len(lit)-1] == lit[0] {
		return lit[1 : len(lit)-1]
	}

	return strings.TrimPrefix(lit, lit[:1])
}

// callSnippet quotes the call whose argument starts code[len(before):], with
// its string literals restored.
func callSnippet(before, from string, literals []string) string {
	start := strings.LastIndexFunc(before, func(r rune) bool { return r == ';' || r == '\n' || r == '{' || r == '}' })
	text := strings.TrimSpace(before[start+1:]) + from

	if end := strings.IndexAny(text, ";\n"); end >= 0 {
		text = text[:end]
	}

	text = placeholderAnyRE.ReplaceAllStringFunc(text, func(placeholder string) string {
		n, err := strconv.Atoi(strings.Trim(placeholder, `"`))
		if err != nil || n >= len(literals) {
			return placeholder
		}

		return literals[n]
	})

	if len(text) > hiddenSnippetMax {
		text = text[:hiddenSnippetMax] + "..."
	}

	return text
}

// maskStrings strips the comments of a script and replaces each string
// literal by a "N" placeholder, N indexing the returned literals (quotes
// kept). Call sites are then matched on code only.
func maskStrings(script string) (string, []string) {
	var (
		code     strings.Builder
		literals []string
	)

	runes := []rune(script)

	for i := 0; i < len(runes); i++ {
		switch {
		case startsComment(runes, i, '/'):
			i = lineCommentEnd(runes, i)
			if i < len(runes) {
				code.WriteRune('\n')
			}
		case startsComment(runes, i, '*'):
			i = blockCommentEnd(runes, i)

			code.WriteRune(' ')
		case runes[i] == '"' || runes[i] == '\'' || runes[i] == '`':
			end := stringEnd(runes, i)
			code.WriteString(`"` + strconv.Itoa(len(literals)) + `"`)
			literals = append(literals, string(runes[i:end+1]))

			i = end
		default:
			code.WriteRune(runes[i])
		}
	}

	return code.String(), literals
}
