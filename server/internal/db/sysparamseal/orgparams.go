package sysparamseal

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// ShouldSealOrgParameter reports whether an organization parameter is
// encrypted at rest when a master key is configured: a secret row of the
// org-managed namespace (`usr.`, the values `${param:KEY}` resolves). A
// non-secret row stays in plaintext (the API returns its value anyway), and the
// platform's own per-org rows (`encryption.dek`, region keys, …) are left alone.
func ShouldSealOrgParameter(key string, secret bool) bool {
	return secret && strings.HasPrefix(key, paramkeys.OrgKeyPrefix)
}

// GetOrgParameter returns the parameter with its value opened.
func (s *Service) GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error) {
	param, err := s.Service.GetOrgParameter(ctx, orgUID, key)
	if err != nil || param == nil {
		return param, err
	}

	return s.openOrg(ctx, param)
}

// ListOrgParameters returns every parameter of one organization with the
// values opened. One row that cannot be opened fails the whole list.
func (s *Service) ListOrgParameters(ctx context.Context, orgUID string) ([]*models.Parameter, error) {
	params, err := s.Service.ListOrgParameters(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	return s.openOrgAll(ctx, params)
}

// ListOrgParametersByKey returns the org parameters with one key, values opened.
func (s *Service) ListOrgParametersByKey(ctx context.Context, key string) ([]*models.Parameter, error) {
	params, err := s.Service.ListOrgParametersByKey(ctx, key)
	if err != nil {
		return nil, err
	}

	return s.openOrgAll(ctx, params)
}

// SetOrgParameter seals a secret `usr.` value when a master key is configured,
// then writes it. Any other row is written as given, so a row turned from
// secret to non-secret is rewritten in clear by the same call. While the seal
// gate is closed (a live direct-DB worker cannot open `$sp_sealed`), a secret
// value is written in plaintext too, and the next sweep seals it.
func (s *Service) SetOrgParameter(ctx context.Context, orgUID, key string, value any, secret bool) error {
	stored := value

	seal := s.Enabled() && ShouldSealOrgParameter(key, secret)
	if seal {
		if gateErr := s.checkOrgSealGate(ctx); gateErr != nil {
			logGatedWrite(ctx, key, gateErr)
			s.orgSealPending.Store(true)

			seal = false
		}
	}

	if seal {
		envelope, err := s.sealer.Seal(ctx, models.ParameterValue(value))
		if err != nil {
			return fmt.Errorf("seal %s %q: %w", kindOrg, key, err)
		}

		stored = map[string]any{SealedMarkerKey: envelope}
	}

	return s.Service.SetOrgParameter(ctx, orgUID, key, stored, secret)
}

// SealPlaintextOrgParameters seals every secret organization parameter of the
// live organizations still stored in plaintext. Idempotent; a no-op without a
// master key. Returns how many rows it sealed.
//
// While the seal gate is closed it seals nothing and returns an
// *OrgSealDeferredError (errors.Is ErrOrgSealDeferred): the caller logs it and
// retries later.
func (s *Service) SealPlaintextOrgParameters(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}

	if gateErr := s.checkOrgSealGate(ctx); gateErr != nil {
		s.orgSealPending.Store(true)

		return 0, gateErr
	}

	// Cleared before the sweep: a gated write racing it sets the flag again.
	s.orgSealPending.Store(false)

	sealed := 0

	err := s.eachPlaintextOrgSecret(ctx, func(orgUID string, param *models.Parameter) error {
		envelope, sealErr := s.sealer.Seal(ctx, map[string]any(param.Value))
		if sealErr != nil {
			return fmt.Errorf("seal %s %q: %w", kindOrg, param.Key, sealErr)
		}

		if setErr := s.Service.SetOrgParameter(
			ctx, orgUID, param.Key, map[string]any{SealedMarkerKey: envelope}, true,
		); setErr != nil {
			return fmt.Errorf("store sealed %s %q: %w", kindOrg, param.Key, setErr)
		}

		sealed++

		return nil
	})
	if err != nil {
		s.orgSealPending.Store(true)
	}

	return sealed, err
}

// PlaintextSecretOrgParameterCount counts the secret organization parameters
// stored in plaintext, for the startup warning. The keys are the orgs' own and
// are not logged.
func (s *Service) PlaintextSecretOrgParameterCount(ctx context.Context) (int, error) {
	count := 0

	err := s.eachPlaintextOrgSecret(ctx, func(string, *models.Parameter) error {
		count++

		return nil
	})

	return count, err
}

func (s *Service) eachPlaintextOrgSecret(
	ctx context.Context, visit func(orgUID string, param *models.Parameter) error,
) error {
	orgs, err := s.Raw().ListOrganizations(ctx)
	if err != nil {
		return fmt.Errorf("list organizations: %w", err)
	}

	for _, org := range orgs {
		params, listErr := s.Service.ListOrgParameters(ctx, org.UID)
		if listErr != nil {
			return fmt.Errorf("list parameters of organization %s: %w", org.UID, listErr)
		}

		for _, param := range params {
			if param == nil || !ShouldSealOrgParameter(param.Key, isSecret(param)) {
				continue
			}

			if _, isSealed := SealedEnvelope(param.Value); isSealed {
				continue
			}

			if visitErr := visit(org.UID, param); visitErr != nil {
				return visitErr
			}
		}
	}

	return nil
}

// openOrg opens a sealed secret `usr.` row. A row outside that set is returned
// as stored, even if its value happens to look like an envelope: only rows the
// decorator seals are ever opened.
func (s *Service) openOrg(ctx context.Context, param *models.Parameter) (*models.Parameter, error) {
	if !ShouldSealOrgParameter(param.Key, isSecret(param)) {
		return param, nil
	}

	return s.open(ctx, kindOrg, param)
}

func (s *Service) openOrgAll(ctx context.Context, params []*models.Parameter) ([]*models.Parameter, error) {
	for i, param := range params {
		if param == nil {
			continue
		}

		opened, err := s.openOrg(ctx, param)
		if err != nil {
			return nil, err
		}

		params[i] = opened
	}

	return params, nil
}
