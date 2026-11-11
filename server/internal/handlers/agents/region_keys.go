package agents

import (
	"context"
	"errors"
	"slices"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

// ErrInvalidRegionKey is returned when a region key recipient is not an age
// X25519 recipient.
var ErrInvalidRegionKey = errors.New("recipient must be an age X25519 recipient (age1…)")

// auditKeyAction names the action in a region key audit payload.
const auditKeyAction = "action"

// SetRegionKeyRequest is the body of PUT /private-regions/:slug/region-key.
// Only the public recipient is ever sent: the identity stays with the user
// and the region's agents (spec 2026-11-08-01 §2).
type SetRegionKeyRequest struct {
	Recipient string `json:"recipient"`
}

// regionKeyRow fills the shared-key fields of a private region row.
func (s *Service) regionKeyRow(
	ctx context.Context, orgUID string, row *PrivateRegionResponse, key *sealing.RegionKey,
	active []*models.Agent, workerCaps map[string][]string,
) error {
	row.KeyMode = sealing.KeyModePerAgent

	if key == nil {
		return nil
	}

	row.KeyMode = sealing.KeyModeShared
	row.RegionKey = key

	for _, agent := range active {
		if slices.Contains(sealing.RegionKeyFingerprints(workerCaps[agent.UID]), key.Fingerprint) {
			row.AgentsWithRegionKey++
		}
	}

	stale, err := s.countChecksNotSealedTo(ctx, orgUID, row.Region, key.Fingerprint)
	if err != nil {
		return err
	}

	row.ChecksNeedingReseal = stale

	return nil
}

// countChecksNotSealedTo counts the region's checks whose sealed blob does not
// include the region's current key (sealed to an older key or to agent keys),
// or was re-sealed by an agent without a second agent's confirmation.
func (s *Service) countChecksNotSealedTo(ctx context.Context, orgUID, region, fingerprint string) (int, error) {
	checks, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{})
	if err != nil {
		return 0, err
	}

	count := 0

	for _, check := range checks {
		if !regionTargeted(check.Regions, region) || check.ConfigSealed == nil {
			continue
		}

		fps, fpErr := credentials.SealedRecipientFingerprints(*check.ConfigSealed)
		// A peer re-wrap no second agent confirmed counts too (spec
		// 2026-11-11-02).
		if fpErr != nil || !slices.Contains(fps, fingerprint) || credentials.IsSealedUnconfirmed(*check.ConfigSealed) {
			count++
		}
	}

	return count, nil
}

// regionKeyState reads the org's region keys and the workers' capabilities.
func (s *Service) regionKeyState(
	ctx context.Context, orgUID string,
) (map[string]sealing.RegionKey, map[string][]string, error) {
	keys, err := sealing.RegionKeys(ctx, s.db, orgUID)
	if err != nil {
		return nil, nil, err
	}

	caps, err := s.workerCapabilities(ctx)
	if err != nil {
		return nil, nil, err
	}

	return keys, caps, nil
}

// workerCapabilities maps each agent UID to its worker row's capabilities.
func (s *Service) workerCapabilities(ctx context.Context) (map[string][]string, error) {
	workers, err := s.db.ListWorkers(ctx)
	if err != nil {
		return nil, err
	}

	bySlug := make(map[string][]string, len(workers))
	for _, worker := range workers {
		bySlug[worker.Slug] = worker.Capabilities
	}

	return bySlug, nil
}

// capsByAgent resolves worker capabilities per agent UID.
func capsByAgent(rows []*models.Agent, bySlug map[string][]string) map[string][]string {
	out := make(map[string][]string, len(rows))
	for _, row := range rows {
		out[row.UID] = bySlug[agentcrypto.WorkerSlug(row.UID)]
	}

	return out
}

// SetRegionKey makes a private region use a shared region key, or rotates it.
// Old blobs are flagged needsReseal from now on; mixed-mode checks the server
// can decrypt are re-sealed to the new key right away.
func (s *Service) SetRegionKey(
	ctx context.Context, orgSlug, slug string, req *SetRegionKeyRequest,
) (*sealing.RegionKey, error) {
	org, full, err := s.ownedRegion(ctx, orgSlug, slug)
	if err != nil {
		return nil, err
	}

	key, err := sealing.NewRegionKey(req.Recipient, s.now())
	if err != nil {
		return nil, ErrInvalidRegionKey
	}

	keys, err := sealing.RegionKeys(ctx, s.db, org.UID)
	if err != nil {
		return nil, err
	}

	previous, rotated := keys[full]
	keys[full] = key

	if err := sealing.SetRegionKeys(ctx, s.db, org.UID, keys); err != nil {
		return nil, err
	}

	// Blobs sealed to the old key are re-wrapped by an agent that proves it
	// holds it, which takes its public key (spec 2026-11-11-02).
	if rotated && previous.Fingerprint != key.Fingerprint {
		if err := sealing.RetireRegionKey(ctx, s.db, org.UID, previous, s.now()); err != nil {
			return nil, err
		}
	}

	action := "region_key_set"
	if rotated {
		action = "region_key_rotated"
	}

	audit.Record(ctx, s.db, org.UID, models.EventTypeOrgSettingsUpdated,
		audit.Target{Type: "private_region", UID: full, Name: slug},
		models.JSONMap{auditKeyAction: action, "fingerprint": key.Fingerprint})

	s.ResealRegion(ctx, org.UID, full)

	return &key, nil
}

// DeleteRegionKey puts a private region back on a key per agent.
func (s *Service) DeleteRegionKey(ctx context.Context, orgSlug, slug string) error {
	org, full, err := s.ownedRegion(ctx, orgSlug, slug)
	if err != nil {
		return err
	}

	removed, err := s.removeRegionKey(ctx, org.UID, full, slug, "")
	if err != nil || !removed {
		return err
	}

	s.ResealRegion(ctx, org.UID, full)

	return nil
}

// removeRegionKey drops the shared key of a region (a stale one included),
// retires its public part and records the audit event. It reports whether a
// key was there. reason is added to the audit event when not empty.
func (s *Service) removeRegionKey(ctx context.Context, orgUID, full, slug, reason string) (bool, error) {
	keys, err := sealing.StoredRegionKeys(ctx, s.db, orgUID)
	if err != nil {
		return false, err
	}

	previous, ok := keys[full]
	if !ok {
		return false, nil
	}

	delete(keys, full)

	if err := sealing.SetRegionKeys(ctx, s.db, orgUID, keys); err != nil {
		return false, err
	}

	if err := sealing.RetireRegionKey(ctx, s.db, orgUID, previous, s.now()); err != nil {
		return false, err
	}

	details := models.JSONMap{auditKeyAction: "region_key_removed"}
	if reason != "" {
		details["reason"] = reason
	}

	audit.Record(ctx, s.db, orgUID, models.EventTypeOrgSettingsUpdated,
		audit.Target{Type: "private_region", UID: full, Name: slug}, details)

	return true, nil
}

// ownedRegion resolves an org and one of its private region slugs.
func (s *Service) ownedRegion(ctx context.Context, orgSlug, slug string) (*models.Organization, string, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, "", err
	}

	full := regions.PrivateRegionSlug(slug)

	owned, err := s.regions.OwnsPrivateRegion(ctx, org.UID, full)
	if err != nil {
		return nil, "", err
	}

	if !owned {
		return nil, "", ErrRegionNotFound
	}

	return org, full, nil
}
