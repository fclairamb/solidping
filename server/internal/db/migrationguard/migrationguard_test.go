package migrationguard_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
)

func sum(body string) string {
	h := sha256.Sum256([]byte(body))

	return hex.EncodeToString(h[:])
}

// TestChecksumsKeysOnTheNumericPrefix pins the guard's identity model to bun's:
// bun keys an applied migration on the numeric prefix alone, so the guard must
// too — otherwise a renamed-but-not-renumbered file would look like drift, and
// a renumbered one would look like a new migration.
func TestChecksumsKeysOnTheNumericPrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fsys := fstest.MapFS{
		"migrations/001_v0_1_0.up.sql":   {Data: []byte("create table a();")},
		"migrations/001_v0_1_0.down.sql": {Data: []byte("drop table a;")},
		"migrations/013_v0_16_0.up.sql":  {Data: []byte("alter table b add column c;")},
	}

	got, err := migrationguard.Checksums(fsys, "migrations")
	r.NoError(err)
	r.Len(got, 2, "one entry per up migration, keyed by prefix")

	r.Equal("001", got["001"].Name)
	r.Equal("v0_1_0", got["001"].Comment)
	r.Equal(sum("create table a();"), got["001"].Checksum)

	r.Equal("v0_16_0", got["013"].Comment)
	r.Equal(sum("alter table b add column c;"), got["013"].Checksum)
}

// TestChecksumsIgnoreDownMigrations proves a `.down.sql` edit cannot trip the
// guard. A down file never runs during a forward boot, so it cannot desync an
// applied schema, and folding it into the identity would make harmless teardown
// edits fail every deployment.
func TestChecksumsIgnoreDownMigrations(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	before, err := migrationguard.Checksums(fstest.MapFS{
		"migrations/007_v0_6_0.up.sql":   {Data: []byte("select 1;")},
		"migrations/007_v0_6_0.down.sql": {Data: []byte("select 2;")},
	}, "migrations")
	r.NoError(err)

	after, err := migrationguard.Checksums(fstest.MapFS{
		"migrations/007_v0_6_0.up.sql":   {Data: []byte("select 1;")},
		"migrations/007_v0_6_0.down.sql": {Data: []byte("-- rewritten entirely\nselect 99;")},
	}, "migrations")
	r.NoError(err)

	r.Equal(before, after)
}

// TestChecksumsChangeWithContent is the sensitivity control: without this, a
// Checksums that returned a constant would satisfy every other test here.
func TestChecksumsChangeWithContent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	base, err := migrationguard.Checksums(fstest.MapFS{
		"migrations/013_v0_16_0.up.sql": {Data: []byte("alter table workers add column egress_ipv4 boolean;")},
	}, "migrations")
	r.NoError(err)

	rewritten, err := migrationguard.Checksums(fstest.MapFS{
		"migrations/013_v0_16_0.up.sql": {Data: []byte("alter table workers add column capabilities text;")},
	}, "migrations")
	r.NoError(err)

	r.NotEqual(base["013"].Checksum, rewritten["013"].Checksum,
		"rewriting a migration in place must change its checksum — that is the whole guard")
}

// TestMismatchErrorIsActionable pins the operator-facing contract: the message
// names the migration and both repair routes, and the sentinel is matchable.
func TestMismatchErrorIsActionable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	err := &migrationguard.MismatchError{
		Name: "013", Comment: "v0_16_0",
		Recorded: "aaaaaaaaaaaaaaaa", Current: "bbbbbbbbbbbbbbbb",
	}

	r.ErrorIs(err, migrationguard.ErrChecksumMismatch)
	r.Contains(err.Error(), "013_v0_16_0")
	r.Contains(err.Error(), "reset the database")
	r.Contains(err.Error(), migrationguard.ChecksumTable)
	r.Contains(err.Error(), "bbbbbbbbbbbb", "the current checksum must be quotable into the repair SQL")
}
