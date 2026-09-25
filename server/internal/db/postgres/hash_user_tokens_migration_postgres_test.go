package postgres

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portHashUserTokensMigration is distinct from every other _postgres_test.go
// embedded port in the repo.
const portHashUserTokensMigration = 15560

// hashUserTokensSection returns the statements of the hash-user-tokens section
// of a 024 file, read from the file itself so the test can never drift from
// what ships. banner is the section's banner line prefix.
func hashUserTokensSection(t *testing.T, file, banner string) []string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("migrations", file))
	require.NoError(t, err)

	idx := strings.Index(string(content), banner)
	require.GreaterOrEqual(t, idx, 0, "the section banner must be present in %s", file)

	section := string(content)[idx:]
	if next := strings.Index(section[len(banner):], "\n-- SECTION: "); next >= 0 {
		section = section[:len(banner)+next]
	}

	var statements []string

	for _, chunk := range strings.Split(section, "--bun:split") {
		var kept []string

		for _, line := range strings.Split(chunk, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				kept = append(kept, line)
			}
		}

		if statement := strings.TrimSpace(strings.Join(kept, "\n")); statement != "" {
			statements = append(statements, statement)
		}
	}

	return statements
}

// TestMigration024HashesUserTokens_Postgres is the Postgres twin of the SQLite
// migration test (spec 2026-09-25-23). Initialize() already ran the section
// and its Go half on an empty database, so the plaintext column is gone. The
// section's down half then brings the v0.32 shape back (and deletes every
// row, which a downgrade has to), plaintext rows are seeded, and the up half
// plus the Go step run again on them.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestMigration024HashesUserTokens_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portHashUserTokensMigration, RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	columns := func(column string) int {
		t.Helper()

		var count int
		r.NoError(svc.DB().QueryRowContext(ctx,
			`select count(*) from information_schema.columns
			  where table_schema = current_schema() and table_name = 'user_tokens' and column_name = ?`, column,
		).Scan(&count))

		return count
	}

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := svc.DB().ExecContext(ctx, query, args...)
		r.NoError(execErr, query)
	}

	r.Zero(columns("token"), "Initialize runs the Go half: the plaintext column is gone")
	r.Equal(1, columns("token_hash"))

	const (
		orgUID  = "00000000-0000-0000-0000-00000000f100"
		userUID = "00000000-0000-0000-0000-00000000f200"
	)

	exec(`insert into organizations (uid, slug, name) values (?, 'acme-hash-tokens', 'Acme')`, orgUID)
	exec(`insert into users (uid, email) values (?, 'alice@acme.com')`, userUID)
	exec(`insert into user_tokens (user_uid, organization_uid, token_hash, type) values (?, ?, 'x', 'pat')`,
		userUID, orgUID)

	// Down: every row goes, the v0.32 plaintext column comes back.
	for _, statement := range hashUserTokensSection(t, "024_v0_33_0.down.sql", "-- SECTION: hash-user-tokens\n") {
		exec(statement)
	}

	r.Equal(1, columns("token"))
	r.Zero(columns("token_hash"))

	var remaining int
	r.NoError(svc.DB().QueryRowContext(ctx, `select count(*) from user_tokens`).Scan(&remaining))
	r.Zero(remaining, "a downgrade cannot restore plaintext, so it deletes every row")

	type seeded struct {
		uid, value, kind string
		deleted          bool
	}

	seeds := []seeded{
		{"00000000-0000-0000-0000-00000000f301", "c2Vzc2lvbi1yZWZyZXNoLXRva2VuLXZhbHVlLTMyYg", "refresh", false},
		{"00000000-0000-0000-0000-00000000f302", "pat_0123456789abcdef0123456789abcdef01234567", "pat", false},
		{"00000000-0000-0000-0000-00000000f303", "b2F1dGgtcmVmcmVzaC1ncmFudC12YWx1ZS0zMmI", "oauth_refresh", false},
		{"00000000-0000-0000-0000-00000000f304", "cmV2b2tlZC1zZXNzaW9uLXRva2VuLXZhbHVlLTMyYg", "refresh", true},
	}

	for _, s := range seeds {
		var deletedAt any
		if s.deleted {
			deletedAt = "2026-09-20T10:00:00Z"
		}

		exec(`insert into user_tokens (uid, user_uid, organization_uid, token, type, deleted_at)
		      values (?, ?, ?, ?, ?, ?)`, s.uid, userUID, orgUID, s.value, s.kind, deletedAt)
	}

	for _, statement := range hashUserTokensSection(t, "024_v0_33_0.up.sql", "-- SECTION: hash-user-tokens  (spec") {
		exec(statement)
	}

	r.Equal(1, columns("token"), "the SQL half leaves the plaintext column to the Go half")

	hashed, err := db.HashPlaintextUserTokens(ctx, svc.DB())
	r.NoError(err)
	r.Equal(len(seeds), hashed)
	r.Zero(columns("token"))

	hexSHA256 := regexp.MustCompile(`^[0-9a-f]{64}$`)

	for _, s := range seeds {
		var stored string
		r.NoError(svc.DB().QueryRowContext(ctx,
			`select token_hash from user_tokens where uid = ?`, s.uid).Scan(&stored))

		sum := sha256.Sum256([]byte(s.value))
		r.Regexp(hexSHA256, stored)
		r.Equal(hex.EncodeToString(sum[:]), stored, "row %s holds sha256(token)", s.uid)

		row, lookupErr := svc.GetUserTokenByToken(ctx, s.value)
		if s.deleted {
			r.ErrorIs(lookupErr, sql.ErrNoRows, "a revoked token stays revoked")

			continue
		}

		r.NoError(lookupErr, "the original %s token still authenticates", s.kind)
		r.Equal(s.uid, row.UID)
	}

	again, err := db.HashPlaintextUserTokens(ctx, svc.DB())
	r.NoError(err)
	r.Zero(again, "a second run has nothing to do")
}
