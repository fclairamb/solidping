package app

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// openAPITagIndex is the minimal shape needed to compare the top-level `tags:`
// block against the tags the operations actually reference.
type openAPITagIndex struct {
	Info struct {
		License struct {
			Name string `yaml:"name"`
			URL  string `yaml:"url"`
		} `yaml:"license"`
	} `yaml:"info"`
	Tags []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	} `yaml:"tags"`
	Paths map[string]map[string]struct {
		Tags []string `yaml:"tags"`
	} `yaml:"paths"`
}

func loadOpenAPITags(t *testing.T) openAPITagIndex {
	t.Helper()

	raw, err := openAPIFiles.ReadFile("openapi/openapi.yaml")
	require.NoError(t, err)

	var spec openAPITagIndex

	require.NoError(t, yaml.Unmarshal(raw, &spec))
	require.NotEmpty(t, spec.Paths, "the embedded spec must have parsed")
	require.NotEmpty(t, spec.Tags, "the embedded spec must declare top-level tags")

	return spec
}

// operationTags returns the distinct tag names referenced by operations, and
// the number of operations carrying no tag at all.
func operationTags(spec openAPITagIndex) ([]string, int) {
	untagged := 0
	seen := map[string]bool{}

	for _, methods := range spec.Paths {
		for method, op := range methods {
			// `parameters` and `summary` are siblings of the HTTP methods on a
			// path item; they are not operations and carry no tags.
			switch method {
			case "get", "put", "post", "delete", "options", "head", "patch", "trace":
			default:
				continue
			}

			if len(op.Tags) == 0 {
				untagged++

				continue
			}

			for _, tag := range op.Tags {
				seen[tag] = true
			}
		}
	}

	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}

	sort.Strings(tags)

	return tags, untagged
}

// TestOpenAPI_EveryOperationTagIsDeclared guards the API reference sidebar.
//
// `web/docs` groups the reference by tag (`groupPathsBy: "tag"` with
// `categoryLinkSource: "tag"`), and the generator builds one category — and one
// category landing page — per *globally declared* tag that is referenced. An
// operation carrying a tag that is missing from the top-level `tags:` block
// gets no landing page and no description, and the `UNTAGGED` fallback does not
// catch it either: that bucket only collects operations with no tags at all.
//
// This shipped once: `StatusPages` (38 operations) and `StatusUpdates` (5) were
// set on operations but never declared, so the entire public status-page API
// would have degraded silently the moment grouping was turned on. The build
// prints no warning for it, so the assertion lives here.
func TestOpenAPI_EveryOperationTagIsDeclared(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := loadOpenAPITags(t)

	declared := map[string]bool{}
	for _, tag := range spec.Tags {
		r.NotEmpty(tag.Name, "a declared tag must have a name")
		r.NotEmpty(tag.Description,
			"tag %q needs a description: it becomes the body of its category landing page", tag.Name)
		r.False(declared[tag.Name], "tag %q is declared twice", tag.Name)
		declared[tag.Name] = true
	}

	used, untagged := operationTags(spec)
	r.Zero(untagged, "every operation must carry a tag, or it lands in the UNTAGGED bucket")

	var undeclared []string

	for _, tag := range used {
		if !declared[tag] {
			undeclared = append(undeclared, tag)
		}
	}

	r.Empty(undeclared,
		"these tags are used by operations but missing from the top-level tags: block, "+
			"so their endpoints get no sidebar category page: %v", undeclared)
}

// TestOpenAPI_NoDeclaredTagIsUnused is the other half: a declared tag nothing
// references produces a dangling entry in the spec (and, for tools that render
// every declared tag, an empty section).
func TestOpenAPI_NoDeclaredTagIsUnused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := loadOpenAPITags(t)

	used, _ := operationTags(spec)
	usedSet := map[string]bool{}

	for _, tag := range used {
		usedSet[tag] = true
	}

	var unused []string

	for _, tag := range spec.Tags {
		if !usedSet[tag.Name] {
			unused = append(unused, tag.Name)
		}
	}

	r.Empty(unused, "these tags are declared but no operation references them: %v", unused)
}

// TestOpenAPI_LicenseHasURL pins the fix for the empty License block on the API
// reference landing page.
//
// The docs generator only renders a license link when `url` (3.0) or
// `identifier` (3.1 only — this spec is 3.0.0) is set; a bare `name:` renders an
// `<h3>License</h3>` with nothing under it. Dropping the URL therefore makes the
// reference stop stating the project's license without breaking any build.
func TestOpenAPI_LicenseHasURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := loadOpenAPITags(t)

	r.Equal("AGPL-3.0", spec.Info.License.Name)
	r.Equal("https://www.gnu.org/licenses/agpl-3.0.html", spec.Info.License.URL)
}
