// Package credentials implements envelope encryption for sensitive
// configuration values stored at rest. The KEK comes from outside the
// database (env var or mounted file); per-org DEKs are wrapped with the KEK
// and persisted as JSONB in the parameters table. AES-256-GCM is used for
// both layers; nonces are random per call.
//
// This package only protects against database theft. It does not defend
// against a compromised server process, a malicious admin, or any
// authenticated user with API access — those threat models are out of
// scope.
package credentials

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Envelope format identifiers. Keep the version field so future rotations
// can ship a new format alongside the old one.
const (
	envelopeVersion = 1
	envelopeAlg     = "AES-256-GCM"
)

// Errors returned by the credentials service.
var (
	ErrDisabled         = errors.New("credentials encryption is disabled (no master key configured)")
	ErrInvalidKey       = errors.New("master key must decode to exactly 32 bytes")
	ErrUnknownAlgorithm = errors.New("unknown envelope algorithm")
	ErrUnknownVersion   = errors.New("unknown envelope version")
	ErrDEKNotLoaded     = errors.New("org DEK not loaded after ensure")
	ErrDEKBadType       = errors.New("org DEK has unexpected type in cache")
	// ErrOrgKeyUnavailable marks every failure to obtain the ORG's key, as
	// opposed to a failure to open one particular envelope with a key that is
	// fine. Callers use it to tell an operator problem (wrong master key on
	// this process, unreadable DEK row) apart from a per-check problem, so the
	// user-facing advice stops being "re-save the check's credentials" when
	// re-saving cannot possibly help.
	ErrOrgKeyUnavailable = errors.New("organization encryption key could not be opened")
	// ErrDEKRoundTrip marks a freshly generated DEK that did not survive a
	// reload through the store. It means the storage shape changed under us;
	// failing here keeps the damage to one request instead of poisoning every
	// other process for the lifetime of the row.
	ErrDEKRoundTrip = errors.New("newly stored org DEK did not survive a reload")
)

// envelopeJSON is the on-disk shape of an encrypted blob. The separation of
// nonce and ciphertext lets us spot-decrypt without reconstructing the GCM
// payload format ourselves.
type envelopeJSON struct {
	Version int    `json:"v"`
	Alg     string `json:"alg"`
	Nonce   string `json:"nonce"`
	CT      string `json:"ct"`
}

// DEKStore lets the service load and persist per-org DEKs. The store is
// passed in so callers can plug a real database without forcing this
// package to depend on db.Service (which would create an import cycle).
type DEKStore interface {
	// LoadDEK returns the wrapped DEK envelope for an org, or
	// (nil, false) if none exists.
	LoadDEK(ctx context.Context, orgUID string) ([]byte, bool, error)
	// SaveDEK writes the wrapped DEK envelope for an org. Implementations
	// must store this value as a secret (e.g., parameters.secret = true).
	SaveDEK(ctx context.Context, orgUID string, wrapped []byte) error
}

// Service is the public encryption API. Enabled() returns false when no
// master key is configured; in that case Encrypt/Decrypt return
// ErrDisabled and the caller is expected to fall back to plaintext storage
// (V1 behavior — explicitly documented).
type Service interface {
	Enabled() bool
	EncryptForOrg(ctx context.Context, orgUID string, plaintext map[string]any) (string, error)
	DecryptForOrg(ctx context.Context, orgUID string, envelope string) (map[string]any, error)
	EnsureOrgKey(ctx context.Context, orgUID string) error
	// DEKCacheLen reports how many per-org DEKs are cached in memory; surfaced
	// as a Prometheus gauge and in the memory snapshot for leak analysis.
	DEKCacheLen() int
}

// service implements Service.
type service struct {
	kek      []byte
	store    DEKStore
	dekCache sync.Map // map[orgUID][]byte
}

// NewService builds a credentials service. masterKey is the raw 32-byte KEK;
// callers decode it from base64 / file content and pass the bytes here.
// If masterKey is nil or empty the service operates in disabled mode.
func NewService(masterKey []byte, store DEKStore) (Service, error) {
	if len(masterKey) == 0 {
		return &service{kek: nil, store: store}, nil
	}

	if len(masterKey) != 32 {
		return nil, ErrInvalidKey
	}

	keyCopy := make([]byte, len(masterKey))
	copy(keyCopy, masterKey)

	return &service{kek: keyCopy, store: store}, nil
}

