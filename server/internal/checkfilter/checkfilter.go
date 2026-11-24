// Package checkfilter bounds and normalizes the `checkUid` list filter that
// `/results`, `/incidents` and their MCP tools accept as a comma-separated
// list of check UIDs or slugs. Each identifier costs a lookup, so the raw list
// is capped before anything else touches it.
package checkfilter

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// MaxIdentifiers caps the raw entries of one `checkUid` list filter. A
// dashboard filter never needs more; dash0 sends a handful at most.
const MaxIdentifiers = 100

// ErrTooManyIdentifiers is returned when a `checkUid` list has more raw
// entries than MaxIdentifiers.
// Keep the number in the message in sync with MaxIdentifiers.
var ErrTooManyIdentifiers = errors.New("checkUid accepts at most 100 identifiers")

// Split splits a raw comma-separated `checkUid` value. It enforces the cap on
// the raw split, before any trimming or dedup, so empty and repeated entries
// count too. An empty value yields nil.
func Split(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}

	// Count first so an oversized value is refused without allocating the split.
	if strings.Count(raw, ",")+1 > MaxIdentifiers {
		return nil, ErrTooManyIdentifiers
	}

	return strings.Split(raw, ","), nil
}

// CheckCount returns ErrTooManyIdentifiers when an already split list exceeds
// the cap.
func CheckCount(identifiers []string) error {
	if len(identifiers) > MaxIdentifiers {
		return ErrTooManyIdentifiers
	}

	return nil
}

// Normalize is the resolvers' backstop for callers that bypass Split: it keeps
// the first MaxIdentifiers raw entries, trims them, drops empty ones and
// dedups the rest while keeping their order, so each distinct identifier is
// looked up once.
func Normalize(identifiers []string) []string {
	if len(identifiers) > MaxIdentifiers {
		identifiers = identifiers[:MaxIdentifiers]
	}

	seen := make(map[string]struct{}, len(identifiers))
	out := make([]string, 0, len(identifiers))

	for _, id := range identifiers {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		if _, dup := seen[id]; dup {
			continue
		}

		seen[id] = struct{}{}
		out = append(out, id)
	}

	return out
}

const (
	// MaxSearchBytes caps the bytes of a check search `q` that are used.
	MaxSearchBytes = 256
	// MaxSearchTerms caps the distinct words of a check search `q`.
	MaxSearchTerms = 8
)

// SearchTerms turns a raw check search `q` into the bounded list of distinct
// words to match. It truncates q to MaxSearchBytes on a rune boundary, splits
// on whitespace, drops duplicates and keeps the first MaxSearchTerms words.
func SearchTerms(raw string) []string {
	if len(raw) > MaxSearchBytes {
		cut := MaxSearchBytes
		for cut > 0 && !utf8.RuneStart(raw[cut]) {
			cut--
		}

		raw = raw[:cut]
	}

	words := strings.Fields(raw)
	terms := make([]string, 0, min(len(words), MaxSearchTerms))
	seen := make(map[string]struct{}, MaxSearchTerms)

	for _, word := range words {
		if _, dup := seen[word]; dup {
			continue
		}

		seen[word] = struct{}{}

		terms = append(terms, word)
		if len(terms) == MaxSearchTerms {
			break
		}
	}

	return terms
}
