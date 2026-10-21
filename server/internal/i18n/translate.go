package i18n

import (
	"fmt"
	"html/template"
	"strings"
)

// Plural leaf suffixes: a plural key "x" is stored as "x_one" and "x_other".
const (
	pluralOne   = "_one"
	pluralOther = "_other"
	countParam  = "count"
)

// T returns the catalog string for key with its {name} placeholders filled
// from kv, alternating names and values: T("common.footer.stopAlerts",
// "check", name). A value is printed with fmt.Sprint; format dates, durations
// and numbers with the Locale methods first. A missing key renders the
// English value (and is recorded, see MissingKeys).
func (l Locale) T(key string, kv ...any) string {
	return interpolate(l.lookup(key), params(kv), false)
}

// Tn is T for a count-dependent string, stored as two leaves, key_one and
// key_other. French uses _one for 0 and 1, English, German and Spanish for 1
// only. {count} is filled with the localized count unless kv names it: the
// language's decimal separator, but no thousands separator, so a producer
// shared with other channels keeps printing "1234" like its old %d.
// count may be any integer or float type (a JSON round trip turns ints into
// float64); anything else picks _other.
func (l Locale) Tn(key string, count any, kv ...any) string {
	values := params(kv)
	form := pluralOther

	if n, ok := toFloat(count); ok {
		if isOne(l.Lang(), n) {
			form = pluralOne
		}

		if _, set := values[countParam]; !set {
			values[countParam] = l.formatCount(n)
		}
	}

	return interpolate(l.lookup(key+form), values, false)
}

// THTML is T for a catalog string that carries markup ("<strong>{since}</strong>").
// The catalog text is trusted; every value is HTML-escaped, except a
// template.HTML value (the output of the link helper), which is already safe.
// Never use it in a subject or text block: those are not HTML.
func (l Locale) THTML(key string, kv ...any) template.HTML {
	// The catalog is embedded and reviewed; every value is escaped by interpolate.
	return template.HTML(interpolate(l.lookup(key), params(kv), true))
}

// isOne reports whether count n takes the _one form in lang (CLDR cardinal
// rules, integer and decimal counts alike).
func isOne(lang string, n float64) bool {
	if lang == LangFrench {
		return n >= 0 && n < 2
	}

	return n == 1
}

// params turns an alternating name/value list into a map. A name that is not
// a string, or a trailing name without a value, is ignored: the placeholder
// then stays visible, which is what the render tests look for.
func params(kv []any) map[string]any {
	out := make(map[string]any, len(kv)/2)

	for i := 0; i+1 < len(kv); i += 2 {
		if name, ok := kv[i].(string); ok {
			out[name] = kv[i+1]
		}
	}

	return out
}

// interpolate replaces each {name} of raw with its value, in one pass, so a
// value that itself reads "{date}" (a check named so) is printed as is rather
// than substituted again. A {name} with no value is left in place.
func interpolate(raw string, values map[string]any, escape bool) string {
	if len(values) == 0 || !strings.Contains(raw, "{") {
		return raw
	}

	var out strings.Builder

	out.Grow(len(raw))

	for {
		start := strings.IndexByte(raw, '{')
		if start < 0 {
			out.WriteString(raw)

			return out.String()
		}

		end := strings.IndexByte(raw[start:], '}')
		if end < 0 {
			out.WriteString(raw)

			return out.String()
		}

		end += start
		name := raw[start+1 : end]

		value, found := values[name]
		if !found || !isPlaceholderName(name) {
			out.WriteString(raw[:start+1])
			raw = raw[start+1:]

			continue
		}

		out.WriteString(raw[:start])
		out.WriteString(render(value, escape))
		raw = raw[end+1:]
	}
}

func isPlaceholderName(name string) bool {
	if name == "" {
		return false
	}

	for _, char := range name {
		isLetter := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
		if !isLetter && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}

	return true
}

func render(value any, escape bool) string {
	var text string

	switch typed := value.(type) {
	case template.HTML:
		return string(typed)
	case nil:
		return ""
	case string:
		text = typed
	case *string:
		if typed != nil {
			text = *typed
		}
	default:
		text = fmt.Sprint(value)
	}

	if escape {
		return template.HTMLEscapeString(text)
	}

	return text
}
