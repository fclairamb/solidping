package checkjobsvc

import (
	"context"
	"reflect"

	"github.com/fclairamb/solidping/server/internal/secretref"
)

// ParamOverlay resolves the `${param:KEY}` references in a config and returns
// ONLY the keys whose value actually changed — the overlay to lay over the
// stored config to obtain the effective one.
//
// It is the server half of "store the reference, resolve at execution" (spec
// 2026-09-11-03). `param:` is org data the API owns, so it is resolved here, at
// the claim/dispatch boundary, and never written back to any row:
//
//   - on the in-process path the overlay is merged into the claimed job's
//     in-memory config, next to the secrets MergeJobSecrets already merged;
//   - on the agent path the overlay is folded into the SEALED payload, where
//     the agent's own merge puts it over the reference still sitting in the
//     public wire config — so a resolved value never crosses the wire in clear.
//
// `${env:}` is deliberately left alone (secretref.APIResolver skips it): it
// belongs to whichever process executes the check, which for a deported agent
// is that agent's own environment.
//
// An unresolvable reference is an error the caller turns into an explicit
// error result, exactly as it does for an unopenable envelope: running the
// check with a literal "${param:…}" in its body would quietly probe the wrong
// thing, and skipping it silently would wedge the job with nothing in the
// check's history to explain it.
func ParamOverlay(
	ctx context.Context, store secretref.ParamStore, orgUID string, config map[string]any,
) (map[string]any, error) {
	// An empty overlay is the common case (most checks reference nothing), and
	// it is a perfectly good answer — not a missing one.
	empty := map[string]any{}

	if len(config) == 0 {
		return empty, nil
	}

	resolved, replaced, err := secretref.ResolveConfig(
		ctx, config, secretref.APIResolver(store, orgUID))
	if err != nil {
		return nil, err
	}

	if !replaced {
		return empty, nil
	}

	overlay := make(map[string]any)

	for key, value := range resolved {
		if !reflect.DeepEqual(value, config[key]) {
			overlay[key] = value
		}
	}

	return overlay, nil
}
