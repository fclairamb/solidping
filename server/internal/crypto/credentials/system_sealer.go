package credentials

// The system data key: one random DEK, wrapped by the KEK exactly like the
// per-org ones, that seals the secret SYSTEM parameters (rows of `parameters`
// with organization_uid IS NULL: the JWT signing key, the SSO client secrets,
// the SMTP and LDAP passwords, …). It reuses the service, the envelope and its
// versioning below unchanged: the system scope is just one more DEK owner, keyed
// by a pseudo org UID that no organization can carry.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SystemDEKParameterKey is the system parameter holding the KEK-wrapped system
// DEK. It is written with secret=true and never sealed itself.
const SystemDEKParameterKey = "encryption.system_dek"

// systemScope is the DEK-cache key of the system DEK. It is not a UID, so it
// can never collide with an organization's entry.
const systemScope = "system"

// ErrSystemDEKMissing reports a sealed system parameter whose data key row is
// gone. Opening never generates a new key: a fresh one could not open anything
// already sealed.
var ErrSystemDEKMissing = errors.New("system data key (" + SystemDEKParameterKey + ") is missing")

// SystemParameterDB is the slice of db.Service the system DEK store needs. It
// must be the RAW service, not the sealing decorator, or loading the key would
// recurse into the decorator.
type SystemParameterDB interface {
	GetSystemParameter(ctx context.Context, key string) (*models.Parameter, error)
	GetOrCreateSystemParameter(
		ctx context.Context, key string, value any, secret bool,
	) (*models.Parameter, bool, error)
}

// SystemDEKStore implements DEKStore over the system parameter
// SystemDEKParameterKey. The org UID argument is ignored: there is one key.
type SystemDEKStore struct {
	db SystemParameterDB
}

// NewSystemDEKStore builds the system DEK store over the raw database service.
func NewSystemDEKStore(database SystemParameterDB) SystemDEKStore {
	return SystemDEKStore{db: database}
}

// LoadDEK reads the wrapped system DEK envelope.
func (s SystemDEKStore) LoadDEK(ctx context.Context, _ string) ([]byte, bool, error) {
	param, err := s.db.GetSystemParameter(ctx, SystemDEKParameterKey)
	if err != nil {
		return nil, false, fmt.Errorf("get system dek: %w", err)
	}

	if param == nil {
		return nil, false, nil
	}

	raw, err := json.Marshal(param.Value)
	if err != nil {
		return nil, false, fmt.Errorf("marshal system dek param value: %w", err)
	}

	envelope, err := unwrapDEKParameterValue(raw)
	if err != nil {
		return nil, false, err
	}

	return envelope, true, nil
}

// SaveDEK persists the wrapped system DEK. It creates the row atomically and
// never overwrites one: several replicas booting together on an empty database
// must all end up on the SAME key. A replica that lost the race sees its own
// key differ on the reload (ErrDEKRoundTrip) and SystemSealer retries once,
// picking up the winner's key.
func (s SystemDEKStore) SaveDEK(ctx context.Context, _ string, wrapped []byte) error {
	if _, _, err := s.db.GetOrCreateSystemParameter(ctx, SystemDEKParameterKey, string(wrapped), true); err != nil {
		return fmt.Errorf("set system dek: %w", err)
	}

	return nil
}

// SystemSealer seals and opens system parameter values with the system DEK.
type SystemSealer struct {
	svc *service
}

// NewSystemSealer builds the sealer. With an empty masterKey it is disabled:
// Seal and Open return ErrDisabled.
func NewSystemSealer(masterKey []byte, database SystemParameterDB) (*SystemSealer, error) {
	svc, err := newService(masterKey, NewSystemDEKStore(database))
	if err != nil {
		return nil, err
	}

	return &SystemSealer{svc: svc}, nil
}

// Enabled reports whether a master key is configured.
func (s *SystemSealer) Enabled() bool {
	return s != nil && s.svc.Enabled()
}

// Seal encrypts plaintext under the system DEK, creating that key on first use.
func (s *SystemSealer) Seal(ctx context.Context, plaintext map[string]any) (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}

	if err := s.svc.EnsureOrgKey(ctx, systemScope); err != nil {
		if !errors.Is(err, ErrDEKRoundTrip) {
			return "", err
		}

		// Lost the creation race to another replica: nothing was cached, so
		// this second call loads the winner's key.
		if err := s.svc.EnsureOrgKey(ctx, systemScope); err != nil {
			return "", err
		}
	}

	return s.svc.EncryptForOrg(ctx, systemScope, plaintext)
}

// Open decrypts an envelope produced by Seal. It loads the system DEK but
// never generates one.
func (s *SystemSealer) Open(ctx context.Context, envelope string) (map[string]any, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}

	if _, cached := s.svc.dekCache.Load(systemScope); !cached {
		if err := s.svc.reloadOrgKey(ctx, systemScope); err != nil {
			if _, found, loadErr := s.svc.store.LoadDEK(ctx, systemScope); loadErr == nil && !found {
				return nil, ErrSystemDEKMissing
			}

			return nil, err
		}
	}

	return s.svc.DecryptForOrg(ctx, systemScope, envelope)
}
