// Package sysparamseal encrypts the secret system parameters at rest.
//
// It is a decorator around db.Service, installed once at startup, so that the
// ~40 readers of system parameters (systemconfig, handlers/system, jmap,
// opsnotify, …) keep calling GetSystemParameter / ListSystemParameters and get
// plaintext back, while the row in the database holds an envelope sealed with
// the system DEK (credentials.SystemSealer).
//
// On disk a sealed row is `{"value": {"$sp_sealed": "<envelope>"}}`; the
// envelope holds the JSON of the value as the raw service would have stored
// it (`{"value": …}`), so opening restores the row exactly.
//
// Like the rest of the credential envelope, this only protects against a read
// of the database (a dump, a backup, a replica credential).
package sysparamseal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SealedMarkerKey is the key of the inner object a sealed row stores.
const SealedMarkerKey = "$sp_sealed"

// Errors returned by the decorator.
var (
	// ErrSealedUnavailable marks a sealed system parameter this process cannot
	// open: no master key configured, or a different one. It fails the boot
	// (systemconfig lists every system parameter) rather than letting a reader
	// treat the row as missing and, for the JWT key, generate a new one.
	ErrSealedUnavailable = errors.New("encrypted system parameter cannot be opened")
	// ErrReservedKey refuses writes to the system DEK row through the API path:
	// overwriting or deleting it would make every sealed row unreadable.
	ErrReservedKey = errors.New("system parameter is reserved for the encryption key")
)

const (
	hintNoKey = "no master key is configured on this process: set SP_ENCRYPTION_MASTER_KEY " +
		"(or SP_ENCRYPTION_MASTER_KEY_FILE) to the key the database was sealed with"
	hintWrongKey = "check that SP_ENCRYPTION_MASTER_KEY (or SP_ENCRYPTION_MASTER_KEY_FILE) " +
		"is the key the database was sealed with"
)

// Sealer is the encryption the decorator delegates to
// (credentials.SystemSealer in production).
type Sealer interface {
	Enabled() bool
	Seal(ctx context.Context, plaintext map[string]any) (string, error)
	Open(ctx context.Context, envelope string) (map[string]any, error)
}

// Predicate decides whether a system parameter is sealed on write.
type Predicate func(key string, secret bool) bool

// Service decorates a db.Service: every method passes through except the
// system-parameter ones.
type Service struct {
	db.Service

	sealer     Sealer
	shouldSeal Predicate
}

// Wrap installs the decorator. sealer may be disabled (no master key): writes
// then stay plaintext, as before, and sealed rows fail to open with an error
// naming the fix.
func Wrap(inner db.Service, sealer Sealer, shouldSeal Predicate) *Service {
	if shouldSeal == nil {
		shouldSeal = func(_ string, secret bool) bool { return secret }
	}

	return &Service{Service: inner, sealer: sealer, shouldSeal: shouldSeal}
}

// Raw returns the undecorated service.
func (s *Service) Raw() db.Service {
	return s.Service
}

// Enabled reports whether secret writes are sealed.
func (s *Service) Enabled() bool {
	return s.sealer != nil && s.sealer.Enabled()
}

// GetSystemParameter returns the parameter with its value opened.
func (s *Service) GetSystemParameter(ctx context.Context, key string) (*models.Parameter, error) {
	param, err := s.Service.GetSystemParameter(ctx, key)
	if err != nil || param == nil {
		return param, err
	}

	return s.open(ctx, param)
}

// ListSystemParameters returns every system parameter with its value opened.
func (s *Service) ListSystemParameters(ctx context.Context) ([]*models.Parameter, error) {
	params, err := s.Service.ListSystemParameters(ctx)
	if err != nil {
		return nil, err
	}

	for i, param := range params {
		opened, openErr := s.open(ctx, param)
		if openErr != nil {
			return nil, openErr
		}

		params[i] = opened
	}

	return params, nil
}

// SetSystemParameter seals the value when the predicate asks for it and a
// master key is configured, then writes it.
func (s *Service) SetSystemParameter(ctx context.Context, key string, value any, secret bool) error {
	if key == credentials.SystemDEKParameterKey {
		return fmt.Errorf("%w: %s", ErrReservedKey, key)
	}

	stored, err := s.sealValue(ctx, key, value, secret)
	if err != nil {
		return err
	}

	return s.Service.SetSystemParameter(ctx, key, stored, secret)
}

