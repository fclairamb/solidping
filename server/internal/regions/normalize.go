package regions

import (
	"context"
	"fmt"
)

// orgSlugAliases returns every slug the organization answers on: its current
// slug plus each slug it used to answer on before a rename
// (organization_previous_slugs, spec 2026-08-08-11). Used only to decide
// whether a caller-supplied LEGACY `@<org>/<slug>` region names THIS org.
func (s *Service) orgSlugAliases(ctx context.Context, orgUID string) (map[string]struct{}, error) {
	org, err := s.db.GetOrganization(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("failed to load organization for region normalization: %w", err)
	}

	previous, err := s.db.ListOrganizationPreviousSlugs(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("failed to load previous org slugs for region normalization: %w", err)
	}

	aliases := make(map[string]struct{}, len(previous)+1)
	aliases[org.Slug] = struct{}{}

	for _, row := range previous {
		aliases[row.Slug] = struct{}{}
	}

	return aliases, nil
}

// hasLegacyPrivateRegion reports whether any entry uses the legacy
// fully-qualified `@<org>/<slug>` spelling. It exists so the common case (no
// legacy input) costs zero database round-trips.
func hasLegacyPrivateRegion(regionSlugs []string) bool {
	for _, region := range regionSlugs {
		if _, _, ok := ParseLegacyPrivateRegion(region); ok {
			return true
		}
	}

	return false
}

// NormalizeRegionsForOrg canonicalizes a caller-supplied region set for one
// organization, and is the ONLY place legacy input is tolerated:
//
//   - `@<org>/<slug>` is rewritten to the org-relative `@<slug>` when <org> is
//     the org's current slug or one of its recorded previous slugs;
//   - `@<org>/<slug>` naming ANY OTHER org is rejected with
//     ErrForeignPrivateRegion. That rejection is a security property: silently
//     stripping the org would let one tenant name another tenant's region.
//
// The result is de-duplicated, order-preserving — posting both `@old/paris` and
// `@paris` must yield exactly ONE `@paris`, not two identical dispatch rows
// (check_jobs is UNIQUE on (check_uid, region)).
func (s *Service) NormalizeRegionsForOrg(
	ctx context.Context, orgUID string, regionSlugs []string,
) ([]string, error) {
	var aliases map[string]struct{}

	if hasLegacyPrivateRegion(regionSlugs) {
		var err error

		aliases, err = s.orgSlugAliases(ctx, orgUID)
		if err != nil {
			return nil, err
		}
	}

	out := make([]string, 0, len(regionSlugs))
	seen := make(map[string]struct{}, len(regionSlugs))

	for _, region := range regionSlugs {
		normalized := region

		if org, slug, ok := ParseLegacyPrivateRegion(region); ok {
			if _, mine := aliases[org]; !mine {
				return nil, fmt.Errorf("%w: %q", ErrForeignPrivateRegion, region)
			}

			normalized = PrivateRegionSlug(slug)
		}

		if _, dup := seen[normalized]; dup {
			continue
		}

		seen[normalized] = struct{}{}

		out = append(out, normalized)
	}

	return out, nil
}

// normalizeStoredRegionsForOrg is the lenient counterpart used for values the
// org already has on file (the `default_regions` parameter), which migration
// 012 has normally already rewritten. A legacy entry naming this org is
// canonicalized; anything else is passed through untouched rather than turned
// into an error, because a stored value must never make a check write fail —
// and a foreign spelling is harmless: it simply matches no agent, exactly as
// today (every claim path filters by organization_uid).
func (s *Service) normalizeStoredRegionsForOrg(
	ctx context.Context, orgUID string, regionSlugs []string,
) []string {
	if !hasLegacyPrivateRegion(regionSlugs) {
		return regionSlugs
	}

	aliases, err := s.orgSlugAliases(ctx, orgUID)
	if err != nil {
		return regionSlugs
	}

	out := make([]string, 0, len(regionSlugs))
	seen := make(map[string]struct{}, len(regionSlugs))

	for _, region := range regionSlugs {
		normalized := region

		if org, slug, ok := ParseLegacyPrivateRegion(region); ok {
			if _, mine := aliases[org]; mine {
				normalized = PrivateRegionSlug(slug)
			}
		}

		if _, dup := seen[normalized]; dup {
			continue
		}

		seen[normalized] = struct{}{}

		out = append(out, normalized)
	}

	return out
}
