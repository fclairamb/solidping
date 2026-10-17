// Package catalog is the embedded list of vendors whose status page serves the
// Atlassian Statuspage v2 summary (/api/v2/summary.json). It feeds the picker
// of the vendor-status check form and the default name of a check.
package catalog

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed catalog.json
var raw []byte

// Entry is one vendor of the catalog.
type Entry struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Category string `json:"category"`
}

//nolint:gochecknoglobals // parse-once cache of an embedded file
var (
	once    sync.Once
	entries []Entry
)

// All returns the catalog, in file order. The slice must not be modified.
func All() []Entry {
	once.Do(func() {
		if err := json.Unmarshal(raw, &entries); err != nil {
			panic("checkvendorstatus: invalid catalog.json: " + err.Error())
		}
	})

	return entries
}

// Find returns the entry with the given slug.
func Find(slug string) (Entry, bool) {
	entries := All()
	for i := range entries {
		if entries[i].Slug == slug {
			return entries[i], true
		}
	}

	return Entry{}, false
}
