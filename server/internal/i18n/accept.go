package i18n

import "golang.org/x/text/language"

// FromAcceptLanguage is the most preferred language of an Accept-Language
// header that has a catalog, "" when none has one (or the header is absent or
// malformed).
func FromAcceptLanguage(header string) string {
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return ""
	}

	for _, tag := range tags {
		if lang := Normalize(tag.String()); lang != "" {
			return lang
		}
	}

	return ""
}

// Has reports whether key exists in the English catalog, the one every other
// language mirrors. It lets a caller with an open-ended key space (an event
// type) fall back to its own text without recording a missing key.
func (l Locale) Has(key string) bool {
	cats := l.cats
	if cats == nil {
		cats = catalogs
	}

	_, ok := cats[LangEnglish][key]

	return ok
}
