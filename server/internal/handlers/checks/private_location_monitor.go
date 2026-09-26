package checks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	plconfig "github.com/fclairamb/solidping/server/internal/checkers/checkprivatelocation/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// The private-location liveness monitor's lifecycle (spec 2026-09-25-05 §2):
// created with the location, removed with it, backfilled at startup and
// re-ensured after every enrollment, and never recreated once the org deleted
// it (the opt-out lives on the region definition).

var (
	// ErrPrivateLocationNotOwned is returned when a private-location check
	// names a region that is not one of the org's own private locations.
	ErrPrivateLocationNotOwned = errors.New("region must be one of this organization's private locations")
	// ErrPrivateLocationRegionImmutable is returned when an update tries to
	// repoint a private-location monitor at another location.
	ErrPrivateLocationRegionImmutable = errors.New("the location a private-location monitor watches cannot be changed")
)

// systemCreateKey marks a CreateCheck call made by the server itself rather
// than on behalf of a caller, so it is kept out of the activation funnel.
type systemCreateKey struct{}

func withSystemCreate(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemCreateKey{}, true)
}

func isSystemCreate(ctx context.Context) bool {
	value, _ := ctx.Value(systemCreateKey{}).(bool)

	return value
}

// privateLocationRegionOf reads the watched `@<slug>` region off a config.
func privateLocationRegionOf(config map[string]any) string {
	region, _ := config[plconfig.ConfigKeyRegion].(string)

	return region
}

// validatePrivateLocationConfig is the org-scoped half of the type's
// validation: the region must be one of THIS org's private locations. A private
// region string carries no org (it is org-relative), so without this check an
// org could point a monitor at `@office` while only another org owns an
// `@office` — the monitor would then read this org's (empty) agent set, but
// the claim it makes is still a lie. Runs on create, update, import and the
// validate endpoint through configValidationErrors.
func (s *Service) validatePrivateLocationConfig(
	ctx context.Context, orgUID, checkType string, effective map[string]any,
) error {
	if checkerdef.CheckType(checkType) != checkerdef.CheckTypePrivateLocation {
		return nil
	}

	slug := plconfig.RegionSlug(privateLocationRegionOf(effective))
	if slug == "" {
		// The offline validator reports the malformed shape.
		return nil
	}

	def, err := s.privateLocationDefinition(ctx, orgUID, slug)
	if err != nil {
		return err
	}

	if def == nil {
		return checkerdef.NewConfigError(plconfig.ConfigKeyRegion, ErrPrivateLocationNotOwned.Error())
	}

	return nil
}

// assertPrivateLocationRegionUnchanged refuses an update that repoints a
// monitor: the monitor is the location's, and the dashboard offers its region
// read-only.
func assertPrivateLocationRegionUnchanged(check *models.Check, merged map[string]any) error {
	if checkerdef.CheckType(check.Type) != checkerdef.CheckTypePrivateLocation {
		return nil
	}

	if privateLocationRegionOf(merged) != privateLocationRegionOf(check.Config) {
		return ErrPrivateLocationRegionImmutable
	}

	return nil
}

// privateLocationDefinition returns the org's private location with this raw
// slug, or nil.
func (s *Service) privateLocationDefinition(
	ctx context.Context, orgUID, slug string,
) (*regions.RegionDefinition, error) {
	defs, err := s.regions.GetOrgCustomRegions(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	for i := range defs {
		if defs[i].Slug == slug {
			return &defs[i], nil
		}
	}

	return nil, nil //nolint:nilnil // "no such location" is a normal answer
}

// privateLocationMonitors returns the org's live private-location monitors,
// keyed by the region they watch. Should two ever watch the same region (a
// user created a second one through the API), the oldest wins.
func (s *Service) privateLocationMonitors(ctx context.Context, orgUID string) (map[string]*models.Check, error) {
	all := "all"

	checks, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{
		Types:    []string{string(checkerdef.CheckTypePrivateLocation)},
		Internal: &all,
	})
	if err != nil {
		return nil, fmt.Errorf("list private-location monitors: %w", err)
	}

	byRegion := make(map[string]*models.Check, len(checks))

	for _, check := range checks {
		region := privateLocationRegionOf(check.Config)
		if region == "" {
			continue
		}

		if existing, ok := byRegion[region]; ok && existing.CreatedAt.Before(check.CreatedAt) {
			continue
		}

		byRegion[region] = check
	}

	return byRegion, nil
}

