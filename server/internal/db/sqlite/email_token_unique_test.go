package sqlite

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
)

const (
	sharedEmailToken = "0123456789abcdef0123456789abcdef0123456789abcdef"
	otherEmailToken  = "fedcba9876543210fedcba9876543210fedcba9876543210"
)

var mintedEmailTokenPattern = regexp.MustCompile(`^[0-9a-f]{48}$`)

type emailTokenRig struct {
	t   *testing.T
	svc *Service
}

func newEmailTokenRig(t *testing.T) *emailTokenRig {
	t.Helper()

	svc, err := New(t.Context(), Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	rig := &emailTokenRig{t: t, svc: svc}
	require.NoError(t, rig.exec(`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`))

	return rig
}

func (rig *emailTokenRig) exec(query string, args ...any) error {
	_, err := rig.svc.DB().ExecContext(rig.t.Context(), query, args...)

	return err
}

func (rig *emailTokenRig) insert(uid, token, createdAt string, deleted bool) error {
	deletedAt := any(nil)
	if deleted {
		deletedAt = "2026-11-01 00:00:00"
	}

	return rig.exec(`insert into checks (uid, organization_uid, slug, type, config, created_at, deleted_at)
		values (?, 'org-1', ?, 'email', json_object('token', ?), ?, ?)`, uid, uid, token, createdAt, deletedAt)
}

func (rig *emailTokenRig) token(uid string) string {
	var token string
	require.NoError(rig.t, rig.svc.DB().QueryRowContext(rig.t.Context(),
		`select json_extract(config, '$.token') from checks where uid = ?`, uid).Scan(&token))

	return token
}

// TestEmailTokenUniqueIndex: two live email checks cannot share a token, while
// a soft-deleted check's token blocks nothing (spec 2026-11-14-01).
func TestEmailTokenUniqueIndex(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newEmailTokenRig(t)

	r.NoError(rig.insert("chk-a", sharedEmailToken, "2026-11-01 10:00:00", false))
	r.Error(rig.insert("chk-b", sharedEmailToken, "2026-11-01 11:00:00", false),
		"a second live email check with the same token must be refused")

	r.NoError(rig.insert("chk-gone", otherEmailToken, "2026-11-01 10:00:00", true))
	r.NoError(rig.insert("chk-new", otherEmailToken, "2026-11-01 11:00:00", false),
		"a deleted check's token must not block a new one")

	found, err := rig.svc.GetCheckByEmailToken(t.Context(), otherEmailToken)
	r.NoError(err)
	r.Equal("chk-new", found.UID)
}

// TestGetCheckByEmailTokenRefusesDuplicates: with the index gone (corruption),
// two matches are an error, never the first row.
func TestGetCheckByEmailTokenRefusesDuplicates(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newEmailTokenRig(t)

	r.NoError(rig.exec(`drop index idx_checks_email_token_unique`))
	r.NoError(rig.insert("chk-a", sharedEmailToken, "2026-11-01 10:00:00", false))
	r.NoError(rig.insert("chk-b", sharedEmailToken, "2026-11-01 11:00:00", false))

	_, err := rig.svc.GetCheckByEmailToken(t.Context(), sharedEmailToken)
	r.ErrorIs(err, db.ErrDuplicateEmailToken)

	_, err = rig.svc.GetCheckByEmailToken(t.Context(), otherEmailToken)
	r.Error(err)
	r.NotErrorIs(err, db.ErrDuplicateEmailToken)
}

// TestEmailTokenUniqueMigration replays the email-token-unique section on a
// database holding duplicates: the oldest live check keeps its token, every
// other live duplicate gets a fresh one, and deleted or unique rows are
// untouched.
func TestEmailTokenUniqueMigration(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newEmailTokenRig(t)

	r.NoError(rig.exec(`drop index idx_checks_email_token_unique`))
	r.NoError(rig.insert("chk-newer", sharedEmailToken, "2026-11-01 11:00:00", false))
	r.NoError(rig.insert("chk-oldest", sharedEmailToken, "2026-11-01 10:00:00", false))
	r.NoError(rig.insert("chk-newest", sharedEmailToken, "2026-11-01 12:00:00", false))
	r.NoError(rig.insert("chk-deleted", sharedEmailToken, "2026-11-01 09:00:00", true))
	r.NoError(rig.insert("chk-other", otherEmailToken, "2026-11-01 08:00:00", false))

	r.NoError(rig.exec(migrationSection(t, "email-token-unique")))

	r.Equal(sharedEmailToken, rig.token("chk-oldest"))
	r.Equal(sharedEmailToken, rig.token("chk-deleted"))
	r.Equal(otherEmailToken, rig.token("chk-other"))

	newer, newest := rig.token("chk-newer"), rig.token("chk-newest")
	r.Regexp(mintedEmailTokenPattern, newer)
	r.Regexp(mintedEmailTokenPattern, newest)
	r.NotEqual(sharedEmailToken, newer)
	r.NotEqual(sharedEmailToken, newest)
	r.NotEqual(newer, newest)

	r.Error(rig.insert("chk-dup", sharedEmailToken, "2026-11-01 13:00:00", false),
		"the migration must leave the unique index in place")
}
