// Package embeddedpg owns the full lifecycle of embedded-PostgreSQL
// instances used by tests: it creates and owns the data directory, marks it
// with an owner PID so a startup sweep can tell live suites from orphans,
// starts postgres, and spawns a detached watchdog process that reaps
// postgres if this process dies without cleaning up (e.g. SIGKILL).
//
// Every test suite that boots an embedded PostgreSQL should go through
// Start/Stop here instead of creating its own data directory — a single
// unified directory-naming scheme and marker format is what makes the
// startup sweep (sweep.go) able to reliably distinguish live instances from
// orphans left behind by a killed process.
package embeddedpg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres/v2"
)

// unifiedPrefix is the directory-name prefix used by every new instance
// created through this package. The startup sweep also recognizes several
// legacy prefixes used by call sites before they were migrated to this
// package (see sweep.go).
const unifiedPrefix = "solidping-embedded-pg"

// ownerMarkerFile is the JSON marker written into the data directory before
// postgres starts, recording which process owns this instance.
const ownerMarkerFile = "owner.json"

// sweepOnce ensures the startup sweep (Part B) runs at most once per
// process, before the first instance is started.
//
//nolint:gochecknoglobals // intentional: sweep must run once per process, not once per Instance
var sweepOnce sync.Once

// Options configures a new embedded-PostgreSQL instance.
type Options struct {
	// Suite names the caller (e.g. "scenario", "notifier") — becomes part
	// of the data-directory name, purely for human debugging.
	Suite string

	// Port is the TCP port postgres listens on. Zero lets embedded-postgres
	// pick its own default.
	Port uint32

	// Database, Username, Password are the initial database/role created by
	// postgres on first start. Empty values fall back to sane test defaults.
	Database string
	Username string
	Password string

	// StartParameters are extra postgresql.conf-style settings, e.g.
	// {"shared_buffers": "256kB"}.
	StartParameters map[string]string
}

// ownerMarker is the JSON structure written to ownerMarkerFile.
type ownerMarker struct {
	OwnerPID  int       `json:"ownerPid"`
	StartedAt time.Time `json:"startedAt"`
	Suite     string    `json:"suite"`
}

// Instance is a running embedded-PostgreSQL instance owned by this package.
type Instance struct {
	dataDir string
	// runtimeDir is this instance's private embedded-postgres runtime path
	// (holds the pwfile). Removed by Stop; see the Start comment for why it
	// must not live inside dataDir.
	runtimeDir string
	dsn        string
	pg         *embeddedpostgres.EmbeddedPostgres
	watchdog   *watchdogHandle
}

// DSN returns the PostgreSQL connection string for this instance.
func (i *Instance) DSN() string {
	return i.dsn
}

// DataDir returns the data directory owned by this instance.
func (i *Instance) DataDir() string {
	return i.dataDir
}

// Stop stops postgres, stops the watchdog (best-effort — a stray watchdog
// is harmless since it no-ops once the data dir and PIDs are gone, but
// killing it directly avoids a lingering `sh` per test run), and removes
// the data directory.
func (i *Instance) Stop() error {
	var stopErr error
	if i.pg != nil {
		stopErr = i.pg.Stop()
	}

	if i.watchdog != nil {
		i.watchdog.kill()
	}

	if i.dataDir != "" {
		_ = os.RemoveAll(i.dataDir)
	}

	if i.runtimeDir != "" {
		_ = os.RemoveAll(i.runtimeDir)
	}

	if stopErr != nil {
		return fmt.Errorf("failed to stop embedded postgres: %w", stopErr)
	}

	return nil
}

// sharedCachePath is the ONE directory every instance downloads and extracts
// the PostgreSQL binaries into and then reuses (embedded-postgres v2 keeps one
// subdirectory per distribution digest and takes care of concurrent access).
// It sits under ~/.embedded-postgres-go so CI's actions/cache of that directory
// keeps working.
//
// SP_TEST_PG_BINARIES_PATH overrides it for an environment where the home
// directory is not writable.
func sharedCachePath() string {
	if custom := os.Getenv("SP_TEST_PG_BINARIES_PATH"); custom != "" {
		return custom
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), unifiedPrefix+"-binaries")
	}

	return filepath.Join(home, ".embedded-postgres-go", "v2")
}

// resolved holds an Options with every zero value replaced by its default, so
// the connection string and the embedded-postgres config cannot disagree about
// what was actually started.
type resolved struct {
	database    string
	username    string
	password    string
	port        uint32
	startParams map[string]string
}

