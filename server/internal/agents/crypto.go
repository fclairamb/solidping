// Package agents implements the server-side identity, enrollment, and
// signature-authentication primitives for deported (org-scoped) check agents.
//
// A deported agent holds two keypairs it generates itself and never shares the
// private halves of:
//
//   - Ed25519 for identity — every reconnect is authenticated by an Ed25519
//     signature over method|path|timestamp|nonce, so the server never stores a
//     replayable bearer credential for an agent, only its public key.
//   - X25519 (age) for credential decryption — see internal/crypto/credentials.
//
// Enrollment is one-shot: an admin mints a token (shown once), the agent presents
// it on its first connection, and the server atomically consumes it while
// creating the agent row.
package agents

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// EnrollmentTokenPrefix is prepended to a minted enrollment token so it is
// recognizable in logs and pasteable config. The token is shown exactly once at
// creation; only its SHA-256 hash is ever stored.
const EnrollmentTokenPrefix = "spe_"

// enrollmentTokenBytes is the number of random bytes in an enrollment token.
const enrollmentTokenBytes = 32

// DefaultClockSkew is the accepted +/- clock skew for reconnect signatures.
const DefaultClockSkew = 5 * time.Minute

// Errors returned by the agent crypto/auth primitives.
var (
	ErrBadSignature     = errors.New("agent signature verification failed")
	ErrClockSkew        = errors.New("agent request timestamp outside accepted clock skew")
	ErrInvalidPublicKey = errors.New("invalid ed25519 public key")
	ErrInvalidTimestamp = errors.New("invalid timestamp")
)

// GenerateEnrollmentToken returns a fresh one-shot enrollment token (with the
// spe_ prefix) and its SHA-256 hash, in that order. Only the hash is persisted;
// the token is displayed once and never stored.
func GenerateEnrollmentToken() (string, string, error) {
	buf := make([]byte, enrollmentTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate enrollment token: %w", err)
	}

	token := EnrollmentTokenPrefix + hex.EncodeToString(buf)

	return token, HashEnrollmentToken(token), nil
}

// HashEnrollmentToken returns the SHA-256 hex digest of an enrollment token. The
// server stores and looks up tokens exclusively by this hash.
func HashEnrollmentToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

// KeyFingerprint returns a short, stable, non-secret fingerprint of a
// base64-encoded Ed25519 public key, shown in UI/logs to identify an agent.
func KeyFingerprint(publicKeyB64 string) string {
	sum := sha256.Sum256([]byte(publicKeyB64))

	return hex.EncodeToString(sum[:8])
}

// SignatureChallenge builds the canonical message an agent signs on reconnect:
// the HTTP method, request path, timestamp, and nonce joined by '|'. Both the
// agent (signing) and the server (verifying) must build it identically.
func SignatureChallenge(method, path, timestamp, nonce string) []byte {
	return []byte(method + "|" + path + "|" + timestamp + "|" + nonce)
}

// VerifySignature checks an Ed25519 signature over the reconnect challenge. Both
// the public key and the signature are base64 (standard encoding).
func VerifySignature(publicKeyB64, method, path, timestamp, nonce, signatureB64 string) error {
	pub, err := ParsePublicKey(publicKeyB64)
	if err != nil {
		return err
	}

	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("%w: decode signature: %w", ErrBadSignature, err)
	}

	if !ed25519.Verify(pub, SignatureChallenge(method, path, timestamp, nonce), sig) {
		return ErrBadSignature
	}

	return nil
}

// ParsePublicKey decodes a base64 Ed25519 public key and validates its length.
func ParsePublicKey(publicKeyB64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrInvalidPublicKey, err)
	}

	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidPublicKey, len(raw), ed25519.PublicKeySize)
	}

	return ed25519.PublicKey(raw), nil
}

// ValidateTimestamp parses an RFC3339 timestamp and ensures it is within +/- skew
// of now.
func ValidateTimestamp(timestamp string, now time.Time, skew time.Duration) error {
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTimestamp, err)
	}

	delta := now.Sub(ts)
	if delta < 0 {
		delta = -delta
	}

	if delta > skew {
		return fmt.Errorf("%w: %s off", ErrClockSkew, delta)
	}

	return nil
}

// AgentKeys is the full keypair set an agent generates at first startup. The
// private halves stay on the agent (persisted to file or env); only the two
// public strings are ever sent to the server.
type AgentKeys struct {
	// Ed25519PublicKey is the base64 identity public key (auth signatures).
	Ed25519PublicKey string `json:"ed25519PublicKey"`
	// Ed25519PrivateKey is the base64 identity private key (agent-only).
	Ed25519PrivateKey string `json:"ed25519PrivateKey"`
	// X25519Recipient is the age recipient ("age1…") credentials are sealed to.
	X25519Recipient string `json:"x25519Recipient"`
	// X25519Identity is the age identity ("AGE-SECRET-KEY-1…", agent-only).
	X25519Identity string `json:"x25519Identity"`
}

// GenerateAgentKeys generates a fresh Ed25519 identity keypair and an X25519
// (age) encryption keypair for a new agent. Used agent-side at enrollment.
func GenerateAgentKeys() (*AgentKeys, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}

	identity, recipient, err := credentials.GenerateX25519Keypair()
	if err != nil {
		return nil, err
	}

	return &AgentKeys{
		Ed25519PublicKey:  base64.StdEncoding.EncodeToString(pub),
		Ed25519PrivateKey: base64.StdEncoding.EncodeToString(priv),
		X25519Recipient:   recipient,
		X25519Identity:    identity,
	}, nil
}

// Sign produces the base64 Ed25519 signature over the reconnect challenge, using
// a base64 private key. Used agent-side.
func (k *AgentKeys) Sign(method, path, timestamp, nonce string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(k.Ed25519PrivateKey)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}

	if len(raw) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("%w: bad private key length", ErrBadSignature)
	}

	sig := ed25519.Sign(ed25519.PrivateKey(raw), SignatureChallenge(method, path, timestamp, nonce))

	return base64.StdEncoding.EncodeToString(sig), nil
}

// ConstantTimeHashEqual compares two token hashes without leaking timing.
func ConstantTimeHashEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// WorkerSlug derives the deterministic workers.slug an enrolled agent's worker
// row uses. It lives here (rather than in the WS handler) because the agent GC
// job must resolve the same slug to retire a dead machine's worker row.
func WorkerSlug(agentUID string) string {
	compact := strings.ReplaceAll(agentUID, "-", "")
	if len(compact) > 12 {
		compact = compact[:12]
	}

	return "ag-" + strings.ToLower(compact)
}
