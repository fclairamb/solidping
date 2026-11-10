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
	"strings"

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
	// Origin records who produced the ciphertext: SealOriginServer or
	// SealOriginBrowser (spec 2026-11-08-01). Written by the server, never
	// taken from a request. Absent on legacy server-sealed blobs. The agent's
	// decoder ignores it.
	Origin string `json:"origin,omitempty"`
}

// Seal origins (spec 2026-11-08-01).
const (
	// SealOriginServer marks a blob the server sealed from plaintext it
	// received.
	SealOriginServer = "server"
	// SealOriginBrowser marks a blob sealed by the client (dash0 or any API
	// client) for the agents' public keys: the server never saw the plaintext.
	SealOriginBrowser = "browser"
)

// MaxSealedCiphertextBytes caps a client-sealed age file (decoded).
const MaxSealedCiphertextBytes = 64 * 1024

// Client-sealed ciphertext validation errors.
var (
	ErrSealedCiphertextEncoding = errors.New("ct is not valid base64")
	ErrSealedCiphertextTooLarge = errors.New("ct is larger than 64 KiB")
	ErrSealedCiphertextHeader   = errors.New("ct is not an age file sealed only to X25519 recipients")
	ErrSealedStanzaCount        = errors.New("ct does not hold one X25519 stanza per fingerprint")
)

// RecipientFingerprint returns a short, stable, non-secret fingerprint of an age
// X25519 recipient string (public key). Used to record who a sealed blob was
// sealed to and to detect membership drift. Safe to show in UI/logs.
func RecipientFingerprint(recipient string) string {
	sum := sha256.Sum256([]byte(recipient))

	return hex.EncodeToString(sum[:8])
}

// GenerateX25519Keypair generates a fresh age X25519 identity. It returns the
// secret identity string ("AGE-SECRET-KEY-1…", kept by the agent) and the public
// recipient string ("age1…", stored on the server so credentials can be sealed
// to it), in that order. The server never holds the identity.
func GenerateX25519Keypair() (string, string, error) {
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

	for _, recipient := range recipients {
		rec, parseErr := age.ParseX25519Recipient(recipient)
		if parseErr != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidRecipient, parseErr)
		}

		parsed = append(parsed, rec)
		fingerprints = append(fingerprints, RecipientFingerprint(recipient))
	}

	sort.Strings(fingerprints)

	body, err := json.Marshal(secrets)
	if err != nil {
		return "", fmt.Errorf("marshal secrets: %w", err)
	}

	var buf bytes.Buffer

	writer, err := age.Encrypt(&buf, parsed...)
	if err != nil {
		return "", fmt.Errorf("age encrypt: %w", err)
	}

	if _, writeErr := writer.Write(body); writeErr != nil {
		return "", fmt.Errorf("age write: %w", writeErr)
	}

	if closeErr := writer.Close(); closeErr != nil {
		return "", fmt.Errorf("age close: %w", closeErr)
	}

	envelope := sealedEnvelopeJSON{
		Version:      sealedEnvelopeVersion,
		Alg:          sealedEnvelopeAlg,
		Fingerprints: fingerprints,
		CT:           base64.StdEncoding.EncodeToString(buf.Bytes()),
		Origin:       SealOriginServer,
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
	return UnsealWithIdentities(envelope, identity)
}

// UnsealWithIdentities unseals a region-sealed envelope with any of several age
// X25519 identities: the agent's own key and the shared region keys it holds
// (spec 2026-11-08-01 §2). age tries each identity against each stanza.
func UnsealWithIdentities(envelope string, identities ...string) (map[string]any, error) {
	env, err := parseSealedEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	parsed := make([]age.Identity, 0, len(identities))

	for _, identity := range identities {
		id, parseErr := age.ParseX25519Identity(identity)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidIdentity, parseErr)
		}

		parsed = append(parsed, id)
	}

	if len(parsed) == 0 {
		return nil, ErrInvalidIdentity
	}

	ct, err := base64.StdEncoding.DecodeString(env.CT)
	if err != nil {
		return nil, fmt.Errorf("decode sealed ciphertext: %w", err)
	}

	rdr, err := age.Decrypt(bytes.NewReader(ct), parsed...)
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

// X25519RecipientOf returns the public recipient ("age1…") of an age X25519
// identity string.
func X25519RecipientOf(identity string) (string, error) {
	id, err := age.ParseX25519Identity(identity)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidIdentity, err)
	}

	return id.Recipient().String(), nil
}

// ValidateRecipient reports whether recipient is a valid age X25519 recipient.
func ValidateRecipient(recipient string) error {
	if _, err := age.ParseX25519Recipient(recipient); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRecipient, err)
	}

	return nil
}

