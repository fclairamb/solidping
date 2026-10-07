package dbctx

import (
	"context"
	"sync"
)

// ChangeSource says who is changing a check and why, for the check version
// history (spec 2026-10-03-06). Handler entry points (dashboard/API create
// and update, config-as-code apply and import, MCP) put one on the context;
// the DB layer reads it when it records a version. No change source means a
// system change.
//
// One ChangeSource is one logical change: every check write made under it is
// folded into a single version per check. A PATCH that renames a check and
// edits its labels (two DB writes) therefore records one version, not two.
type ChangeSource struct {
	// Origin is a models.CheckVersionOrigin value.
	Origin string
	// UserUID is the acting user, empty for none.
	UserUID string
	// Reason is an optional one-line note stored on the version.
	Reason string
	// BaseVersion is the version this change was built on, stored on the
	// version it records (an AI repair applied directly, spec 2026-10-03-07).
	BaseVersion *int

	mu sync.Mutex
	// versions maps a check UID to the version row already written under
	// this change, which later writes amend instead of adding a new row.
	versions map[string]int
}

type changeSourceKey struct{}

// WithChangeSource returns a context carrying a fresh change source.
func WithChangeSource(ctx context.Context, origin, userUID string) context.Context {
	return WithChange(ctx, &ChangeSource{Origin: origin, UserUID: userUID})
}

// WithChange returns a context carrying the given change source.
func WithChange(ctx context.Context, source *ChangeSource) context.Context {
	return context.WithValue(ctx, changeSourceKey{}, source)
}

// ChangeSourceFromContext returns the change source on the context, or nil.
func ChangeSourceFromContext(ctx context.Context) *ChangeSource {
	source, _ := ctx.Value(changeSourceKey{}).(*ChangeSource)

	return source
}

// RecordedVersion returns the version already written for the check under
// this change, if any.
func (c *ChangeSource) RecordedVersion(checkUID string) (int, bool) {
	if c == nil {
		return 0, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	version, ok := c.versions[checkUID]

	return version, ok
}

// SetRecordedVersion remembers the version written for the check under this
// change. Also used to make a write amend an existing row, which is how
// approving a proposal turns that very row into the applied version.
func (c *ChangeSource) SetRecordedVersion(checkUID string, version int) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.versions == nil {
		c.versions = map[string]int{}
	}

	c.versions[checkUID] = version
}
