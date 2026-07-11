package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"

	"github.com/fclairamb/solidping/server/internal/regions"
)

// envRegions is read manually with os.Getenv: koanf collapses underscores in
// SP_* names into dots, so single-purpose seed vars follow the same manual
// pattern as the SP_ENTITLEMENTS_* seeds in saas.go.
const envRegions = "SP_REGIONS"

// SeedRegionsFromEnv seeds the `regions` system parameter from the SP_REGIONS
// env var — a JSON list of region definitions, e.g.
//
//	[{"slug": "default", "emoji": "🇪🇺", "name": "EU1 (default)"},
//	 {"slug": "us-1", "emoji": "🇺🇸", "name": "US1"}]
//
// This lets a deployment name its regions declaratively (the dashboard renders
// "{emoji} {name}" wherever a region shows up) instead of hand-editing the DB
// parameter. The parameter is only written when the env var is set, so
// API/DB edits still win day-to-day on installs that don't pin regions in
// their deployment env. A malformed value logs an error and skips seeding —
// a bad region list must never prevent the server from booting.
func (s *Server) SeedRegionsFromEnv(ctx context.Context) error {
	raw := strings.TrimSpace(os.Getenv(envRegions))
	if raw == "" {
		return nil
	}

	var defs []regions.RegionDefinition
	if err := json.Unmarshal([]byte(raw), &defs); err != nil {
		slog.ErrorContext(ctx,
			"Ignoring malformed "+envRegions+" (expected a JSON list of {slug, emoji, name})",
			"error", err)

		return nil
	}

	if len(defs) == 0 {
		slog.ErrorContext(ctx, "Ignoring "+envRegions+": region list is empty")

		return nil
	}

	seen := make(map[string]bool, len(defs))
	for i := range defs {
		defs[i].Slug = strings.TrimSpace(defs[i].Slug)
		if defs[i].Slug == "" {
			slog.ErrorContext(ctx, "Ignoring "+envRegions+": region entry without a slug", "index", i)

			return nil
		}

		if seen[defs[i].Slug] {
			slog.ErrorContext(ctx, "Ignoring "+envRegions+": duplicate region slug", "slug", defs[i].Slug)

			return nil
		}
		seen[defs[i].Slug] = true

		// A definition without a name still needs something to display.
		if strings.TrimSpace(defs[i].Name) == "" {
			defs[i].Name = defs[i].Slug
		}
	}

	if err := s.dbService.SetSystemParameter(ctx, regions.ParamRegions, defs, false); err != nil {
		return err
	}

	slog.InfoContext(ctx, "Seeded regions from env",
		"key", regions.ParamRegions, "count", len(defs))

	return nil
}
