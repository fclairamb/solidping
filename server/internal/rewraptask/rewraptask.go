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
)

// Codes an agent finishes a refused re-wrap with.
const (
	// CodeCannotOpen is a blob none of the agent's keys opens.
	CodeCannotOpen = "REWRAP_CANNOT_OPEN"
	// CodeInvalidInput is a malformed task input.
	CodeInvalidInput = "REWRAP_INVALID_INPUT"
	// CodeSealFailed is a recipient list the agent could not seal to.
	CodeSealFailed = "REWRAP_SEAL_FAILED"
)

// Codes the server writes on a failed re-wrap, and reasons it reports for a
// check it could not start one for.
const (
	// CodeInvalidResult is a returned blob that fails validation (stanza
	// count, size, encoding).
	CodeInvalidResult = "invalid_result"
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
	// those agents runs the re-wrap: older than 0.47, or its operator set
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

// Payload is the task payload: the hash of the blob to re-wrap, so a blob
// replaced in the meantime is never overwritten.
type Payload struct {
	EnvelopeSHA256 string `json:"envelopeSha256"`
}

// Input is the claim input: the sealed blob and the recipients to seal to.
type Input struct {
	Envelope   string   `json:"envelope"`
	Recipients []string `json:"recipients"`
}

// Result is the finish body: the new ciphertext and its fingerprints, or the
// code of a refusal.
type Result struct {
	CT           string   `json:"ct,omitempty"`
	Fingerprints []string `json:"fingerprints,omitempty"`
	ErrorCode    string   `json:"errorCode,omitempty"`
}

// envelopeHash is the hex SHA-256 of a stored envelope.
func envelopeHash(envelope string) string {
	sum := sha256.Sum256([]byte(envelope))

	return hex.EncodeToString(sum[:])
}

func isAgentRefusal(code string) bool {
	switch code {
	case CodeCannotOpen, CodeInvalidInput, CodeSealFailed:
		return true
	}

	return false
}
