package i18n

import (
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"
)

// printers formats numbers per language (decimal and grouping separators,
// percent sign spacing) from CLDR data. A Printer copies itself per call, so
// sharing one is safe.
//
//nolint:gochecknoglobals // immutable after package initialization.
var printers = map[string]*message.Printer{
	LangEnglish: message.NewPrinter(language.English),
	LangFrench:  message.NewPrinter(language.French),
	LangGerman:  message.NewPrinter(language.German),
	LangSpanish: message.NewPrinter(language.Spanish),
}

// monthKeys are the catalog leaf names of the months, January first.
//
//nolint:gochecknoglobals // constant table; Go has no const arrays.
var monthKeys = [12]string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}

const (
	hoursPerDay = 24
	clockLayout = "15:04"
)

// Date renders an instant in the locale's timezone, 24-hour clock, zone
// abbreviation included: "Jul 5, 12:00 CEST", "5 juil., 12:00 CEST",
// "5. Juli, 12:00 CEST", "5 jul, 12:00 CEST". The year is added only when it
// is not the current one. A numeric zone ("+03") reads "UTC+03".
//
// value may be a time.Time, a *time.Time or an RFC 3339 string (what a time
// becomes after a job's JSON round trip). Any other string is returned as is,
// so a producer that still pre-formats keeps rendering; nil and the zero time
// give "".
func (l Locale) Date(value any) string {
	when, raw, ok := toTime(value)
	if !ok {
		return raw
	}

	local := when.In(l.Location())

	key := "common.date.dateTime"
	if !l.isCurrentYear(local) {
		key = "common.date.dateTimeYear"
	}

	return l.T(key,
		"month", l.monthName(local.Month(), "short"),
		"day", l.dayOfMonth(local.Day()),
		"year", local.Year(),
		"time", local.Format(clockLayout),
		"zone", zoneLabel(local),
	)
}

// Day renders the calendar date of an instant in the locale's timezone,
// without the time: "Jul 12", "12 juil.", "12. Juli", "12 jul". The year is
// added only when it is not the current one. Same inputs as Date. For a day
// anchored to another zone (a UTC day strip), see In.
func (l Locale) Day(value any) string {
	when, raw, ok := toTime(value)
	if !ok {
		return raw
	}

	local := when.In(l.Location())

	key := "common.date.day"
	if !l.isCurrentYear(local) {
		key = "common.date.dayYear"
	}

	return l.T(key, "month", l.monthName(local.Month(), "short"), "day", l.dayOfMonth(local.Day()), "year", local.Year())
}

// dayOfMonth is the day number as a date prints it. French writes the first
// of the month as an ordinal: "1er juil.", then "2 juil.".
func (l Locale) dayOfMonth(day int) any {
	if day == 1 && l.Lang() == LangFrench {
		return "1er"
	}

	return day
}

// Month renders the month of an instant, in the locale's timezone, with its
// year: "July 2026", "juillet 2026", "Juli 2026", "julio de 2026". Same
// inputs as Date. For a month anchored to another zone (a report period),
// see In.
func (l Locale) Month(value any) string {
	when, raw, ok := toTime(value)
	if !ok {
		return raw
	}

	local := when.In(l.Location())

	return l.T("common.date.month", "month", l.monthName(local.Month(), "long"), "year", local.Year())
}

// Duration renders a length of time with localized units: "1h 5m",
// "1 h 5 min", "1 Std. 5 Min.", "1 h 5 min". Seconds are kept below the hour
// and dropped above it, minutes are dropped above a day; under a second reads
// "0s".
//
// value may be a time.Duration, an integer or float count of NANOSECONDS (a
// Duration after a JSON round trip), or a string time.ParseDuration accepts.
// Any other string is returned as is; nil gives "".
func (l Locale) Duration(value any) string {
	span, raw, ok := toDuration(value)
	if !ok {
		return raw
	}

	if span < time.Second {
		return l.unit("seconds", 0)
	}

	span = span.Round(time.Second)

	var (
		major, minor           string
		majorValue, minorValue int
	)

	switch {
	case span < time.Minute:
		return l.unit("seconds", int(span.Seconds()))
	case span < time.Hour:
		major, majorValue = "minutes", int(span.Minutes())
		minor, minorValue = "seconds", int(span.Seconds())%60
	case span < hoursPerDay*time.Hour:
		major, majorValue = "hours", int(span.Hours())
		minor, minorValue = "minutes", int(span.Minutes())%60
	default:
		major, majorValue = "days", int(span.Hours())/hoursPerDay
		minor, minorValue = "hours", int(span.Hours())%hoursPerDay
	}

	if minorValue == 0 {
		return l.unit(major, majorValue)
	}

	return l.unit(major, majorValue) + " " + l.unit(minor, minorValue)
}

// Number renders a number with exactly decimals fraction digits and the
// language's separators: "1,234.5" in English, "1 234,5" in French. value may
// be any integer or float type, or a numeric string. Any other string is
// returned as is; anything else gives "".
func (l Locale) Number(value any, decimals int) string {
	n, ok := toFloat(value)
	if !ok {
		text, _ := value.(string)

		return text
	}

	return l.printer().Sprint(number.Decimal(n,
		number.MinFractionDigits(decimals), number.MaxFractionDigits(decimals)))
}