// PrivateLocationMonitor returns the monitor watching one of the org's private
// locations, or nil when there is none.
func (s *Service) PrivateLocationMonitor(ctx context.Context, orgUID, slug string) (*models.Check, error) {
	monitors, err := s.privateLocationMonitors(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	return monitors[regions.PrivateRegionSlug(slug)], nil
}

// PrivateLocationMonitors returns the org's monitors keyed by raw location
// slug, for the Private Locations page.
func (s *Service) PrivateLocationMonitors(ctx context.Context, orgUID string) (map[string]*models.Check, error) {
	monitors, err := s.privateLocationMonitors(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	out := make(map[string]*models.Check, len(monitors))

	for region, check := range monitors {
		if slug := plconfig.RegionSlug(region); slug != "" {
			out[slug] = check
		}
	}

	return out, nil
}

// EnsurePrivateLocationMonitor creates the liveness monitor of one private
// location unless it already has one or the org opted out. Idempotent: it is
// what CreatePrivateRegion, the startup backfill and every enrollment call.
// Returns the created check, or nil when nothing was created.
func (s *Service) EnsurePrivateLocationMonitor(ctx context.Context, orgUID, slug string) (*models.Check, error) {
	def, err := s.privateLocationDefinition(ctx, orgUID, slug)
	if err != nil {
		return nil, err
	}

	if def == nil || def.LivenessMonitorOff {
		return nil, nil //nolint:nilnil // no location, or opted out: nothing to create
	}

	existing, err := s.PrivateLocationMonitor(ctx, orgUID, slug)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		return nil, nil //nolint:nilnil // already monitored
	}

	return s.createPrivateLocationMonitor(ctx, orgUID, def)
}

// createPrivateLocationMonitor goes through CreateCheck like any check, so the
// monitor gets the org's default integrations, the check.created event and
// its place on dynamic status pages — with no special case in those paths.
func (s *Service) createPrivateLocationMonitor(
	ctx context.Context, orgUID string, def *regions.RegionDefinition,
) (*models.Check, error) {
	org, err := s.db.GetOrganization(ctx, orgUID)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	name := def.Name
	if name == "" {
		name = def.Slug
	}

	response, err := s.CreateCheck(withSystemCreate(ctx), org.Slug, CreateCheckRequest{
		Name:   plconfig.NamePrefix + name,
		Type:   string(checkerdef.CheckTypePrivateLocation),
		Config: map[string]any{plconfig.ConfigKeyRegion: regions.PrivateRegionSlug(def.Slug)},
	})
	if err != nil {
		return nil, fmt.Errorf("create private-location monitor for %q: %w", def.Slug, err)
	}

	return s.db.GetCheck(ctx, orgUID, response.UID)
}

// RemovePrivateLocationMonitor deletes the monitor of a private location that
// is being deleted. It records NO opt-out: the location itself is gone. Call it
// after the location was removed from the org's definitions.
func (s *Service) RemovePrivateLocationMonitor(ctx context.Context, orgUID, slug string) error {
	monitor, err := s.PrivateLocationMonitor(ctx, orgUID, slug)
	if err != nil || monitor == nil {
		return err
	}

	org, err := s.db.GetOrganization(ctx, orgUID)
	if err != nil {
		return ErrOrganizationNotFound
	}

	return s.DeleteCheck(ctx, org.Slug, monitor.UID)
}

// EnablePrivateLocationMonitor is the Private Locations page's one-click
// re-enable: it clears the opt-out, then re-enables the existing monitor or
// creates a fresh one. Returns the monitor.
func (s *Service) EnablePrivateLocationMonitor(ctx context.Context, orgUID, slug string) (*models.Check, error) {
	found, err := s.regions.SetPrivateLivenessMonitorOff(ctx, orgUID, slug, false)
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, fmt.Errorf("%w: %s", ErrPrivateLocationNotOwned, slug)
	}

	monitor, err := s.PrivateLocationMonitor(ctx, orgUID, slug)
	if err != nil {
		return nil, err
	}

	if monitor == nil {
		return s.EnsurePrivateLocationMonitor(ctx, orgUID, slug)
	}

	if monitor.Enabled {
		return monitor, nil
	}

	org, err := s.db.GetOrganization(ctx, orgUID)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	enabled := true
	if _, err := s.UpdateCheck(ctx, org.Slug, monitor.UID, &UpdateCheckRequest{Enabled: &enabled}); err != nil {
		return nil, fmt.Errorf("enable private-location monitor: %w", err)
	}

	monitor.Enabled = true

	return monitor, nil
}

