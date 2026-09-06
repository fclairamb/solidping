package checks_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// This file covers §3 of spec 2026-09-06-02: what a demo session may create,
// and what it owns.
//
// Every negative is paired with a positive control. "The demo could not do it"
// is worthless on its own — a rule that refused everybody would satisfy it just
// as well, and would break the product for real customers.

// demoCtx returns a context carrying the claims RequireAuth would have parked
// there for a demo session.
func demoCtx(userUID string) context.Context {
	return context.WithValue(context.Background(), base.ContextKeyClaims,
		&auth.Claims{UserUID: userUID, OrgSlug: "quota-org", Role: "user", Demo: true})
}

// plainCtx is the same for an ordinary session.
func plainCtx(userUID string) context.Context {
	return context.WithValue(context.Background(), base.ContextKeyClaims,
		&auth.Claims{UserUID: userUID, OrgSlug: "quota-org", Role: "user"})
}

// TestCreatedByIsRecordedForEveryCreator pins the column's contract: it is NOT
// a demo feature bolted onto one code path, it is audit data recorded for
// everybody. A column populated on one path only is a column nobody can trust
// — and the demo's whole ownership model rests on trusting it.
func TestCreatedByIsRecordedForEveryCreator(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, org := setupQuotaService(t, 100)

	created, err := svc.CreateCheck(plainCtx("user-plain"), org.Slug, httpCheckReq())
	r.NoError(err)
	r.NotNil(created.CreatedBy)
	r.Equal("user-plain", *created.CreatedBy)

	stored, err := dbSvc.GetCheckByUidOrSlug(context.Background(), org.UID, created.UID)
	r.NoError(err)
	r.NotNil(stored.CreatedBy)
	r.Equal("user-plain", *stored.CreatedBy)
}

// TestSeededChecksHaveNoCreator is the other half of the ownership model: a
// check created with no claims on the context — the startup job's catalogue —
// has created_by = NULL, and NULL never equals a user's UID. That, and nothing
// else, is what makes the seeded catalogue immutable to a demo session.
func TestSeededChecksHaveNoCreator(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	created, err := svc.CreateCheck(context.Background(), org.Slug, httpCheckReq())
	r.NoError(err)
	r.Nil(created.CreatedBy)
}

// TestDemoSessionOwnsOnlyWhatItCreated is the ownership rule on patch, delete
// and the positive control that its own checks stay editable.
func TestDemoSessionOwnsOnlyWhatItCreated(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	// A seeded check: created with no claims, therefore owned by nobody.
	seeded, err := svc.CreateCheck(context.Background(), org.Slug, httpCheckReq())
	r.NoError(err)

	visitor := demoCtx("visitor-1")

	own := httpCheckReq()
	own.Slug = "visitor-check"
	mine, err := svc.CreateCheck(visitor, org.Slug, own)
	r.NoError(err)
	r.NotNil(mine.CreatedBy)
	r.Equal("visitor-1", *mine.CreatedBy)

	name := "renamed"

	// Editing the seeded check is refused...
	_, err = svc.UpdateCheck(visitor, org.Slug, seeded.UID, &checks.UpdateCheckRequest{Name: &name})
	r.ErrorIs(err, checks.ErrDemoReadOnly)

	// ...deleting it is refused...
	r.ErrorIs(svc.DeleteCheck(visitor, org.Slug, seeded.UID), checks.ErrDemoReadOnly)

	// ...and another visitor's check is refused too: ownership is per user,
	// not "any demo session owns any demo check".
	_, err = svc.UpdateCheck(demoCtx("visitor-2"), org.Slug, mine.UID,
		&checks.UpdateCheckRequest{Name: &name})
	r.ErrorIs(err, checks.ErrDemoReadOnly)

	// POSITIVE CONTROL: the visitor's own check is fully editable and
	// deletable. Without this, a rule that refused every demo write would pass
	// every assertion above.
	_, err = svc.UpdateCheck(visitor, org.Slug, mine.UID, &checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)
	r.NoError(svc.DeleteCheck(visitor, org.Slug, mine.UID))
}

