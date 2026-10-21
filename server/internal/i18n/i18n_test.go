package i18n

import (
	"encoding/json"
	"html/template"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"en":      "en",
		"fr":      "fr",
		"de":      "de",
		"es":      "es",
		"fr-FR":   "fr",
		"FR_ca":   "fr",
		" de-AT ": "de",
		"es-419":  "es",
		"EN":      "en",
		"":        "",
		"it":      "",
		"pt-BR":   "",
		"-fr":     "",
		"french":  "",
	}

	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, want, Normalize(in))
		})
	}
}

func TestNew_FallsBackIndependently(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, locale, timezone string
		wantLang, wantTZ       string
	}{
		{"both set", "fr-FR", "Europe/Paris", LangFrench, "Europe/Paris"},
		{"unsupported language keeps the zone", "it", "Europe/Rome", LangEnglish, "Europe/Rome"},
		{"unknown zone keeps the language", "de", "Mars/Olympus", LangGerman, "UTC"},
		{"Local is refused", "es", "Local", LangSpanish, "UTC"},
		{"nothing", "", "", LangEnglish, "UTC"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			loc := New(tc.locale, tc.timezone)
			r.Equal(tc.wantLang, loc.Lang())
			r.Equal(tc.wantTZ, loc.Timezone())
		})
	}

	t.Run("zero value", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		r.Equal(LangEnglish, Default.Lang())
		r.Equal(time.UTC, Default.Location())
		r.Equal(LangEnglish, English.Lang())
	})
}

func TestParseTimezone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	loc, err := ParseTimezone("Europe/Paris")
	r.NoError(err)
	r.Equal("Europe/Paris", loc.String())

	again, err := ParseTimezone("Europe/Paris")
	r.NoError(err)
	r.Same(loc, again, "a zone is loaded once")

	for _, bad := range []string{"", "  ", "Local", "Europe/Atlantis"} {
		_, err := ParseTimezone(bad)
		r.ErrorIs(err, ErrUnknownTimezone, bad)
	}
}

func TestT_Interpolation(t *testing.T) {
	t.Parallel()

	fr := New(LangFrench, "")

	t.Run("fills placeholders", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "Ne plus recevoir les alertes pour API", fr.T("common.footer.stopAlerts", "check", "API"))
	})

	t.Run("a value is never substituted again", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "Stop alerts for {check} {date}",
			Default.T("common.footer.stopAlerts", "check", "{check} {date}", "date", "never"))
	})

	t.Run("an unfilled placeholder stays visible", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "Stop alerts for {check}", Default.T("common.footer.stopAlerts"))
		require.Equal(t, "Stop alerts for {check}", Default.T("common.footer.stopAlerts", "check"))
	})

	t.Run("nil and pointer values", func(t *testing.T) {
		t.Parallel()

		name := "API"
		require.Equal(t, "Stop alerts for API", Default.T("common.footer.stopAlerts", "check", &name))
		require.Equal(t, "Stop alerts for ", Default.T("common.footer.stopAlerts", "check", nil))
	})
}

func TestT_MissingKeyFallsBackToEnglishAndIsRecorded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := Locale{lang: LangGerman, cats: map[string]map[string]string{
		LangEnglish: {"test.onlyEnglish": "Only in English"},
		LangGerman:  {},
	}}

	r.Equal("Only in English", fixture.T("test.onlyEnglish"))
	r.Contains(MissingKeys(), "de:test.onlyEnglish")

	r.Equal("test.nowhere.at.all", New(LangFrench, "").T("test.nowhere.at.all"))
	r.Contains(MissingKeys(), "fr:test.nowhere.at.all")
	r.Contains(MissingKeys(), "en:test.nowhere.at.all")
}

// pluralFixture holds plural keys: the real common namespace has none yet.
func pluralFixture(lang string) Locale {
	return Locale{lang: lang, cats: map[string]map[string]string{
		LangEnglish: {"test.checks_one": "{count} check", "test.checks_other": "{count} checks"},
		LangFrench:  {"test.checks_one": "{count} contrôle", "test.checks_other": "{count} contrôles"},
		LangGerman:  {"test.checks_one": "{count} Check", "test.checks_other": "{count} Checks"},
		LangSpanish: {"test.checks_one": "{count} check", "test.checks_other": "{count} checks"},
	}}
}

