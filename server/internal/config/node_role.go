package config

import (
	"fmt"
	"slices"
	"strings"
)

// nodeRoleSeparator separates the entries of a multi-value SP_NODE_ROLE.
const nodeRoleSeparator = ","

// MultiValueNodeRoles returns the roles that may be combined in a
// comma-separated SP_NODE_ROLE (e.g. "api,jobs"). "all" and "agent" are
// deliberately absent: both describe the whole process, so combining them with
// anything else is a configuration mistake rather than a refinement.
func MultiValueNodeRoles() []string {
	return []string{NodeRoleAPI, NodeRoleJobs, NodeRoleChecks}
}

// exclusiveNodeRoles are the roles that must appear alone.
//
//nolint:gochecknoglobals // immutable lookup table
var exclusiveNodeRoles = []string{NodeRoleAll, NodeRoleAgent}

// NodeRoleSet is the parsed form of SP_NODE_ROLE: the set of roles this process
// was configured with. A single-value configuration yields a one-element set,
// so the historic spellings ("all", "api", "jobs", "checks", "agent") and the
// multi-value form ("api,jobs") share one code path.
type NodeRoleSet map[string]bool

// Has reports raw membership — whether role was literally listed. It does NOT
// expand "all", which is what the validation rules need ("checks" requires a
// region, but "all" never has).
func (s NodeRoleSet) Has(role string) bool {
	return s[role]
}

// Runs reports whether this set makes the node run role, expanding "all" to
// every subsystem. The exclusive roles are matched literally: "all" does not
// imply agent mode, and agent mode implies nothing else.
func (s NodeRoleSet) Runs(role string) bool {
	if slices.Contains(exclusiveNodeRoles, role) {
		return s[role]
	}

	return s[NodeRoleAll] || s[role]
}

// String renders the set in the canonical MultiValueNodeRoles order, for log
// and error messages.
func (s NodeRoleSet) String() string {
	ordered := make([]string, 0, len(s))

	for _, role := range append(append([]string{}, exclusiveNodeRoles...), MultiValueNodeRoles()...) {
		if s[role] {
			ordered = append(ordered, role)
		}
	}

	return strings.Join(ordered, nodeRoleSeparator)
}

// ParseNodeRoles parses a raw SP_NODE_ROLE value into a NodeRoleSet.
//
// Accepted forms:
//   - one of the historic exact values: "all", "api", "jobs", "checks", "agent";
//   - a comma-separated list of distinct MultiValueNodeRoles entries, e.g.
//     "api,jobs" — the combination that lets a node serve the dashboard/API and
//     process background jobs while a separate, dedicated node executes checks.
//
// Everything else is an error, on purpose: a typo that silently disabled the
// check executor would look exactly like a healthy node doing nothing.
func ParseNodeRoles(raw string) (NodeRoleSet, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w, got '%s'", ErrInvalidNodeRole, raw)
	}

	segments := strings.Split(raw, nodeRoleSeparator)
	roles := make(NodeRoleSet, len(segments))

	for _, segment := range segments {
		role := strings.TrimSpace(segment)

		switch {
		case role == "":
			return nil, fmt.Errorf("%w: empty entry in '%s'", ErrInvalidNodeRole, raw)
		case !slices.Contains(ValidNodeRoles(), role):
			return nil, fmt.Errorf("%w, got '%s'", ErrInvalidNodeRole, role)
		case roles[role]:
			return nil, fmt.Errorf("%w: '%s' is listed twice in '%s'", ErrInvalidNodeRole, role, raw)
		}

		roles[role] = true
	}

	if len(roles) > 1 {
		for _, exclusive := range exclusiveNodeRoles {
			if roles[exclusive] {
				return nil, fmt.Errorf(
					"%w: '%s' cannot be combined with other roles, got '%s' (combinable roles: %s)",
					ErrExclusiveNodeRole, exclusive, raw,
					strings.Join(MultiValueNodeRoles(), nodeRoleSeparator),
				)
			}
		}
	}

	return roles, nil
}

// NodeRoles returns the parsed SP_NODE_ROLE set, or an empty set when the
// configured value is invalid — Validate() rejects those before startup, and an
// empty set makes every ShouldRun* report false rather than guessing.
//
// It parses on every call by design: the `node.role` system parameter can
// overwrite Config.Node.Role from the database after Validate() has run
// (see systemconfig), so a set cached at load time would go stale.
func (c *Config) NodeRoles() NodeRoleSet {
	roles, err := ParseNodeRoles(c.Node.Role)
	if err != nil {
		return NodeRoleSet{}
	}

	return roles
}

// ShouldRunAPI returns true if this node should run the HTTP server.
func (c *Config) ShouldRunAPI() bool {
	return c.NodeRoles().Runs(NodeRoleAPI)
}

// ShouldRunJobs returns true if this node should run the job processor.
func (c *Config) ShouldRunJobs() bool {
	return c.NodeRoles().Runs(NodeRoleJobs)
}

// ShouldRunChecks returns true if this node should run the check executor.
func (c *Config) ShouldRunChecks() bool {
	return c.NodeRoles().Runs(NodeRoleChecks)
}

// IsAgentMode reports whether this process runs as a deported (org-scoped)
// agent: no database, no migrations, no HTTP server. The role is exclusive, so
// this is never true alongside any other subsystem.
func (c *Config) IsAgentMode() bool {
	return c.NodeRoles().Runs(NodeRoleAgent)
}