// ValidateSealedCiphertext sanity-checks a client-sealed age file before it is
// stored (spec 2026-11-08-01 §4.3): valid base64, at most 64 KiB, and an age
// header holding exactly wantStanzas X25519 stanzas and nothing else. age hides
// who a stanza is for, so this is a sanity check, not proof of the recipients.
func ValidateSealedCiphertext(ct string, wantStanzas int) error {
	if base64.StdEncoding.DecodedLen(len(ct)) > MaxSealedCiphertextBytes+3 {
		return ErrSealedCiphertextTooLarge
	}

	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		return ErrSealedCiphertextEncoding
	}

	if len(raw) > MaxSealedCiphertextBytes {
		return ErrSealedCiphertextTooLarge
	}

	header, err := age.ExtractHeader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSealedCiphertextHeader, err)
	}

	x25519 := 0

	for _, line := range strings.Split(string(header), "\n") {
		if !strings.HasPrefix(line, "-> ") {
			continue
		}

		if !strings.HasPrefix(line, "-> X25519 ") {
			return ErrSealedCiphertextHeader
		}

		x25519++
	}

	if x25519 != wantStanzas {
		return fmt.Errorf("%w: header has %d, fingerprints list %d", ErrSealedStanzaCount, x25519, wantStanzas)
	}

	return nil
}

// BuildClientSealedEnvelope wraps a client-sealed age file into the stored v2
// envelope, with origin SealOriginBrowser. The fingerprints are sorted so the
// stored shape matches what SealForRecipients writes.
func BuildClientSealedEnvelope(fingerprints []string, ciphertext string) (string, error) {
	sorted := append([]string(nil), fingerprints...)
	sort.Strings(sorted)

	out, err := json.Marshal(sealedEnvelopeJSON{
		Version:      sealedEnvelopeVersion,
		Alg:          sealedEnvelopeAlg,
		Fingerprints: sorted,
		CT:           ciphertext,
		Origin:       SealOriginBrowser,
	})
	if err != nil {
		return "", fmt.Errorf("marshal sealed envelope: %w", err)
	}

	return string(out), nil
}

// SealedCiphertext returns the age file (base64) and the sorted fingerprints
// of a sealed envelope: what a client sends as sealedSecrets.ct and
// .fingerprints (spec 2026-11-10-03).
func SealedCiphertext(envelope string) (string, []string, error) {
	env, err := parseSealedEnvelope(envelope)
	if err != nil {
		return "", nil, err
	}

	fps := append([]string(nil), env.Fingerprints...)
	sort.Strings(fps)

	return env.CT, fps, nil
}

// SealForClient seals a secret map to recipients the way a client does and
// returns the parts of a sealedSecrets object: the sorted fingerprints and
// the base64 age file. Used by `solidping seal` and the agent's re-wrap.
func SealForClient(recipients []string, secrets map[string]any) ([]string, string, error) {
	envelope, err := SealForRecipients(recipients, secrets)
	if err != nil {
		return nil, "", err
	}

	ct, fps, _ := SealedCiphertext(envelope) // the envelope was just built: it parses

	return fps, ct, nil
}

// RewrapSealedEnvelope replaces the ciphertext and fingerprints of a stored
// sealed envelope and keeps everything else, its origin included (peer
// re-wrap, spec 2026-11-10-03): a blob re-sealed by an agent stays a browser
// blob when the browser sealed it.
func RewrapSealedEnvelope(envelope string, fingerprints []string, ciphertext string) (string, error) {
	env, err := parseSealedEnvelope(envelope)
	if err != nil {
		return "", err
	}

	sorted := append([]string(nil), fingerprints...)
	sort.Strings(sorted)

	env.Fingerprints = sorted
	env.CT = ciphertext

	out, _ := json.Marshal(env) //nolint:errchkjson // plain strings: cannot fail

	return string(out), nil
}

// SealedOrigin returns who sealed a stored envelope: SealOriginBrowser,
// SealOriginServer (also for a legacy blob without the field), or "" when the
// envelope is not a sealed blob.
func SealedOrigin(envelope string) string {
	env, err := parseSealedEnvelope(envelope)
	if err != nil {
		return ""
	}

	if env.Origin == SealOriginBrowser {
		return SealOriginBrowser
	}

	return SealOriginServer
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

// NeedsReseal reports whether a sealed blob's recipient set differs from the
// currently-active one, in either direction:
//
//   - an active agent is MISSING from the blob (it joined the region after the
//     write and cannot decrypt it — availability problem), or
//   - the blob lists a recipient that is NO LONGER active (a revoked agent
//     could still decrypt this ciphertext — hygiene problem; the honest caveat
//     stands: a revoked agent already saw past credentials, rotate them).
//
// Either way the fix is the same: re-save the check's credentials so a fresh
// blob is sealed to exactly the active set.
func NeedsReseal(envelope string, activeFingerprints []string) (bool, error) {
	sealed, err := SealedRecipientFingerprints(envelope)
	if err != nil {
		return false, err
	}

	if len(sealed) != len(activeFingerprints) {
		return true, nil
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
