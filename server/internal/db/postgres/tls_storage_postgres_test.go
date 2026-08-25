package postgres

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// portTLSStorage is distinct from every other _postgres_test.go embedded port
// in this package (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portTLSStorage = 15463

// TestTLSStorage_Postgres proves the certmagic-backing key-value store behaves
// identically on a REAL Postgres backend: byte-exact binary round-trips through
// bytea, collation-proof prefix listing and subtree delete, and the conditional
// lock upsert under genuine concurrency (the property SQLite's single-writer
// tests cannot prove). Self-skips under -short and on any embedded-startup
// error.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestTLSStorage_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portTLSStorage, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	// The embedded server runs with max_connections=10 (3 of them reserved for
	// superusers) and NewEmbedded does not apply pool limits, so bound the pool
	// here: the lock race below wants real concurrent writers, not 16 raw
	// connections and a "too many clients" failure that proves nothing.
	s.DB().SetMaxOpenConns(4)

	const key = "certificates/acme-v02/status.acme.com/status.acme.com.key"

	_, err = s.TLSStorageLoad(ctx, key)
	r.ErrorIs(err, sql.ErrNoRows)

	// A PEM-ish payload with embedded NUL and high bytes: bytea must not mangle
	// it (a text column would).
	payload := []byte{0x00, 0x01, 0xFF, 'k', 'e', 'y', 0x00}
	r.NoError(s.TLSStorageStore(ctx, key, payload))

	loaded, err := s.TLSStorageLoad(ctx, key)
	r.NoError(err)
	r.Equal(payload, loaded)

	info, err := s.TLSStorageStat(ctx, key)
	r.NoError(err)
	r.Equal(int64(len(payload)), info.Size)
	r.WithinDuration(time.Now(), info.ModifiedAt, time.Minute)

	exists, err := s.TLSStorageExists(ctx, key)
	r.NoError(err)
	r.True(exists)

	testTLSStoragePrefixPostgres(ctx, t, s)
	testTLSStorageLocksPostgres(ctx, t, s)
	testTLSStoragePrefixCollationPostgres(ctx, t, s)
}

// tlsStoragePrefixFixture returns the keys a prefix check seeds: the subtree of
// "certificates" (the bare key plus everything nested under it) and the
// neighbors that must stay OUT of it. The neighbors straddle the '/' (0x2F)
// boundary on both sides — '-' is 0x2D, '0' is 0x30, 'X' is 0x58 — and include
// a shorter "certificate/…" key, so the assertions pin both edges of the match
// instead of only proving that something came back.
//
// Keep the two lists in named variables rather than returning both composite
// literals inline from a single `return`. gofmt (Go 1.27) and gofumpt disagree
// about how to indent that construct — one de-indents the elements, the other
// re-indents them — and golangci-lint enables both, so no formatting of it can
// satisfy the linter. Named locals sidestep the disagreement entirely.
func tlsStoragePrefixFixture() ([]string, []string) {
	subtree := []string{
		"certificates",
		"certificates/acme-v02/other.acme.com/other.acme.com.crt",
		"certificates/acme-v02/status.acme.com/status.acme.com.crt",
		"certificates/acme-v02/status.acme.com/status.acme.com.key",
	}

	neighbors := []string{
		"certificate/acme-v02/status.acme.com/status.acme.com.crt",
		"certificates-backup/acme-v02/status.acme.com/status.acme.com.crt",
		"certificates0/acme-v02/status.acme.com/status.acme.com.crt",
		"certificatesX/acme-v02/status.acme.com/status.acme.com.crt",
	}

	return subtree, neighbors
}

// tlsStorageSeedPrefixFixture empties the table and re-seeds the fixture, so a
// prefix check asserts on an exact key set rather than on counts.
func tlsStorageSeedPrefixFixture(ctx context.Context, t *testing.T, s *Service) {
	t.Helper()
	r := require.New(t)

	_, err := s.db.NewRaw("DELETE FROM tls_storage").Exec(ctx)
	r.NoError(err)

	subtree, neighbors := tlsStoragePrefixFixture()
	for _, key := range append(append([]string{}, subtree...), neighbors...) {
		r.NoError(s.TLSStorageStore(ctx, key, []byte(key)))
	}
}

