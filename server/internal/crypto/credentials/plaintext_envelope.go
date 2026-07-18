package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Plaintext envelope constants. The plaintext envelope lives in the SAME
// `config_private` / `settings_private` TEXT column as the v1 symmetric
// (AES-GCM) and v2 region-sealed (age-x25519) envelopes, with its own
// version/alg marker so the three never collide and any reader can tell which
// open path applies.
//
// It carries NO encryption: it is the structural separation used when no master
// key is configured (the documented V1 self-hosted fallback). Its whole purpose
// is to keep secret VALUES out of the public `config` column even when the
// server cannot encrypt them at rest — the API-side leak this fixes. It is
// openable without any key, which is exactly why the claim/dispatch merge and
// every other server-side decrypt seam can reconstitute it. It does NOT change
// the at-rest threat model: with no master key, secrets are still stored in
// clear (just in a separate column that the API never echoes).
const (
	plaintextEnvelopeVersion = 3
	plaintextEnvelopeAlg     = "plaintext"
)

// ErrNotPlaintextEnvelope is returned when a blob is not a v3 plaintext blob.
var ErrNotPlaintextEnvelope = errors.New("envelope is not a v3 plaintext blob")

// plaintextEnvelopeJSON is the on-disk shape of a plaintext envelope. `Data`
// holds the secret map verbatim (no ciphertext) — the envelope only relocates
// the secrets out of the public column, it does not protect them at rest.
type plaintextEnvelopeJSON struct {
	Version int            `json:"v"`
	Alg     string         `json:"alg"`
	Data    map[string]any `json:"data"`
}

// SealPlaintext wraps a secret map into a v3 plaintext envelope string ready to
// persist in a `config_private` / `settings_private` TEXT column. No key
// required. A nil map is treated as empty.
func SealPlaintext(secrets map[string]any) (string, error) {
	if secrets == nil {
		secrets = map[string]any{}
	}

	envelope := plaintextEnvelopeJSON{
		Version: plaintextEnvelopeVersion,
		Alg:     plaintextEnvelopeAlg,
		Data:    secrets,
	}

	out, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal plaintext envelope: %w", err)
	}

	return string(out), nil
}

// OpenPlaintext unwraps a v3 plaintext envelope back to its secret map. Returns
// ErrNotPlaintextEnvelope when the blob is a different envelope type.
func OpenPlaintext(envelope string) (map[string]any, error) {
	env, err := parsePlaintextEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	if env.Data == nil {
		return map[string]any{}, nil
	}

	return env.Data, nil
}

// IsPlaintextEnvelope reports whether a stored envelope is a v3 plaintext blob
// (as opposed to the v1 symmetric envelope, the v2 sealed blob, or anything
// else).
func IsPlaintextEnvelope(envelope string) bool {
	env, err := parsePlaintextEnvelope(envelope)

	return err == nil && env.Version == plaintextEnvelopeVersion
}

// RequiresKey reports whether opening an envelope needs a configured master
// key. Plaintext (v3) envelopes never do; AES-GCM (v1) and region-sealed (v2)
// ones always do. Callers use it to gate their "encryption disabled" error
// paths so a plaintext envelope stays openable with no key. An empty string
// requires no key (there is nothing to open).
func RequiresKey(envelope string) bool {
	if envelope == "" {
		return false
	}

	return !IsPlaintextEnvelope(envelope)
}

func parsePlaintextEnvelope(envelope string) (*plaintextEnvelopeJSON, error) {
	var env plaintextEnvelopeJSON
	if err := json.Unmarshal([]byte(envelope), &env); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotPlaintextEnvelope, err)
	}

	if env.Version != plaintextEnvelopeVersion || env.Alg != plaintextEnvelopeAlg {
		return nil, ErrNotPlaintextEnvelope
	}

	return &env, nil
}
