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
		return fmt.Errorf("load org DEK: %w", err)
	}

	if found {
		dek, dekErr := s.decryptWith(s.kek, wrapped)
		if dekErr != nil {
			return fmt.Errorf("unwrap org DEK: %w", dekErr)
		}

		s.dekCache.Store(orgUID, dek)

		return nil
	}

	dek := make([]byte, 32)
	if _, randErr := io.ReadFull(rand.Reader, dek); randErr != nil {
		return fmt.Errorf("generate org DEK: %w", randErr)
	}

	wrappedEnvelope, err := s.encryptWith(s.kek, dek)
	if err != nil {
		return fmt.Errorf("wrap new org DEK: %w", err)
	}

	if err := s.store.SaveDEK(ctx, orgUID, wrappedEnvelope); err != nil {
		return fmt.Errorf("persist new org DEK: %w", err)
	}

	s.dekCache.Store(orgUID, dek)

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
func (s *service) DecryptForOrg(ctx context.Context, orgUID string, envelope string) (map[string]any, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}

	if err := s.EnsureOrgKey(ctx, orgUID); err != nil {
		return nil, err
	}

	dekRaw, ok := s.dekCache.Load(orgUID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrDEKNotLoaded, orgUID)
	}

	dek, dekOk := dekRaw.([]byte)
	if !dekOk {
		return nil, ErrDEKBadType
	}

	plain, err := s.decryptWith(dek, []byte(envelope))
	if err != nil {
		return nil, err
	}

	out := map[string]any{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("unmarshal plaintext: %w", err)
	}

	return out, nil
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
