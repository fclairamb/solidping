package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/cli/apihelper"
	"github.com/fclairamb/solidping/server/pkg/cli/config"
	"github.com/fclairamb/solidping/server/pkg/cli/output"
)

// TestFormatConvertSummaryClean verifies a clean convert's headline and exit
// code, in both preview and applied mode, mirroring
// TestFormatImportSummaryClean.
func TestFormatConvertSummaryClean(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := convertResultSummary{Converted: 5, Created: 5, Updated: 0, Unchanged: 0, Unmanaged: 0}

	preview := formatConvertSummary(&result, true)
	r.Equal("PREVIEW: converted=5 created=5 updated=0 unchanged=0 unmanaged=0", preview.headline)
	r.Empty(preview.warningLines)
	r.Empty(preview.errorLines)
	r.Equal("Re-run with --apply to write these checks.", preview.footer)
	r.Equal(0, preview.exitCode)

	applied := formatConvertSummary(&result, false)
	r.Equal("APPLIED: converted=5 created=5 updated=0 unchanged=0 unmanaged=0", applied.headline)
	r.Empty(applied.footer)
	r.Equal(0, applied.exitCode)
}

// TestFormatConvertSummaryWarningsAndErrors verifies warning-line rendering
// (item+field, item-only, document-level) and the per-item error lines /
// non-zero exit code, mirroring TestFormatImportSummaryWithErrors.
func TestFormatConvertSummaryWarningsAndErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := convertResultSummary{
		Converted: 2,
		Created:   1,
		Warnings: []convertWarning{
			{Item: "MQTT broker", Field: "mqttPassword", Message: "the MQTT password was deliberately not imported"},
			{Item: "Radius auth", Message: "monitor skipped: no SolidPing counterpart"},
			{Message: "notification bindings are not imported"},
		},
		Errors: []convertItemError{
			{Index: 1, Slug: "bad-check", Error: "invalid slug format"},
		},
	}

	summary := formatConvertSummary(&result, false)
	r.Equal("APPLIED: converted=2 created=1 updated=0 unchanged=0 unmanaged=0", summary.headline)
	r.Equal([]string{
		"[MQTT broker] mqttPassword: the MQTT password was deliberately not imported",
		"[Radius auth] monitor skipped: no SolidPing counterpart",
		"notification bindings are not imported",
	}, summary.warningLines)
	r.Equal([]string{"[1] bad-check: invalid slug format"}, summary.errorLines)
	r.Equal(1, summary.exitCode)
}

// newConvertTestContext builds a *Context pointed at srvURL, authenticated
// with a stored PAT (bypassing login entirely) and writing JSON output to a
// buffer this test can assert on directly — text mode writes straight to
// os.Stdout via the output package's package-level Print* helpers, which
// existing CLI tests don't capture either, so JSON mode is what's testable
// here.
func newConvertTestContext(t *testing.T, srvURL string) (*Context, *bytes.Buffer) {
	t.Helper()
	r := require.New(t)

	cfg := &config.Config{URL: srvURL, Org: "acme-org"}
	helper := apihelper.NewHelper(cfg, filepath.Join(t.TempDir(), "token.json"), false)
	r.NoError(helper.SavePAT("pat_test_token"))

	var buf bytes.Buffer

	return &Context{
		Config:       cfg,
		APIHelper:    helper,
		Outputter:    output.NewOutputter(output.FormatJSON, &buf),
		OutputFormat: output.FormatJSON,
	}, &buf
}

// TestChecksImportFromAction_UnsupportedSource verifies --from rejects
// anything other than uptime-kuma-db without ever calling the API.
func TestChecksImportFromAction_UnsupportedSource(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)

	cliCtx, _ := newConvertTestContext(t, srv.URL)

	err := checksImportFromAction(context.Background(), cliCtx, "bogus-source", "irrelevant.db", false)
	r.Error(err)
	r.Contains(err.Error(), "bogus-source")
	r.False(called, "the API must never be called for an unsupported --from source")
}

// TestChecksImportFromAction_NotSQLite verifies a non-SQLite file is refused
// locally (by kumadb) before any network call.
func TestChecksImportFromAction_NotSQLite(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)

	cliCtx, _ := newConvertTestContext(t, srv.URL)

	notSQLite := filepath.Join(t.TempDir(), "not-a-db.db")
	r.NoError(os.WriteFile(notSQLite, []byte("hello"), 0o600))

	err := checksImportFromAction(context.Background(), cliCtx, importSourceUptimeKumaDB, notSQLite, false)
	r.Error(err)
	r.False(called, "the API must never be called when the local file cannot be read")
}

// TestChecksImportFromAction_DryRunIsDefault verifies dry-run is the default
// (--apply not passed) and that the request lands on the convert endpoint
// with source=uptime-kuma and dryRun=true.
func TestChecksImportFromAction_DryRunIsDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path + "?" + req.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"uptime-kuma","converted":1,"dryRun":true,"created":1,"warnings":[]}`))
	}))
	t.Cleanup(srv.Close)

	cliCtx, buf := newConvertTestContext(t, srv.URL)

	err := checksImportFromAction(context.Background(), cliCtx, importSourceUptimeKumaDB, kumaFixturePath(t), false)
	r.NoError(err)
	r.Equal("/api/v1/orgs/acme-org/checks/import/convert?source=uptime-kuma&dryRun=true", gotPath)

	var decoded convertResultSummary
	r.NoError(json.Unmarshal(buf.Bytes(), &decoded))
	r.True(decoded.DryRun)
	r.Equal(1, decoded.Created)
}

// TestChecksImportFromAction_Apply verifies --apply flips dryRun to false on
// the request.
func TestChecksImportFromAction_Apply(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotQuery = req.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"uptime-kuma","converted":1,"dryRun":false,"created":1,"warnings":[]}`))
	}))
	t.Cleanup(srv.Close)

	cliCtx, buf := newConvertTestContext(t, srv.URL)

	err := checksImportFromAction(context.Background(), cliCtx, importSourceUptimeKumaDB, kumaFixturePath(t), true)
	r.NoError(err)
	r.Equal("source=uptime-kuma", gotQuery, "dryRun must be absent from the query once --apply is set")

	var decoded convertResultSummary
	r.NoError(json.Unmarshal(buf.Bytes(), &decoded))
	r.False(decoded.DryRun)
}

// kumaFixturePath returns the real Kuma SQLite fixture shared with kumadb's
// own tests and the importers end-to-end test.
func kumaFixturePath(t *testing.T) string {
	t.Helper()

	return filepath.Join(
		"..", "..", "internal", "handlers", "checks", "importers", "testdata", "uptime-kuma-sqlite", "kuma.db")
}