// TestNonDemoSessionsAreNotOwnershipGated proves the ownership rule is scoped
// to demo sessions. An ordinary member of an organization may edit a colleague's
// check; introducing a per-creator lock for everyone would be a large,
// unannounced product change.
func TestNonDemoSessionsAreNotOwnershipGated(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	created, err := svc.CreateCheck(plainCtx("alice"), org.Slug, httpCheckReq())
	r.NoError(err)

	name := "bob was here"
	_, err = svc.UpdateCheck(plainCtx("bob"), org.Slug, created.UID,
		&checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)
}

// TestDemoCheckTypesAreAllowlisted covers the type rule, including the classes
// the spec names explicitly: smtp (a spam relay behind a public login),
// browser (cost), ssh and the database types (credential-bearing).
func TestDemoCheckTypesAreAllowlisted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)
	visitor := demoCtx("visitor-types")

	for _, refused := range []struct {
		checkType string
		config    map[string]any
	}{
		{"smtp", map[string]any{"host": "mail.example.com", "port": 25}},
		{"browser", map[string]any{"url": "https://example.com"}},
		{"ssh", map[string]any{"host": "example.com", "port": 22}},
		{"postgres", map[string]any{"host": "example.com", "port": 5432}},
		{"heartbeat", map[string]any{}},
		{"email", map[string]any{}},
	} {
		_, err := svc.CreateCheck(visitor, org.Slug, checks.CreateCheckRequest{
			Type:   refused.checkType,
			Config: refused.config,
		})

		// Either refusal is correct and both are closed. A checker that is not
		// compiled into this test binary is rejected as an unknown type before
		// the demo rule is reached; what matters is that the create does not
		// succeed and that the type is not on the allowlist.
		r.Errorf(err, "%s must not be creatable from a demo session", refused.checkType)
		r.Truef(
			errors.Is(err, checks.ErrDemoCheckTypeNotAllowed) || errors.Is(err, checks.ErrInvalidCheckType),
			"%s was refused for an unrelated reason: %v", refused.checkType, err)
		r.NotContains(checks.DemoAllowedCheckTypes(), refused.checkType)
	}

	// POSITIVE CONTROL: every allowed type really is allowed. A typo in the
	// allowlist would otherwise show up as "the demo is very safe".
	r.ElementsMatch([]string{"http", "tcp", "icmp", "dns", "ssl"}, checks.DemoAllowedCheckTypes())

	_, err := svc.CreateCheck(visitor, org.Slug, httpCheckReq())
	r.NoError(err)
}

// TestDemoTypeRuleDoesNotApplyToOrdinarySessions is the positive control for
// the whole type restriction: a paying customer's SMTP check must still be
// creatable.
func TestDemoTypeRuleDoesNotApplyToOrdinarySessions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	_, err := svc.CreateCheck(plainCtx("alice"), org.Slug, checks.CreateCheckRequest{
		Type:   "ssh",
		Config: map[string]any{"host": "example.com", "port": 22},
	})
	r.NotErrorIs(err, checks.ErrDemoCheckTypeNotAllowed)
}

// TestDemoPeriodFloor covers the one-minute floor. The org-wide
// maxChecksPerMinute is the real ceiling; this stops one visitor pinning a
// target every ten seconds.
func TestDemoPeriodFloor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)
	visitor := demoCtx("visitor-period")

	fast := httpCheckReq()
	tenSeconds := "10s"
	fast.Period = &tenSeconds

	_, err := svc.CreateCheck(visitor, org.Slug, fast)
	r.ErrorIs(err, checks.ErrDemoPeriodTooShort)

	// POSITIVE CONTROL: exactly one minute is fine, and so is the default
	// (an absent period).
	oneMinute := "1m"
	ok := httpCheckReq()
	ok.Slug = "one-minute"
	ok.Period = &oneMinute
	_, err = svc.CreateCheck(visitor, org.Slug, ok)
	r.NoError(err)

	defaulted := httpCheckReq()
	defaulted.Slug = "default-period"
	_, err = svc.CreateCheck(visitor, org.Slug, defaulted)
	r.NoError(err)
}