func TestTn_PluralRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		lang  string
		count any
		want  string
	}{
		{LangEnglish, 0, "0 checks"},
		{LangEnglish, 1, "1 check"},
		{LangEnglish, 2, "2 checks"},
		// No thousands separator: a producer shared with other channels keeps
		// its old %d output.
		{LangEnglish, 1234, "1234 checks"},
		{LangFrench, 0, "0 contrôle"},
		{LangFrench, 1, "1 contrôle"},
		{LangFrench, 1.5, "1,5 contrôle"},
		{LangFrench, 2, "2 contrôles"},
		{LangFrench, 1234, "1234 contrôles"},
		{LangGerman, 0, "0 Checks"},
		{LangGerman, 1, "1 Check"},
		{LangGerman, 3, "3 Checks"},
		{LangSpanish, 0, "0 checks"},
		{LangSpanish, 1, "1 check"},
		{LangSpanish, 7, "7 checks"},
		// The job JSON round trip turns every int into a float64.
		{LangEnglish, float64(1), "1 check"},
		{LangEnglish, int64(5), "5 checks"},
		{LangEnglish, "1", "1 check"},
	}

	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, pluralFixture(tc.lang).Tn("test.checks", tc.count))
		})
	}

	t.Run("an explicit count wins", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "one check", pluralFixture(LangEnglish).Tn("test.checks", 1, "count", "one"))
	})

	t.Run("a non-numeric count picks other", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "{count} checks", pluralFixture(LangEnglish).Tn("test.checks", nil))
	})

	t.Run("isOne", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		r.True(isOne(LangFrench, 0))
		r.True(isOne(LangFrench, 1))
		r.False(isOne(LangFrench, 2))
		r.False(isOne(LangFrench, -1))

		for _, lang := range []string{LangEnglish, LangGerman, LangSpanish} {
			r.False(isOne(lang, 0), lang)
			r.True(isOne(lang, 1), lang)
			r.False(isOne(lang, 1.5), lang)
			r.False(isOne(lang, 2), lang)
		}
	})
}

func TestTHTML_EscapesValuesNotMarkup(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	loc := Locale{cats: map[string]map[string]string{
		LangEnglish: {"test.since": "Down since <strong>{since}</strong>, see {link}."},
	}}

	got := loc.THTML("test.since",
		"since", `<script>alert("x")</script> & co`,
		"link", template.HTML(`<a href="https://acme.com">here</a>`))

	r.Equal(template.HTML(`Down since <strong>&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; co</strong>, `+
		`see <a href="https://acme.com">here</a>.`), got)

	// T leaves the same value alone: html/template escapes it at render time,
	// and a subject is not HTML.
	r.Equal(`Stop alerts for A & B`, Default.T("common.footer.stopAlerts", "check", "A & B"))
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC) }
}

func TestDate(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	lastYear := time.Date(2025, 7, 5, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		lang, tz string
		value    any
		want     string
	}{
		{LangEnglish, "", when, "Jul 5, 10:00 UTC"},
		{LangEnglish, "Europe/Paris", when, "Jul 5, 12:00 CEST"},
		{LangFrench, "Europe/Paris", when, "5 juil., 12:00 CEST"},
		{LangGerman, "Europe/Paris", when, "5. Juli, 12:00 CEST"},
		{LangSpanish, "Europe/Paris", when, "5 jul, 12:00 CEST"},
		{LangEnglish, "Europe/Paris", lastYear, "Jul 5, 2025, 12:00 CEST"},
		{LangFrench, "Europe/Paris", lastYear, "5 juil. 2025, 12:00 CEST"},
		{LangGerman, "Europe/Paris", lastYear, "5. Juli 2025, 12:00 CEST"},
		{LangSpanish, "Europe/Paris", lastYear, "5 jul 2025, 12:00 CEST"},
		// Winter time, and a zone the tz database names by offset only.
		{LangFrench, "Europe/Paris", time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC), "15 janv., 10:30 CET"},
		{LangEnglish, "Europe/Istanbul", when, "Jul 5, 13:00 UTC+03"},
		{LangSpanish, "America/Sao_Paulo", when, "5 jul, 07:00 UTC-03"},
		// The reader's zone moves the date across midnight.
		{LangEnglish, "Pacific/Auckland", time.Date(2026, 7, 5, 22, 0, 0, 0, time.UTC), "Jul 6, 10:00 NZST"},
		// Shapes a template receives.
		{LangEnglish, "", &when, "Jul 5, 10:00 UTC"},
		{LangEnglish, "", "2026-07-05T10:00:00Z", "Jul 5, 10:00 UTC"},
		{LangEnglish, "", "2026-07-05T12:00:00+02:00", "Jul 5, 10:00 UTC"},
		{LangEnglish, "", "Jul 5, 10:00 UTC", "Jul 5, 10:00 UTC"},
		{LangEnglish, "", time.Time{}, ""},
		{LangEnglish, "", (*time.Time)(nil), ""},
		{LangEnglish, "", nil, ""},
		{LangEnglish, "", 42, ""},
	}

	for _, tc := range cases {
		t.Run(tc.lang+"/"+tc.tz+"/"+tc.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, New(tc.lang, tc.tz).WithClock(fixedClock()).Date(tc.value))
		})
	}
}

