package secretref

import "strings"

// EnvAllowFunc reports whether `${env:NAME}` may resolve NAME on this process.
//
// It is the boundary of the `env:` scheme. Without it a reference could read
// the executing process's whole environment, and for the in-process worker that
// is the API server: `${env:SP_AUTH_JWT_SECRET}` in a check URL sent the secret
// to whatever target the check pointed at. The operator names what `env:` may
// read (SP_CHECK_ENV_ALLOWLIST, config.Config.EnvRefAllowed) and nothing else
// is in scope.
//
// A nil EnvAllowFunc denies every name: deny by default, like the egress guard
// and the `param:` namespace.
//
// The SAME predicate gates the save path (DisallowedEnvRefs, DocumentResolver)
// and the execution path (ExecutionResolver), so a name accepted at save time
// is exactly a name that resolves at run time.
type EnvAllowFunc func(name string) bool

// EnvAllowed reports whether allow lets `${env:name}` resolve. A nil allow
// denies everything.
func EnvAllowed(allow EnvAllowFunc, name string) bool {
	return allow != nil && allow(name)
}

// AllowEnvNames returns an EnvAllowFunc admitting exactly the given names
// (exact, case-sensitive match, like the environment itself). No names admits
// nothing.
func AllowEnvNames(names ...string) EnvAllowFunc {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = struct{}{}
		}
	}

	return func(name string) bool {
		_, ok := set[name]

		return ok
	}
}

// DisallowedEnvRefs returns the `${env:…}` names in value that allow refuses,
// in order of appearance.
func DisallowedEnvRefs(value string, allow EnvAllowFunc) []string {
	var out []string

	for _, match := range Pattern.FindAllStringSubmatch(value, -1) {
		if match[1] == SchemeEnv && !EnvAllowed(allow, match[2]) {
			out = append(out, match[2])
		}
	}

	return out
}

// DisallowedEnvMessage is the one wording every save-time validator uses for an
// `${env:}` reference outside the operator allowlist. It deliberately says
// nothing about whether the variable exists on the server.
func DisallowedEnvMessage(name string) string {
	return "${env:" + name + "} is not allowed: the server operator must list " + name +
		" in SP_CHECK_ENV_ALLOWLIST (use ${param:…} for an organization-managed secret)"
}
