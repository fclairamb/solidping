package i18n

import (
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
		"day", local.Day(),
		"year", local.Year(),
		"time", local.Format(clockLayout),
		"zone", zoneLabel(local),
	)
}

// Day renders the calendar date of an instant in the locale's timezone,
// without the time: "Jul 12", "12 juil.", "12. Juli", "12 jul". The year is
// added only when it is not the current one. Same inputs as Date.
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

	return l.T(key, "month", l.monthName(local.Month(), "short"), "day", local.Day(), "year", local.Year())
}

// Month renders the month of an instant, in the locale's timezone, with its
// year: "July 2026", "juillet 2026", "Juli 2026", "julio de 2026". Same
// inputs as Date.
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

// formatCount renders a Tn count: no fraction digits for a whole number, up to
// two otherwise.
func (l Locale) formatCount(n float64) string {
	const maxDecimals = 2

	return l.printer().Sprint(number.Decimal(n, number.MaxFractionDigits(maxDecimals)))
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