func TestDay(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	cases := map[string][2]string{
		LangEnglish: {"Jul 12", "Jul 12, 2027"},
		LangFrench:  {"12 juil.", "12 juil. 2027"},
		LangGerman:  {"12. Juli", "12. Juli 2027"},
		LangSpanish: {"12 jul", "12 jul 2027"},
	}

	for lang, want := range cases {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			loc := New(lang, "Europe/Paris").WithClock(fixedClock())
			r.Equal(want[0], loc.Day(when))
			r.Equal(want[1], loc.Day(when.AddDate(1, 0, 0)))
		})
	}

	t.Run("the reader's zone picks the day", func(t *testing.T) {
		t.Parallel()

		late := time.Date(2026, 7, 12, 23, 30, 0, 0, time.UTC)
		require.Equal(t, "Jul 13", New(LangEnglish, "Europe/Paris").WithClock(fixedClock()).Day(late))
	})
}

func TestMonth(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	cases := map[string]string{
		LangEnglish: "July 2026",
		LangFrench:  "juillet 2026",
		LangGerman:  "Juli 2026",
		LangSpanish: "julio de 2026",
	}

	for lang, want := range cases {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, want, New(lang, "").Month(when))
		})
	}

	t.Run("every month has both names", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		for _, lang := range Supported() {
			loc := New(lang, "")
			for month := time.January; month <= time.December; month++ {
				day := time.Date(2026, month, 1, 12, 0, 0, 0, time.UTC)
				r.NotContains(loc.Month(day), "common.", lang)
				r.NotContains(loc.Day(day), "common.", lang)
			}
		}
	})
}

func TestDuration(t *testing.T) {
	t.Parallel()

	// The English rows are the rounding rules notifications.mailDuration has
	// always applied; Duration must keep them.
	english := map[time.Duration]string{
		0:                      "0s",
		500 * time.Millisecond: "0s",
		-time.Minute:           "0s",
		45 * time.Second:       "45s",
		90 * time.Second:       "1m 30s",
		15 * time.Minute:       "15m",
		65 * time.Minute:       "1h 5m",
		2 * time.Hour:          "2h",
		2*time.Hour + 13*time.Minute + 7*time.Second: "2h 13m",
		26 * time.Hour: "1d 2h",
		48 * time.Hour: "2d",
		59*time.Minute + 59*time.Second + 600*time.Millisecond: "1h",
	}

	for span, want := range english {
		t.Run(want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, want, Default.Duration(span))
		})
	}

	localized := map[string][2]string{
		LangEnglish: {"1h 5m", "1d 2h"},
		LangFrench:  {"1 h 5 min", "1 j 2 h"},
		LangGerman:  {"1 Std. 5 Min.", "1 Tg. 2 Std."},
		LangSpanish: {"1 h 5 min", "1 d 2 h"},
	}

	for lang, want := range localized {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			loc := New(lang, "")
			r.Equal(want[0], loc.Duration(65*time.Minute))
			r.Equal(want[1], loc.Duration(26*time.Hour))
		})
	}

	t.Run("job round trip shapes", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		raw, err := json.Marshal(map[string]any{"d": 65 * time.Minute})
		r.NoError(err)

		var decoded map[string]any
		r.NoError(json.Unmarshal(raw, &decoded))

		r.Equal("1h 5m", Default.Duration(decoded["d"]))
		r.Equal("1h 5m", Default.Duration(int64(65*time.Minute)))
		r.Equal("1h 5m", Default.Duration("1h5m"))
		r.Equal("15 minutes", Default.Duration("15 minutes"))
		r.Empty(Default.Duration(nil))
	})
}

