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

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
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
	dataDir  string
	dsn      string
	pg       *embeddedpostgres.EmbeddedPostgres
	watchdog *watchdogHandle
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

	if stopErr != nil {
		return fmt.Errorf("failed to stop embedded postgres: %w", stopErr)
	}

	return nil
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

	if markErr := writeOwnerMarker(dataDir, suite); markErr != nil {
		_ = os.RemoveAll(dataDir)

		return nil, fmt.Errorf("failed to write owner marker: %w", markErr)
	}

	database := opts.Database
	if database == "" {
		database = "solidping_test"
	}

	username := opts.Username
	if username == "" {
		username = "postgres"
	}

	password := opts.Password
	if password == "" {
		password = "postgres"
	}

	port := opts.Port
	if port == 0 {
		port = 5433
	}

	startParams := opts.StartParameters
	if startParams == nil {
		startParams = map[string]string{
			"dynamic_shared_memory_type": "posix",
			"shared_buffers":             "128kB",
			"max_connections":            "10",
		}
	}

	embeddedPG := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Port(port).
			Database(database).
			Username(username).
			Password(password).
			DataPath(dataDir).
			StartParameters(startParams),
	)

	if startErr := embeddedPG.Start(); startErr != nil {
		_ = os.RemoveAll(dataDir)

		return nil, fmt.Errorf("failed to start embedded postgres: %w", startErr)
	}

	dsn := fmt.Sprintf(
		"postgres://%s:%s@localhost:%d/%s?sslmode=disable", username, password, port, database,
	)

	inst := &Instance{
		dataDir: dataDir,
		dsn:     dsn,
		pg:      embeddedPG,
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
