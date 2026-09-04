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

// ErrSecretsOrgKeyUndecryptable is the reason attached to a job whose
// ORGANIZATION key could not be opened at all — the layer above this check's
// envelope. Re-saving the check cannot help (the save path needs the same key),
// so the message must not send the operator down that road: this is a wrong or
// missing master key on this process, or an unreadable DEK row, and the answer
// is in the worker's logs.
var ErrSecretsOrgKeyUndecryptable = errors.New(
	"this organization's encryption key could not be opened on this worker — " +
		"re-saving the check will not help; check the worker logs for \"unwrap org DEK\"",
)

// ResultReason maps a failed secrets open to the static, user-visible reason to
// record on the check result. It names WHICH layer failed — the org key or this
// check's envelope — because the two have opposite remedies. The underlying
// error is only ever logged: it can carry cryptographic detail.
func ResultReason(outcome SecretMerge, err error) error {
	switch {
	case outcome == SecretMergeUnavailable:
		return ErrSecretsUnavailable
	case errors.Is(err, credentials.ErrOrgKeyUnavailable),
		errors.Is(err, ErrSecretsOrgKeyUndecryptable):
		return ErrSecretsOrgKeyUndecryptable
	default:
		return ErrSecretsUndecryptable
	}
}

// SecretMerge is the outcome of MergeJobSecrets.
type SecretMerge int

const (
	// SecretMergeNoop means the job carries no config_private envelope at all:
	// the check simply has no secret fields (secrets, when present, are always
	// split into config_private now — as an AES-GCM envelope with a master key,
	// or a plaintext envelope without one). The job is dispatchable as-is.
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
	private, outcome, err := OpenJobSecrets(ctx, creds, job)
	if outcome != SecretMergeMerged {
		return outcome, err
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

// OpenJobSecrets decrypts a job's config_private envelope and RETURNS the
// plaintext secret map without touching the job. It is the shared half of
// MergeJobSecrets: the same envelope taxonomy (plaintext envelope / no key /
// decrypt failure) for callers that must not merge the secrets into the config.
//
// The system-agent claim path (spec 2026-07-27-01) is exactly such a caller: it
// re-seals these secrets to the claiming agent's X25519 key instead of shipping
// them merged in the wire config, so the plaintext never leaves the server on
// the agent transport. SecretMergeNoop means "no envelope, nothing to seal";
// SecretMergeMerged means the returned map is the opened plaintext.
//
// Like MergeJobSecrets it never logs and never puts a config value in an error.
func OpenJobSecrets(
	ctx context.Context,
	creds credentials.Service,
	job *models.CheckJob,
) (map[string]any, SecretMerge, error) {
	return OpenSecretsEnvelope(ctx, creds, job.OrganizationUID, job.ConfigPrivate)
}

// OpenSecretsEnvelope is the envelope-opening primitive behind MergeJobSecrets
// and OpenJobSecrets, taking the envelope and its owning org directly so a
// caller holding a check row (not a job row) — e.g. the SSH-tunnel block a
// system agent's claim must re-seal — reuses the exact same taxonomy.
func OpenSecretsEnvelope(
	ctx context.Context,
	creds credentials.Service,
	orgUID string,
	envelope *string,
) (map[string]any, SecretMerge, error) {
	if envelope == nil || *envelope == "" {
		return nil, SecretMergeNoop, nil
	}

	switch {
	case credentials.IsPlaintextEnvelope(*envelope):
		// The no-master-key structural-separation envelope: openable with no
		// key, so a worker with encryption disabled keeps executing checks whose
		// secrets were split out of the public config.
		private, err := credentials.OpenPlaintext(*envelope)
		if err != nil {
			return nil, SecretMergeFailed, fmt.Errorf("%w: %w", ErrSecretsUndecryptable, err)
		}

		return private, SecretMergeMerged, nil
	case creds == nil || !creds.Enabled():
		return nil, SecretMergeUnavailable, ErrSecretsUnavailable
	default:
		private, err := creds.DecryptForOrg(ctx, orgUID, *envelope)
		if err != nil {
			return nil, SecretMergeFailed,
				fmt.Errorf("%w: %w", ResultReason(SecretMergeFailed, err), err)
		}

		return private, SecretMergeMerged, nil
	}
}
