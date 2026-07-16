package credentials

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"filippo.io/age"
)

// Asymmetric (region-sealed) envelope constants. The sealed envelope lives in
// the SAME `config_private` TEXT column as the v1 symmetric envelope, but with a
// distinct version/alg marker so the two never collide and either side can tell
// which decrypt path applies. Credentials destined only for an org-private
// region are sealed to the region's agents' X25519 keys — the server cannot
// recover them after write (it holds no matching identity).
const (
	sealedEnvelopeVersion = 2
	sealedEnvelopeAlg     = "age-x25519"
)

// Errors returned by the sealing primitive.
var (
	ErrNoRecipients      = errors.New("no recipients to seal credentials to")
	ErrNotSealedEnvelope = errors.New("envelope is not an age-x25519 sealed blob")
	ErrInvalidRecipient  = errors.New("invalid age X25519 recipient")
	ErrInvalidIdentity   = errors.New("invalid age X25519 identity")
)

// sealedEnvelopeJSON is the on-disk shape of a region-sealed blob. `CT` is the
// base64 of the raw age binary file (JSON of the secret map encrypted to every
// recipient). `Fingerprints` records which recipients the blob was sealed to so
// a "needs re-seal" state can be computed (recipient set vs current active
// agents) without decrypting.
type sealedEnvelopeJSON struct {
	Version      int      `json:"v"`
	Alg          string   `json:"alg"`
	Fingerprints []string `json:"fingerprints"`
	CT           string   `json:"ct"`
}

// RecipientFingerprint returns a short, stable, non-secret fingerprint of an age
// X25519 recipient string (public key). Used to record who a sealed blob was
// sealed to and to detect membership drift. Safe to show in UI/logs.
func RecipientFingerprint(recipient string) string {
	sum := sha256.Sum256([]byte(recipient))

	return hex.EncodeToString(sum[:8])
}

// GenerateX25519Keypair generates a fresh age X25519 identity, returning the
// secret identity string ("AGE-SECRET-KEY-1…", kept by the agent) and the public
// recipient string ("age1…", stored on the server so credentials can be sealed
// to it). The server never holds the identity.
func GenerateX25519Keypair() (identity, recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", fmt.Errorf("generate x25519 identity: %w", err)
	}

	return id.String(), id.Recipient().String(), nil
}

// SealForRecipients seals a map of secret fields to one or more age X25519
// recipients (the public keys of every active agent of the check's private
// region). Any recipient with the matching identity can unseal; the server, which
// holds no identity, cannot. The recipient set must be non-empty.
func SealForRecipients(recipients []string, secrets map[string]any) (string, error) {
	if len(recipients) == 0 {
		return "", ErrNoRecipients
	}

	parsed := make([]age.Recipient, 0, len(recipients))
	fingerprints := make([]string, 0, len(recipients))

	for _, r := range recipients {
		rec, err := age.ParseX25519Recipient(r)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidRecipient, err)
		}

		parsed = append(parsed, rec)
		fingerprints = append(fingerprints, RecipientFingerprint(r))
	}

	sort.Strings(fingerprints)

	body, err := json.Marshal(secrets)
	if err != nil {
		return "", fmt.Errorf("marshal secrets: %w", err)
	}

	var buf bytes.Buffer

	w, err := age.Encrypt(&buf, parsed...)
	if err != nil {
		return "", fmt.Errorf("age encrypt: %w", err)
	}

	if _, err := w.Write(body); err != nil {
		return "", fmt.Errorf("age write: %w", err)
	}

	if err := w.Close(); err != nil {
		return "", fmt.Errorf("age close: %w", err)
	}

	envelope := sealedEnvelopeJSON{
		Version:      sealedEnvelopeVersion,
		Alg:          sealedEnvelopeAlg,
		Fingerprints: fingerprints,
		CT:           base64.StdEncoding.EncodeToString(buf.Bytes()),
	}

	out, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal sealed envelope: %w", err)
	}

	return string(out), nil
}

// UnsealWithIdentity unseals a region-sealed envelope with an age X25519 identity
// string. Returns ErrNotSealedEnvelope when the blob is not a v2 age envelope
// (e.g. it is the v1 symmetric envelope); the caller can then fall back to the
// symmetric path. A wrong/removed identity yields a decrypt error — the agent
// surfaces this as a clear "credentials not sealed for this agent" job error.
func UnsealWithIdentity(identity, envelope string) (map[string]any, error) {
	env, err := parseSealedEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	id, err := age.ParseX25519Identity(identity)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidIdentity, err)
	}

	ct, err := base64.StdEncoding.DecodeString(env.CT)
	if err != nil {
		return nil, fmt.Errorf("decode sealed ciphertext: %w", err)
	}

	rdr, err := age.Decrypt(bytes.NewReader(ct), id)
	if err != nil {
		return nil, fmt.Errorf("age decrypt: %w", err)
	}

	plain, err := io.ReadAll(rdr)
	if err != nil {
		return nil, fmt.Errorf("read sealed plaintext: %w", err)
	}

	out := map[string]any{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("unmarshal sealed plaintext: %w", err)
	}

	return out, nil
}

// IsSealedEnvelope reports whether a stored envelope string is a v2 age-x25519
// sealed blob (as opposed to the v1 symmetric envelope or plaintext).
func IsSealedEnvelope(envelope string) bool {
	env, err := parseSealedEnvelope(envelope)

	return err == nil && env.Version == sealedEnvelopeVersion
}

// SealedRecipientFingerprints returns the fingerprints a sealed blob was sealed
// to. Errors if the envelope is not a sealed blob.
func SealedRecipientFingerprints(envelope string) ([]string, error) {
	env, err := parseSealedEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	return env.Fingerprints, nil
}

// NeedsReseal reports whether a sealed blob is missing any currently-active
// recipient fingerprint — i.e. an active agent joined the region after the blob
// was written and cannot decrypt it. A blob sealed to a superset of the active
// set (e.g. a revoked agent still listed) does NOT by itself need a re-seal for
// availability, but revocation handling re-seals separately.
func NeedsReseal(envelope string, activeFingerprints []string) (bool, error) {
	sealed, err := SealedRecipientFingerprints(envelope)
	if err != nil {
		return false, err
	}

	have := make(map[string]struct{}, len(sealed))
	for _, f := range sealed {
		have[f] = struct{}{}
	}

	for _, f := range activeFingerprints {
		if _, ok := have[f]; !ok {
			return true, nil
		}
	}

	return false, nil
}

// parseSealedEnvelope decodes and validates the v2 sealed envelope shape.
func parseSealedEnvelope(envelope string) (*sealedEnvelopeJSON, error) {
	var env sealedEnvelopeJSON
	if err := json.Unmarshal([]byte(envelope), &env); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotSealedEnvelope, err)
	}

	if env.Version != sealedEnvelopeVersion || env.Alg != sealedEnvelopeAlg {
		return nil, ErrNotSealedEnvelope
	}

	return &env, nil
}
