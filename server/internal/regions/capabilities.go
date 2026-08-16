package regions

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Capability names. A map key rather than a struct field so the next capability
// is purely additive on the wire (spec 2026-08-15-11 non-goals).
//
// The names themselves live on the model, because they are the values stored
// verbatim in workers.capabilities and a producer must be able to name one
// without importing this package. Re-exported here unchanged: these are the
// keys of the public regions API response.
const (
	// CapabilityIPv4 reports whether checks pinned to `ipVersion: ipv4` can
	// actually leave this region.
	CapabilityIPv4 = models.CapabilityIPv4
	// CapabilityIPv6 reports whether checks pinned to `ipVersion: ipv6` can
	// actually leave this region.
	CapabilityIPv6 = models.CapabilityIPv6
)

// Capability values. THREE states, deliberately — not a boolean.
//
// "unknown" is a real answer (no live worker, or none reporting yet), and
// collapsing it to "no" is exactly the lie this feature exists to stop telling:
// it would paint every region served by an agent predating the capability
// report as IPv6-incapable, and users would avoid regions that work fine.
const (
	// CapabilityYes means at least one live worker in the region reports the
	// family.
	CapabilityYes = "yes"
	// CapabilityNo means live workers reported, and none of them has the family.
	CapabilityNo = "no"
	// CapabilityUnknown means nothing live has reported: no live worker at all,
	// or only workers that do not report the capability.
	CapabilityUnknown = "unknown"
)

// WorkerLivenessWindow is how recently a worker must have checked in for its
// self-reported capability to still count. The in-process worker beats every
// 50s and a connected agent refreshes its row on claim frames at the same
// cadence, so this leaves several missed beats of slack before a region stops
// advertising what a departed worker used to provide.
const WorkerLivenessWindow = 5 * time.Minute

// aggregate folds a set of workers into the region's advertised verdict for one
// capability name.
//
// ANY, NOT ALL: a job lands on exactly one worker, so a single live worker with
// the capability makes such a check in this region succeed. Requiring all of
// them would under-advertise a region that works.
//
// Workers reporting nothing (a NULL set) contribute nothing in either
// direction — they can neither create a "yes" nor turn an otherwise-unreported
// region into a "no". A worker that reported a set WITHOUT this name is a real
// "no": a non-nil set is closed.
func aggregate(workers []*models.Worker, name string) string {
	reported := false

	for _, worker := range workers {
		switch worker.Capability(name) {
		case models.CapabilityStatePresent:
			return CapabilityYes
		case models.CapabilityStateAbsent:
			reported = true
		case models.CapabilityStateUnknown:
		}
	}

	if reported {
		return CapabilityNo
	}

	return CapabilityUnknown
}

// aggregatedCapabilities is every capability the region layer publishes a
// verdict for. Adding one is a pure string addition — no migration, no column,
// no new aggregation function.
var aggregatedCapabilities = []string{ //nolint:gochecknoglobals // the published capability registry
	CapabilityIPv4,
	CapabilityIPv6,
}

// capabilitiesFor renders the capability map for a worker set.
func capabilitiesFor(workers []*models.Worker) map[string]string {
	out := make(map[string]string, len(aggregatedCapabilities))
	for _, name := range aggregatedCapabilities {
		out[name] = aggregate(workers, name)
	}

	return out
}

// liveWorkers loads every worker that has checked in inside the liveness
// window. One query serves both the cloud and the private aggregation below;
// the table holds one row per executor, so this is small by construction.
func (s *Service) liveWorkers(ctx context.Context) ([]*models.Worker, error) {
	workers, err := s.db.ListLiveWorkers(ctx, time.Now().Add(-WorkerLivenessWindow))
	if err != nil {
		return nil, fmt.Errorf("failed to list live workers: %w", err)
	}

	return workers, nil
}

// AnnotateGlobalCapabilities fills in Capabilities on cloud region definitions,
// in place.
//
// Cloud regions are hierarchical: a worker in "eu-west-1" serves jobs tagged
// "eu", so membership uses the same MatchesRegion the claim path uses rather
// than string equality. Workers carrying the reserved private prefix are
// skipped — a cloud region must never inherit a private location's capability
// (ValidateWorkerRegion already forbids such a worker, so this is belt and
// braces).
func (s *Service) AnnotateGlobalCapabilities(ctx context.Context, defs []RegionDefinition) error {
	if len(defs) == 0 {
		return nil
	}

	workers, err := s.liveWorkers(ctx)
	if err != nil {
		return err
	}

	for i := range defs {
		matched := make([]*models.Worker, 0, len(workers))

		for _, worker := range workers {
			if worker.Region == nil || IsPrivateRegion(*worker.Region) {
				continue
			}

			if MatchesRegion(*worker.Region, defs[i].Slug) {
				matched = append(matched, worker)
			}
		}

		defs[i].Capabilities = capabilitiesFor(matched)
	}

	return nil
}

