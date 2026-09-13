package testsupport_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// errBoom stands in for whatever postgres.New returned.
var errBoom = errors.New("listen tcp :15530: bind: address already in use")

// recorderTB records what a helper did to the test instead of ending it, so the
// FAILURE branch can be observed. *testing.T cannot be used for that: its
// Fatalf really does end the calling test.
type recorderTB struct {
	helperCalls int
	skipped     string
	failed      string
}

func (r *recorderTB) Helper() { r.helperCalls++ }

func (r *recorderTB) Skipf(format string, args ...any) {
	r.skipped = fmt.Sprintf(format, args...)
}

func (r *recorderTB) Fatalf(format string, args ...any) {
	r.failed = fmt.Sprintf(format, args...)
}

func TestRequirePostgresReadsTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "FALSE", want: false},
		{value: " no ", want: false},
		{value: "off", want: false},
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "yes", want: true},
	} {
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv(testsupport.EnvRequirePostgres, tc.value)
			require.Equal(t, tc.want, testsupport.RequirePostgres())
		})
	}
}

// TestPostgresUnavailableSkipsWhenNotRequired is the developer-laptop default:
// no environment variable, so a missing Postgres stays a skip.
func TestPostgresUnavailableSkipsWhenNotRequired(t *testing.T) {
	t.Setenv(testsupport.EnvRequirePostgres, "")

	rec := &recorderTB{}
	testsupport.PostgresUnavailable(rec, errBoom)

	r := require.New(t)
	r.Empty(rec.failed, "an unset SP_TEST_REQUIRE_POSTGRES must not fail the test")
	r.Contains(rec.skipped, "embedded postgres unavailable")
	r.Contains(rec.skipped, errBoom.Error())
	r.Positive(rec.helperCalls, "the helper must mark itself as a helper")
}

// TestPostgresUnavailableFailsWhenRequired is the whole point of the package:
// with SP_TEST_REQUIRE_POSTGRES=1 an unavailable Postgres FAILS. Without this
// being observed at least once, the CI job built on it is decoration.
func TestPostgresUnavailableFailsWhenRequired(t *testing.T) {
	t.Setenv(testsupport.EnvRequirePostgres, "1")

	rec := &recorderTB{}
	testsupport.PostgresUnavailable(rec, errBoom)

	r := require.New(t)
	r.Empty(rec.skipped, "SP_TEST_REQUIRE_POSTGRES=1 must never skip")
	r.Contains(rec.failed, "embedded postgres unavailable")
	r.Contains(rec.failed, errBoom.Error())
	r.Contains(rec.failed, testsupport.EnvRequirePostgres)
	r.Contains(strings.ToUpper(rec.failed), "FAILURE")
}

func TestPostgresInitFailedSkipsWhenNotRequired(t *testing.T) {
	t.Setenv(testsupport.EnvRequirePostgres, "off")

	rec := &recorderTB{}
	testsupport.PostgresInitFailed(rec, errBoom)

	r := require.New(t)
	r.Empty(rec.failed)
	r.Contains(rec.skipped, "embedded postgres init failed")
}

func TestPostgresInitFailedFailsWhenRequired(t *testing.T) {
	t.Setenv(testsupport.EnvRequirePostgres, "1")

	rec := &recorderTB{}
	testsupport.PostgresInitFailed(rec, errBoom)

	r := require.New(t)
	r.Empty(rec.skipped)
	r.Contains(rec.failed, "embedded postgres init failed")
	r.Contains(rec.failed, errBoom.Error())
}

// TestPostgresUnavailableTestMainTellsTheCallerToDie is the TestMain path: no
// *testing.T exists there, so the decision is "abort the binary or not".
func TestPostgresUnavailableTestMainTellsTheCallerToDie(t *testing.T) {
	t.Setenv(testsupport.EnvRequirePostgres, "1")

	out := &bytes.Buffer{}
	mustExit := testsupport.PostgresUnavailableTestMain(out, errBoom)

	r := require.New(t)
	r.True(mustExit, "SP_TEST_REQUIRE_POSTGRES=1 must abort the test binary, not run it empty")
	r.Contains(out.String(), "embedded postgres unavailable")
	r.Contains(out.String(), errBoom.Error())
	r.Contains(out.String(), "FAILURE")
}

func TestPostgresUnavailableTestMainKeepsSkippingWhenNotRequired(t *testing.T) {
	t.Setenv(testsupport.EnvRequirePostgres, "")

	out := &bytes.Buffer{}
	mustExit := testsupport.PostgresUnavailableTestMain(out, errBoom)

	r := require.New(t)
	r.False(mustExit, "an unset variable must leave the local skip behavior alone")
	r.Contains(out.String(), "embedded postgres unavailable")
	r.Contains(out.String(), testsupport.EnvRequirePostgres)
}

// TestRealTestingTSatisfiesTB is the compile-time link between the recorder
// tests above and reality: the interface the helpers take is one *testing.T
// actually implements, so what was proven with a recorder is what a real test
// gets.
func TestRealTestingTSatisfiesTB(t *testing.T) {
	t.Parallel()

	var tb testsupport.TB = t
	require.NotNil(t, tb)
}
