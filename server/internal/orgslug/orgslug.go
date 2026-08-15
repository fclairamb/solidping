// Package orgslug owns the organization-slug rule and the shared slug
// generator used by the OAuth/integration flows that auto-create organizations
// (Slack sign-in, Slack integration install, Discord).
//
// This package is the SINGLE SOURCE OF TRUTH for what a valid org slug looks
// like (3-20 chars, [a-z0-9] start/end, [a-z0-9-] body). Everything that
// validates or parses a slug must call IsValid rather than restating the rule:
// the pattern used to be spelled out independently in the auth handlers and
// again in the Telegram ref parser, which agreed only by coincidence. A
// divergence there was silent — loosening the canonical rule would have left
// organizations unable to use org-qualified Telegram references, answered only
// by a generic "I need an incident number".
package orgslug

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// MinLen is the minimum length of a valid org slug. A normalized candidate
	// shorter than this is not usable and is skipped by the generator.
	MinLen = 3
	// MaxLen is the maximum length of a valid org slug, including any numeric
	// collision suffix the generator appends.
	MaxLen = 20
	// fallbackSlug is used when no candidate normalizes to a usable base.
	fallbackSlug = "org"
)

// slugRegex is built FROM MinLen/MaxLen rather than written out, so the rule
// and the generator's bounds cannot drift apart. The body quantifier is
// {MinLen-2, MaxLen-2} because the first and last characters are matched
// separately (they may not be hyphens).
var slugRegex = regexp.MustCompile(
	fmt.Sprintf(`^[a-z0-9][a-z0-9-]{%d,%d}[a-z0-9]$`, MinLen-2, MaxLen-2),
)

// IsValid reports whether s is a valid organization slug: MinLen-MaxLen
// characters, lowercase alphanumeric at both ends, lowercase alphanumeric or
// hyphen in between.
//
// Callers that accept user-typed input should lowercase before calling —
// slugs are only ever stored lowercase, and this rule rejects uppercase.
func IsValid(s string) bool {
	return slugRegex.MatchString(s)
}

// Finder looks up organizations by slug. It is satisfied by db.Service.
type Finder interface {
	GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error)
}

// Slugify normalizes one candidate to a valid slug base, or "" if nothing
// usable remains. The pipeline is: lowercase, spaces to '-', keep [a-z0-9-],
// collapse repeated '-', trim '-', require len >= 3 (else ""), cap at 20, then
// trim any trailing '-' introduced by the cap.
func Slugify(s string) string {
	base := strings.ToLower(s)
	base = strings.ReplaceAll(base, " ", "-")

	var filtered strings.Builder

	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			filtered.WriteRune(r)
		}
	}

	base = filtered.String()

	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}

	base = strings.Trim(base, "-")

	// A candidate that normalizes to fewer than MinLen chars is not usable.
	if len(base) < MinLen {
		return ""
	}

	if len(base) > MaxLen {
		base = base[:MaxLen]
	}

	// Capping may leave a trailing hyphen; trim it. The result is still
	// guaranteed to be >= MinLen because we only cap when len > MaxLen
	// (20) and a single trailing hyphen trim cannot drop below 3.
	base = strings.TrimRight(base, "-")

	return base
}

// GenerateUnique returns the first candidate that Slugifies to a non-empty
// base, falling back to "org" when none are usable. It then ensures uniqueness
// via Finder.GetOrganizationBySlug, appending 2, 3, ... on collision. The
// final slug (base + numeric suffix) is capped at 20 chars by truncating the
// base, so very long workspace domains never overflow the slug rules. A lookup
// error is treated as "slug available" (matching the prior behavior).
func GenerateUnique(ctx context.Context, finder Finder, candidates ...string) string {
	base := fallbackSlug

	for _, candidate := range candidates {
		if slug := Slugify(candidate); slug != "" {
			base = slug

			break
		}
	}

	slug := base
	suffix := 2

	for {
		if _, err := finder.GetOrganizationBySlug(ctx, slug); err != nil {
			// Slug is available (not found or lookup error).
			return slug
		}

		suffixStr := strconv.Itoa(suffix)

		// Cap base so base+suffix never exceeds MaxLen.
		trimmedBase := base
		if len(trimmedBase)+len(suffixStr) > MaxLen {
			trimmedBase = trimmedBase[:MaxLen-len(suffixStr)]
			trimmedBase = strings.TrimRight(trimmedBase, "-")
		}

		slug = trimmedBase + suffixStr
		suffix++
	}
}
