package files

import "strings"

// Public-attachment topic grammar. A topic is `<entity>/<uid>/<kind>` — the
// same shape internal/handlers/attachments defines. It is re-stated here rather
// than imported because `attachments` imports THIS package; the dependency only
// runs one way, and a topic parser is a dozen lines. A test pins that the two
// agree on what a well-formed topic looks like.
const (
	// EntityStatusPages is the entity segment for status-page attachments.
	EntityStatusPages = "status-pages"
	// EntityOrganizations is the entity segment for organization attachments.
	EntityOrganizations = "organizations"
	// KindLogo is the brand-logo slot.
	KindLogo = "logo"
	// KindFavicon is the favicon slot (status pages only).
	KindFavicon = "favicon"
)

// maxPublicTopicLength bounds a topic before it is parsed. Topics are
// machine-generated (`status-pages/<uuid>/favicon` is 52 chars), but this
// function is reached from an unauthenticated route with a caller-supplied file
// UID, so the length is checked rather than assumed.
const maxPublicTopicLength = 200

// publicTopics is the CLOSED allowlist of attachment topics served unsigned:
// entity segment → the kinds of that entity that are public.
//
//nolint:gochecknoglobals // an immutable lookup table, the rule's single owner
var publicTopics = map[string]map[string]bool{
	EntityStatusPages:   {KindLogo: true, KindFavicon: true},
	EntityOrganizations: {KindLogo: true},
}

// IsPublicTopic reports whether an attachment topic is served unsigned.
//
// Closed allowlist: unknown entity, unknown kind, malformed or empty topic all
// deny. Everything not named in publicTopics is private, including every future
// attachment kind, which is the point — a new kind becomes public by an edit
// HERE, never by default.
//
// This is the entire authorization rule for the public asset route. It is
// deliberately not "is this the current logo of a live page": the file row's own
// topic says what the blob is, and the file row's `deleted_at` says whether it
// is still current, which is what makes replace and clear un-publish a blob (see
// Service.GetPublicFile, which only ever reads live rows).
func IsPublicTopic(topic string) bool {
	parsed, ok := splitTopic(topic)
	if !ok {
		return false
	}

	return publicTopics[parsed.entity][parsed.kind]
}

// parsedTopic is a topic split into its three segments.
type parsedTopic struct {
	entity string
	uid    string
	kind   string
}

// splitTopic validates and splits a topic into its three segments. Strict on
// purpose — this stands between a stored string and an unsigned public read, so
// anything ambiguous (empty segment, extra segment, traversal, whitespace,
// trailing slash) is rejected rather than normalized.
func splitTopic(topic string) (parsedTopic, bool) {
	if topic == "" || len(topic) > maxPublicTopicLength {
		return parsedTopic{}, false
	}

	parts := strings.Split(topic, "/")
	if len(parts) != 3 {
		return parsedTopic{}, false
	}

	for _, part := range parts {
		if !isTopicSegment(part) {
			return parsedTopic{}, false
		}
	}

	return parsedTopic{entity: parts[0], uid: parts[1], kind: parts[2]}, true
}

// isTopicSegment allows only a non-empty run of [A-Za-z0-9.-] — note that `_`
// is NOT allowed, matching attachments.isTopicSegment.
//
// That covers UUIDs and kind names while excluding the characters that make a
// topic dangerous: `%` and `_` are LIKE wildcards in the prefix reap, and `/`
// would let a stored value invent extra segments. `.` is permitted inside a
// segment (kind names may carry one) but a segment that is only dots is not —
// `.` and `..` are how a traversal is spelled.
func isTopicSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}

	for _, char := range segment {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '.':
		default:
			return false
		}
	}

	return true
}

// StatusPageTopicPrefix returns the reap prefix for one status page:
// `status-pages/<uid>/`. The trailing slash is load-bearing — without it the
// prefix of page `abc` would also match page `abcdef`.
func StatusPageTopicPrefix(pageUID string) string {
	return EntityStatusPages + "/" + pageUID + "/"
}

// StatusPageAssetTopic returns the exact topic a status page's brand asset is
// stored under (`kind` is KindLogo or KindFavicon).
func StatusPageAssetTopic(pageUID, kind string) string {
	return StatusPageTopicPrefix(pageUID) + kind
}

// OrganizationTopicPrefix returns the reap prefix for one organization:
// `organizations/<uid>/`.
func OrganizationTopicPrefix(orgUID string) string {
	return EntityOrganizations + "/" + orgUID + "/"
}

// OrganizationLogoTopic returns the exact topic an organization's logo is
// stored under.
func OrganizationLogoTopic(orgUID string) string {
	return OrganizationTopicPrefix(orgUID) + KindLogo
}
