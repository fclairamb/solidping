package sysparamseal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// The app_settings keys that hold a private key (spec 2026-11-26-06). They are
// sealed with the system DEK like the secret system parameters. The matching
// public halves (SP certificate, VAPID public key) stay in plaintext.
const (
	// AppSettingSAMLSPPrivateKey is the SAML SP private key (PEM).
	AppSettingSAMLSPPrivateKey = "saml.sp_private_key_pem"
	// AppSettingVAPIDPrivateKey is the Web Push VAPID private key.
	AppSettingVAPIDPrivateKey = "webpush.vapid_private_key"
)

// secretAppSettingKeys lists the sealed app_settings keys, sorted.
func secretAppSettingKeys() []string {
	return []string{AppSettingSAMLSPPrivateKey, AppSettingVAPIDPrivateKey}
}

// IsSecretAppSetting reports whether the app_settings key is sealed at rest.
func IsSecretAppSetting(key string) bool {
	return key == AppSettingSAMLSPPrivateKey || key == AppSettingVAPIDPrivateKey
}

// GetAppSetting returns the setting with its value opened.
func (s *Service) GetAppSetting(ctx context.Context, key string) (string, error) {
	value, err := s.Service.GetAppSetting(ctx, key)
	if err != nil {
		return value, err
	}

	return s.openAppSetting(ctx, key, value)
}

// GetAppSettings reads the settings in one statement and opens the sealed ones.
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

// SetAppSetting seals a secret setting when a master key is set, then writes it.
func (s *Service) SetAppSetting(ctx context.Context, key, value string) error {
	stored, err := s.sealAppSetting(ctx, key, value)
	if err != nil {
		return err
	}

	return s.Service.SetAppSetting(ctx, key, stored)
}

// CreateAppSettingsIfAbsent seals the secret settings, then inserts the pairs
// in one statement like the raw service.
func (s *Service) CreateAppSettingsIfAbsent(ctx context.Context, settings map[string]string) error {
	stored, err := s.sealAppSettings(ctx, settings)
	if err != nil {
		return err
	}

	return s.Service.CreateAppSettingsIfAbsent(ctx, stored)
}

// SetAppSettings seals the secret settings, then upserts the pairs in one
// statement like the raw service.
func (s *Service) SetAppSettings(ctx context.Context, settings map[string]string) error {
	stored, err := s.sealAppSettings(ctx, settings)
	if err != nil {
		return err
	}

	return s.Service.SetAppSettings(ctx, stored)
}

// SealPlaintextAppSettings seals the secret app settings still stored in
// plaintext. The value does not change. Idempotent; a no-op without a master
// key. Returns how many rows it sealed.
func (s *Service) SealPlaintextAppSettings(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}

	keys, values, err := s.plaintextAppSettings(ctx)
	if err != nil {
		return 0, err
	}

	sealed := 0

	for _, key := range keys {
		stored, sealErr := s.sealAppSetting(ctx, key, values[key])
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

// PlaintextAppSettingKeys lists the secret app settings stored in plaintext,
// for the startup warning.
func (s *Service) PlaintextAppSettingKeys(ctx context.Context) ([]string, error) {
	keys, _, err := s.plaintextAppSettings(ctx)

	return keys, err
}

func (s *Service) plaintextAppSettings(ctx context.Context) ([]string, map[string]string, error) {
	values, err := s.Service.GetAppSettings(ctx, secretAppSettingKeys())
	if err != nil {
		return nil, nil, fmt.Errorf("read secret app settings: %w", err)
	}

	var keys []string

	for key, value := range values {
		if _, isSealed := SealedAppSettingEnvelope(value); !isSealed && value != "" {
			keys = append(keys, key)
		}
	}

	sort.Strings(keys)

	return keys, values, nil
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

// sealAppSetting returns what to hand the raw service: the sealed marker
// object as JSON, or value untouched when the key is not secret, the value is
// empty, or no master key is set.
func (s *Service) sealAppSetting(ctx context.Context, key, value string) (string, error) {
	if !s.Enabled() || !IsSecretAppSetting(key) || value == "" {
		return value, nil
	}

	if _, isSealed := SealedAppSettingEnvelope(value); isSealed {
		return value, nil
	}

	envelope, err := s.sealer.Seal(ctx, map[string]any{"value": value})
	if err != nil {
		return "", fmt.Errorf("seal app setting %q: %w", key, err)
	}

	encoded, err := json.Marshal(map[string]string{SealedMarkerKey: envelope})
	if err != nil {
		return "", fmt.Errorf("encode sealed app setting %q: %w", key, err)
	}

	return string(encoded), nil
}

// openAppSetting replaces a sealed value with its plaintext. A sealed value
// this process cannot open is ErrSealedUnavailable, never "missing": the
// callers would generate a new key pair over it.
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

	opened, ok := plain["value"].(string)
	if !ok {
		return "", fmt.Errorf("%w: app setting %q: unexpected sealed content", ErrSealedUnavailable, key)
	}

	return opened, nil
}

// SealedAppSettingEnvelope returns the envelope of a sealed app setting value
// (`{"$sp_sealed": "<envelope>"}`).
func SealedAppSettingEnvelope(value string) (string, bool) {
	if !strings.HasPrefix(strings.TrimSpace(value), "{") {
		return "", false
	}

	var marker map[string]string
	if err := json.Unmarshal([]byte(value), &marker); err != nil || len(marker) != 1 {
		return "", false
	}

	envelope, ok := marker[SealedMarkerKey]

	return envelope, ok && envelope != ""
}