// DecodeMasterKey parses a base64 string into the 32-byte raw key. Returns
// ErrInvalidKey if the decoded length is wrong. Helper for the config
// loader.
func DecodeMasterKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, ErrDisabled
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode master key: %w", err)
		}
	}

	if len(raw) != 32 {
		return nil, ErrInvalidKey
	}

	return raw, nil
}

func (s *service) Enabled() bool {
	return s.kek != nil
}

// DEKCacheLen returns the number of per-org DEKs currently cached. The cache is
// an unbounded sync.Map that grows O(orgs) for the process lifetime and is never
// evicted, so this count is the memory-analysis signal for the DEK-cache
// hypothesis. O(n) range over the map; only called at metrics scrape time.
func (s *service) DEKCacheLen() int {
	count := 0
	s.dekCache.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// EnsureOrgKey loads (or generates and persists) the per-org DEK. Cached on
// success.
func (s *service) EnsureOrgKey(ctx context.Context, orgUID string) error {
	if !s.Enabled() {
		return ErrDisabled
	}

	if _, ok := s.dekCache.Load(orgUID); ok {
		return nil
	}

	wrapped, found, err := s.store.LoadDEK(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("%w: load org DEK: %w", ErrOrgKeyUnavailable, err)
	}

	if found {
		dek, dekErr := s.decryptWith(s.kek, wrapped)
		if dekErr != nil {
			return fmt.Errorf("%w: unwrap org DEK: %w", ErrOrgKeyUnavailable, dekErr)
		}

		s.dekCache.Store(orgUID, dek)

		return nil
	}

	dek := make([]byte, 32)
	if _, randErr := io.ReadFull(rand.Reader, dek); randErr != nil {
		return fmt.Errorf("%w: generate org DEK: %w", ErrOrgKeyUnavailable, randErr)
	}

	wrappedEnvelope, err := s.encryptWith(s.kek, dek)
	if err != nil {
		return fmt.Errorf("%w: wrap new org DEK: %w", ErrOrgKeyUnavailable, err)
	}

	if err := s.store.SaveDEK(ctx, orgUID, wrappedEnvelope); err != nil {
		return fmt.Errorf("%w: persist new org DEK: %w", ErrOrgKeyUnavailable, err)
	}

	if err := s.verifyStoredDEK(ctx, orgUID, dek); err != nil {
		return err
	}

	s.dekCache.Store(orgUID, dek)

	return nil
}

// verifyStoredDEK reloads a just-written DEK through the store and unwraps it
// with the KEK, before anything is cached.
//
// Without this the in-memory cache hides a storage-shape bug perfectly: the
// process that generated the key keeps encrypting under the cached bytes and
// sees nothing wrong, while every other process — and this one after its next
// restart — cannot open the row. That is precisely how a write/read mismatch
// stayed invisible for months. A round-trip failure must therefore break the
// FIRST encrypt, loudly, rather than silently arm a cross-process outage.
func (s *service) verifyStoredDEK(ctx context.Context, orgUID string, dek []byte) error {
	stored, found, err := s.store.LoadDEK(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("%w: %w: reload: %w", ErrOrgKeyUnavailable, ErrDEKRoundTrip, err)
	}

	if !found {
		return fmt.Errorf("%w: %w: not found after save", ErrOrgKeyUnavailable, ErrDEKRoundTrip)
	}

	reloaded, err := s.decryptWith(s.kek, stored)
	if err != nil {
		return fmt.Errorf("%w: %w: unwrap org DEK: %w", ErrOrgKeyUnavailable, ErrDEKRoundTrip, err)
	}

	if !bytes.Equal(reloaded, dek) {
		return fmt.Errorf("%w: %w: reloaded key differs", ErrOrgKeyUnavailable, ErrDEKRoundTrip)
	}

	return nil
}

// EncryptForOrg encrypts a JSON-marshalable map under the org's DEK. The
// returned string is the JSON envelope ready to persist in a TEXT column.
func (s *service) EncryptForOrg(ctx context.Context, orgUID string, plaintext map[string]any) (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}

	if err := s.EnsureOrgKey(ctx, orgUID); err != nil {
		return "", err
	}

	dekRaw, ok := s.dekCache.Load(orgUID)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrDEKNotLoaded, orgUID)
	}

	dek, dekOk := dekRaw.([]byte)
	if !dekOk {
		return "", ErrDEKBadType
	}

	body, err := json.Marshal(plaintext)
	if err != nil {
		return "", fmt.Errorf("marshal plaintext: %w", err)
	}

	envelope, err := s.encryptWith(dek, body)
	if err != nil {
		return "", err
	}

	return string(envelope), nil
}

