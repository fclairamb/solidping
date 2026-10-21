// Package i18n localizes the text SolidPing renders on the server: emails
// first (spec 2026-10-21-01), in English, French, German and Spanish, the same
// set as the dashboard.
//
// A Locale is a language plus a timezone. Its zero value is English in UTC,
// which is also what every unknown or unset preference falls back to. Strings
// live in embedded JSON catalogs (locales/<lang>/<namespace>.json), looked up
// by dotted key ("common.footer.unsubscribe"). Dates, durations and numbers are
// formatted by the Locale so that a template never prints an English month or
// a dot decimal separator to a French reader.
//
// The phase-B guide for adding strings is wiki/conventions/email-i18n.md.
package i18n

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// Supported language codes.
const (
	LangEnglish = "en"
	LangFrench  = "fr"
	LangGerman  = "de"
	LangSpanish = "es"
)

// Supported lists every language a catalog exists for, English first. Same
// set as dash0 and status0.
func Supported() []string {
	return []string{LangEnglish, LangFrench, LangGerman, LangSpanish}
}

// Normalize maps a BCP 47 tag ("fr-FR", "FR_ca", " de ") to a supported
// language code, or "" when the language is not supported. Only the primary
// subtag counts, case-insensitively.
func Normalize(tag string) string {
	lang := strings.ToLower(strings.TrimSpace(tag))
	if idx := strings.IndexAny(lang, "-_"); idx >= 0 {
		lang = lang[:idx]
	}

	switch lang {
	case LangEnglish, LangFrench, LangGerman, LangSpanish:
		return lang
	default:
		return ""
	}
}

// ErrUnknownTimezone is returned by ParseTimezone for a name the tz database
// does not know, an empty name, or "Local" (the server's own zone, which says
// nothing about the reader).
var ErrUnknownTimezone = errors.New("unknown timezone")

// locationCache keeps loaded zones: time.LoadLocation reads the tz database on
// every call, and a mail run asks for the same few zones over and over.
//
//nolint:gochecknoglobals // process-wide memo of an immutable lookup.
var locationCache sync.Map

// ParseTimezone resolves an IANA zone name ("Europe/Paris").
func ParseTimezone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" {
		return nil, ErrUnknownTimezone
	}

	if cached, ok := locationCache.Load(name); ok {
		loc, _ := cached.(*time.Location)

		return loc, nil
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, errors.Join(ErrUnknownTimezone, err)
	}

	locationCache.Store(name, loc)

	return loc, nil
}

// Locale is the language and timezone one message is rendered for. The zero
// value is English in UTC. Build one with New, never field by field.
type Locale struct {
	lang     string
	location *time.Location
	// now is the clock Date and Day compare against to decide whether the year
	// is worth printing. nil means time.Now; tests pin it with WithClock.
	now func() time.Time
	// cats replaces the embedded catalogs. nil in production; tests set it to
	// exercise keys the real catalogs do not have (plurals, markup).
	cats map[string]map[string]string
}

// Default is the locale used when nothing is known about the reader: English,
// UTC. It is the zero Locale.
//
//nolint:gochecknoglobals // a named zero value, never mutated.
var Default = Locale{}

// English is English in UTC, for the output other channels (Slack, Discord,
// SMS...) keep. Same value as Default, different intent: a producer shared
// with those channels keeps X() == XIn(i18n.English).
//
//nolint:gochecknoglobals // a named zero value, never mutated.
var English = Locale{}

// New builds a Locale from a stored preference. The language and the timezone
// fall back independently: an unsupported language gives English, an unknown
// or empty timezone gives UTC.
func New(locale, timezone string) Locale {
	out := Locale{lang: Normalize(locale)}

	if loc, err := ParseTimezone(timezone); err == nil {
		out.location = loc
	}

	return out
}

// Lang is the language code: "en", "fr", "de" or "es".
func (l Locale) Lang() string {
	if l.lang == "" {
		return LangEnglish
	}

	return l.lang
}

// Location is the timezone dates are shown in.
func (l Locale) Location() *time.Location {
	if l.location == nil {
		return time.UTC
	}

	return l.location
}

// Timezone is the IANA name of Location ("UTC" when unset), the form a job
// config stores.
func (l Locale) Timezone() string {
	return l.Location().String()
}

// WithClock returns a copy whose "current year" comes from now. Tests only.
func (l Locale) WithClock(now func() time.Time) Locale {
	l.now = now

	return l
}

func (l Locale) currentTime() time.Time {
	if l.now == nil {
		return time.Now()
	}

	return l.now()
}