// tlsStorageListKeys is List reduced to the keys it reports.
func tlsStorageListKeys(ctx context.Context, t *testing.T, s *Service, prefix string) []string {
	t.Helper()

	infos, err := s.TLSStorageList(ctx, prefix)
	require.NoError(t, err)

	keys := make([]string, len(infos))
	for i := range infos {
		keys[i] = infos[i].Key
	}

	return keys
}

// testTLSStoragePrefixPostgres asserts the prefix contract of List/Delete on
// real Postgres: the bare key and its whole subtree are in, every neighbor
// straddling the '/' boundary is out, and a prefix delete takes the subtree
// with it and nothing else.
func testTLSStoragePrefixPostgres(ctx context.Context, t *testing.T, s *Service) {
	t.Helper()
	r := require.New(t)

	tlsStorageSeedPrefixFixture(ctx, t, s)

	subtree, neighbors := tlsStoragePrefixFixture()

	r.ElementsMatch(subtree, tlsStorageListKeys(ctx, t, s, "certificates"),
		"a prefix listing must return the bare key plus its subtree, and no boundary neighbor")

	r.ElementsMatch([]string{
		"certificates/acme-v02/status.acme.com/status.acme.com.crt",
		"certificates/acme-v02/status.acme.com/status.acme.com.key",
	}, tlsStorageListKeys(ctx, t, s, "certificates/acme-v02/status.acme.com"))

	r.NoError(s.TLSStorageDelete(ctx, "certificates/acme-v02/status.acme.com"))

	r.ElementsMatch([]string{
		"certificates",
		"certificates/acme-v02/other.acme.com/other.acme.com.crt",
	}, tlsStorageListKeys(ctx, t, s, "certificates"),
		"a prefix delete must remove the whole subtree it names")

	for _, neighbor := range neighbors {
		alive, err := s.TLSStorageExists(ctx, neighbor)
		r.NoError(err)
		r.True(alive, "a prefix delete must not reach the boundary neighbor %q", neighbor)
	}
}

// testTLSStoragePrefixCollationPostgres re-runs the prefix contract with the
// key column forced to a collation that primary-ignores punctuation.
//
// THIS IS THE REGRESSION GUARD FOR THE COLLATION BUG. The prefix queries used
// to be a half-open range (key >= 'certificates/' AND key < 'certificates0').
// Postgres' < / >= on text honor the collation, and glibc's en_US.utf8 — the
// default of the official postgres image and of most distro installs — ignores
// '/' at the primary level, so 'certificates0' sorts BELOW
// 'certificates/acme-v02/…' and the range matched ZERO rows: List returned
// nothing (the custom-domain certificate chip stayed "none" forever) and prefix
// Delete left the subtree, private keys included, behind. Nothing caught it
// because the embedded server here uses macOS' libc collation, which is
// byte-ordered, so the range looked correct locally. ICU's "shifted" variable
// weighting reproduces glibc's behavior exactly and is available on any
// ICU-enabled build, so the check no longer depends on the machine's locale.
func testTLSStoragePrefixCollationPostgres(ctx context.Context, t *testing.T, s *Service) {
	t.Helper()
	r := require.New(t)

	collation := tlsStorageHazardCollation(ctx, t, s)
	if collation == "" {
		t.Log("no punctuation-ignoring collation available: skipping the collation regression guard")

		return
	}

	const alter = "ALTER TABLE tls_storage ALTER COLUMN key TYPE text COLLATE ?"

	_, err := s.db.NewRaw(alter, bun.Ident(collation)).Exec(ctx)
	r.NoError(err)

	testTLSStoragePrefixPostgres(ctx, t, s)

	_, err = s.db.NewRaw(alter, bun.Ident("default")).Exec(ctx)
	r.NoError(err)
}