// DecryptForOrg unwraps a JSON envelope back to the original map.
//
// A v3 *plaintext* envelope (the no-master-key structural-separation fallback)
// is opened up-front, before the enabled gate: it carries no ciphertext and
// needs no key, so it must reconstitute even on a disabled service. This is the
// single centralized seam, so every DecryptForOrg caller (notifications,
// kubernetes, re-seal, PATCH loaders, credmigrate) transparently handles the
// plaintext envelope with no per-site special-casing.
func (s *service) DecryptForOrg(ctx context.Context, orgUID string, envelope string) (map[string]any, error) {
	if IsPlaintextEnvelope(envelope) {
		return OpenPlaintext(envelope)
	}

	if !s.Enabled() {
		return nil, ErrDisabled
	}

	plain, usedCachedDEK, err := s.openWithOrgKey(ctx, orgUID, envelope)
	if err != nil && usedCachedDEK {
		// The cached DEK is the only thing we can be wrong about here that a
		// reload can fix: the row may have been rewritten (rotation, repair)
		// since this process cached it. Drop that ONE entry and cold-reload
		// EXACTLY once — no loop, no cache-wide flush. The absence of any
		// invalidation is why the last bad DEK write was permanent and
		// invisible to the process that made it.
		s.dekCache.Delete(orgUID)

		// Reload, never regenerate: if the row has gone missing, minting a new
		// DEK here would make every already-encrypted secret for this org
		// permanently unopenable. A failed reload keeps the original error.
		if reloadErr := s.reloadOrgKey(ctx, orgUID); reloadErr == nil {
			plain, _, err = s.openWithOrgKey(ctx, orgUID, envelope)
		}
	}

	if err != nil {
		return nil, err
	}

	out := map[string]any{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("unmarshal plaintext: %w", err)
	}

	return out, nil
}

// reloadOrgKey re-reads the org DEK from the store and caches it. Unlike
// EnsureOrgKey it NEVER generates a key: it exists for the invalidation retry,
// where an absent row means "something is wrong", not "this org has no key
// yet". Generating there would orphan every secret already encrypted for the
// org.
func (s *service) reloadOrgKey(ctx context.Context, orgUID string) error {
	wrapped, found, err := s.store.LoadDEK(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("%w: load org DEK: %w", ErrOrgKeyUnavailable, err)
	}

	if !found {
		return fmt.Errorf("%w: org DEK row is gone", ErrOrgKeyUnavailable)
	}

	dek, err := s.decryptWith(s.kek, wrapped)
	if err != nil {
		return fmt.Errorf("%w: unwrap org DEK: %w", ErrOrgKeyUnavailable, err)
	}

	s.dekCache.Store(orgUID, dek)

	return nil
}

// openWithOrgKey ensures the org DEK is available and opens one envelope with
// it. The bool reports whether the failure happened while USING the key (as
// opposed to while obtaining it), which is the only case where dropping the
// cache entry and reloading could change the outcome.
func (s *service) openWithOrgKey(ctx context.Context, orgUID, envelope string) ([]byte, bool, error) {
	if err := s.EnsureOrgKey(ctx, orgUID); err != nil {
		return nil, false, err
	}

	dekRaw, ok := s.dekCache.Load(orgUID)
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", ErrDEKNotLoaded, orgUID)
	}

	dek, dekOk := dekRaw.([]byte)
	if !dekOk {
		return nil, false, ErrDEKBadType
	}

	plain, err := s.decryptWith(dek, []byte(envelope))
	if err != nil {
		return nil, true, err
	}

	return plain, false, nil
}

func (s *service) encryptWith(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	cipherText := gcm.Seal(nil, nonce, plaintext, nil)

	envelope := envelopeJSON{
		Version: envelopeVersion,
		Alg:     envelopeAlg,
		Nonce:   base64.StdEncoding.EncodeToString(nonce),
		CT:      base64.StdEncoding.EncodeToString(cipherText),
	}

	return json.Marshal(envelope)
}

func (s *service) decryptWith(key, raw []byte) ([]byte, error) {
	var env envelopeJSON
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse envelope: %w", err)
	}

	if env.Version != envelopeVersion {
		return nil, ErrUnknownVersion
	}

	if env.Alg != envelopeAlg {
		return nil, ErrUnknownAlgorithm
	}

	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}

	cipherText, err := base64.StdEncoding.DecodeString(env.CT)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}

	plain, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}

	return plain, nil
}
