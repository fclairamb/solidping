package sysparamseal

// Sealing of the private keys stored in app_settings (spec 2026-11-26-06).
//
// app_settings holds plain strings. A sealed value is the JSON string
// `{"$sp_sealed":"<envelope>"}`, the envelope holding `{"value": "<plaintext>"}`
// sealed with the system DEK, the same envelope as the sealed system
// parameters. Neither a PEM block nor a base64 key can start with `{`, so the
// two forms never collide.
//
// Only the keys of SecretAppSettingKeys are sealed: the SP certificate and the
// VAPID public key are public and stay in plaintext.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// App settings sealed with the system DEK when a master key is configured.
const (
	AppSettingSAMLSPPrivateKey    = "saml.sp_private_key_pem"
	AppSettingWebPushVAPIDPrivKey = "webpush.vapid_private_key"
)

// SecretAppSettingKeys lists the app settings sealed on write.
func SecretAppSettingKeys() []string {
	return []string{AppSettingSAMLSPPrivateKey, AppSettingWebPushVAPIDPrivKey}
}

// IsSecretAppSetting reports whether key is sealed on write.
func IsSecretAppSetting(key string) bool {
	return key == AppSettingSAMLSPPrivateKey || key == AppSettingWebPushVAPIDPrivKey
}

// SealedAppSettingEnvelope returns the envelope of a sealed app setting value.
func SealedAppSettingEnvelope(value string) (string, bool) {
	if !strings.HasPrefix(value, "{") {
		return "", false
	}

	var wrapper map[string]any
	if err := json.Unmarshal([]byte(value), &wrapper); err != nil {
		return "", false
	}

	envelope, ok := wrapper[SealedMarkerKey].(string)

	return envelope, ok && envelope != ""
}

// GetAppSetting returns the value with a sealed secret opened.
func (s *Service) GetAppSetting(ctx context.Context, key string) (string, error) {
	value, err := s.Service.GetAppSetting(ctx, key)
	if err != nil {
		return value, err
	}

	return s.openAppSetting(ctx, key, value)
}

// GetAppSettings returns the values with the sealed secrets opened.
func (s *Service) GetAppSettings(ctx context.Context, keys []string) (map[string]string, error) {
	values, err := s.Service.GetAppSettings(ctx, keys)
	if err != nil {
		return nil, err
	}

	for key, value := range values {
		opened, openErr := s.openAppSetting(ctx, key, value)
		if openErr != nil {
			return nil, openErr
		}

		values[key] = opened
	}

	return values, nil
}

// SetAppSetting seals a secret value before writing it.
func (s *Service) SetAppSetting(ctx context.Context, key, value string) error {
	stored, err := s.sealAppSetting(ctx, key, value)
	if err != nil {
		return err
	}

	return s.Service.SetAppSetting(ctx, key, stored)
}

// SetAppSettings seals the secret values before writing them in one statement.
func (s *Service) SetAppSettings(ctx context.Context, settings map[string]string) error {
	stored, err := s.sealAppSettings(ctx, settings)
	if err != nil {
		return err
	}

	return s.Service.SetAppSettings(ctx, stored)
}

// CreateAppSettingsIfAbsent seals the secret values before the insert.
func (s *Service) CreateAppSettingsIfAbsent(ctx context.Context, settings map[string]string) error {
	stored, err := s.sealAppSettings(ctx, settings)
	if err != nil {
		return err
	}

	return s.Service.CreateAppSettingsIfAbsent(ctx, stored)
}

// SealPlaintextAppSettings seals the secret app settings still stored in
// plaintext. The values do not change. Idempotent; a no-op without a master
// key. Returns how many rows it sealed.
func (s *Service) SealPlaintextAppSettings(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}

	values, err := s.Service.GetAppSettings(ctx, SecretAppSettingKeys())
	if err != nil {
		return 0, fmt.Errorf("read secret app settings: %w", err)
	}

	sealed := 0

	for _, key := range SecretAppSettingKeys() {
		value, found := values[key]
		if !found || value == "" {
			continue
		}

		if _, isSealed := SealedAppSettingEnvelope(value); isSealed {
			continue
		}

		stored, sealErr := s.sealAppSetting(ctx, key, value)
		if sealErr != nil {
			return sealed, sealErr
		}

		if setErr := s.Service.SetAppSetting(ctx, key, stored); setErr != nil {
			return sealed, fmt.Errorf("store sealed app setting %q: %w", key, setErr)
		}

		sealed++
	}

	return sealed, nil
}

// PlaintextSecretAppSettingKeys lists the secret app settings stored in
// plaintext, for the startup warning.
func (s *Service) PlaintextSecretAppSettingKeys(ctx context.Context) ([]string, error) {
	values, err := s.Service.GetAppSettings(ctx, SecretAppSettingKeys())
	if err != nil {
		return nil, fmt.Errorf("read secret app settings: %w", err)
	}

	var keys []string

	for _, key := range SecretAppSettingKeys() {
		value, found := values[key]
		if !found || value == "" {
			continue
		}

		if _, isSealed := SealedAppSettingEnvelope(value); !isSealed {
			keys = append(keys, key)
		}
	}

	return keys, nil
}

func (s *Service) sealAppSettings(ctx context.Context, settings map[string]string) (map[string]string, error) {
	stored := make(map[string]string, len(settings))

	for key, value := range settings {
		sealed, err := s.sealAppSetting(ctx, key, value)
		if err != nil {
			return nil, err
		}

		stored[key] = sealed
	}

	return stored, nil
}

// sealAppSetting returns what to hand the raw service: the sealed wrapper, or
// value untouched when the key is not secret (or no master key is set).
func (s *Service) sealAppSetting(ctx context.Context, key, value string) (string, error) {
	if !s.Enabled() || !IsSecretAppSetting(key) || value == "" {
		return value, nil
	}

	envelope, err := s.sealer.Seal(ctx, models.ParameterValue(value))
	if err != nil {
		return "", fmt.Errorf("seal app setting %q: %w", key, err)
	}

	wrapper, err := json.Marshal(map[string]string{SealedMarkerKey: envelope})
	if err != nil {
		return "", fmt.Errorf("marshal sealed app setting %q: %w", key, err)
	}

	return string(wrapper), nil
}

// openAppSetting replaces a sealed value with its plaintext. A sealed value
// that cannot be opened is an ErrSealedUnavailable error, never "missing": a
// caller treating it as missing would generate a new key over it.
func (s *Service) openAppSetting(ctx context.Context, key, value string) (string, error) {
	envelope, isSealed := SealedAppSettingEnvelope(value)
	if !isSealed {
		return value, nil
	}

	if !s.Enabled() {
		return "", fmt.Errorf("%w: app setting %q: %s", ErrSealedUnavailable, key, hintNoKey)
	}

	plain, err := s.sealer.Open(ctx, envelope)
	if err != nil {
		slog.ErrorContext(ctx, "cannot open encrypted app setting", "key", key, "error", err)

		return "", fmt.Errorf("%w: app setting %q: %s: %w", ErrSealedUnavailable, key, hintWrongKey, err)
	}

	text, ok := plain[models.ParameterValueKey].(string)
	if !ok {
		return "", fmt.Errorf("%w: app setting %q: sealed value is not a string", ErrSealedUnavailable, key)
	}

	return text, nil
}
