package heartbeatpush

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Annotation limits. Every one of them bounds what a single beat can add to
// the results table — in particular MaxAnnotationPairs bounds metric-key
// cardinality, which is the only unbounded-growth vector an annotation has.
const (
	// MaxAnnotationBytes bounds the whole annotation (inside the line cap).
	MaxAnnotationBytes = 128
	// MaxAnnotationPairs bounds how many key=value fields one beat may carry.
	MaxAnnotationPairs = 10
	// MaxAnnotationKeyBytes / MaxAnnotationValueBytes bound each half.
	MaxAnnotationKeyBytes   = 32
	MaxAnnotationValueBytes = 64
	// MaxStatusWordBytes bounds the optional leading status word.
	MaxStatusWordBytes = 32
)

// Annotation is the parsed optional annotation of a beat.
//
// Parsing is TOTAL: there is no error return, and no input can make a beat
// invalid. If the remainder does not match the grammar it lands in Raw and the
// beat still counts. Aliveness comes first — a firmware typo in a key name must
// never make a healthy device look dead. (Under SP2 the MAC covers the raw
// bytes either way, so tolerating a bad grammar costs no authenticity.)
type Annotation struct {
	// Status is the optional leading bare word (e.g. "started", "alive").
	// V1 stores it as an opaque annotation and gives it NO semantics: a pushed
	// "fail" raising incidents drags in recovery-lifecycle design, and under
	// SP1 would let a token holder raise false alarms. The docs reserve
	// started/ok/fail so firmware can adopt the vocabulary now.
	Status string
	// Numeric holds the key=value pairs whose value parsed as a finite number.
	// These become first-class time series in the result's `metrics` jsonb.
	Numeric map[string]float64
	// Text holds the key=value pairs whose value did not parse as a number.
	Text map[string]string
	// Raw is the whole annotation, sanitized, when the grammar did not match.
	// Mutually exclusive with the three fields above.
	Raw string
}

// IsEmpty reports whether the annotation carries nothing worth storing.
func (a Annotation) IsEmpty() bool {
	return a.Status == "" && a.Raw == "" && len(a.Numeric) == 0 && len(a.Text) == 0
}

// ParseAnnotation parses `[status-word] [key1=value1 key2=value2 ...]`.
//
// Grammar rules, all of which degrade to Raw rather than to an error:
//   - an optional leading bare token (no "="), at most MaxStatusWordBytes and
//     restricted to a safe charset;
//   - then up to MaxAnnotationPairs key=value pairs, split on the FIRST "=" so
//     values may contain "="; keys are [a-z0-9_]{1,32}; values are at most
//     MaxAnnotationValueBytes and (there being no quoting) contain no spaces;
//   - a bare token anywhere after the first is a grammar violation;
//   - NaN and ±Inf are rejected as numbers and fall through to Text.
//
// Control characters are stripped from every stored string. The stored values
// are untrusted input: they are HTML-escaped where the dashboard renders them
// and are never logged raw.
func ParseAnnotation(raw string) Annotation {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Annotation{}
	}

	if len(trimmed) > MaxAnnotationBytes {
		return Annotation{Raw: sanitizeAnnotationText(trimmed[:MaxAnnotationBytes])}
	}

	parsed, ok := parseAnnotationFields(strings.Fields(trimmed))
	if !ok {
		return Annotation{Raw: sanitizeAnnotationText(trimmed)}
	}

	return parsed
}

// parseAnnotationFields does the grammar walk, returning ok=false on any
// violation so the caller can fall back to Raw.
func parseAnnotationFields(fields []string) (Annotation, bool) {
	out := Annotation{}

	for i, field := range fields {
		key, value, isPair := strings.Cut(field, "=")
		if !isPair {
			// A bare word is only legal as the very first token.
			if i != 0 || len(field) > MaxStatusWordBytes || !validStatusWord(field) {
				return Annotation{}, false
			}

			out.Status = field

			continue
		}

		if !out.addPair(key, value) {
			return Annotation{}, false
		}
	}

	return out, true
}

// addPair validates and stores one key=value field, returning false on any
// grammar violation (bad key, oversized value, too many pairs, duplicate key).
func (a *Annotation) addPair(key, value string) bool {
	if !validAnnotationKey(key) || len(value) > MaxAnnotationValueBytes {
		return false
	}

	if len(a.Numeric)+len(a.Text) >= MaxAnnotationPairs {
		return false
	}

	if _, dup := a.Numeric[key]; dup {
		return false
	}

	if _, dup := a.Text[key]; dup {
		return false
	}

	if num, err := strconv.ParseFloat(value, 64); err == nil &&
		!math.IsNaN(num) && !math.IsInf(num, 0) {
		if a.Numeric == nil {
			a.Numeric = make(map[string]float64, MaxAnnotationPairs)
		}

		a.Numeric[key] = num

		return true
	}

	if a.Text == nil {
		a.Text = make(map[string]string, MaxAnnotationPairs)
	}

	a.Text[key] = sanitizeAnnotationText(value)

	return true
}

// validAnnotationKey enforces [a-z0-9_]{1,32}. The charset is deliberately
// narrower than the values': keys become metric names and jsonb object keys,
// so they must be boring.
func validAnnotationKey(key string) bool {
	if key == "" || len(key) > MaxAnnotationKeyBytes {
		return false
	}

	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}

	return true
}

// validStatusWord allows letters, digits and a little punctuation, so
// "started", "ok", "boot-2" all pass while anything that could confuse a log
// line or a renderer does not.
func validStatusWord(word string) bool {
	if word == "" {
		return false
	}

	for _, r := range word {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' && r != ':' {
			return false
		}
	}

	return true
}

// SanitizeForLog strips control characters from a wire-supplied string before
// it reaches a log line.
//
// Exported because the ingest logs the beat's target (org / identifier), and
// those bytes come off an unauthenticated socket exactly like an annotation
// does. One stripping rule for every wire-supplied string, rather than two
// that can drift apart.
func SanitizeForLog(text string) string {
	return sanitizeAnnotationText(text)
}

// sanitizeAnnotationText drops control characters (including the newline that
// frames the protocol and anything that could forge a log line) and replaces
// invalid UTF-8, so nothing stored from the wire can escape the context it is
// rendered in.
func sanitizeAnnotationText(text string) string {
	return strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			return -1
		}

		return r
	}, strings.ToValidUTF8(text, ""))
}
