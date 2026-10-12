package secretref

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// The generated-value names a `${var:NAME}` reference may use. Lowercase and
// case-sensitive: `${var:UUID}` is unknown.
const (
	// VarTimestamp is the run's UTC time as 17 digits, YYYYMMDDhhmmssSSS.
	VarTimestamp = "timestamp"
	// VarISO8601 is the run's UTC time to the second, 2026-10-12T14:30:05Z.
	VarISO8601 = "iso8601"
	// VarUUID is a random (v4) UUID.
	VarUUID = "uuid"
	// VarRandom is 8 characters from [a-z0-9]: safe in a hostname and in a
	// case-insensitive path.
	VarRandom = "random"
)

const (
	timestampLayout = "20060102150405.000"
	iso8601Layout   = "2006-01-02T15:04:05Z"
	randomLength    = 8
	randomAlphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// KnownVars returns the generated-value names, in the order the docs and the
// validation messages list them.
func KnownVars() []string {
	return []string{VarTimestamp, VarISO8601, VarUUID, VarRandom}
}

// IsKnownVar reports whether name is a generated-value name.
func IsKnownVar(name string) bool {
	for _, known := range KnownVars() {
		if known == name {
			return true
		}
	}

	return false
}

// UnknownVarMessage is the one wording every validator uses for an unknown
// `${var:…}` name.
func UnknownVarMessage(name string) string {
	return fmt.Sprintf("unknown variable ${%s:%s} (known: %s)",
		SchemeVar, name, strings.Join(KnownVars(), ", "))
}

// UnknownVarRefs returns the unknown `${var:…}` names in a string, in order,
// duplicates included.
func UnknownVarRefs(value string) []string {
	var unknown []string

	for _, match := range Pattern.FindAllStringSubmatch(value, -1) {
		if match[1] == SchemeVar && !IsKnownVar(match[2]) {
			unknown = append(unknown, match[2])
		}
	}

	return unknown
}

// NewVarResolver returns a resolver for the `var` scheme. It memoizes per
// name, so every occurrence of `${var:uuid}` in one walk (the URL and a header)
// gets the same value; a new resolver gives new values. Any other scheme is
// skipped. now is the run's clock, rnd its randomness (crypto/rand in
// production).
func NewVarResolver(now time.Time, rnd io.Reader) ResolveFunc {
	var (
		mu     sync.Mutex
		values = map[string]string{}
	)

	return func(_ context.Context, scheme, name string) (string, error) {
		if scheme != SchemeVar {
			return "", ErrSkip
		}

		mu.Lock()
		defer mu.Unlock()

		if value, ok := values[name]; ok {
			return value, nil
		}

		value, err := generateVar(now, rnd, name)
		if err != nil {
			return "", err
		}

		values[name] = value

		return value, nil
	}
}

func generateVar(now time.Time, rnd io.Reader, name string) (string, error) {
	switch name {
	case VarTimestamp:
		return strings.Replace(now.UTC().Format(timestampLayout), ".", "", 1), nil
	case VarISO8601:
		return now.UTC().Format(iso8601Layout), nil
	case VarUUID:
		id, err := uuid.NewRandomFromReader(rnd)
		if err != nil {
			return "", fmt.Errorf("%w: generating uuid: %w", Unresolvedf(SchemeVar, name), err)
		}

		return id.String(), nil
	case VarRandom:
		return randomString(rnd, name)
	default:
		return "", Unresolvedf(SchemeVar, name)
	}
}

// randomString draws randomLength characters from randomAlphabet with
// rejection sampling, so every character is equally likely.
func randomString(rnd io.Reader, name string) (string, error) {
	const limit = 256 - 256%len(randomAlphabet)

	out := make([]byte, 0, randomLength)
	buf := make([]byte, randomLength*2)

	for len(out) < randomLength {
		if _, err := io.ReadFull(rnd, buf); err != nil {
			return "", fmt.Errorf("%w: generating random: %w", Unresolvedf(SchemeVar, name), err)
		}

		for _, b := range buf {
			if int(b) >= limit || len(out) == randomLength {
				continue
			}

			out = append(out, randomAlphabet[int(b)%len(randomAlphabet)])
		}
	}

	return string(out), nil
}

// newRunVarResolver is the production var resolver: the wall clock and
// crypto/rand.
func newRunVarResolver() ResolveFunc {
	return NewVarResolver(time.Now(), rand.Reader)
}