// tlsStorageHazardCollation returns the name of a collation under which the
// abandoned key-range form is provably wrong — one where 'certificates/a'
// sorts ABOVE the range's exclusive upper bound 'certificates0'. It creates an
// ICU collation with shifted variable weighting (glibc's en_US.utf8 behavior),
// falling back to any installed collation that shows the same ordering, and
// returns "" when the server can offer neither.
func tlsStorageHazardCollation(ctx context.Context, t *testing.T, s *Service) string {
	t.Helper()

	const icuName = "tls_storage_punct_ignoring"

	_, err := s.db.NewRaw(
		"CREATE COLLATION IF NOT EXISTS " + icuName +
			" (provider = icu, locale = 'en-u-ka-shifted', deterministic = true)").Exec(ctx)
	if err != nil {
		t.Logf("ICU collation unavailable (%v), falling back to the installed collations", err)
	}

	var installed []string
	if scanErr := s.db.NewRaw(`SELECT collname FROM pg_collation
WHERE collname NOT IN ('C', 'POSIX', 'ucs_basic')
  AND collencoding IN (-1, pg_char_to_encoding(current_setting('server_encoding')))
ORDER BY collname LIMIT 200`).Scan(ctx, &installed); scanErr != nil {
		t.Logf("could not list collations: %v", scanErr)
	}

	candidates := make([]string, 0, 1+len(installed))
	candidates = append(candidates, icuName)
	candidates = append(candidates, installed...)

	for _, name := range candidates {
		var hazard bool

		query := `SELECT ('certificates/a' COLLATE ?) > 'certificates0'`
		if scanErr := s.db.NewRaw(query, bun.Ident(name)).Scan(ctx, &hazard); scanErr != nil {
			continue
		}

		if hazard {
			return name
		}
	}

	return ""
}

// testTLSStorageLocksPostgres exercises the Locker contract on real Postgres:
// exactly one winner under contention, owner-scoped refresh/release, and
// reclaim of an expired lease.
func testTLSStorageLocksPostgres(ctx context.Context, t *testing.T, s *Service) {
	t.Helper()
	r := require.New(t)

	const (
		key     = "issue_cert_status.acme.com"
		racers  = 16
		leaseIn = time.Minute
	)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		errs    []error
	)

	wg.Add(racers)

	for i := range racers {
		go func(idx int) {
			defer wg.Done()

			ok, err := s.TLSStorageAcquireLock(ctx, key, "owner-"+string(rune('a'+idx)), time.Now().Add(leaseIn))

			mu.Lock()
			defer mu.Unlock()

			// Assertions belong on the test goroutine (testifylint go-require),
			// so collect and check after the wait.
			if err != nil {
				errs = append(errs, err)

				return
			}

			if ok {
				winners++
			}
		}(i)
	}

	wg.Wait()
	r.Empty(errs, "concurrent lock attempts must not error")
	r.Equal(1, winners, "the conditional upsert must admit exactly one holder")

	// An expired lease is reclaimable; a live one is not.
	r.NoError(s.TLSStorageReleaseLock(ctx, key, "owner-a"))

	taken, err := s.TLSStorageAcquireLock(ctx, key, "owner-stale", time.Now().Add(-time.Second))
	r.NoError(err)

	if taken {
		reclaimed, reclaimErr := s.TLSStorageAcquireLock(ctx, key, "owner-fresh", time.Now().Add(leaseIn))
		r.NoError(reclaimErr)
		r.True(reclaimed, "an expired lease must be reclaimable")

		blocked, blockErr := s.TLSStorageAcquireLock(ctx, key, "owner-other", time.Now().Add(leaseIn))
		r.NoError(blockErr)
		r.False(blocked, "a live lease must block other owners")

		stillMine, refreshErr := s.TLSStorageRefreshLock(ctx, key, "owner-fresh", time.Now().Add(2*leaseIn))
		r.NoError(refreshErr)
		r.True(stillMine)

		notMine, refreshErr := s.TLSStorageRefreshLock(ctx, key, "owner-stale", time.Now().Add(2*leaseIn))
		r.NoError(refreshErr)
		r.False(notMine)
	}
}
