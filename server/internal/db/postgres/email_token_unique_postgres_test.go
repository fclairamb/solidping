package postgres

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

const (
	sharedEmailTokenPG = "0123456789abcdef0123456789abcdef0123456789abcdef"
	otherEmailTokenPG  = "fedcba9876543210fedcba9876543210fedcba9876543210"
	emailTokenOrgPG    = "11111111-1111-1111-1111-111111111111"
)

var mintedEmailTokenPatternPG = regexp.MustCompile(`^[0-9a-f]{48}$`)

type emailTokenRigPG struct {
	t   *testing.T
	svc *Service
}

func newEmailTokenRigPG(t *testing.T) *emailTokenRigPG {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := New(ctx, &Config{Embedded: true, Port: testsupport.FreePort(t), RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	rig := &emailTokenRigPG{t: t, svc: svc}
	require.NoError(t, rig.exec(`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`, emailTokenOrgPG))

	return rig
}

func (rig *emailTokenRigPG) exec(query string, args ...any) error {
	_, err := rig.svc.DB().ExecContext(rig.t.Context(), query, args...)

	return err
}

func (rig *emailTokenRigPG) insert(uid, token, createdAt string, deleted bool) error {
	deletedAt := any(nil)
	if deleted {
		deletedAt = "2026-11-01 00:00:00+00"
	}

	return rig.exec(`insert into checks (uid, organization_uid, slug, type, config, created_at, deleted_at)
		values (?, ?, ?, 'email', jsonb_build_object('token', ?::text), ?::timestamptz, ?::timestamptz)`,
		uid, emailTokenOrgPG, "chk-"+uid, token, createdAt, deletedAt)
}

func (rig *emailTokenRigPG) token(uid string) string {
	var token string
	require.NoError(rig.t, rig.svc.DB().QueryRowContext(rig.t.Context(),
		`select config->>'token' from checks where uid = ?`, uid).Scan(&token))

	return token
}

// TestEmailTokenUniqueIndex_Postgres is the Postgres twin of the SQLite test:
// two live email checks cannot share a token, a deleted one blocks nothing.
func TestEmailTokenUniqueIndex_Postgres(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newEmailTokenRigPG(t)

	r.NoError(rig.insert("a0000000-0000-0000-0000-000000000001", sharedEmailTokenPG, "2026-11-01 10:00:00+00", false))
	r.Error(rig.insert("a0000000-0000-0000-0000-000000000002", sharedEmailTokenPG, "2026-11-01 11:00:00+00", false),
		"a second live email check with the same token must be refused")

	r.NoError(rig.insert("b0000000-0000-0000-0000-000000000001", otherEmailTokenPG, "2026-11-01 10:00:00+00", true))
	r.NoError(rig.insert("b0000000-0000-0000-0000-000000000002", otherEmailTokenPG, "2026-11-01 11:00:00+00", false),
		"a deleted check's token must not block a new one")

	found, err := rig.svc.GetCheckByEmailToken(t.Context(), otherEmailTokenPG)
	r.NoError(err)
	r.Equal("b0000000-0000-0000-0000-000000000002", found.UID)
}

// TestGetCheckByEmailTokenRefusesDuplicates_Postgres: with the index gone
// (corruption), two matches are an error, never the first row.
func TestGetCheckByEmailTokenRefusesDuplicates_Postgres(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newEmailTokenRigPG(t)

	r.NoError(rig.exec(`drop index idx_checks_email_token_unique`))
	r.NoError(rig.insert("a0000000-0000-0000-0000-000000000001", sharedEmailTokenPG, "2026-11-01 10:00:00+00", false))
	r.NoError(rig.insert("a0000000-0000-0000-0000-000000000002", sharedEmailTokenPG, "2026-11-01 11:00:00+00", false))

	_, err := rig.svc.GetCheckByEmailToken(t.Context(), sharedEmailTokenPG)
	r.ErrorIs(err, db.ErrDuplicateEmailToken)
}

// TestEmailTokenUniqueMigration_Postgres replays the email-token-unique section
// on a database holding duplicates: the oldest live check keeps its token, the
// other live duplicates get fresh ones, deleted and unique rows are untouched.
func TestEmailTokenUniqueMigration_Postgres(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newEmailTokenRigPG(t)

	const (
		newer   = "c0000000-0000-0000-0000-000000000001"
		oldest  = "c0000000-0000-0000-0000-000000000002"
		newest  = "c0000000-0000-0000-0000-000000000003"
		deleted = "c0000000-0000-0000-0000-000000000004"
		other   = "c0000000-0000-0000-0000-000000000005"
	)

	r.NoError(rig.exec(`drop index idx_checks_email_token_unique`))
	r.NoError(rig.insert(newer, sharedEmailTokenPG, "2026-11-01 11:00:00+00", false))
	r.NoError(rig.insert(oldest, sharedEmailTokenPG, "2026-11-01 10:00:00+00", false))
	r.NoError(rig.insert(newest, sharedEmailTokenPG, "2026-11-01 12:00:00+00", false))
	r.NoError(rig.insert(deleted, sharedEmailTokenPG, "2026-11-01 09:00:00+00", true))
	r.NoError(rig.insert(other, otherEmailTokenPG, "2026-11-01 08:00:00+00", false))

	r.NoError(rig.exec(migrationSection(t, "email-token-unique")))

	r.Equal(sharedEmailTokenPG, rig.token(oldest))
	r.Equal(sharedEmailTokenPG, rig.token(deleted))
	r.Equal(otherEmailTokenPG, rig.token(other))

	newerToken, newestToken := rig.token(newer), rig.token(newest)
	r.Regexp(mintedEmailTokenPatternPG, newerToken)
	r.Regexp(mintedEmailTokenPatternPG, newestToken)
	r.NotEqual(sharedEmailTokenPG, newerToken)
	r.NotEqual(newerToken, newestToken)

	r.Error(rig.insert("c0000000-0000-0000-0000-000000000006", sharedEmailTokenPG, "2026-11-01 13:00:00+00", false),
		"the migration must leave the unique index in place")
}
