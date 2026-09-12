package secretref

import (
	"context"
	"fmt"
	"os"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// ParamStore is the narrow slice of the DB service a `${param:}` lookup needs.
// Taking an interface (rather than db.Service) keeps this package a leaf, so
// the worker, the agent dispatch path and the checks handlers can all share the
// grammar without dragging the whole database surface with them.
type ParamStore interface {
	GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error)
	GetSystemParameter(ctx context.Context, key string) (*models.Parameter, error)
}

// StringValue extracts the stringified value from a parameter's `{"value": …}`
// envelope — the single shape both engines write.
func StringValue(param *models.Parameter) (string, bool) {
	if param == nil {
		return "", false
	}

	raw, ok := param.Value[models.ParameterValueKey]
	if !ok {
		return "", false
	}

	switch val := raw.(type) {
	case string:
		return val, true
	case fmt.Stringer:
		return val.String(), true
	default:
		return fmt.Sprintf("%v", val), true
	}
}

// LookupParam resolves a `${param:KEY}` reference: the org-scoped parameter
// wins over the system-wide one. The `secret` flag only masks API responses, so
// the plaintext value is read directly here.
func LookupParam(ctx context.Context, store ParamStore, orgUID, key string) (string, error) {
	if store == nil {
		return "", Unresolvedf(SchemeParam, key)
	}

	// A reserved key is platform material, not org data: `${param:encryption.dek}`
	// in an HTTP check's body would POST the org's wrapped encryption key to a
	// URL of the author's choosing, and `${param:email.password}` would do the
	// same for the instance's SMTP credentials (the system-parameter fallback
	// below reaches those). Refused as simply "not found" — the reference is
	// unresolvable, and saying which internal key exists is itself a hint.
	if paramkeys.IsReserved(key) {
		return "", Unresolvedf(SchemeParam, key)
	}

	if orgParam, err := store.GetOrgParameter(ctx, orgUID, key); err == nil && orgParam != nil {
		if value, ok := StringValue(orgParam); ok {
			return value, nil
		}
	}

	if sysParam, err := store.GetSystemParameter(ctx, key); err == nil && sysParam != nil {
		if value, ok := StringValue(sysParam); ok {
			return value, nil
		}
	}

	return "", Unresolvedf(SchemeParam, key)
}

// LookupEnv resolves an `${env:NAME}` reference against this process's
// environment.
func LookupEnv(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", Unresolvedf(SchemeEnv, name)
	}

	return value, nil
}

// DocumentResolver resolves BOTH schemes. It is the write-path resolver: an
// import or apply uses it to prove every reference in the document is
// resolvable before storing anything — it never stores what it resolved.
func DocumentResolver(store ParamStore, orgUID string) ResolveFunc {
	return func(ctx context.Context, scheme, name string) (string, error) {
		switch scheme {
		case SchemeEnv:
			return LookupEnv(name)
		case SchemeParam:
			return LookupParam(ctx, store, orgUID, name)
		default:
			return "", Unresolvedf(scheme, name)
		}
	}
}

// APIResolver resolves `${param:}` only and SKIPS `${env:}`. It is the
// dispatch-side resolver: `param:` is org data the API owns, so it is resolved
// here and travels inside the sealed job payload, while `env:` is deliberately
// left for whichever process executes the check — on a deported agent that is
// the agent's own environment, which is the per-region-secret feature.
func APIResolver(store ParamStore, orgUID string) ResolveFunc {
	return func(ctx context.Context, scheme, name string) (string, error) {
		if scheme != SchemeParam {
			return "", ErrSkip
		}

		return LookupParam(ctx, store, orgUID, name)
	}
}

// ExecutionResolver resolves `${env:}` from this process's environment and
// FAILS on anything else. It is the last stop: a `${param:}` still present here
// means the API could not resolve it (a deleted parameter, or a private-region
// agent whose sealed envelope the server cannot open), and the check must go
// visibly red rather than send the literal `${param:…}` to the target.
func ExecutionResolver() ResolveFunc {
	return func(_ context.Context, scheme, name string) (string, error) {
		if scheme != SchemeEnv {
			return "", Unresolvedf(scheme, name)
		}

		return LookupEnv(name)
	}
}
