package models

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

// Translation is the full hand-written text of one entry in one language.
// Title is always set; Body (markdown) only applies to status updates and is
// empty on a publication title.
type Translation struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// Translations holds the per-language text a person wrote next to the literal
// column, keyed by language code ("fr"). An entry is complete or absent: the
// literal column is the page's own language and is never repeated here. nil
// (stored as NULL) means no translation.
type Translations map[string]Translation

// TranslationsPatch is the presence-aware PATCH field: Set tells an absent key
// (leave untouched) from a present one, whose Value replaces the whole map;
// null and {} both clear it.
type TranslationsPatch struct {
	Set   bool
	Value Translations
}

// UnmarshalJSON marks the field present, null included.
func (p *TranslationsPatch) UnmarshalJSON(data []byte) error {
	p.Set = true

	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		p.Value = nil

		return nil
	}

	return json.Unmarshal(data, &p.Value)
}

// MarshalJSON writes the replacement map (null when clearing).
func (p TranslationsPatch) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.Value)
}

// IsZero lets omitempty drop an absent patch.
func (p TranslationsPatch) IsZero() bool {
	return !p.Set
}

// Translation validation failures, in the order they are checked.
var (
	// ErrTranslationLanguage: the key is not a supported UI language code.
	ErrTranslationLanguage = errors.New("translation language is not supported")
	// ErrTranslationOwnLanguage: the entry is for the page's own language,
	// which the literal column already holds.
	ErrTranslationOwnLanguage = errors.New("translation for the page's own language is not allowed")
	// ErrTranslationTitleRequired: an entry must have a title.
	ErrTranslationTitleRequired = errors.New("translation title is required")
	// ErrTranslationTitleTooLong: the title exceeds the literal field's limit.
	ErrTranslationTitleTooLong = errors.New("translation title is too long")
	// ErrTranslationBodyRequired: the literal body is set, so the entry needs one.
	ErrTranslationBodyRequired = errors.New("translation body is required")
	// ErrTranslationBodyTooLong: the body exceeds the literal field's limit.
	ErrTranslationBodyTooLong = errors.New("translation body is too long")
	// ErrTranslationBodyNotAllowed: this kind of entry has no body.
	ErrTranslationBodyNotAllowed = errors.New("translation body is not allowed here")
)

// TranslationRules says what a Translations map is checked against.
type TranslationRules struct {
	// PageLanguage is the page's own language code ("" means English).
	PageLanguage string
	// MaxTitle / MaxBody are the literal fields' length limits (in bytes).
	MaxTitle, MaxBody int
	// WithBody is true for a status update (a body is part of an entry) and
	// false for a publication title.
	WithBody bool
	// LiteralBody is the literal body_markdown: when it is not empty, an entry
	// must carry a body too.
	LiteralBody string
}

// Validate checks every entry. The error is one of the ErrTranslation*
// values, wrapped with the offending language code by the caller if needed.
func (t Translations) Validate(rules TranslationRules) error {
	own := i18n.New(rules.PageLanguage, "").Lang()

	for lang, entry := range t {
		if i18n.Normalize(lang) != lang {
			return ErrTranslationLanguage
		}

		if lang == own {
			return ErrTranslationOwnLanguage
		}

		if entry.Title == "" {
			return ErrTranslationTitleRequired
		}

		if len(entry.Title) > rules.MaxTitle {
			return ErrTranslationTitleTooLong
		}

		if !rules.WithBody {
			if entry.Body != "" {
				return ErrTranslationBodyNotAllowed
			}

			continue
		}

		if entry.Body == "" && rules.LiteralBody != "" {
			return ErrTranslationBodyRequired
		}

		if len(entry.Body) > rules.MaxBody {
			return ErrTranslationBodyTooLong
		}
	}

	return nil
}
