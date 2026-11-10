package checks

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
)

// ErrRewrapUnavailable is returned when the server runs without the re-wrap
// kind (it is wired at startup).
var ErrRewrapUnavailable = errors.New("peer re-wrap is not available on this server")

// Rewrapper sends peer re-wraps of sealed secrets to the agents that can open
// them (spec 2026-11-10-03). Implemented by rewraptask.Server.
type Rewrapper interface {
	Start(ctx context.Context, orgUID string, checks []*models.Check) ([]rewraptask.Outcome, error)
	Status(ctx context.Context, orgUID string, checkUIDs []string) ([]rewraptask.Outcome, error)
}

// SetRewrapper wires the peer re-wrap.
func (s *Service) SetRewrapper(rewrapper Rewrapper) {
	s.rewrap = rewrapper
}

// RewrapRequest is the body of POST /orgs/:org/sealing/rewrap: the checks of
// one private region flagged needsReseal, the given checks, or every flagged
// check of the org when both are empty.
type RewrapRequest struct {
	Region    string   `json:"region,omitempty"`
	CheckUIDs []string `json:"checkUids,omitempty"`
}

// RewrapOutcome is one check's re-wrap state, with the check's name.
type RewrapOutcome struct {
	rewraptask.Outcome

	CheckName string `json:"checkName,omitempty"`
}

// StartRewrap sends a peer re-wrap for the requested checks (spec
// 2026-11-10-03 §1). A check that gets none is reported with its reason.
func (s *Service) StartRewrap(ctx context.Context, orgSlug string, req *RewrapRequest) ([]RewrapOutcome, error) {
	if s.rewrap == nil {
		return nil, ErrRewrapUnavailable
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	candidates, err := s.rewrapCandidates(ctx, org.UID, req)
	if err != nil {
		return nil, err
	}

	outcomes, err := s.rewrap.Start(ctx, org.UID, candidates)
	if err != nil {
		return nil, err
	}

	return withCheckNames(outcomes, candidates), nil
}

// RewrapStatus returns the latest re-wrap of the given checks (of every check
// with a recent one when none is given).
func (s *Service) RewrapStatus(ctx context.Context, orgSlug string, checkUIDs []string) ([]RewrapOutcome, error) {
	if s.rewrap == nil {
		return nil, ErrRewrapUnavailable
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	outcomes, err := s.rewrap.Status(ctx, org.UID, checkUIDs)
	if err != nil {
		return nil, err
	}

	checks := make([]*models.Check, 0, len(outcomes))

	for i := range outcomes {
		if check, getErr := s.db.GetCheck(ctx, org.UID, outcomes[i].CheckUID); getErr == nil && check != nil {
			checks = append(checks, check)
		}
	}

	return withCheckNames(outcomes, checks), nil
}

// rewrapCandidates resolves the checks a re-wrap request names.
func (s *Service) rewrapCandidates(ctx context.Context, orgUID string, req *RewrapRequest) ([]*models.Check, error) {
	if len(req.CheckUIDs) > 0 {
		return s.checksByRef(ctx, orgUID, req.CheckUIDs)
	}

	if req.Region != "" {
		owned, err := s.regions.OwnsPrivateRegion(ctx, orgUID, req.Region)
		if err != nil {
			return nil, err
		}

		if !owned {
			return nil, ErrSealedSecretsRequirePrivateRegions
		}
	}

	return s.flaggedChecks(ctx, orgUID, req.Region)
}

// checksByRef loads checks by UID or slug; an unknown one is ErrCheckNotFound.
func (s *Service) checksByRef(ctx context.Context, orgUID string, refs []string) ([]*models.Check, error) {
	out := make([]*models.Check, 0, len(refs))

	for _, ref := range refs {
		check, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, ref)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && check == nil) {
			return nil, ErrCheckNotFound
		}

		if err != nil {
			return nil, err
		}

		out = append(out, check)
	}

	return out, nil
}

// flaggedChecks lists the org's sealed checks flagged needsReseal, of one
// region when region is set.
func (s *Service) flaggedChecks(ctx context.Context, orgUID, region string) ([]*models.Check, error) {
	checks, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{})
	if err != nil {
		return nil, err
	}

	out := make([]*models.Check, 0)

	for _, check := range checks {
		if check.ConfigSealed == nil || (region != "" && !slices.Contains(check.Regions, region)) {
			continue
		}

		if need := s.computeNeedsReseal(ctx, check); need != nil && *need {
			out = append(out, check)
		}
	}

	return out, nil
}

func withCheckNames(outcomes []rewraptask.Outcome, checks []*models.Check) []RewrapOutcome {
	names := make(map[string]string, len(checks))
	for _, check := range checks {
		names[check.UID] = checkDisplayName(check)
	}

	out := make([]RewrapOutcome, 0, len(outcomes))
	for i := range outcomes {
		out = append(out, RewrapOutcome{Outcome: outcomes[i], CheckName: names[outcomes[i].CheckUID]})
	}

	return out
}