// Pct renders a percentage, given in percent (99.95, not 0.9995), with
// exactly decimals fraction digits and the language's percent sign spacing:
// "99.95%" in English, "99,95 %" (no-break space) in French, German and
// Spanish. Same inputs as Number.
func (l Locale) Pct(value any, decimals int) string {
	n, ok := toFloat(value)
	if !ok {
		text, _ := value.(string)

		return text
	}

	const percent = 100

	return l.printer().Sprint(number.Percent(n/percent,
		number.MinFractionDigits(decimals), number.MaxFractionDigits(decimals)))
}

// Signed renders a number with an explicit sign, for a change between two
// periods: "+1.5", "-0.25", "+1,5" in French. A value that rounds to zero at
// decimals reads "+0". Same inputs as Number.
func (l Locale) Signed(value any, decimals int) string {
	n, ok := toFloat(value)
	if !ok {
		text, _ := value.(string)

		return text
	}

	factor := math.Pow(10, float64(decimals))
	rounded := math.Round(n*factor) / factor

	if rounded < 0 {
		return l.Number(rounded, decimals)
	}

	return "+" + l.Number(math.Abs(rounded), decimals)
}

// ListAnd joins items as "a, b and c" ("a, b y c", "a e Ibiza" in Spanish).
func (l Locale) ListAnd(items any) string {
	return l.list(items, "common.list.and", isSpanishISound)
}

// ListOr joins items as "a, b or c" ("a, b o c", "a u Okta" in Spanish).
func (l Locale) ListOr(items any) string {
	return l.list(items, "common.list.or", isSpanishOSound)
}

// list joins items with ", " and puts the conjunction of key before the last
// one. Spanish swaps y/o for e/u before a word starting with that vowel sound;
// key+"BeforeVowel" holds that form (the plain one in other languages).
// items may be a []string or the []any a job's JSON round trip produces.
func (l Locale) list(items any, key string, vowelSound func(string) bool) string {
	words := toStrings(items)

	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}

	last := words[len(words)-1]
	if l.Lang() == LangSpanish && vowelSound(last) {
		key += "BeforeVowel"
	}

	return l.T(key, "head", strings.Join(words[:len(words)-1], ", "), "last", last)
}

// isSpanishISound: "y" becomes "e" before /i/ ("i", "hi"), but not before a
// diphthong ("hie", "hia": "agua y hielo").
func isSpanishISound(word string) bool {
	lower := strings.ToLower(strings.TrimSpace(word))
	for _, prefix := range []string{"i", "í", "hi", "hí"} {
		rest, ok := strings.CutPrefix(lower, prefix)
		if !ok {
			continue
		}

		return !strings.HasPrefix(prefix, "h") || !strings.ContainsAny(firstRune(rest), "aeiouáéó")
	}

	return false
}

// isSpanishOSound: "o" becomes "u" before /o/ ("o", "ho").
func isSpanishOSound(word string) bool {
	lower := strings.ToLower(strings.TrimSpace(word))
	for _, prefix := range []string{"o", "ó", "ho", "hó"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	return false
}

func firstRune(text string) string {
	for _, char := range text {
		return string(char)
	}

	return ""
}

// toStrings accepts the shapes a list reaches a template in.
func toStrings(items any) []string {
	switch typed := items.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}

		return out
	default:
		return nil
	}
}

// formatCount renders a Tn count: no fraction digits for a whole number, up to
// two otherwise, and no thousands separator (see Tn).
func (l Locale) formatCount(n float64) string {
	const maxDecimals = 2

	return l.printer().Sprint(number.Decimal(n, number.MaxFractionDigits(maxDecimals), number.NoSeparator()))
}

func (l Locale) printer() *message.Printer {
	return printers[l.Lang()]
}

func (l Locale) unit(name string, value int) string {
	return l.T("common.duration."+name, "n", value)
}

func (l Locale) monthName(month time.Month, form string) string {
	return l.T("common.months." + form + "." + monthKeys[month-1])
}

func (l Locale) isCurrentYear(local time.Time) bool {
	return local.Year() == l.currentTime().In(l.Location()).Year()
}

// zoneLabel is the zone abbreviation of a time, "UTC+03" for the zones the tz
// database names by offset only.
func zoneLabel(local time.Time) string {
	abbr := local.Format("MST")
	if strings.HasPrefix(abbr, "+") || strings.HasPrefix(abbr, "-") {
		return "UTC" + abbr
	}

	return abbr
}

// toTime accepts the shapes a date reaches a template in. ok is false when
// there is nothing to format; raw is then what to print instead.
func toTime(value any) (time.Time, string, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, "", !typed.IsZero()
	case *time.Time:
		if typed == nil {
			return time.Time{}, "", false
		}

		return *typed, "", !typed.IsZero()
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err != nil {
			return time.Time{}, typed, false
		}

		return parsed, "", !parsed.IsZero()
	default:
		return time.Time{}, "", false
	}
}

// toDuration accepts the shapes a duration reaches a template in.
func toDuration(value any) (time.Duration, string, bool) {
	switch typed := value.(type) {
	case time.Duration:
		return typed, "", true
	case *time.Duration:
		if typed == nil {
			return 0, "", false
		}

		return *typed, "", true
	case string:
		parsed, err := time.ParseDuration(typed)
		if err != nil {
			return 0, typed, false
		}

		return parsed, "", true
	default:
		if n, ok := toFloat(value); ok {
			return time.Duration(n), "", true
		}

		return 0, "", false
	}
}

// toFloat accepts every integer and float type, and numeric strings.
func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)

		return parsed, err == nil
	default:
		return 0, false
	}
}
