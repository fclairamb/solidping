package credentials

// ParamStore implements DEKStore against the existing per-org parameters
// table. The wrapped DEK envelope is stored under a single key
// (`encryption.dek`) with secret=true so it never appears in API responses
// alongside non-secret org settings.
//
// We deliberately don't depend on db.Service here — the caller wires a
// pair of small function adapters so the credentials package stays
// import-cycle-free.

import (
	"context"
	"encoding/json"
	"fmt"
)

// dekParameterKey is where the wrapped DEK lives in the parameters table.
// The key follows the convention documented in server/CLAUDE.md (dotted
// hierarchy, snake_case within a segment).
const dekParameterKey = "encryption.dek"

// LoadParamFn fetches a parameter value (as raw JSON, or nil if missing).
type LoadParamFn func(ctx context.Context, orgUID, key string) (json.RawMessage, bool, error)

// SaveParamFn writes a parameter value with the secret flag.
type SaveParamFn func(ctx context.Context, orgUID, key string, value any, secret bool) error

// ParamStore satisfies DEKStore using the supplied function adapters.
type ParamStore struct {
	Load LoadParamFn
	Save SaveParamFn
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

	// The stored value is a JSON-encoded string holding the envelope JSON
	// (we wrote it via SetOrgParameter(string)). Unwrap if it starts with
	// a quote; pass through otherwise so a raw envelope still works.
	if value[0] == '"' {
		var unquoted string
		if err := json.Unmarshal(value, &unquoted); err != nil {
			return nil, false, fmt.Errorf("unwrap dek param value: %w", err)
		}

		return []byte(unquoted), true, nil
	}

	return []byte(value), true, nil
}

// SaveDEK persists the wrapped envelope as a secret parameter row. The
// value is wrapped as a JSON string so the parameters value column (jsonb)
// always sees valid JSON regardless of the envelope's exact encoding.
func (s ParamStore) SaveDEK(ctx context.Context, orgUID string, wrapped []byte) error {
	if err := s.Save(ctx, orgUID, dekParameterKey, string(wrapped), true); err != nil {
		return fmt.Errorf("set org dek: %w", err)
	}

	return nil
}
