package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// minSealedOrgParamWorkerVersion is the first release whose direct-DB worker
// opens a sealed (`$sp_sealed`) secret organization parameter when it resolves
// `${param:…}` (spec 2026-11-27-02). An older one substitutes the Go rendering
// of the envelope for the secret (fclairamb/solidping-business#118).
const minSealedOrgParamWorkerVersion = "0.57.0"

// orgParamSealRetryInterval is how often a deferred seal of the secret org
// parameters is retried: one liveness window, the time an upgraded worker's
// predecessor takes to stop counting as live.
const orgParamSealRetryInterval = regions.WorkerLivenessWindow

// workerLister is the slice of the DB the seal gate reads.
type workerLister interface {
	ListWorkers(ctx context.Context) ([]*models.Worker, error)
}

// directWorkerOrgSealGate returns the gate that keeps the secret org
// parameters in plaintext while a live direct-DB worker cannot open them
// (spec 2026-11-28-01): a worker seen within the liveness window, not an
// enrolled agent (the server resolves an agent's references itself), that
// reports no version or one older than minSealedOrgParamWorkerVersion. A worker
// on the server's own build always passes, so a `dev` build gates nothing on
// itself.
func directWorkerOrgSealGate(store workerLister, serverVersion string, now func() time.Time) sysparamseal.OrgSealGate {
	return func(ctx context.Context) ([]string, error) {
		workers, err := store.ListWorkers(ctx)
		if err != nil {
			return nil, fmt.Errorf("list workers: %w", err)
		}

		cutoff := regions.LivenessCutoff(now())

		var blockers []string

		for _, worker := range workers {
			if worker == nil || !blocksOrgSeal(worker, serverVersion, cutoff) {
				continue
			}

			reported := "no version"
			if worker.Version != nil {
				reported = *worker.Version
			}

			blockers = append(blockers, fmt.Sprintf("%s (%s)", worker.Slug, reported))
		}

		return blockers, nil
	}
}

// blocksOrgSeal reports whether worker holds the seal back.
func blocksOrgSeal(worker *models.Worker, serverVersion string, liveCutoff time.Time) bool {
	if worker.LastActiveAt == nil || worker.LastActiveAt.Before(liveCutoff) {
		return false
	}

	if agents.IsWorkerSlug(worker.Slug) && strings.HasPrefix(worker.Name, "agent:") {
		return false
	}

	if worker.Version == nil || *worker.Version == "" {
		return true
	}

	if *worker.Version == serverVersion {
		return false
	}

	return !versionAtLeast(*worker.Version, minSealedOrgParamWorkerVersion)
}

// releaseCore matches the `X.Y.Z` a build version starts with (`0.57.0`,
// `v0.57.0`, `0.57.0-3-gabc1234-dirty` from git describe).
var releaseCore = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// versionAtLeast compares the release cores of two versions. A version without
// one (`dev`, a commit hash) is never at least anything: an unknown build is
// not trusted to open a sealed value.
func versionAtLeast(version, minimum string) bool {
	got, ok := releaseTriple(version)
	if !ok {
		return false
	}

	want, ok := releaseTriple(minimum)
	if !ok {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}

	return true
}

func releaseTriple(version string) ([3]int, bool) {
	var triple [3]int

	match := releaseCore.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return triple, false
	}

	for i := range triple {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return triple, false
		}

		triple[i] = value
	}

	return triple, true
}

// sealOrgParameters runs the org-parameter sweep and reports whether it is
// still deferred. A deferral is logged (warn at boot, debug on a retry), never
// an error: plaintext keeps working for every version.
func (s *Server) sealOrgParameters(ctx context.Context, atBoot bool) (bool, error) {
	sealedOrgParams, err := s.sysParams.SealPlaintextOrgParameters(ctx)

	var deferred *sysparamseal.OrgSealDeferredError
	if errors.As(err, &deferred) {
		level := slog.LevelDebug
		if atBoot {
			level = slog.LevelWarn
		}

		slog.Log(ctx, level, "sealing secret organization parameters deferred: "+
			"a live direct-DB worker cannot open them yet; upgrade it to this version, "+
			"the seal is retried until it is",
			"workers", deferred.Blockers, "reason", deferred.Cause,
			"minimumWorkerVersion", minSealedOrgParamWorkerVersion,
			"retryEvery", orgParamSealRetryInterval.String())

		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("auto-migrate organization parameters: %w", err)
	}

	if sealedOrgParams > 0 {
		slog.InfoContext(ctx, "encrypted plaintext secret organization parameters",
			"orgParametersSealed", sealedOrgParams, "atStartup", atBoot)
	}

	return false, nil
}

// retryDeferredOrgParamSeal runs one retry tick: a sweep when a secret org
// parameter was left in plaintext by the gate. It returns whether one is
// still pending.
func (s *Server) retryDeferredOrgParamSeal(ctx context.Context) bool {
	if s.sysParams == nil || !s.sysParams.OrgSealPending() {
		return false
	}

	pending, err := s.sealOrgParameters(ctx, false)
	if err != nil {
		slog.WarnContext(ctx, "retrying the seal of secret organization parameters failed", "error", err)

		return true
	}

	return pending
}

// startOrgParamSealRetry starts runOrgParamSealRetry when there can be
// something to seal: a master key and the auto-migration on.
func (s *Server) startOrgParamSealRetry(ctx context.Context) {
	if s.sysParams == nil || !s.sysParams.Enabled() || !s.config.Encryption.AutoMigrate {
		return
	}

	s.workersWg.Add(1)

	go s.runOrgParamSealRetry(ctx)
}

// runOrgParamSealRetry retries a deferred seal of the secret org parameters
// every orgParamSealRetryInterval until ctx ends (spec 2026-11-28-01). It
// keeps running after a successful sweep: a secret written while a later
// old worker was live is sealed by the next tick.
func (s *Server) runOrgParamSealRetry(ctx context.Context) {
	defer s.workersWg.Done()

	ticker := time.NewTicker(orgParamSealRetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.retryDeferredOrgParamSeal(ctx)
		}
	}
}
