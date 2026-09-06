package checks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Demo-session write rules (spec 2026-09-06-02).
//
// The route-level guard in RequireAuth decides WHICH endpoints a demo session
// may call at all. It cannot decide WHICH CHECK a call may touch, or what a
// created check may contain — those are questions about rows and payloads, so
// they live here, in the service, on every path that writes a check.
var (
	// ErrDemoReadOnly is the ownership refusal: a demo session tried to edit,
	// delete or clone a check it did not create. Seeded catalogue checks carry
	// created_by = NULL, so they are immutable to every demo session by
	// construction — no "protected" column anywhere.
	ErrDemoReadOnly = errors.New("the demo can only change checks it created")
	// ErrDemoCheckTypeNotAllowed refuses a check type outside the
	// side-effect-free, credential-free set. Notably excluded: smtp (send mode
	// is a spam relay behind a public login), email, browser (cost), ssh,
	// kubernetes, docker and every database type.
	ErrDemoCheckTypeNotAllowed = errors.New("this check type is not available in the demo")
	// ErrDemoPeriodTooShort refuses a sub-minute period. The org-wide
	// maxChecksPerMinute entitlement is the real ceiling; this floor just keeps
	// one visitor from pinning a target every ten seconds.
	ErrDemoPeriodTooShort = errors.New("demo checks must run at most once per minute")
	// ErrDemoPrivateRegion refuses a private (deported-agent) region. The demo
	// org will never own a custom_regions parameter, so this is already
	// structurally true — asserting it means it stays true if that ever
	// changes.
	ErrDemoPrivateRegion = errors.New("the demo can only use public regions")
)

// demoMinPeriod is the floor on a demo-created check's period.
const demoMinPeriod = time.Minute

// demoFieldRegions names the regions field in a demo refusal.
const demoFieldRegions = "regions"

// demoAllowedCheckTypes is the complete set of check types a demo session may
// create. An allowlist, like the route guard: a check type added next year is
// unavailable in the demo until somebody deliberately adds it here.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const slices.
var demoAllowedCheckTypes = []string{
	string(checkerdef.CheckTypeHTTP),
	string(checkerdef.CheckTypeTCP),
	string(checkerdef.CheckTypeICMP),
	string(checkerdef.CheckTypeDNS),
	string(checkerdef.CheckTypeSSL),
}

// DemoAllowedCheckTypes returns the demo-writable check types. Exported so the
// dashboard's type picker and the tests read the same list the service
// enforces.
func DemoAllowedCheckTypes() []string {
	return slices.Clone(demoAllowedCheckTypes)
}

// callerIdentity is the demo-relevant identity of whoever is making the
// request, read off the context RequireAuth populated.
//
// A zero value (no claims — a startup job, a background sweep, a unit test
// calling the service directly) is deliberately "not a demo session, and no
// creator to record". That is exactly what makes the seeded catalogue's checks
// created_by = NULL.
type callerIdentity struct {
	userUID string
	demo    bool
}

// callerFromContext extracts the caller's identity from the request context.
func callerFromContext(ctx context.Context) callerIdentity {
	claims, ok := mw.GetClaimsFromContext(ctx)
	if !ok || claims == nil {
		return callerIdentity{}
	}

	return callerIdentity{userUID: claims.UserUID, demo: claims.Demo}
}

// createdByForCaller returns the value to store in checks.created_by, or nil
// when there is no human creator (server-side seeding, background jobs).
func createdByForCaller(ctx context.Context) *string {
	caller := callerFromContext(ctx)
	if caller.userUID == "" {
		return nil
	}

	uid := caller.userUID

	return &uid
}

// assertDemoMayWriteCheck enforces ownership: a demo session may PATCH, DELETE
// or clone only a check it created itself.
//
// Non-demo callers pass untouched — this is not a general authorization rule,
// and the org-membership check has already run in RequireOrgAccess.
func assertDemoMayWriteCheck(ctx context.Context, check *models.Check) error {
	caller := callerFromContext(ctx)
	if !caller.demo {
		return nil
	}

	if check == nil || check.CreatedBy == nil || *check.CreatedBy != caller.userUID {
		return ErrDemoReadOnly
	}

	return nil
}

// assertDemoCheckShape enforces what a demo session may put IN a check: the
// type allowlist, the period floor and public regions only.
//
// checkType is the resolved type, period the resolved period (zero means "the
// default", which is one minute and therefore fine), and resolvedRegions the
// region set after ResolveRegionsForCheck — the stored shape, not the raw
// request, so an alias or a default cannot smuggle a private region in.
func assertDemoCheckShape(ctx context.Context, checkType string, period time.Duration, resolvedRegions []string) error {
	if !callerFromContext(ctx).demo {
		return nil
	}

	if !slices.Contains(demoAllowedCheckTypes, strings.ToLower(checkType)) {
		return fmt.Errorf("%w: %s (allowed: %s)",
			ErrDemoCheckTypeNotAllowed, checkType, strings.Join(demoAllowedCheckTypes, ", "))
	}

	if period > 0 && period < demoMinPeriod {
		return fmt.Errorf("%w: %s is below the %s floor",
			ErrDemoPeriodTooShort, period, demoMinPeriod)
	}

	for _, region := range resolvedRegions {
		if strings.HasPrefix(region, regions.PrivateRegionPrefix) {
			return fmt.Errorf("%w: %s", ErrDemoPrivateRegion, region)
		}
	}

	return nil
}

// isDemoFieldError reports whether err is one of the demo payload refusals,
// which are caller mistakes (a 400 naming the field) rather than the ownership
// refusal (a 403 DEMO_READ_ONLY).
func isDemoFieldError(err error) bool {
	return errors.Is(err, ErrDemoCheckTypeNotAllowed) ||
		errors.Is(err, ErrDemoPeriodTooShort) ||
		errors.Is(err, ErrDemoPrivateRegion)
}

// demoFieldName maps a demo payload refusal to the request field it is about,
// so the dashboard can attach the message to the right input.
func demoFieldName(err error) string {
	switch {
	case errors.Is(err, ErrDemoCheckTypeNotAllowed):
		return fieldType
	case errors.Is(err, ErrDemoPeriodTooShort):
		return fieldPeriod
	case errors.Is(err, ErrDemoPrivateRegion):
		return demoFieldRegions
	default:
		return fieldBody
	}
}
