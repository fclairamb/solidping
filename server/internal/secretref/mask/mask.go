// Package mask hides resolved secret reference values (`${param:…}`,
// `${env:…}`) in what a check stores: its output, metric keys, diagnostics and
// error text. It is shared by the process that runs a check (checkworker) and
// by the server receiving an agent's result (agentws), which masks again what
// an agent predating the masking sent unmasked.
package mask

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// Token stands for a resolved secret reference value in a stored result,
	// the same token the plan diff uses.
	Token = "***"

	// MinValueLen is the shortest resolved value that is masked. Every
	// `${param:…}` value is collected, secret or not, and a short one (a port,
	// a region code) occurs in unrelated text: masking it would blank half the
	// output while protecting nothing worth the name.
	MinValueLen = 4
)

// Values returns the resolved values worth masking, longest first so a value
// that contains another one is masked whole.
func Values(values []string) []string {
	var kept []string

	for _, value := range values {
		if len(value) >= MinValueLen && !slices.Contains(kept, value) {
			kept = append(kept, value)
		}
	}

	slices.SortFunc(kept, func(a, b string) int { return len(b) - len(a) })

	return kept
}

// String replaces every secret of secrets (as returned by Values) in text.
func String(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, Token)
	}

	return text
}

func containsAny(text string, secrets []string) bool {
	return slices.ContainsFunc(secrets, func(secret string) bool { return strings.Contains(text, secret) })
}

// ErrorText returns the text of an error result with every resolved value
// masked. An error written before the probe (a config that fails to parse once
// its references are resolved) describes the resolved text: `url.Parse` quotes
// the whole URL. A value is masked in its raw spelling and in its Go-quoted one
// (`%q`, which escapes a control character or a quote the value may hold).
func ErrorText(values []string, text string) string {
	secrets := Values(values)
	if len(secrets) == 0 {
		return text
	}

	spellings := slices.Clone(secrets)

	for _, secret := range secrets {
		quoted := strconv.Quote(secret)
		if inner := quoted[1 : len(quoted)-1]; inner != secret && !slices.Contains(spellings, inner) {
			spellings = append(spellings, inner)
		}
	}

	slices.SortFunc(spellings, func(a, b string) int { return len(b) - len(a) })

	return String(text, spellings)
}

// Output returns output with every resolved value replaced by Token, in string
// values and map keys at any depth, whatever Go types the checker used
// (`[]string`, `map[string][]string`, …). Numbers and booleans are never
// touched. checkUID only labels the log line of a withheld output.
//
// The output is stored as JSON, so it is walked in its JSON form: the secrets
// are first looked for in the encoded text (in their JSON-escaped spelling),
// and only an output that carries one is decoded and rebuilt. Any other output
// is returned as is, untouched and uncopied.
func Output(values []string, output map[string]any, checkUID string) map[string]any {
	secrets := Values(values)
	if len(secrets) == 0 || output == nil {
		return output
	}

	blob, err := json.Marshal(output)
	if err != nil {
		return output
	}

	if !slices.ContainsFunc(secrets, func(secret string) bool {
		escaped, escErr := json.Marshal(secret)

		return escErr == nil && bytes.Contains(blob, escaped[1:len(escaped)-1])
	}) {
		return output
	}

	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.UseNumber()

	var generic map[string]any
	if decodeErr := decoder.Decode(&generic); decodeErr != nil {
		// Never store the clear value: withhold the output rather than leak it.
		slog.Warn("check output withheld: it carried a resolved secret reference value",
			"checkUid", checkUID, "error", decodeErr)

		return map[string]any{"error": "output withheld: it carried a secret reference value"}
	}

	masked, _ := JSONValue(generic, secrets).(map[string]any)

	return masked
}

// JSONValue masks the strings and map keys of a decoded JSON value.
func JSONValue(value any, secrets []string) any {
	switch typed := value.(type) {
	case string:
		return String(typed, secrets)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[String(key, secrets)] = JSONValue(item, secrets)
		}

		return out
	case []any:
		out := make([]any, len(typed))
		for idx, item := range typed {
			out[idx] = JSONValue(item, secrets)
		}

		return out
	default:
		return value
	}
}

// MetricKeys returns metrics with the secrets masked in its keys (a checker
// may key a metric by a queried zone). Values are numbers and are left alone.
// Metrics without a secret are returned as is.
func MetricKeys(metrics map[string]any, secrets []string) map[string]any {
	found := false

	for key := range metrics {
		if containsAny(key, secrets) {
			found = true

			break
		}
	}

	if !found {
		return metrics
	}

	out := make(map[string]any, len(metrics))
	for key, value := range metrics {
		out[String(key, secrets)] = value
	}

	return out
}

// Diagnostics returns diagnostics with the secrets masked in their text: the
// captured failure response (URL, status line, headers, body), the network
// failure endpoint and the screenshot error. A screenshot is an image and
// cannot be masked. The caller's struct is never modified: the masked parts
// are copies.
func Diagnostics(diagnostics *checkerdef.Diagnostics, secrets []string) *checkerdef.Diagnostics {
	if diagnostics == nil || len(secrets) == 0 {
		return diagnostics
	}

	out := *diagnostics
	out.ScreenshotError = String(diagnostics.ScreenshotError, secrets)

	if response := diagnostics.FailureResponse; response != nil {
		masked := *response
		masked.URL = String(response.URL, secrets)
		masked.StatusLine = String(response.StatusLine, secrets)
		masked.Body = String(response.Body, secrets)

		if response.Headers != nil {
			masked.Headers = make(map[string]string, len(response.Headers))
			for key, value := range response.Headers {
				masked.Headers[String(key, secrets)] = String(value, secrets)
			}
		}

		out.FailureResponse = &masked
	}

	if failure := diagnostics.NetworkFailure; failure != nil {
		masked := *failure
		masked.Host = String(failure.Host, secrets)
		masked.Address = String(failure.Address, secrets)
		out.NetworkFailure = &masked
	}

	return &out
}