// resolveOptions fills in the defaults for anything the caller left unset.
func resolveOptions(opts *Options) resolved {
	res := resolved{
		database:    opts.Database,
		username:    opts.Username,
		password:    opts.Password,
		port:        opts.Port,
		startParams: opts.StartParameters,
	}

	if res.database == "" {
		res.database = "solidping_test"
	}

	if res.username == "" {
		res.username = "postgres"
	}

	if res.password == "" {
		res.password = "postgres"
	}

	if res.port == 0 {
		res.port = 5433
	}

	if res.startParams == nil {
		res.startParams = map[string]string{
			"dynamic_shared_memory_type": "posix",
			"shared_buffers":             "128kB",
			"max_connections":            "10",
		}
	}

	return res
}

// embeddedConfig builds the embedded-postgres config for one instance. See the
// Start comment for why RuntimePath is private per instance while CachePath
// is shared.
func embeddedConfig(opts *Options, dataDir, runtimeDir string) embeddedpostgres.Config {
	res := resolveOptions(opts)

	return embeddedpostgres.DefaultConfig().
		Port(res.port).
		Database(res.database).
		Username(res.username).
		Password(res.password).
		DataPath(dataDir).
		RuntimePath(runtimeDir).
		CachePath(sharedCachePath()).
		StartParameters(res.startParams)
}

// Start creates a fresh, self-owned data directory, runs the startup sweep
// (once per process), starts a new embedded-PostgreSQL instance in it, and
// spawns a parent-death watchdog. Callers must call Stop on the returned
// Instance when done (normally via t.Cleanup).
func Start(opts *Options) (*Instance, error) {
	sweepOnce.Do(func() { sweepOrphans(os.TempDir()) })

	suite := opts.Suite
	if suite == "" {
		suite = "default"
	}

	dataDir, err := os.MkdirTemp(os.TempDir(), unifiedPrefix+"-"+suite+"-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create embedded-postgres data dir: %w", err)
	}

	// Each instance gets its OWN runtime directory, and they all share ONE
	// binaries cache (CachePath), so PostgreSQL is downloaded and extracted
	// once, not once per instance, and CI's cache of ~/.embedded-postgres-go
	// keeps paying off. embedded-postgres creates a uniquely named workspace
	// inside the runtime path and never erases the path itself, so Stop
	// removes it. The name deliberately does NOT start with unifiedPrefix+"-",
	// so the orphan sweep (sweep.go isCandidateDir) does not mistake it for an
	// instance directory missing its owner marker and delete it out from under
	// a live instance. A hard-killed process leaks only that workspace.
	//
	// The data directory must be EMPTY when embedded-postgres initializes it
	// (it refuses a non-empty directory without a PG_VERSION), so the owner
	// marker is written only once the server is up. The window before that is
	// covered by unmarkedGraceAge.
	runtimeDir, err := os.MkdirTemp(os.TempDir(), "solidping-pgruntime-"+suite+"-*")
	if err != nil {
		_ = os.RemoveAll(dataDir)

		return nil, fmt.Errorf("failed to create embedded-postgres runtime dir: %w", err)
	}

	embeddedPG := embeddedpostgres.NewDatabase(embeddedConfig(opts, dataDir, runtimeDir))

	if startErr := embeddedPG.Start(); startErr != nil {
		_ = os.RemoveAll(dataDir)
		_ = os.RemoveAll(runtimeDir)

		return nil, fmt.Errorf("failed to start embedded postgres: %w", startErr)
	}

	// Now that the server is up, mark the instance as owned so a concurrent
	// process's startup sweep recognizes it as live.
	if markErr := writeOwnerMarker(dataDir, suite); markErr != nil {
		_ = embeddedPG.Stop()
		_ = os.RemoveAll(dataDir)
		_ = os.RemoveAll(runtimeDir)

		return nil, fmt.Errorf("failed to write owner marker: %w", markErr)
	}

	res := resolveOptions(opts)
	dsn := fmt.Sprintf(
		"postgres://%s:%s@localhost:%d/%s?sslmode=disable",
		res.username, res.password, res.port, res.database,
	)

	inst := &Instance{
		dataDir:    dataDir,
		runtimeDir: runtimeDir,
		dsn:        dsn,
		pg:         embeddedPG,
	}

	// The watchdog is best-effort infrastructure hardening: if it fails to
	// spawn (e.g. unsupported platform), the instance is still usable —
	// the startup sweep remains the safety net.
	inst.watchdog = startWatchdog(os.Getpid(), dataDir)

	return inst, nil
}

// writeOwnerMarker writes the owner.json marker into dataDir, recording this
// process's PID so the startup sweep can later tell whether this instance's
// owner is still alive.
func writeOwnerMarker(dataDir, suite string) error {
	marker := ownerMarker{
		OwnerPID:  os.Getpid(),
		StartedAt: time.Now().UTC(),
		Suite:     suite,
	}

	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("failed to marshal owner marker: %w", err)
	}

	if writeErr := os.WriteFile(filepath.Join(dataDir, ownerMarkerFile), data, 0o600); writeErr != nil {
		return fmt.Errorf("failed to write owner marker file: %w", writeErr)
	}

	return nil
}
