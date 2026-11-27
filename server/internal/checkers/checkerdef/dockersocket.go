package checkerdef

import (
	"context"
	"fmt"
)

// The docker check reaches the Docker daemon through the moby client, not
// through DialLocalSocket, so checkers.unix_socket_paths does not cover it.
// Its unix:// hosts are held to their own allowlist instead (spec
// 2026-11-27-03): without it a check author could make the process send an
// HTTP request to any Unix socket it can open.

// DockerSocketSetting is the configuration key of the Docker socket
// allowlist, named in every refusal.
const DockerSocketSetting = "checkers.docker_socket_paths"

// DockerSocketEnv is its comma-separated environment override.
const DockerSocketEnv = "SP_CHECKERS_DOCKER_SOCKET_PATHS"

// DefaultDockerSocketPaths is the Docker socket allowlist when the operator
// sets none: the two usual locations of the rootful Docker daemon.
func DefaultDockerSocketPaths() []string {
	return []string{"/var/run/docker.sock", "/run/docker.sock"}
}

// DockerSocketTarget is implemented by the docker check config: path is the
// Unix socket its host names (empty for a tcp:// host), field the config key
// holding it.
type DockerSocketTarget interface {
	DockerSocket() (field, path string)
}

// DockerSocketOf returns the Unix socket cfg's docker host names, ok=false
// when cfg is not a docker config or targets a tcp:// host.
// The results are the config field, the path and ok.
func DockerSocketOf(cfg Config) (string, string, bool) {
	target, isTarget := cfg.(DockerSocketTarget)
	if !isTarget || target == nil {
		return "", "", false
	}

	field, path := target.DockerSocket()

	return field, path, path != ""
}

// DockerSocketAllowed applies the path syntax rule and the Docker socket
// allowlist (same matching as LocalSocketAllowed) to path. An empty
// allowlist refuses every path.
func DockerSocketAllowed(allowlist []string, path string) error {
	if err := ValidateLocalSocketPath(path); err != nil {
		return err
	}

	if socketPathListed(allowlist, path) {
		return nil
	}

	if len(allowlist) == 0 {
		return fmt.Errorf("%w: docker unix sockets are off; an operator can list allowed paths in %s (%s)",
			ErrLocalSocketNotAllowed, DockerSocketSetting, DockerSocketEnv)
	}

	return fmt.Errorf("%w: %s is not covered by %s (%s)",
		ErrLocalSocketNotAllowed, path, DockerSocketSetting, DockerSocketEnv)
}

type dockerSocketAllowlistCtxKey struct{}

// WithDockerSocketAllowlist returns a context carrying the Docker socket
// allowlist of the process that executes the check. The worker sets it on
// every execution context.
func WithDockerSocketAllowlist(ctx context.Context, allowlist []string) context.Context {
	if allowlist == nil {
		allowlist = []string{}
	}

	return context.WithValue(ctx, dockerSocketAllowlistCtxKey{}, allowlist)
}

// DockerSocketAllowlistFrom returns the allowlist carried by ctx, or
// DefaultDockerSocketPaths when ctx carries none.
func DockerSocketAllowlistFrom(ctx context.Context) []string {
	if ctx != nil {
		if allowlist, ok := ctx.Value(dockerSocketAllowlistCtxKey{}).([]string); ok {
			return allowlist
		}
	}

	return DefaultDockerSocketPaths()
}
