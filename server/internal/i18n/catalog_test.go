package i18n

import (
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"unicode"

	"github.com/stretchr/testify/require"
)

// The catalog guards mirror web/dash0/src/locales/*.test.ts: key parity,
// placeholder parity, untranslated values (with an allowlist that cannot
// rot) and, specific to Go catalogs, plural leaf pairing. Each guard is a pure
// function run against the real catalogs, then against a fixture that must
// fail it (the positive control): a guard that cannot fail proves nothing.

// identicalAllowlist names the "<key>" whose value is deliberately the same as
// English in the listed languages. Only for a brand, a technical term the
// language uses as is, or a word spelled the same. Never to silence a string
// nobody translated. Same rules as dash0's identical-allowlist.ts.
//
//nolint:gochecknoglobals // test table.
var identicalAllowlist = map[string][]string{
	"common.header.logoAlt":   {LangFrench, LangGerman}, // "Logo"
	"common.footer.dashboard": {LangGerman},             // "Dashboard"
	"common.months.long.apr":  {LangGerman},             // "April"
	"common.months.long.aug":  {LangGerman},             // "August"
	"common.months.long.sep":  {LangGerman},             // "September"
	"common.months.long.nov":  {LangGerman},             // "November"
}

func otherLangs() []string {
	return []string{LangFrench, LangGerman, LangSpanish}
}

// missingAndExtra compares the key set of other against en.
func missingAndExtra(en, other map[string]string) ([]string, []string) {
	var missing, extra []string

	for key := range en {
		if _, ok := other[key]; !ok {
			missing = append(missing, key)
		}
	}

	for key := range other {
		if _, ok := en[key]; !ok {
			extra = append(extra, key)
		}
	}

	sort.Strings(missing)
	sort.Strings(extra)

	return missing, extra
}

