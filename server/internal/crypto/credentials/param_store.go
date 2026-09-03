package credentials

// ParamStore implements DEKStore against the existing per-org parameters
// table. The wrapped DEK envelope is stored under a single key
// (`encryption.dek`) with secret=true so it never appears in API responses
// alongside non-secret org settings.
//
// We deliberately don't depend on db.Service here — NewParamStore takes the
// two-method slice of it we actually need, so the credentials package stays
// import-cycle-free while production and tests still share one adapter.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// dekParameterKey is where the wrapped DEK lives in the parameters table.
// The key follows the convention documented in server/CLAUDE.md (dotted
// hierarchy, snake_case within a segment).
const dekParameterKey = "encryption.dek"

// ErrDEKParamShape is returned when the stored `encryption.dek` value is JSON
// we cannot recognize as either the scalar parameter envelope or a raw DEK
// envelope. It is deliberately distinct from ErrUnknownVersion: the value never
// reached the crypto layer, so blaming an envelope version misdirects whoever
// reads the log.
var ErrDEKParamShape = errors.New("org DEK parameter has an unrecognized shape")

// LoadParamFn fetches a parameter value (as raw JSON, or nil if missing).
type LoadParamFn func(ctx context.Context, orgUID, key string) (json.RawMessage, bool, error)

// SaveParamFn writes a parameter value with the secret flag.
type SaveParamFn func(ctx context.Context, orgUID, key string, value any, secret bool) error

// ParamStore satisfies DEKStore using the supplied function adapters.
type ParamStore struct {
	Load LoadParamFn
	Save SaveParamFn
}

// OrgParameterDB is the narrow slice of db.Service the DEK store needs. Taking
// an interface rather than db.Service keeps this package free of the db import
// (and of the cycle that would come with it).
type OrgParameterDB interface {
	GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error)
	SetOrgParameter(ctx context.Context, orgUID, key string, value any, secret bool) error
}

// NewParamStore builds the production DEK store over a database service.
//
// This is THE adapter: the server (app.BuildCredentialsService), the CLI and
// the tests all go through it. A test that hand-rolls its own copy is how the
// write/read shape mismatch this file now guards against reached production —
// the tests exercised different wiring than the server did.
func NewParamStore(database OrgParameterDB) ParamStore {
	return ParamStore{
		Load: func(ctx context.Context, orgUID, key string) (json.RawMessage, bool, error) {
			param, getErr := database.GetOrgParameter(ctx, orgUID, key)
			if getErr != nil {
				return nil, false, getErr
			}

			if param == nil {
				return nil, false, nil
			}

			raw, mErr := json.Marshal(param.Value)
			if mErr != nil {
				return nil, false, fmt.Errorf("marshal dek param value: %w", mErr)
			}

			return raw, true, nil
		},
		Save: func(ctx context.Context, orgUID, key string, value any, secret bool) error {
			return database.SetOrgParameter(ctx, orgUID, key, value, secret)
		},
	}
}

// LoadDEK reads the wrapped DEK envelope from the parameters table.
func (s ParamStore) LoadDEK(ctx context.Context, orgUID string) ([]byte, bool, error) {
	value, found, err := s.Load(ctx, orgUID, dekParameterKey)
	if err != nil {
		return nil, false, fmt.Errorf("get org dek: %w", err)
	}

	if !found || len(value) == 0 {
		return nil, false, nil
	}

	envelope, err := unwrapDEKParameterValue(value)
	if err != nil {
		return nil, false, err
	}

	return envelope, true, nil
}

// unwrapDEKParameterValue normalizes whatever is stored in the `encryption.dek`
// parameter row into the raw DEK envelope JSON.
//
// SetOrgParameter has always wrapped scalars as `{"value": <v>}`
// (models.ParameterValue), so the row a running server writes reads back as
// `{"value":"{\"v\":1,…}"}`. Passing that straight to the crypto layer — which
// is what this function replaces — parsed it as an envelope with v == 0 and
// failed every cold load with "unknown envelope version".
//
// Three shapes are accepted, in this order:
//
//	{"value": …}  the standard scalar parameter envelope; the inner value is
//	              either the envelope JSON as a string or the envelope object
//	{"v":1,…}     a bare DEK envelope object, for stores that don't wrap
//	"…"           a JSON string holding the envelope, for stores that hand back
//	              the scalar unwrapped
//
// Anything else is an explicit, descriptive error rather than a crypto-layer
// one: an object with neither `value` nor `v` is ambiguous, and guessing would
// reproduce exactly the misdiagnosis this fix exists to remove.
func unwrapDEKParameterValue(value json.RawMessage) ([]byte, error) {
	return unwrapDEKParameterValueDepth(value, 1)
}

// unwrapDEKParameterValueDepth carries the remaining number of `{"value": …}`
// hops allowed, so a pathological `{"value":{"value":…}}` row terminates with
// an error instead of recursing.
func unwrapDEKParameterValueDepth(value json.RawMessage, depth int) ([]byte, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: empty value", ErrDEKParamShape)
	}

	switch trimmed[0] {
	case '"':
		var unquoted string
		if err := json.Unmarshal(trimmed, &unquoted); err != nil {
			return nil, fmt.Errorf("unwrap dek param value: %w", err)
		}

		return []byte(unquoted), nil

	case '{':
		return unwrapDEKParameterObject(trimmed, depth)

	default:
		return nil, fmt.Errorf("%w: not a JSON object or string", ErrDEKParamShape)
	}
}

func unwrapDEKParameterObject(trimmed json.RawMessage, depth int) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, fmt.Errorf("unwrap dek param value: %w", err)
	}

	if inner, ok := fields[models.ParameterValueKey]; ok {
		if depth <= 0 {
			return nil, fmt.Errorf("%w: nested %q wrappers", ErrDEKParamShape, models.ParameterValueKey)
		}

		return unwrapDEKParameterValueDepth(inner, depth-1)
	}

	// A bare envelope object: it carries the version field the crypto layer
	// keys on, so hand it through untouched.
	if _, ok := fields["v"]; ok {
		return trimmed, nil
	}

	return nil, fmt.Errorf(
		"%w: JSON object with neither %q nor %q",
		ErrDEKParamShape, models.ParameterValueKey, "v",
	)
}

// SaveDEK persists the wrapped envelope as a secret parameter row. The
// value is passed as a string; the parameters table wraps every scalar as
// {"value": …} on the way in, which LoadDEK unwraps on the way out.
func (s ParamStore) SaveDEK(ctx context.Context, orgUID string, wrapped []byte) error {
	if err := s.Save(ctx, orgUID, dekParameterKey, string(wrapped), true); err != nil {
		return fmt.Errorf("set org dek: %w", err)
	}

	return nil
}
