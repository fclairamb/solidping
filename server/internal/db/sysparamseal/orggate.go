package sysparamseal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// OrgSealGate reports what still prevents the secret organization parameters
// from being sealed: one description per live reader that cannot open
// `$sp_sealed` (spec 2026-11-28-01). An empty answer opens the gate. In
// production it lists the direct-DB workers older than the first version that
// opens sealed org parameters: they resolve `${param:…}` themselves, straight
// from the database.
type OrgSealGate func(ctx context.Context) ([]string, error)

// ErrOrgSealDeferred marks a seal of the secret organization parameters
// deferred by the gate. Plaintext rows keep working for every version, so
// deferring is always safe; the caller retries later.
var ErrOrgSealDeferred = errors.New("sealing secret organization parameters deferred")

// OrgSealDeferredError carries why the seal was deferred: the readers that
// block it, or the error the gate failed with.
type OrgSealDeferredError struct {
	Blockers []string
	Cause    error
}

func (e *OrgSealDeferredError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", ErrOrgSealDeferred, e.Cause)
	}

	return fmt.Sprintf("%s: waiting for %s", ErrOrgSealDeferred, strings.Join(e.Blockers, ", "))
}

// Is makes errors.Is(err, ErrOrgSealDeferred) match.
func (e *OrgSealDeferredError) Is(target error) bool {
	return target == ErrOrgSealDeferred
}

// Unwrap returns the gate's own error, if any.
func (e *OrgSealDeferredError) Unwrap() error {
	return e.Cause
}

// SetOrgSealGate installs the gate. Call it once at startup, before the
// service is shared; nil (the default) keeps the gate always open.
func (s *Service) SetOrgSealGate(gate OrgSealGate) {
	s.orgSealGate = gate
}

// OrgSealPending reports whether a secret organization parameter was left in
// plaintext because the gate was closed (at the startup sweep or on a write),
// so a later sweep has work to do.
func (s *Service) OrgSealPending() bool {
	return s.orgSealPending.Load()
}

// checkOrgSealGate returns nil when sealing may proceed, an
// *OrgSealDeferredError otherwise.
func (s *Service) checkOrgSealGate(ctx context.Context) error {
	if s.orgSealGate == nil {
		return nil
	}

	blockers, err := s.orgSealGate(ctx)
	if err != nil {
		return &OrgSealDeferredError{Cause: err}
	}

	if len(blockers) > 0 {
		return &OrgSealDeferredError{Blockers: blockers}
	}

	return nil
}

// logGatedWrite notes a secret write kept in plaintext by the gate.
func logGatedWrite(ctx context.Context, key string, err error) {
	slog.DebugContext(ctx, "secret organization parameter written in plaintext until the seal gate opens",
		"key", key, "reason", err)
}
