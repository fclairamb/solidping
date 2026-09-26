package authhandoff

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portAuthHandoffPG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// internal/db/incident_number_test.go).
const portAuthHandoffPG = 15548

// handoffCase is one behavior, run against both engines: single use and the
// hash-only storage both hinge on SQL (DELETE ... RETURNING, the text key),
// which each dialect implements on its own.
type handoffCase struct {
	name string
	run  func(t *testing.T, dbService db.Service)
}

func handoffCases() []handoffCase {
	return []handoffCase{
		{"round trip then single use", testRoundTripSingleUse},
		{"expired code", testExpiredCode},
		{"unknown and malformed codes", testUnknownCode},
		{"tampered or foreign payload", testForgedPayload},
		{"row holds the hash, never the code or a token", testStoredRowIsOpaque},
		{"org-less session", testOrgLessSession},
		{"concurrent redeem has exactly one winner", testConcurrentRedeem},
		{"sweep removes only expired codes", testSweep},
	}
}

func TestHandoff_SQLite(t *testing.T) {
	t.Parallel()

	for _, tc := range handoffCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, newSQLite(t))
		})
	}
}

//nolint:paralleltest,tparallel // one embedded PG instance shared by every sub-test
func TestHandoff_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	dbService := newPostgres(t)

	for _, tc := range handoffCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, dbService)
		})
	}
}

func newSQLite(t *testing.T) db.Service {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	return dbService
}

func newPostgres(t *testing.T) db.Service {
	t.Helper()

	ctx := t.Context()

	dbService, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portAuthHandoffPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbService.Close() })

	if initErr := dbService.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	return dbService
}

// seedSession creates a user and an org and returns an org-scoped session for
// them. Names are unique per call so cases can share one Postgres database.
func seedSession(t *testing.T, dbService db.Service) *Session {
	t.Helper()

	ctx := t.Context()
	suffix := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	suffix = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}

		return -1
	}, suffix)

	if len(suffix) > 12 {
		suffix = suffix[len(suffix)-12:]
	}

	slug := "ho" + suffix + randomSuffix(t)

	org := models.NewOrganization(slug, "Handoff "+slug)
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	user := models.NewUser(slug + "@acme.com")
	require.NoError(t, dbService.CreateUser(ctx, user))

	return &Session{
		AccessToken:  "access-" + slug,
		RefreshToken: "refresh-" + slug,
		ExpiresIn:    3600,
		UserUID:      user.UID,
		OrgSlug:      org.Slug,
		ReturnTo:     "/d/orgs/" + org.Slug + "/checks",
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()

	code, err := newCode()
	require.NoError(t, err)

	return strings.ToLower(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}

		return -1
	}, code))[:4]
}

func testRoundTripSingleUse(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	session := seedSession(t, dbService)

	code, err := Issue(ctx, dbService, session)
	r.NoError(err)
	r.Len(code, 43, "32 bytes, base64url without padding")

	got, err := Redeem(ctx, dbService, code)
	r.NoError(err)
	r.Equal(session, got)

	_, err = Redeem(ctx, dbService, code)
	r.ErrorIs(err, ErrInvalidCode, "a code is single use")
}

func testExpiredCode(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	session := seedSession(t, dbService)

	code := insertRow(ctx, t, dbService, session, time.Now().Add(-time.Second))

	_, err := Redeem(ctx, dbService, code)
	r.ErrorIs(err, ErrInvalidCode)

	// Consumed all the same: the row does not linger for a second try.
	_, err = dbService.ConsumeAuthHandoffCode(ctx, HashCode(code))
	r.ErrorIs(err, sql.ErrNoRows)
}

func testUnknownCode(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	unknown, err := newCode()
	r.NoError(err)

	for _, code := range []string{unknown, "", "not-a-code", strings.Repeat("a", maxCodeLen+1)} {
		_, redeemErr := Redeem(ctx, dbService, code)
		r.ErrorIs(redeemErr, ErrInvalidCode, "code %q", code)
	}
}