// AnnotatePrivateCapabilities fills in Capabilities on one organization's
// private-location definitions (raw, un-namespaced slugs), in place.
//
// ORG SCOPING IS THE WHOLE POINT HERE. `@aws-paris` is only unique within an
// org (wiki/conventions/regions.md), and the workers table carries no
// organization_uid — an org agent's worker row does not even carry the region,
// it is NULL by design. So membership is resolved the only way that is
// org-safe: through the agents table, which is filtered by organization_uid AND
// the exact private region string, and then mapped to worker rows by the
// deterministic agent worker slug. Matching on the region column instead would
// silently pool two organizations' identically-named locations.
func (s *Service) AnnotatePrivateCapabilities(
	ctx context.Context, orgUID string, defs []RegionDefinition,
) error {
	if len(defs) == 0 {
		return nil
	}

	workers, err := s.liveWorkers(ctx)
	if err != nil {
		return err
	}

	bySlug := make(map[string]*models.Worker, len(workers))
	for _, worker := range workers {
		bySlug[worker.Slug] = worker
	}

	for i := range defs {
		region := PrivateRegionSlug(defs[i].Slug)

		orgAgents, listErr := s.db.ListActiveAgentsByRegion(ctx, orgUID, region)
		if listErr != nil {
			return fmt.Errorf("failed to list agents for %q: %w", region, listErr)
		}

		matched := make([]*models.Worker, 0, len(orgAgents))

		for _, agent := range orgAgents {
			if worker, ok := bySlug[agents.WorkerSlug(agent.UID)]; ok {
				matched = append(matched, worker)
			}
		}

		defs[i].Capabilities = capabilitiesFor(matched)
	}

	return nil
}

// CapabilityIndex returns every region an organization can target — cloud
// regions plus its own private locations — annotated with capabilities and
// keyed by the slug actually stored on a check (`@<slug>` for a private
// location). It is the lookup the check-validation warning uses.
//
// An org with no access to another org's private locations simply never sees
// them here: the private half comes from this org's `custom_regions` and is
// resolved through this org's agents.
func (s *Service) CapabilityIndex(ctx context.Context, orgUID string) (map[string]RegionDefinition, error) {
	globalDefs, err := s.GetGlobalRegions(ctx)
	if err != nil {
		return nil, err
	}

	if capErr := s.AnnotateGlobalCapabilities(ctx, globalDefs); capErr != nil {
		return nil, capErr
	}

	privateDefs, err := s.GetOrgCustomRegions(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	if capErr := s.AnnotatePrivateCapabilities(ctx, orgUID, privateDefs); capErr != nil {
		return nil, capErr
	}

	index := make(map[string]RegionDefinition, len(globalDefs)+len(privateDefs))

	for i := range globalDefs {
		index[globalDefs[i].Slug] = globalDefs[i]
	}

	for i := range privateDefs {
		def := privateDefs[i]
		def.Slug = PrivateRegionSlug(def.Slug)
		index[def.Slug] = def
	}

	return index, nil
}

// CapabilityFor reads one verdict off a definition, defaulting to "unknown" for
// a definition nobody annotated or a capability nobody published. Callers must
// treat the absence of an answer as unknown, never as "no".
func (d RegionDefinition) CapabilityFor(name string) string {
	if d.Capabilities == nil {
		return CapabilityUnknown
	}

	if value, ok := d.Capabilities[name]; ok && value != "" {
		return value
	}

	return CapabilityUnknown
}

// IPv4Capability reads the IPv4 verdict off a definition.
func (d RegionDefinition) IPv4Capability() string {
	return d.CapabilityFor(CapabilityIPv4)
}

// IPv6Capability reads the IPv6 verdict off a definition.
func (d RegionDefinition) IPv6Capability() string {
	return d.CapabilityFor(CapabilityIPv6)
}
