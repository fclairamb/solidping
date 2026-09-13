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
//
// One method, deliberately. It used to have a GetSystemParameter twin, because
// `${param:}` fell back to the system-wide table when the org had no such key —
// which made every instance-wide credential (`msteams.app_secret`,
// `posthog.personal_api_key`, `telegram.webhook_secret`, the SMTP password…)
// readable by any org admin who could write a check config. The fallback is
// gone: `param:` is the org-scoped, API-managed form the spec describes, and
// there is nothing else for it to read.
type ParamStore interface {
	GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error)
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

// LookupParam resolves a `${param:KEY}` reference against the referencing
// organization's OWN parameters, and nothing else.
//
// The lookup is deny-by-default: it reads exactly one row,
// `paramkeys.StorageKey(key)`, inside the namespace only an org admin can write
// (see the paramkeys package doc for why that namespace exists rather than a
// denylist). A platform key — org-scoped like `encryption.dek`, or instance-wide
// like `msteams.app_secret` — is unreachable because it is not in the namespace.
//
// The `secret` flag only masks API responses, so the plaintext value is read
// directly here.
func LookupParam(ctx context.Context, store ParamStore, orgUID, key string) (string, error) {
	if store == nil {
		return "", Unresolvedf(SchemeParam, key)
	}

	param, err := store.GetOrgParameter(ctx, orgUID, paramkeys.StorageKey(key))
	if err != nil || param == nil {
		return "", Unresolvedf(SchemeParam, key)
	}

	value, ok := StringValue(param)
	if !ok {
		return "", Unresolvedf(SchemeParam, key)
	}

	return value, nil
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