var (
	placeholderRE = regexp.MustCompile(`\{[A-Za-z0-9_]+\}`)
	tagRE         = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9]*`)
)

// tokens is the sorted, de-duplicated set of {placeholders} and markup tags of
// a value. Word order may change in translation; the set may not.
func tokens(value string) string {
	found := append(placeholderRE.FindAllString(value, -1), tagRE.FindAllString(value, -1)...)
	sort.Strings(found)

	return strings.Join(compactStrings(found), "|")
}

func compactStrings(sorted []string) []string {
	out := make([]string, 0, len(sorted))

	for i, value := range sorted {
		if i == 0 || value != sorted[i-1] {
			out = append(out, value)
		}
	}

	return out
}

func tokenMismatches(en, other map[string]string) []string {
	var out []string

	for key, enValue := range en {
		value, ok := other[key]
		if !ok {
			continue
		}

		if tokens(enValue) != tokens(value) {
			out = append(out, key+" (en "+tokens(enValue)+", got "+tokens(value)+")")
		}
	}

	sort.Strings(out)

	return out
}

// hasTranslatableText: a value with no letters outside its placeholders
// ("{month} {day}, {time} {zone}") has nothing to translate.
func hasTranslatableText(value string) bool {
	stripped := placeholderRE.ReplaceAllString(value, "")

	return strings.ContainsFunc(stripped, unicode.IsLetter)
}

func untranslated(en, other map[string]string, lang string, allowlist map[string][]string) []string {
	var out []string

	for key, enValue := range en {
		if !hasTranslatableText(enValue) || other[key] != enValue {
			continue
		}

		if containsString(allowlist[key], lang) {
			continue
		}

		out = append(out, key)
	}

	sort.Strings(out)

	return out
}

func staleAllowlistEntries(cats map[string]map[string]string, lang string, allowlist map[string][]string) []string {
	var out []string

	for key, langs := range allowlist {
		if !containsString(langs, lang) {
			continue
		}

		enValue, ok := cats[LangEnglish][key]
		if !ok || cats[lang][key] != enValue {
			out = append(out, key)
		}
	}

	sort.Strings(out)

	return out
}

// unpairedPluralLeaves lists every x_one without an x_other and the reverse.
func unpairedPluralLeaves(keys map[string]string) []string {
	var out []string

	for key := range keys {
		if base, ok := strings.CutSuffix(key, pluralOne); ok {
			if _, paired := keys[base+pluralOther]; !paired {
				out = append(out, key)
			}
		}

		if base, ok := strings.CutSuffix(key, pluralOther); ok {
			if _, paired := keys[base+pluralOne]; !paired {
				out = append(out, key)
			}
		}
	}

	sort.Strings(out)

	return out
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}

	return false
}

func TestCatalogs_EveryLanguageIsLoaded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, lang := range Supported() {
		r.NotEmpty(catalogs[lang], lang)
		r.Contains(catalogs[lang], "common.footer.unsubscribe", lang)
	}
}

func TestCatalogs_KeyParity(t *testing.T) {
	t.Parallel()

	for _, lang := range otherLangs() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			missing, extra := missingAndExtra(catalogs[LangEnglish], catalogs[lang])
			r.Empty(missing, "keys en has and %s lacks", lang)
			r.Empty(extra, "keys %s has and en lacks", lang)
		})
	}

	t.Run("positive control", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		missing, extra := missingAndExtra(
			map[string]string{"common.a": "A", "common.b": "B"},
			map[string]string{"common.a": "A", "common.c": "C"},
		)
		r.Equal([]string{"common.b"}, missing)
		r.Equal([]string{"common.c"}, extra)
	})
}

func TestCatalogs_PlaceholderParity(t *testing.T) {
	t.Parallel()

	for _, lang := range otherLangs() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			require.Empty(t, tokenMismatches(catalogs[LangEnglish], catalogs[lang]))
		})
	}

	t.Run("positive control", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		got := tokenMismatches(
			map[string]string{
				"common.dropped": "Stop alerts for {check}",
				"common.renamed": "Down since {since}",
				"common.tag":     "Down since <strong>{since}</strong>",
				"common.moved":   "{a} then {b}",
			},
			map[string]string{
				"common.dropped": "Ne plus recevoir d'alertes",
				"common.renamed": "En panne depuis {depuis}",
				"common.tag":     "En panne depuis {since}",
				"common.moved":   "{b} puis {a}",
			},
		)
		r.Len(got, 3)
		r.Contains(got[0], "common.dropped")
		r.Contains(got[1], "common.renamed")
		r.Contains(got[2], "common.tag")
	})
}

func TestCatalogs_NothingLeftUntranslated(t *testing.T) {
	t.Parallel()

	for _, lang := range otherLangs() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Empty(untranslated(catalogs[LangEnglish], catalogs[lang], lang, identicalAllowlist),
				"identical to en and not in identicalAllowlist")
			r.Empty(staleAllowlistEntries(catalogs, lang, identicalAllowlist),
				"identicalAllowlist entries that are no longer identical (or gone)")
		})
	}

	t.Run("positive control", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		en := map[string]string{
			"common.copied":  "View incident",
			"common.brand":   "Logo",
			"common.pattern": "{month} {day}",
		}
		fr := map[string]string{
			"common.copied":  "View incident",
			"common.brand":   "Logo",
			"common.pattern": "{month} {day}",
		}
		allowlist := map[string][]string{"common.brand": {LangFrench}, "common.gone": {LangFrench}}

		r.Equal([]string{"common.copied"}, untranslated(en, fr, LangFrench, allowlist))
		r.Equal([]string{"common.gone"}, staleAllowlistEntries(
			map[string]map[string]string{LangEnglish: en, LangFrench: fr}, LangFrench, allowlist))
	})
}

func TestCatalogs_PluralLeavesComeInPairs(t *testing.T) {
	t.Parallel()

	for _, lang := range Supported() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			require.Empty(t, unpairedPluralLeaves(catalogs[lang]))
		})
	}

	t.Run("positive control", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, []string{"common.lonely_one", "common.orphan_other"}, unpairedPluralLeaves(map[string]string{
			"common.ok_one":       "{count} check",
			"common.ok_other":     "{count} checks",
			"common.lonely_one":   "{count} failure",
			"common.orphan_other": "{count} regions",
		}))
	})
}

func TestLoadCatalogs_RejectsBrokenFiles(t *testing.T) {
	t.Parallel()

	valid := `{"a": {"b": "B"}}`

	fixture := func(fr string) fstest.MapFS {
		files := fstest.MapFS{}
		for _, lang := range Supported() {
			files["locales/"+lang+"/common.json"] = &fstest.MapFile{Data: []byte(valid)}
		}

		files["locales/fr/common.json"] = &fstest.MapFile{Data: []byte(fr)}

		return files
	}

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		cats, err := loadCatalogs(fixture(valid))
		r.NoError(err)
		r.Equal("B", cats[LangFrench]["common.a.b"])
	})

	for name, content := range map[string]string{
		"malformed JSON":  `{"a": `,
		"number leaf":     `{"a": 3}`,
		"list leaf":       `{"a": ["x"]}`,
		"null leaf":       `{"a": null}`,
		"top-level array": `["a"]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := loadCatalogs(fixture(content))
			require.Error(t, err)
		})
	}

	t.Run("missing language directory", func(t *testing.T) {
		t.Parallel()

		files := fixture(valid)
		delete(files, "locales/es/common.json")

		_, err := loadCatalogs(files)
		require.Error(t, err)
	})
}
