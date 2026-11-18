package importers

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// credentialFieldHint matches a selector or input name that points at a
// credential field. It is shared by every importer that turns typed values
// into a script (Pingdom TMS fill, Chrome Recorder change): a value typed into
// such a field goes to the check's encrypted secrets, never the script.
//
// pin and otp only match as whole tokens, so "shipping" or "spinner" do not.
// A selector carrying type=password or [type="password"] matches through
// "password".
var credentialFieldHint = regexp.MustCompile(
	`(?i)pass|pwd|(?:^|[^a-z])(?:pin|otp)(?:[^a-z]|$)|secret|token|api[-_ ]?key|` +
		`mot de passe|contraseña`)

const (
	// credentialMinLength is the shortest value the entropy rule considers.
	credentialMinLength = 20
	// credentialMinEntropy is the Shannon entropy, in bits per character,
	// above which a long value without spaces is treated as a credential.
	credentialMinEntropy = 3.5
)

// isTypedSecret reports whether a value typed into a field must be kept out
// of the script: the field (any of its selectors or names) looks like a
// credential field, or the value itself looks like a credential.
func isTypedSecret(value string, fields ...string) bool {
	for _, field := range fields {
		if credentialFieldHint.MatchString(field) {
			return true
		}
	}

	return looksLikeCredential(value)
}

// looksLikeCredential reports whether a value is a bearer token or a long,
// high-entropy string with no spaces (an API key, a generated password).
// Emails and URLs are left alone: they are long, but not secrets.
func looksLikeCredential(value string) bool {
	if strings.HasPrefix(value, "Bearer ") {
		return true
	}

	if len(value) < credentialMinLength || strings.ContainsAny(value, "@") || strings.Contains(value, "://") {
		return false
	}

	hasLetter, hasDigit := false, false

	for _, r := range value {
		switch {
		case unicode.IsSpace(r):
			return false
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}

	return hasLetter && hasDigit && shannonEntropy(value) >= credentialMinEntropy
}

// shannonEntropy is the entropy of value, in bits per character.
func shannonEntropy(value string) float64 {
	counts := map[rune]int{}
	total := 0

	for _, r := range value {
		counts[r]++
		total++
	}

	entropy := 0.0

	for _, count := range counts {
		p := float64(count) / float64(total)
		entropy -= p * math.Log2(p)
	}

	return entropy
}

// typedValuesLeftFormat is the warning added when typed values stayed in the
// script, so the operator reviews them rather than assuming every credential
// was handled. Its only verb is the count.
const typedValuesLeftFormat = "%d typed value(s) stayed in the script; move any credential to secrets.NAME"
