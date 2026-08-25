package files_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
)

const testUID = "6f1c9f34-6c2d-4a8b-9f7f-2b1a0d5e3c77"

// TestIsPublicTopicIsAClosedAllowlist is the whole authorization rule for the
// unsigned public asset route, so it is tested as a closed set rather than by
// example: everything not named is private, including every future attachment
// kind.
//
// The malformed cases are not padding. This function reads a stored string and
// decides whether an unauthenticated caller may have the bytes; a parser that
// normalized `status-pages/<uid>/logo/../../incidents/x` instead of rejecting it
// would publish org-operational evidence.
func TestIsPublicTopicIsAClosedAllowlist(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		topic string
		want  bool
	}{
		// --- Allowed: the entire public surface. ---
		{"status page logo", "status-pages/" + testUID + "/logo", true},
		{"status page favicon", "status-pages/" + testUID + "/favicon", true},
		{"organization logo", "organizations/" + testUID + "/logo", true},

		// --- Denied: real topics that must stay private. ---
		{"incident screenshot", "incidents/" + testUID + "/screenshot", false},
		{"unknown kind on a public entity", "status-pages/" + testUID + "/harfile", false},
		{"favicon is not an org kind", "organizations/" + testUID + "/favicon", false},
		{"unknown entity", "checks/" + testUID + "/logo", false},

		// --- Denied: malformed. ---
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"whitespace around a valid topic", " status-pages/" + testUID + "/logo ", false},
		{"trailing slash", "status-pages/" + testUID + "/logo/", false},
		{"leading slash", "/status-pages/" + testUID + "/logo", false},
		{"two segments", "status-pages/" + testUID, false},
		{"extra segment", "status-pages/" + testUID + "/logo/extra", false},
		{"traversal", "status-pages/" + testUID + "/logo/../../incidents/x", false},
		{"dot segments", "status-pages/../logo", false},
		{"empty uid segment", "status-pages//logo", false},
		{"underscore is not a topic character", "status_pages/" + testUID + "/logo", false},
		{"like wildcard in uid", "status-pages/%/logo", false},
		{"newline injection", "status-pages/" + testUID + "/logo\nx", false},
		{"over the length bound", "status-pages/" + strings.Repeat("a", 300) + "/logo", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, files.IsPublicTopic(tc.topic), "topic %q", tc.topic)
		})
	}
}

// TestPublicTopicBuildersProduceAllowlistedTopics closes the loop: the topics
// the producers actually write must be the ones the allowlist admits. Without
// this, a typo in a builder would make a freshly uploaded logo 404 and both
// halves would still "pass" their own tests.
func TestPublicTopicBuildersProduceAllowlistedTopics(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(files.IsPublicTopic(files.StatusPageAssetTopic(testUID, files.KindLogo)))
	r.True(files.IsPublicTopic(files.StatusPageAssetTopic(testUID, files.KindFavicon)))
	r.True(files.IsPublicTopic(files.OrganizationLogoTopic(testUID)))

	// The reap prefixes are prefixes of the topics they reap, and carry the
	// trailing slash that stops page `abc` from reaping page `abcdef`.
	r.True(strings.HasPrefix(files.StatusPageAssetTopic(testUID, files.KindLogo), files.StatusPageTopicPrefix(testUID)))
	r.True(strings.HasPrefix(files.OrganizationLogoTopic(testUID), files.OrganizationTopicPrefix(testUID)))
	r.True(strings.HasSuffix(files.StatusPageTopicPrefix(testUID), "/"))
	r.True(strings.HasSuffix(files.OrganizationTopicPrefix(testUID), "/"))

	// ...and a prefix is NOT itself a public topic: it is two segments plus an
	// empty third, which must not be served.
	r.False(files.IsPublicTopic(files.StatusPageTopicPrefix(testUID)))
	r.False(files.IsPublicTopic(files.OrganizationTopicPrefix(testUID)))
}

// TestPublicTopicGrammarMatchesTheAttachmentRail pins that the re-stated parser
// agrees with attachments.ParseTopic on what a well-formed topic is.
//
// The grammar is duplicated on purpose — `attachments` imports `files`, so the
// dependency only runs one way — and duplicated rules drift. This is the test
// that notices.
func TestPublicTopicGrammarMatchesTheAttachmentRail(t *testing.T) {
	t.Parallel()

	// Every shape either parser is asked about, public or not.
	topics := []string{
		"status-pages/" + testUID + "/logo",
		"organizations/" + testUID + "/logo",
		"incidents/" + testUID + "/screenshot",
		"",
		"   ",
		"status-pages/" + testUID + "/logo/",
		"status-pages/" + testUID,
		"status-pages/" + testUID + "/logo/extra",
		"status-pages//logo",
		"status_pages/" + testUID + "/logo",
		"status-pages/%/logo",
		"status-pages/../logo",
	}

	for _, topic := range topics {
		_, railErr := attachments.ParseTopic(topic)
		wellFormed := railErr == nil

		// A malformed topic can never be public; a well-formed one may or may
		// not be, depending on the allowlist. So: public ⇒ well-formed.
		if files.IsPublicTopic(topic) {
			require.True(t, wellFormed,
				"IsPublicTopic admitted %q, which attachments.ParseTopic rejects", topic)
		}

		// The stronger claim for the ones both agree are fine: a well-formed
		// topic whose entity/kind is allowlisted must be public.
		if wellFormed && strings.HasPrefix(topic, "status-pages/") && strings.HasSuffix(topic, "/logo") {
			require.True(t, files.IsPublicTopic(topic), "%q is a well-formed status-page logo", topic)
		}
	}
}
