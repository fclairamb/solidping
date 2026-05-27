// Package orgslug provides a shared organization-slug generator used by the
// OAuth/integration flows that auto-create organizations (Slack sign-in,
// Slack integration install, Discord). Slugs produced satisfy the existing
// org-slug rules (3-20 chars, [a-z0-9] start/end, [a-z0-9-] body).
package orgslug

import (
	"context"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// minSlugLen is the minimum length a normalized candidate must reach to
	// be considered usable. Candidates shorter than this are skipped.
	minSlugLen = 3
	// maxSlugLen is the maximum length of a generated slug (including any
	// numeric collision suffix).
	maxSlugLen = 20
	// fallbackSlug is used when no candidate normalizes to a usable base.
	fallbackSlug = "org"
)

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

	// A candidate that normalizes to fewer than minSlugLen chars is not usable.
	if len(base) < minSlugLen {
		return ""
	}

	if len(base) > maxSlugLen {
		base = base[:maxSlugLen]
	}

	// Capping may leave a trailing hyphen; trim it. The result is still
	// guaranteed to be >= minSlugLen because we only cap when len > maxSlugLen
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

		// Cap base so base+suffix never exceeds maxSlugLen.
		trimmedBase := base
		if len(trimmedBase)+len(suffixStr) > maxSlugLen {
			trimmedBase = trimmedBase[:maxSlugLen-len(suffixStr)]
			trimmedBase = strings.TrimRight(trimmedBase, "-")
		}

		slug = trimmedBase + suffixStr
		suffix++
	}
}