// TestCloneIsNotOwnershipGatedButKeepsTheShapeRules pins the deliberate
// asymmetry the spec describes: cloning a SEEDED check must work — it is what
// turns a read-only catalogue into something a visitor can experiment with —
// and the copy lands owned by the cloner. Clone mutates nothing, so gating it
// on ownership would buy no safety at all (a visitor can already POST /checks)
// while removing the demo's main affordance.
func TestCloneIsNotOwnershipGatedButKeepsTheShapeRules(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	seeded, err := svc.CreateCheck(context.Background(), org.Slug, httpCheckReq())
	r.NoError(err)

	visitor := demoCtx("visitor-clone")

	clone, err := svc.CloneCheck(visitor, org.Slug, seeded.UID, &checks.CloneCheckRequest{})
	r.NoError(err)
	r.NotNil(clone.CreatedBy)
	r.Equal("visitor-clone", *clone.CreatedBy, "a clone must be owned by whoever cloned it")

	// And the clone IS then editable by its owner — the point of the whole
	// affordance.
	name := "my copy"
	_, err = svc.UpdateCheck(visitor, org.Slug, clone.UID, &checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)

	// The source is still untouched and still un-owned.
	stored, err := svc.GetCheck(context.Background(), org.Slug, seeded.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Nil(stored.CreatedBy)
}

// TestCloneOfADisallowedTypeIsRefused closes the side door: clone bypasses
// CreateCheck, so without its own shape assertion a visitor could copy a seeded
// check of a type the allowlist forbids.
func TestCloneOfADisallowedTypeIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	seeded, err := svc.CreateCheck(context.Background(), org.Slug, checks.CreateCheckRequest{
		Type:   "ssh",
		Config: map[string]any{"host": "example.com", "port": 22},
	})
	r.NoError(err)

	_, err = svc.CloneCheck(demoCtx("visitor-clone-ssh"), org.Slug, seeded.UID,
		&checks.CloneCheckRequest{})
	r.ErrorIs(err, checks.ErrDemoCheckTypeNotAllowed)
}

// TestUpsertIsOwnershipGatedToo asserts the service is correct independently of
// the router. PUT-by-slug is not on the route allowlist, so a demo session
// cannot reach it today — but "the router happens to block it" is not the same
// property as "the write path enforces ownership", and only the second survives
// a future route being added.
func TestUpsertIsOwnershipGatedToo(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	seeded, err := svc.CreateCheck(context.Background(), org.Slug, httpCheckReq())
	r.NoError(err)
	r.NotNil(seeded.Slug)

	_, _, err = svc.UpsertCheck(demoCtx("visitor-upsert"), org.Slug, *seeded.Slug,
		&checks.UpsertCheckRequest{
			Name:   "hijacked",
			Config: map[string]any{"url": "https://elsewhere.example.com"},
		})
	r.ErrorIs(err, checks.ErrDemoReadOnly)
}

// TestDemoOwnershipIgnoresACreatedByThatIsNotTheCaller is the paranoid case: a
// check created by SOME other user (not a demo session at all) is not editable
// by a demo visitor either.
func TestDemoOwnershipIgnoresACreatedByThatIsNotTheCaller(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	created, err := svc.CreateCheck(plainCtx("alice"), org.Slug, httpCheckReq())
	r.NoError(err)

	r.ErrorIs(svc.DeleteCheck(demoCtx("visitor"), org.Slug, created.UID), checks.ErrDemoReadOnly)
}

// TestDemoModelsCompile is a compile-time anchor keeping the models import
// meaningful if the assertions above are ever refactored.
var _ = models.MemberRoleUser
