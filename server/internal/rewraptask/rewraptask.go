// Package rewraptask re-seals a check's sealed secrets for the current
// recipients of its private regions, on an agent that can open them (spec
// 2026-11-10-03, peer re-wrap).
//
// A new agent in a per-agent region, or a region key rotation, leaves every
// sealed blob of the region flagged needsReseal. The server cannot open those
// blobs, so it sends one worker_tasks row of kind "rewrap" per check, targeted
// at one online agent that is a recipient of the blob. The agent opens the
// blob with its own key or a region key it holds, seals the plaintext to the
// recipient list the server sent, and returns only the new ciphertext. The
// server validates it like a sealedSecrets write and stores it, keeping the
// blob's origin. The server relays ciphertext only.
//
// Two checks keep an agent from replacing a blob it cannot open (spec
// 2026-11-11-02):
//
//   - possession: the task carries a random value sealed to the one key the
//     agent was picked for (its own, or a region key it advertises). The agent
//     sends it back with the new ciphertext; the server keeps only its hash
//     and refuses a result without it;
//   - confirmation: before the swap, a second online agent holding a current
//     recipient key opens the new blob (a "rewrap_verify" task, with its own
//     possession challenge), checks it carries only secret fields of the
//     check type (and the same plaintext as the old blob when it can open
//     that one too), and answers. With no second holder online, the blob is
//     stored marked unconfirmed, and the check stays flagged.
//
// One task per check keeps a payload under 100 KiB (a blob is at most 64 KiB
// of age file, base64-encoded), far below the 1 MiB request and 4 MiB claim
// caps of the worker task transport: nothing needs chunking.
package rewraptask

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Names of the kind.
const (
	// Kind is the worker_tasks.kind of a re-wrap.
	Kind = "rewrap"
	// Capability is what an agent running the executor reports. The server
	// only targets agents reporting it: an older agent would never claim.
	Capability = "rewrap"
	// VerifyKind is the worker_tasks.kind of the confirmation of a re-wrap
	// by a second agent.
	VerifyKind = "rewrap_verify"
	// VerifyCapability is what an agent running the confirmation reports.
	VerifyCapability = "rewrap-verify"
)

// Codes an agent finishes a refused re-wrap with.
const (
	// CodeCannotOpen is a blob none of the agent's keys opens.
	CodeCannotOpen = "REWRAP_CANNOT_OPEN"
	// CodeInvalidInput is a malformed task input.
	CodeInvalidInput = "REWRAP_INVALID_INPUT"
	// CodeSealFailed is a recipient list the agent could not seal to.
	CodeSealFailed = "REWRAP_SEAL_FAILED"
	// CodeNotSecret is a plaintext carrying a key that is not a secret field
	// of the check type, or bound to another target: never re-sealed nor
	// confirmed.
	CodeNotSecret = "REWRAP_NOT_SECRET"
	// CodeVerifyCannotOpen is a re-sealed blob the confirming agent cannot
	// open with its current key.
	CodeVerifyCannotOpen = "REWRAP_VERIFY_CANNOT_OPEN"
	// CodeVerifyMismatch is a re-sealed blob whose plaintext differs from the
	// old blob's.
	CodeVerifyMismatch = "REWRAP_VERIFY_MISMATCH"
)

// Codes the server writes on a failed re-wrap, and reasons it reports for a
// check it could not start one for.
const (
	// CodeInvalidResult is a returned blob that fails validation (stanza
	// count, size, encoding), or a result without the possession challenge.
	CodeInvalidResult = "invalid_result"
	// CodeNoProvableKey is a check whose online key holders all hold it by
	// a region key the server has no public key for (rotated away before
	// the server kept retired keys): none can prove it holds it.
	CodeNoProvableKey = "no_provable_key"
	// CodeNotConfirmed is the outcome code of a re-wrap stored without a
	// second agent to confirm it: done, but the check stays flagged.
	CodeNotConfirmed = "not_confirmed"
	// CodeRecipientsChanged is a returned blob sealed to a recipient set that
	// is not (or no longer) the current one.
	CodeRecipientsChanged = "recipients_changed"
	// CodeCheckChanged is a check whose blob changed while the re-wrap ran.
	CodeCheckChanged = "check_changed"
	// CodeTimedOut is a re-wrap no agent finished within Timeout.
	CodeTimedOut = "timed_out"
	// CodeNoOnlineRecipient is a check no online agent can open.
	CodeNoOnlineRecipient = "no_online_recipient"
	// CodeRewrapNotOffered is a check an online agent could open, but none of
	// those agents runs the re-wrap: older than 0.48 (no possession
	// challenge, spec 2026-11-11-02), or its operator set
	// SP_AGENT_REWRAP_ENABLED=false (spec 2026-11-10-04).
	CodeRewrapNotOffered = "rewrap_not_offered"
	// CodeNotSealed is a check without a sealed blob to re-wrap.
	CodeNotSealed = "not_sealed"
	// CodeUpToDate is a check already sealed to the current recipients.
	CodeUpToDate = "up_to_date"
	// CodeNoRecipients is a check whose regions have nothing to seal to.
	CodeNoRecipients = "no_recipients"
	// CodeAlreadyRunning is a check with another active worker task.
	CodeAlreadyRunning = "already_running"
)

