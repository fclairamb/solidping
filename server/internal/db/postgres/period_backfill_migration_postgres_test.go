package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portPeriodBackfill is distinct from every other _postgres_test.go embedded
// port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portPeriodBackfill = 15523

// periodBackfillSQL is the period-below-floor-backfill block of
// 021_v0_28_0.up.sql (spec 2026-09-11-07). Only this block is re-run here —
// Initialize() already applied the full migration once, harmlessly, against a
// fresh database with no checks yet. Keep in sync with
// migrations/021_v0_28_0.up.sql.
const periodBackfillSQL = `
update checks
   set period = case type
                  when 'ssl'     then interval '6 hours'
                  when 'domain'  then interval '24 hours'
                  when 'dnsbl'   then interval '1 hour'
                  when 'js'      then interval '1 minute'
                  when 'browser' then interval '5 minutes'
                end
 where type in ('ssl', 'domain', 'dnsbl', 'js', 'browser')
   and period = interval '1 minute'
   and period < case type
                  when 'ssl'     then interval '1 hour'
                  when 'domain'  then interval '6 hours'
                  when 'dnsbl'   then interval '15 minutes'
                  when 'js'      then interval '30 seconds'
                  when 'browser' then interval '1 minute'
                end;
`

// TestMigration021BackfillsPeriodBelowFloor_Postgres is the Postgres twin of
// the SQLite backfill test. The two dialects express the predicate over
// completely different storage (native interval arithmetic here, zero-padded
// HH:MM:SS text comparison there), so each needs its own proof.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestMigration021BackfillsPeriodBelowFloor_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portPeriodBackfill, RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	const orgUID = "00000000-0000-0000-0000-0000000000f0"

	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acme-period', 'Acme Period')`, orgUID)
	r.NoError(err)

	seeds := []struct {
		uid, slug, checkType, period string
	}{
		// The fingerprint: at the flat 1m default AND below the type's own
		// floor. Must be raised to the type's own DefaultPeriod.
		{"00000000-0000-0000-0000-000000000001", "ssl-flat", "ssl", "00:01:00"},
		{"00000000-0000-0000-0000-000000000002", "domain-flat", "domain", "00:01:00"},
		{"00000000-0000-0000-0000-000000000003", "dnsbl-flat", "dnsbl", "00:01:00"},
		// Below its own floor, but NOT the 1m fingerprint — grandfathered.
		{"00000000-0000-0000-0000-000000000004", "dnsbl-human", "dnsbl", "00:05:00"},
		// At 1m, but 1m is not below THEIR OWN floor (js: 30s, browser: 1m).
		{"00000000-0000-0000-0000-000000000005", "js-flat", "js", "00:01:00"},
		{"00000000-0000-0000-0000-000000000006", "browser-flat", "browser", "00:01:00"},
		// A type with no floor at all, at 1m.
		{"00000000-0000-0000-0000-000000000007", "http-flat", "http", "00:01:00"},
	}

	for _, seed := range seeds {
		_, err = svc.DB().ExecContext(ctx,
			`insert into checks (uid, organization_uid, slug, type, config, period)
			 values (?, ?, ?, ?, '{}', ?::interval)`,
			seed.uid, orgUID, seed.slug, seed.checkType, seed.period)
		r.NoError(err)
	}

	_, err = svc.DB().ExecContext(ctx, periodBackfillSQL)
	r.NoError(err)

	periodSeconds := func(slug string) float64 {
		var seconds float64
		r.NoError(svc.DB().QueryRowContext(ctx,
			`select extract(epoch from period) from checks where slug = ?`, slug).Scan(&seconds))

		return seconds
	}

	const delta = 0.001

	r.InDelta((6 * time.Hour).Seconds(), periodSeconds("ssl-flat"), delta,
		"ssl at the 1m fingerprint must be raised to its 6h default")
	r.InDelta((24 * time.Hour).Seconds(), periodSeconds("domain-flat"), delta,
		"domain at the 1m fingerprint must be raised to its 24h default")
	r.InDelta(time.Hour.Seconds(), periodSeconds("dnsbl-flat"), delta,
		"dnsbl at the 1m fingerprint must be raised to its 1h default")

	r.InDelta((5 * time.Minute).Seconds(), periodSeconds("dnsbl-human"), delta,
		"a below-floor period NOT at the 1m fingerprint is grandfathered")
	r.InDelta(time.Minute.Seconds(), periodSeconds("js-flat"), delta,
		"js at 1m is not below its own 30s floor")
	r.InDelta(time.Minute.Seconds(), periodSeconds("browser-flat"), delta,
		"browser at 1m is not below its own 1m floor")
	r.InDelta(time.Minute.Seconds(), periodSeconds("http-flat"), delta,
		"http has no floor at all, so it is never touched")
}
