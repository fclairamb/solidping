// Package orgparams is the org-admin CRUD surface over an organization's own
// parameters — the values a config-as-code document references as
// `${param:KEY}`.
//
// Before spec 2026-09-11-03 the only HTTP route onto the `parameters` table was
// `/api/v1/system/parameters`, super-admin only. The wiki told an org admin to
// write `${param:api_token}` in a manifest and there was no way for them to
// create `api_token`; `${env:}` was the only fallback, and an env var on the API
// pod is a deploy per secret per org — not a SaaS story. This package is that
// missing half.
//
// Two invariants shape it:
//
//   - A `secret: true` parameter is WRITE-ONLY. Its value never comes back, from
//     the list or from the single-key read. There is no "reveal" route.
//   - Platform-owned keys are refused (paramkeys.Validate). The same table holds
//     the org's wrapped encryption DEK.
package orgparams

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// MaxValueBytes caps a single parameter value. A parameter is a credential or a
// short setting, not a blob store: 8 KiB is generous for a token, a private key
// or a connection string and small enough that the table stays a configuration
// table.
const MaxValueBytes = 8 * 1024

var (
	// ErrOrgNotFound is returned when the org slug resolves to nothing.
	ErrOrgNotFound = errors.New("organization not found")
	// ErrNotFound is returned when the key does not exist for this org.
	ErrNotFound = errors.New("parameter not found")
	// ErrValueTooLarge is returned for a value above MaxValueBytes.
	ErrValueTooLarge = errors.New("parameter value is too large")
)

// Service implements the org-parameter business rules over the database.
type Service struct {
	db db.Service
}

// NewService constructs a Service.
func NewService(database db.Service) *Service {
	return &Service{db: database}
}

// Parameter is the API projection of one org parameter. Value is omitted
// entirely — not blanked — when the parameter is secret, so a client cannot
// mistake an empty string for the stored value.
type Parameter struct {
	Key       string    `json:"key"`
	Value     *string   `json:"value,omitempty"`
	Secret    bool      `json:"secret"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ListResponse wraps the list, per the repo's `{ "data": [...] }` convention.
type ListResponse struct {
	Data []*Parameter `json:"data"`
}

// SetRequest is the PUT body. It mirrors the system handler's
// SetParameterRequest so the two parameter surfaces read the same, except that
// Value is a string here: a `${param:}` reference substitutes into a config
// string, so a non-string value has nowhere to go.
type SetRequest struct {
	Value  string `json:"value"`
	Secret *bool  `json:"secret,omitempty"`
}

// List returns every org-managed parameter, secret values elided. Reserved
// platform keys are filtered out: they live in the same table but they are not
// the organization's to see or edit.
func (s *Service) List(ctx context.Context, orgSlug string) (*ListResponse, error) {
	org, err := s.org(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListOrgParameters(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("list org parameters: %w", err)
	}

	out := make([]*Parameter, 0, len(rows))

	for _, row := range rows {
		if paramkeys.IsReserved(row.Key) {
			continue
		}

		out = append(out, project(row))
	}

	return &ListResponse{Data: out}, nil
}

// Get returns one parameter, with its value only when it is not secret.
func (s *Service) Get(ctx context.Context, orgSlug, key string) (*Parameter, error) {
	org, err := s.org(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	if validErr := paramkeys.Validate(key); validErr != nil {
		return nil, validErr
	}

	row, err := s.db.GetOrgParameter(ctx, org.UID, key)
	if err != nil {
		return nil, fmt.Errorf("get org parameter: %w", err)
	}

	if row == nil {
		return nil, ErrNotFound
	}

	return project(row), nil
}

// Set creates or replaces a parameter. Writing an existing key is the rotation
// path: same key, new value, references unchanged.
func (s *Service) Set(ctx context.Context, orgSlug, key string, req *SetRequest) (*Parameter, error) {
	org, err := s.org(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	if validErr := paramkeys.Validate(key); validErr != nil {
		return nil, validErr
	}

	if len(req.Value) > MaxValueBytes {
		return nil, ErrValueTooLarge
	}

	secret := true
	if req.Secret != nil {
		secret = *req.Secret
	}

	if setErr := s.db.SetOrgParameter(ctx, org.UID, key, req.Value, secret); setErr != nil {
		return nil, fmt.Errorf("set org parameter: %w", setErr)
	}

	row, err := s.db.GetOrgParameter(ctx, org.UID, key)
	if err != nil || row == nil {
		// The write succeeded; answer from what we know rather than failing a
		// successful mutation on a read-back hiccup.
		return &Parameter{Key: key, Secret: secret, UpdatedAt: time.Now()}, nil //nolint:nilerr // see above
	}

	return project(row), nil
}

// Delete removes a parameter. A missing key is ErrNotFound rather than a silent
// 204: deleting a parameter breaks every check that references it, so the
// operator should learn they deleted nothing.
func (s *Service) Delete(ctx context.Context, orgSlug, key string) error {
	org, err := s.org(ctx, orgSlug)
	if err != nil {
		return err
	}

	if validErr := paramkeys.Validate(key); validErr != nil {
		return validErr
	}

	row, err := s.db.GetOrgParameter(ctx, org.UID, key)
	if err != nil {
		return fmt.Errorf("get org parameter: %w", err)
	}

	if row == nil {
		return ErrNotFound
	}

	if delErr := s.db.DeleteOrgParameter(ctx, org.UID, key); delErr != nil {
		return fmt.Errorf("delete org parameter: %w", delErr)
	}

	return nil
}

func (s *Service) org(ctx context.Context, orgSlug string) (*models.Organization, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return nil, ErrOrgNotFound
	}

	return org, nil
}

// project maps a stored row onto the API shape, eliding a secret value.
func project(row *models.Parameter) (out *Parameter) {
	secret := row.Secret != nil && *row.Secret

	out = &Parameter{Key: row.Key, Secret: secret, UpdatedAt: row.UpdatedAt}
	if secret {
		return out
	}

	if value, ok := stringValue(row); ok {
		out.Value = &value
	}

	return out
}

func stringValue(row *models.Parameter) (string, bool) {
	raw, ok := row.Value[models.ParameterValueKey]
	if !ok {
		return "", false
	}

	if str, isStr := raw.(string); isStr {
		return str, true
	}

	return fmt.Sprintf("%v", raw), true
}
