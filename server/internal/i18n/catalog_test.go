package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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

// The identical allowlist names the keys whose value is deliberately the same
// as English in some languages. Only for a brand, a technical term the
// language uses as is, or a word spelled the same. Never to silence a string
// nobody translated. Same rules as dash0's identical-allowlist.ts.
//
// It lives in testdata/identical/<namespace>.json, one file per namespace, so
// two phase B groups never edit the same file:
//
//	{"<namespace>.<key>": {"<lang>": "<why>"}}
var (
	errAllowlistForeignKey = errors.New("allowlist key outside its file's namespace")
	errAllowlistLang       = errors.New("allowlist language must be fr, de or es")
	errAllowlistNoReason   = errors.New("allowlist entry without a reason")
)

// loadIdenticalAllowlist reads every <namespace>.json of fsys into
// key -> languages.
func loadIdenticalAllowlist(fsys fs.FS) (map[string][]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}

	out := map[string][]string{}

	for _, entry := range entries {
		namespace, isJSON := strings.CutSuffix(entry.Name(), ".json")
		if entry.IsDir() || !isJSON {
			continue
		}

		raw, readErr := fs.ReadFile(fsys, entry.Name())
		if readErr != nil {
			return nil, readErr
		}

		var file map[string]map[string]string
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}

		for key, langs := range file {
			if !strings.HasPrefix(key, namespace+".") {
				return nil, fmt.Errorf("%w: %s in %s", errAllowlistForeignKey, key, entry.Name())
			}

			for lang, why := range langs {
				if !containsString(otherLangs(), lang) {
					return nil, fmt.Errorf("%w: %s %q", errAllowlistLang, key, lang)
				}

				if strings.TrimSpace(why) == "" {
					return nil, fmt.Errorf("%w: %s %s", errAllowlistNoReason, key, lang)
				}

				out[key] = append(out[key], lang)
			}
		}
	}

	return out, nil
}

func identicalAllowlist(t *testing.T) map[string][]string {
	t.Helper()

	allowlist, err := loadIdenticalAllowlist(os.DirFS("testdata/identical"))
	require.NoError(t, err)

	return allowlist
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

		for key := range catalogs[lang] {
			// Reserved for the emailpreview positive control (locale_test.go).
			r.False(strings.HasPrefix(key, "control."), "%s: the control namespace is reserved", key)
		}
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

	allowlist := identicalAllowlist(t)

	for _, lang := range otherLangs() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Empty(untranslated(catalogs[LangEnglish], catalogs[lang], lang, allowlist),
				"identical to en and not in testdata/identical/<namespace>.json")
			r.Empty(staleAllowlistEntries(catalogs, lang, allowlist),
				"testdata/identical entries that are no longer identical (or gone)")
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

func TestIdenticalAllowlist_RejectsBrokenFiles(t *testing.T) {
	t.Parallel()

	file := func(content string) fstest.MapFS {
		return fstest.MapFS{"common.json": &fstest.MapFile{Data: []byte(content)}}
	}

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		got, err := loadIdenticalAllowlist(file(`{"common.logo": {"fr": "same word"}}`))
		r.NoError(err)
		r.Equal(map[string][]string{"common.logo": {LangFrench}}, got)
	})

	for name, tc := range map[string]struct {
		content string
		want    error
	}{
		"a key of another namespace": {`{"incident.logo": {"fr": "same word"}}`, errAllowlistForeignKey},
		"english":                    {`{"common.logo": {"en": "same word"}}`, errAllowlistLang},
		"an unknown language":        {`{"common.logo": {"it": "same word"}}`, errAllowlistLang},
		"no reason":                  {`{"common.logo": {"de": " "}}`, errAllowlistNoReason},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := loadIdenticalAllowlist(file(tc.content))
			require.ErrorIs(t, err, tc.want)
		})
	}

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()

		_, err := loadIdenticalAllowlist(file(`{"common.logo": ["fr"]}`))
		require.Error(t, err)
	})
}

// pluralOneWithoutCount lists every x_one leaf that does not print {count}.
// French reads _one for 0 too, so "One check" would say "Un contrôle"
// for zero checks.
func pluralOneWithoutCount(keys map[string]string) []string {
	var out []string

	for key, value := range keys {
		if strings.HasSuffix(key, pluralOne) && !strings.Contains(value, "{"+countParam+"}") {
			out = append(out, key)
		}
	}

	sort.Strings(out)

	return out
}

func TestCatalogs_PluralOneShowsTheCount(t *testing.T) {
	t.Parallel()

	for _, lang := range Supported() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			require.Empty(t, pluralOneWithoutCount(catalogs[lang]))
		})
	}

	t.Run("positive control", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, []string{"common.blind_one"}, pluralOneWithoutCount(map[string]string{
			"common.blind_one":   "One check is blind",
			"common.blind_other": "{count} checks are blind",
			"common.ok_one":      "{count} check",
		}))
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