// recordPrivateLocationOptOut remembers that the org deleted a location's
// monitor, so neither the backfill nor an enrollment recreates it. A location
// that no longer exists (its deletion removed the monitor) is a no-op.
func (s *Service) recordPrivateLocationOptOut(ctx context.Context, check *models.Check) {
	if checkerdef.CheckType(check.Type) != checkerdef.CheckTypePrivateLocation {
		return
	}

	slug := plconfig.RegionSlug(privateLocationRegionOf(check.Config))
	if slug == "" {
		return
	}

	if _, err := s.regions.SetPrivateLivenessMonitorOff(ctx, check.OrganizationUID, slug, true); err != nil {
		slog.WarnContext(ctx, "failed to record the private-location monitor opt-out",
			"checkUid", check.UID, "region", slug, "error", err)
	}
}

// BackfillPrivateLocationMonitors runs at startup: every private location of
// every org gets exactly one monitor unless the org opted out, and a monitor
// whose location no longer exists is removed. Best effort per org. Returns how
// many monitors it created.
func (s *Service) BackfillPrivateLocationMonitors(ctx context.Context) (int, error) {
	orgs, err := s.db.ListOrganizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}

	created := 0

	for _, org := range orgs {
		count, orgErr := s.backfillOrgPrivateLocationMonitors(ctx, org)
		if orgErr != nil {
			slog.WarnContext(ctx, "private-location monitor backfill failed for an organization",
				"orgUid", org.UID, "error", orgErr)
		}

		created += count
	}

	return created, nil
}

func (s *Service) backfillOrgPrivateLocationMonitors(ctx context.Context, org *models.Organization) (int, error) {
	defs, err := s.regions.GetOrgCustomRegions(ctx, org.UID)
	if err != nil {
		return 0, err
	}

	monitors, err := s.privateLocationMonitors(ctx, org.UID)
	if err != nil {
		return 0, err
	}

	created := 0
	known := make(map[string]bool, len(defs))

	for i := range defs {
		def := &defs[i]
		region := regions.PrivateRegionSlug(def.Slug)
		known[region] = true

		if def.LivenessMonitorOff || monitors[region] != nil {
			continue
		}

		if _, createErr := s.createPrivateLocationMonitor(ctx, org.UID, def); createErr != nil {
			return created, createErr
		}

		created++
	}

	// A monitor left behind by a location whose deletion did not finish.
	for region, monitor := range monitors {
		if known[region] {
			continue
		}

		if delErr := s.DeleteCheck(ctx, org.Slug, monitor.UID); delErr != nil {
			return created, delErr
		}
	}

	return created, nil
}