// Timeout is how long a re-wrap may stay pending or claimed before it is
// reported (and recorded) as timed out.
const Timeout = 5 * time.Minute

// Keys of the task payloads. A payload stays on the server: the claim sends
// the kind's Input, never the payload.
const (
	// payloadEnvelopeSHA256 is the hash of the blob to re-wrap, so a blob
	// replaced in the meantime is never overwritten.
	payloadEnvelopeSHA256 = "envelopeSha256"
	// payloadChallenge is the possession challenge, sealed to the key the
	// agent was picked for.
	payloadChallenge = "challenge"
	// payloadChallengeSHA256 is the hash of the challenge's value.
	payloadChallengeSHA256 = "challengeSha256"
	// payloadConfirm is set on a re-wrap once its result passed: confirmPending
	// (a confirmation task was sent) or confirmNone (stored unconfirmed).
	payloadConfirm = "confirm"
	// payloadVerifyTaskUID is the confirmation task of a re-wrap.
	payloadVerifyTaskUID = "verifyTaskUid"
	// payloadVerifierUID is the agent picked to confirm.
	payloadVerifierUID = "verifierUid"
	// payloadVerifierRegion is the region of the agent picked to confirm.
	payloadVerifierRegion = "verifierRegion"
	// payloadVerifyChallenge and payloadVerifyChallengeSHA256 are the
	// confirming agent's possession challenge, copied to the confirmation.
	payloadVerifyChallenge       = "verifyChallenge"
	payloadVerifyChallengeSHA256 = "verifyChallengeSha256"
	// payloadPendingEnvelope is the re-sealed blob waiting for confirmation.
	payloadPendingEnvelope = "pendingEnvelope"
	// payloadRewrapTaskUID is, on a confirmation, the re-wrap it confirms.
	payloadRewrapTaskUID = "rewrapTaskUid"
)

// Values of payloadConfirm.
const (
	confirmPending = "pending"
	confirmNone    = "none"
)

// Input is the claim input: the sealed blob, the recipients to seal to, the
// possession challenge, and the check's type and public config (to refuse a
// plaintext that is not only secret fields of the type).
type Input struct {
	Envelope   string         `json:"envelope"`
	Recipients []string       `json:"recipients"`
	Challenge  string         `json:"challenge,omitempty"`
	CheckType  string         `json:"checkType,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
}

// Result is the finish body: the new ciphertext, its fingerprints and the
// opened challenge, or the code of a refusal.
type Result struct {
	CT           string   `json:"ct,omitempty"`
	Fingerprints []string `json:"fingerprints,omitempty"`
	Challenge    string   `json:"challenge,omitempty"`
	ErrorCode    string   `json:"errorCode,omitempty"`
}

// VerifyInput is the claim input of a confirmation: the re-sealed blob, the
// blob it replaces, the confirming agent's possession challenge, and the
// check's type and public config.
type VerifyInput struct {
	Envelope  string         `json:"envelope"`
	Previous  string         `json:"previous,omitempty"`
	Challenge string         `json:"challenge"`
	CheckType string         `json:"checkType,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
}

// VerifyResult is the finish body of a confirmation: the opened challenge
// (the blob opened and passed), or the code of a refusal.
type VerifyResult struct {
	Challenge string `json:"challenge,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
}

// envelopeHash is the hex SHA-256 of a stored envelope.
func envelopeHash(envelope string) string {
	sum := sha256.Sum256([]byte(envelope))

	return hex.EncodeToString(sum[:])
}

func isAgentRefusal(code string) bool {
	switch code {
	case CodeCannotOpen, CodeInvalidInput, CodeSealFailed, CodeNotSecret:
		return true
	}

	return false
}

func isVerifyRefusal(code string) bool {
	switch code {
	case CodeCannotOpen, CodeInvalidInput, CodeNotSecret, CodeVerifyCannotOpen, CodeVerifyMismatch:
		return true
	}

	return false
}