func testForgedPayload(t *testing.T, dbService db.Service) {
	t.Helper()

	ctx := t.Context()
	session := seedSession(t, dbService)
	intruder := seedSession(t, dbService)

	t.Run("tampered ciphertext", func(t *testing.T) {
		r := require.New(t)

		code := insertRowWith(ctx, t, dbService, session, time.Now().Add(TTL), func(payload string) string {
			// Flip one character in the middle of the ciphertext.
			mid := len(payload) / 2
			swapped := byte('A')
			if payload[mid] == 'A' {
				swapped = 'B'
			}

			return payload[:mid] + string(swapped) + payload[mid+1:]
		})

		_, err := Redeem(ctx, dbService, code)
		r.ErrorIs(err, ErrInvalidCode)
	})

	t.Run("payload sealed for another code", func(t *testing.T) {
		r := require.New(t)

		other, err := newCode()
		r.NoError(err)

		code := insertRowWith(ctx, t, dbService, session, time.Now().Add(TTL), func(string) string {
			sealed, sealErr := seal(other, HashCode(other), []byte(`{"accessToken":"x","userUid":"`+session.UserUID+`"}`))
			r.NoError(sealErr)

			return sealed
		})

		_, err = Redeem(ctx, dbService, code)
		r.ErrorIs(err, ErrInvalidCode)
	})

	t.Run("payload names another user than the row", func(t *testing.T) {
		r := require.New(t)

		code, err := newCode()
		r.NoError(err)

		// A well-formed payload for the intruder, stored on a row bound to
		// the victim: the binding check refuses it.
		plaintext := []byte(`{"accessToken":"x","userUid":"` + intruder.UserUID + `"}`)
		sealed, err := seal(code, HashCode(code), plaintext)
		r.NoError(err)

		r.NoError(dbService.CreateAuthHandoffCode(ctx, &models.AuthHandoffCode{
			CodeHash:  HashCode(code),
			UserUID:   session.UserUID,
			Payload:   sealed,
			ExpiresAt: time.Now().Add(TTL),
			CreatedAt: time.Now(),
		}))

		_, err = Redeem(ctx, dbService, code)
		r.ErrorIs(err, ErrInvalidCode)
	})
}

func testStoredRowIsOpaque(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	session := seedSession(t, dbService)

	code, err := Issue(ctx, dbService, session)
	r.NoError(err)

	// The plaintext code is not a key.
	_, err = dbService.ConsumeAuthHandoffCode(ctx, code)
	r.ErrorIs(err, sql.ErrNoRows)

	row, err := dbService.ConsumeAuthHandoffCode(ctx, HashCode(code))
	r.NoError(err)
	r.Equal(HashCode(code), row.CodeHash)
	r.Len(row.CodeHash, 64)
	r.Equal(session.UserUID, row.UserUID)
	r.NotNil(row.OrganizationUID, "an org-scoped session binds the row to its org")

	for _, secret := range []string{code, session.AccessToken, session.RefreshToken, session.ReturnTo} {
		r.NotContains(row.Payload, secret)
		r.NotContains(row.CodeHash, secret)
	}
}

func testOrgLessSession(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	session := seedSession(t, dbService)
	session.OrgSlug = ""
	session.RefreshToken = ""
	session.MembershipPending = "acme"

	code, err := Issue(ctx, dbService, session)
	r.NoError(err)

	got, err := Redeem(ctx, dbService, code)
	r.NoError(err)
	r.Equal(session, got)
}

func testConcurrentRedeem(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	session := seedSession(t, dbService)

	code, err := Issue(ctx, dbService, session)
	r.NoError(err)

	const racers = 8

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)

	for range racers {
		wg.Go(func() {
			if _, redeemErr := Redeem(ctx, dbService, code); redeemErr == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}

	wg.Wait()
	r.Equal(1, wins)
}

func testSweep(t *testing.T, dbService db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	session := seedSession(t, dbService)

	expired := insertRow(ctx, t, dbService, session, time.Now().Add(-time.Minute))
	live, err := Issue(ctx, dbService, session)
	r.NoError(err)

	deleted, err := dbService.DeleteExpiredAuthHandoffCodes(ctx, time.Now())
	r.NoError(err)
	r.GreaterOrEqual(deleted, int64(1))

	_, err = dbService.ConsumeAuthHandoffCode(ctx, HashCode(expired))
	r.ErrorIs(err, sql.ErrNoRows)

	_, err = Redeem(ctx, dbService, live)
	r.NoError(err)
}

// insertRow stores session under a fresh code with an explicit expiry.
func insertRow(
	ctx context.Context, t *testing.T, dbService db.Service, session *Session, expiresAt time.Time,
) string {
	t.Helper()

	return insertRowWith(ctx, t, dbService, session, expiresAt, func(payload string) string { return payload })
}

// insertRowWith is insertRow with a hook to corrupt the stored payload.
func insertRowWith(
	ctx context.Context, t *testing.T, dbService db.Service, session *Session,
	expiresAt time.Time, mutate func(string) string,
) string {
	t.Helper()

	r := require.New(t)

	code, err := newCode()
	r.NoError(err)

	plaintext := []byte(`{"accessToken":"` + session.AccessToken +
		`","expiresIn":3600,"userUid":"` + session.UserUID + `"}`)
	sealed, err := seal(code, HashCode(code), plaintext)
	r.NoError(err)

	r.NoError(dbService.CreateAuthHandoffCode(ctx, &models.AuthHandoffCode{
		CodeHash:  HashCode(code),
		UserUID:   session.UserUID,
		Payload:   mutate(sealed),
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}))

	return code
}
