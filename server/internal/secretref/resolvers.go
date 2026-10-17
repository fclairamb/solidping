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
// environment, and only for a name allow admits (see EnvAllowFunc). A refused
// name fails exactly like an unset one, so the error never tells the caller
// whether the variable exists.
func LookupEnv(name string, allow EnvAllowFunc) (string, error) {
	if !EnvAllowed(allow, name) {
		return "", Unresolvedf(SchemeEnv, name)
	}

	value, ok := os.LookupEnv(name)
	if !ok {
		return "", Unresolvedf(SchemeEnv, name)
	}

	return value, nil
}

// DocumentResolver resolves every scheme. It is the write-path resolver: an
// import or apply uses it to prove every reference in the document is
// resolvable before storing anything — it never stores what it resolved.
//
// A `${var:…}` reference is accepted when its name is known (a generated
// sample stands in for the value) and rejected otherwise.
//
// An `${env:…}` reference resolves only when allowEnv admits its name, so the
// write path cannot be used as an oracle for which server variables exist.
func DocumentResolver(store ParamStore, orgUID string, allowEnv EnvAllowFunc) ResolveFunc {
	vars := newRunVarResolver()

	return func(ctx context.Context, scheme, name string) (string, error) {
		switch scheme {
		case SchemeEnv:
			return LookupEnv(name, allowEnv)
		case SchemeParam:
			return LookupParam(ctx, store, orgUID, name)
		case SchemeVar:
			return vars(ctx, scheme, name)
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
//
// It also generates `${var:…}` values, from one var resolver per call: one
// ExecutionResolver is one run, so every occurrence of `${var:uuid}` in it gets
// the same value, and the next run gets a new one.
//
// `${env:…}` resolves only for a name allowEnv admits; any other name fails
// with ErrUnresolved, exactly like an unset variable.
func ExecutionResolver(allowEnv EnvAllowFunc) ResolveFunc {
	return ExecutionResolverWith(newRunVarResolver(), allowEnv)
}

// ExecutionResolverWith is ExecutionResolver with the run's var resolver
// supplied by the caller, so the caller can render a second string (the
// result's URL) with the same generated values. vars must come from
// NewVarResolver.
func ExecutionResolverWith(vars ResolveFunc, allowEnv EnvAllowFunc) ResolveFunc {
	return func(ctx context.Context, scheme, name string) (string, error) {
		switch scheme {
		case SchemeVar:
			return vars(ctx, scheme, name)
		case SchemeEnv:
			return LookupEnv(name, allowEnv)
		default:
			return "", Unresolvedf(scheme, name)
		}
	}
}

// VarsOnlyResolver resolves `${var:…}` with vars and leaves every env/param
// reference literal. It renders a string for display (a stored result URL)
// where the generated values belong and the secrets do not.
func VarsOnlyResolver(vars ResolveFunc) ResolveFunc {
	return func(ctx context.Context, scheme, name string) (string, error) {
		if scheme != SchemeVar {
			return "", ErrSkip
		}

		return vars(ctx, scheme, name)
	}
}
