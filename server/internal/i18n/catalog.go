package i18n

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
)

//go:embed locales
var localesFS embed.FS

// errNotAString rejects a catalog leaf that is a number, a list or null.
var errNotAString = errors.New("catalog values must be strings or objects")

// catalogs maps a language to its flattened keys ("common.footer.dashboard")
// and values. Loaded once, when the package initializes: the files are
// embedded, so a malformed one is a build-time bug and panics on the first
// boot or test instead of shipping.
//
//nolint:gochecknoglobals // immutable after package initialization.
var catalogs = mustLoadCatalogs(localesFS)

// mustLoadCatalogs reads every locales/<lang>/<namespace>.json.
func mustLoadCatalogs(fsys fs.FS) map[string]map[string]string {
	out, err := loadCatalogs(fsys)
	if err != nil {
		panic(fmt.Sprintf("i18n: %v", err))
	}

	return out
}

func loadCatalogs(fsys fs.FS) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(Supported()))

	for _, lang := range Supported() {
		entries, err := fs.ReadDir(fsys, path.Join("locales", lang))
		if err != nil {
			return nil, fmt.Errorf("reading locales/%s: %w", lang, err)
		}

		keys := map[string]string{}

		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".json") {
				continue
			}

			raw, readErr := fs.ReadFile(fsys, path.Join("locales", lang, name))
			if readErr != nil {
				return nil, fmt.Errorf("reading %s/%s: %w", lang, name, readErr)
			}

			if err := flattenCatalog(strings.TrimSuffix(name, ".json"), raw, keys); err != nil {
				return nil, fmt.Errorf("%s/%s: %w", lang, name, err)
			}
		}

		out[lang] = keys
	}

	return out, nil
}

// flattenCatalog parses one namespace file and adds its leaves to into, keyed
// "<namespace>.<dotted.path>". Every leaf must be a string.
func flattenCatalog(namespace string, raw []byte, into map[string]string) error {
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	return flattenInto(namespace, tree, into)
}

func flattenInto(prefix string, tree map[string]any, into map[string]string) error {
	for key, value := range tree {
		full := prefix + "." + key

		switch typed := value.(type) {
		case string:
			into[full] = typed
		case map[string]any:
			if err := flattenInto(full, typed, into); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %s is %T", errNotAString, full, value)
		}
	}

	return nil
}

// missingKeys records every "<lang>:<key>" a render asked for and did not
// find, so each one is logged once and tests can assert there are none.
//
//nolint:gochecknoglobals // process-wide record, guarded by its own mutex.
var missingKeys = struct {
	sync.Mutex

	seen map[string]struct{}
}{seen: map[string]struct{}{}}

// MissingKeys lists, sorted, every "<lang>:<key>" looked up since the process
// started that its catalog did not have. Tests fail on it; production logs
// each entry once and renders the English value instead.
func MissingKeys() []string {
	missingKeys.Lock()
	defer missingKeys.Unlock()

	out := make([]string, 0, len(missingKeys.seen))
	for key := range missingKeys.seen {
		out = append(out, key)
	}

	sort.Strings(out)

	return out
}

func recordMissing(lang, key string) {
	id := lang + ":" + key

	missingKeys.Lock()
	_, seen := missingKeys.seen[id]
	missingKeys.seen[id] = struct{}{}
	missingKeys.Unlock()

	if !seen {
		slog.Warn("i18n: missing catalog key, rendering the English value", "lang", lang, "key", key)
	}
}

// lookup returns the raw catalog value of key in the locale's language,
// falling back to English and then to the key itself.
func (l Locale) lookup(key string) string {
	cats := l.cats
	if cats == nil {
		cats = catalogs
	}

	lang := l.Lang()
	if value, ok := cats[lang][key]; ok {
		return value
	}

	recordMissing(lang, key)

	if value, ok := cats[LangEnglish][key]; ok {
		return value
	}

	if lang != LangEnglish {
		recordMissing(LangEnglish, key)
	}

	return key
}