func TestNumberAndPct(t *testing.T) {
	t.Parallel()

	cases := map[string][3]string{
		LangEnglish: {"1,234.5", "99.95%", "99.950"},
		LangFrench:  {"1 234,5", "99,95 %", "99,950"},
		LangGerman:  {"1.234,5", "99,95 %", "99,950"},
		LangSpanish: {"1.234,5", "99,95 %", "99,950"},
	}

	for lang, want := range cases {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			loc := New(lang, "")
			r.Equal(want[0], loc.Number(1234.5, 1))
			r.Equal(want[1], loc.Pct(99.95, 2))
			r.Equal(want[2], loc.Number(99.95, 3))
		})
	}

	t.Run("input shapes", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		r.Equal("42", Default.Number(42, 0))
		r.Equal("42.00", Default.Number(int64(42), 2))
		r.Equal("3.1", Default.Number(float32(3.14), 1))
		r.Equal("99.9", Default.Number("99.94", 1))
		r.Equal("100%", Default.Pct(100, 0))
		r.Equal("n/a", Default.Number("n/a", 1))
		r.Empty(Default.Pct(nil, 1))
	})
}

func TestIn_AnchorsCalendarDatesToAZone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	paris, err := ParseTimezone("Europe/Paris")
	r.NoError(err)

	// The first instant of October in Paris is still September in New York.
	october := time.Date(2026, 10, 1, 0, 0, 0, 0, paris)
	reader := New(LangEnglish, "America/New_York").WithClock(fixedClock())

	r.Equal("September 2026", reader.Month(october), "the reader's zone")
	r.Equal("October 2026", reader.In(paris).Month(october))
	r.Equal("October 2026", reader.InZone("Europe/Paris").Month(october))
	r.Equal("Oct 1", reader.In(paris).Day(october))
	r.Equal("Sep 30", reader.In(time.UTC).Day(october), "a UTC day strip")
	r.Equal("Sep 30", reader.InZone("Mars/Olympus").Day(october), "an unknown zone gives UTC")
	r.Equal("Sep 30", reader.In(nil).Day(october), "nil gives UTC")
	r.Equal(LangEnglish, reader.In(paris).Lang(), "the language is kept")
	r.Equal("Sep 30", reader.Day(october), "In returns a copy")
}

func TestSigned(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("+1.50", Default.Signed(1.5, 2))
	r.Equal("-0.25", Default.Signed(-0.25, 2))
	r.Equal("+3", Default.Signed(3, 0))
	r.Equal("-1,234", Default.Signed(-1234, 0))
	r.Equal("+0.000", Default.Signed(-0.0004, 3), "a value that rounds to zero")
	r.Equal("+1,5", New(LangFrench, "").Signed(1.5, 1))
	r.Equal("-1,5", New(LangGerman, "").Signed(-1.5, 1))
	r.Equal("+2", Default.Signed(float64(2), 0), "a JSON round trip")
	r.Equal("n/a", Default.Signed("n/a", 1))
	r.Empty(Default.Signed(nil, 1))
}

func TestLists(t *testing.T) {
	t.Parallel()

	three := []string{"Google", "GitHub", "Microsoft"}

	cases := map[string][2]string{
		LangEnglish: {"Google, GitHub or Microsoft", "Google, GitHub and Microsoft"},
		LangFrench:  {"Google, GitHub ou Microsoft", "Google, GitHub et Microsoft"},
		LangGerman:  {"Google, GitHub oder Microsoft", "Google, GitHub und Microsoft"},
		LangSpanish: {"Google, GitHub o Microsoft", "Google, GitHub y Microsoft"},
	}

	for lang, want := range cases {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			loc := New(lang, "")
			r.Equal(want[0], loc.ListOr(three))
			r.Equal(want[1], loc.ListAnd(three))
		})
	}

	t.Run("spanish euphony", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		es := New(LangSpanish, "")
		r.Equal("Google u Okta", es.ListOr([]string{"Google", "Okta"}))
		r.Equal("GitHub u Horizon", es.ListOr([]string{"GitHub", "Horizon"}))
		r.Equal("París e Ibiza", es.ListAnd([]string{"París", "Ibiza"}))
		r.Equal("Madrid e Hiroshima", es.ListAnd([]string{"Madrid", "Hiroshima"}))
		r.Equal("agua y hielo", es.ListAnd([]string{"agua", "hielo"}), "not before a diphthong")
		r.Equal("Google or Okta", Default.ListOr([]string{"Google", "Okta"}), "Spanish only")
	})

	t.Run("shapes", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		r.Empty(Default.ListOr(nil))
		r.Empty(Default.ListOr([]string{}))
		r.Equal("Google", Default.ListOr([]string{"Google"}))
		r.Equal("a or b", Default.ListOr([]any{"a", "b"}), "a JSON round trip")
	})
}

func TestZoneLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	kathmandu, err := ParseTimezone("Asia/Kathmandu")
	r.NoError(err)

	when := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	r.Equal("UTC+0545", zoneLabel(when.In(kathmandu)))
	r.Equal("UTC", zoneLabel(when))
}
