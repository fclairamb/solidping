package checkjobsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrSecretsUnavailable is the reason attached to a job whose config_private
// envelope cannot be opened because no master key is configured on this
// process. It is user-visible (it lands in the error result's output on the
// in-process path), so it names the fix rather than the internals.
var ErrSecretsUnavailable = errors.New(
	"check has encrypted credentials but SP_ENCRYPTION_MASTER_KEY is not configured on this worker",
)

// ErrSecretsUndecryptable is the reason attached to a job whose envelope
// exists and a master key is configured, but decryption failed (wrong key,
// missing org DEK, tampered ciphertext). The underlying error is logged, never
// surfaced — it can carry cryptographic detail.
var ErrSecretsUndecryptable = errors.New(
	"check credentials could not be decrypted — re-save the check's credentials",
)

// SecretMerge is the outcome of MergeJobSecrets.
type SecretMerge int

const (
	// SecretMergeNoop means the job carries no encrypted envelope: either
	// encryption is disabled fleet-wide and secrets are plaintext in the
	// public config (the documented V1 self-hosted fallback), or the check
	// simply has no secret fields. The job is dispatchable as-is.
	SecretMergeNoop SecretMerge = iota
	// SecretMergeMerged means the envelope was decrypted and merged into
	// job.Config, and the envelope was stripped off the job.
	SecretMergeMerged
	// SecretMergeUnavailable means an envelope exists but no master key is
	// configured on this process, so it can never be opened.
	SecretMergeUnavailable
	// SecretMergeFailed means an envelope exists and a key is configured, but
	// decryption failed.
	SecretMergeFailed
)

// MergeJobSecrets decrypts a claimed job's config_private envelope for its own
// org and merges the plaintext into job.Config in place, mirroring the
// dispatch-time semantics every executor depends on: a checker must receive
// one merged config map and must never see the envelope.
//
// It is the merge rule shared by every claim path that dispatches a job to a
// checker — currently the in-process claim path
// (checkworker/backend.DirectBackend) — so the strip-the-envelope invariant
// and the failure taxonomy live here once.
//
// It deliberately does NOT decide what happens on failure: the caller does
// (the in-process backend writes an explicit error result). It never logs,
// and never returns a config value in an error — the decrypted map is a
// secret and only ever reaches job.Config.
//
// creds may be nil (a worker built without a credentials service): that is
// treated exactly like a disabled service.
func MergeJobSecrets(
	ctx context.Context,
	creds credentials.Service,
	job *models.CheckJob,
) (SecretMerge, error) {
	if job.ConfigPrivate == nil || *job.ConfigPrivate == "" {
		return SecretMergeNoop, nil
	}

	if creds == nil || !creds.Enabled() {
		return SecretMergeUnavailable, ErrSecretsUnavailable
	}

	private, err := creds.DecryptForOrg(ctx, job.OrganizationUID, *job.ConfigPrivate)
	if err != nil {
		return SecretMergeFailed, fmt.Errorf("%w: %w", ErrSecretsUndecryptable, err)
	}

	job.Config = models.JSONMap(credentials.MergeConfig(job.Config, private))
	// Strip the envelope after merge: an executor must never receive both
	// halves, so that "the plaintext lives only in job.Config, in memory" stays
	// trivially verifiable at any point downstream.
	job.ConfigPrivate = nil
	job.ConfigPrivateKeys = nil
	job.Encrypted = false

	return SecretMergeMerged, nil
}
