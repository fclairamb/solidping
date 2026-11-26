package checkworker

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// resolvedValueMask stands for a resolved secret reference value in a
	// stored result, the same token the plan diff uses.
	resolvedValueMask = "***"

	// minMaskedValueLen is the shortest resolved value that is masked. Every
	// `${param:…}` value is collected, secret or not, and a short one (a port,
	// a region code) occurs in unrelated text: masking it would blank half the
	// output while protecting nothing worth the name.
	minMaskedValueLen = 4
)

// maskableValues returns the resolved values worth masking, longest first so a
// value that contains another one is masked whole.
func maskableValues(values []string) []string {
	var kept []string

	for _, value := range values {
		if len(value) >= minMaskedValueLen && !slices.Contains(kept, value) {
			kept = append(kept, value)
		}
	}

	slices.SortFunc(kept, func(a, b string) int { return len(b) - len(a) })

	return kept
}

func maskString(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, resolvedValueMask)
	}

	return text
}

func containsAny(text string, secrets []string) bool {
	return slices.ContainsFunc(secrets, func(secret string) bool { return strings.Contains(text, secret) })
}

// maskResolvedValues returns output with every resolved secret reference value
// of the job replaced by resolvedValueMask, in string values and map keys at
// any depth, whatever Go types the checker used (`[]string`,
// `map[string][]string`, …). Numbers and booleans are never touched.
//
// The output is stored as JSON, so it is walked in its JSON form: the secrets
// are first looked for in the encoded text (in their JSON-escaped spelling),
// and only an output that carries one is decoded and rebuilt. Any other output
// is returned as is, untouched and uncopied.
func maskResolvedValues(checkJob *models.CheckJob, output map[string]any) map[string]any {
	secrets := maskableValues(checkJob.ResolvedRefValues)
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
			"checkUid", checkJob.CheckUID, "error", decodeErr)

		return map[string]any{"error": "output withheld: it carried a secret reference value"}
	}

	masked, _ := maskJSONValue(generic, secrets).(map[string]any)

	return masked
}

// maskJSONValue masks the strings and map keys of a decoded JSON value.
func maskJSONValue(value any, secrets []string) any {
	switch typed := value.(type) {
	case string:
		return maskString(typed, secrets)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[maskString(key, secrets)] = maskJSONValue(item, secrets)
		}

		return out
	case []any:
		out := make([]any, len(typed))
		for idx, item := range typed {
			out[idx] = maskJSONValue(item, secrets)
		}

		return out
	default:
		return value
	}
}

// maskMetricKeys returns metrics with the resolved secret values masked in its
// keys (a checker may key a metric by a queried zone). Values are numbers and
// are left alone. Metrics without a secret are returned as is.
func maskMetricKeys(metrics map[string]any, secrets []string) map[string]any {
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
		out[maskString(key, secrets)] = value
	}

	return out
}

// maskDiagnostics returns diagnostics with the resolved secret values masked in
// their text: the captured failure response (URL, status line, headers, body),
// the network failure endpoint and the screenshot error. A screenshot is an
// image and cannot be masked. The checker's own struct is never modified: the
// masked parts are copies.
func maskDiagnostics(diagnostics *checkerdef.Diagnostics, secrets []string) *checkerdef.Diagnostics {
	if diagnostics == nil || len(secrets) == 0 {
		return diagnostics
	}

	out := *diagnostics
	out.ScreenshotError = maskString(diagnostics.ScreenshotError, secrets)

	if response := diagnostics.FailureResponse; response != nil {
		masked := *response
		masked.URL = maskString(response.URL, secrets)
		masked.StatusLine = maskString(response.StatusLine, secrets)
		masked.Body = maskString(response.Body, secrets)

		if response.Headers != nil {
			masked.Headers = make(map[string]string, len(response.Headers))
			for key, value := range response.Headers {
				masked.Headers[maskString(key, secrets)] = maskString(value, secrets)
			}
		}

		out.FailureResponse = &masked
	}

	if failure := diagnostics.NetworkFailure; failure != nil {
		masked := *failure
		masked.Host = maskString(failure.Host, secrets)
		masked.Address = maskString(failure.Address, secrets)
		out.NetworkFailure = &masked
	}

	return &out
}