// GetOrCreateSystemParameter seals the value to create like SetSystemParameter
// and opens whichever row wins.
func (s *Service) GetOrCreateSystemParameter(
	ctx context.Context, key string, value any, secret bool,
) (*models.Parameter, bool, error) {
	if key == credentials.SystemDEKParameterKey {
		return nil, false, fmt.Errorf("%w: %s", ErrReservedKey, key)
	}

	stored, err := s.sealValue(ctx, key, value, secret)
	if err != nil {
		return nil, false, err
	}

	param, created, err := s.Service.GetOrCreateSystemParameter(ctx, key, stored, secret)
	if err != nil || param == nil {
		return param, created, err
	}

	opened, err := s.open(ctx, param)
	if err != nil {
		return nil, false, err
	}

	return opened, created, nil
}

// DeleteSystemParameter refuses the system DEK row and passes the rest through.
func (s *Service) DeleteSystemParameter(ctx context.Context, key string) error {
	if key == credentials.SystemDEKParameterKey {
		return fmt.Errorf("%w: %s", ErrReservedKey, key)
	}

	return s.Service.DeleteSystemParameter(ctx, key)
}

// SealPlaintextRows seals every stored secret system parameter still in
// plaintext. Idempotent; a no-op without a master key. Returns how many rows
// it sealed.
func (s *Service) SealPlaintextRows(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}

	params, err := s.Service.ListSystemParameters(ctx)
	if err != nil {
		return 0, fmt.Errorf("list system parameters: %w", err)
	}

	sealed := 0

	for _, param := range params {
		if !s.needsSealing(param) {
			continue
		}

		envelope, sealErr := s.sealer.Seal(ctx, map[string]any(param.Value))
		if sealErr != nil {
			return sealed, fmt.Errorf("seal system parameter %q: %w", param.Key, sealErr)
		}

		if setErr := s.Service.SetSystemParameter(
			ctx, param.Key, map[string]any{SealedMarkerKey: envelope}, isSecret(param),
		); setErr != nil {
			return sealed, fmt.Errorf("store sealed system parameter %q: %w", param.Key, setErr)
		}

		sealed++
	}

	return sealed, nil
}

// PlaintextSecretKeys lists the stored system parameters the predicate marks
// secret that are not sealed, for the startup warning.
func (s *Service) PlaintextSecretKeys(ctx context.Context) ([]string, error) {
	params, err := s.Service.ListSystemParameters(ctx)
	if err != nil {
		return nil, fmt.Errorf("list system parameters: %w", err)
	}

	var keys []string

	for _, param := range params {
		if s.needsSealing(param) {
			keys = append(keys, param.Key)
		}
	}

	return keys, nil
}

func (s *Service) needsSealing(param *models.Parameter) bool {
	if param == nil || param.Key == credentials.SystemDEKParameterKey {
		return false
	}

	if _, isSealed := SealedEnvelope(param.Value); isSealed {
		return false
	}

	return s.shouldSeal(param.Key, isSecret(param))
}

// sealValue returns what to hand the raw service: the sealed marker object, or
// value untouched when it is not to be sealed (or no master key is set).
func (s *Service) sealValue(ctx context.Context, key string, value any, secret bool) (any, error) {
	if !s.Enabled() || !s.shouldSeal(key, secret) {
		return value, nil
	}

	envelope, err := s.sealer.Seal(ctx, models.ParameterValue(value))
	if err != nil {
		return nil, fmt.Errorf("seal system parameter %q: %w", key, err)
	}

	return map[string]any{SealedMarkerKey: envelope}, nil
}

// open replaces a sealed value with its plaintext; plaintext rows (the
// transition, or no master key) are returned unchanged.
func (s *Service) open(ctx context.Context, param *models.Parameter) (*models.Parameter, error) {
	envelope, isSealed := SealedEnvelope(param.Value)
	if !isSealed {
		return param, nil
	}

	if !s.Enabled() {
		return nil, fmt.Errorf("%w: %q: %s", ErrSealedUnavailable, param.Key, hintNoKey)
	}

	plain, err := s.sealer.Open(ctx, envelope)
	if err != nil {
		slog.ErrorContext(ctx, "cannot open encrypted system parameter", "key", param.Key, "error", err)

		return nil, fmt.Errorf("%w: %q: %s: %w", ErrSealedUnavailable, param.Key, hintWrongKey, err)
	}

	opened := *param
	opened.Value = models.JSONMap(plain)

	return &opened, nil
}

// SealedEnvelope returns the envelope of a sealed parameter value.
func SealedEnvelope(value models.JSONMap) (string, bool) {
	inner, ok := value[models.ParameterValueKey].(map[string]any)
	if !ok {
		if typed, typedOK := value[models.ParameterValueKey].(models.JSONMap); typedOK {
			inner = typed
		} else {
			return "", false
		}
	}

	envelope, ok := inner[SealedMarkerKey].(string)

	return envelope, ok && envelope != ""
}

func isSecret(param *models.Parameter) bool {
	return param.Secret != nil && *param.Secret
}
