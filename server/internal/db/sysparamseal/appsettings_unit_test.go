package sysparamseal_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
)

const sealedSettingJSON = `{"$sp_sealed":"env"}`

// fakeSettingsDB overrides the app_settings methods of an otherwise nil db.Service.
type fakeSettingsDB struct {
	fakeDB

	settings map[string]string
	getErr   error
	setErr   error
	// failKey makes SetAppSetting fail for this key only.
	failKey string
	written map[string]string
}

func (f *fakeSettingsDB) GetAppSetting(_ context.Context, key string) (string, error) {
	return f.settings[key], f.getErr
}

func (f *fakeSettingsDB) GetAppSettings(_ context.Context, keys []string) (map[string]string, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}

	out := map[string]string{}

	for _, key := range keys {
		if value, ok := f.settings[key]; ok {
			out[key] = value
		}
	}

	return out, nil
}

func (f *fakeSettingsDB) SetAppSetting(_ context.Context, key, value string) error {
	if f.setErr != nil || key == f.failKey {
		return errBoom
	}

	f.written = map[string]string{key: value}

	return nil
}

func (f *fakeSettingsDB) SetAppSettings(_ context.Context, settings map[string]string) error {
	f.written = settings

	return f.setErr
}

func (f *fakeSettingsDB) CreateAppSettingsIfAbsent(_ context.Context, settings map[string]string) error {
	f.written = settings

	return f.setErr
}

func openingSealer() *fakeSealer {
	return &fakeSealer{enabled: true, opened: map[string]any{"value": "pem"}}
}

func TestGetAppSetting(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	key := sysparamseal.AppSettingVAPIDPrivateKey

	t.Run("opens a sealed value", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{key: sealedSettingJSON}}
		got, err := sysparamseal.Wrap(inner, openingSealer(), nil).GetAppSetting(ctx, key)
		require.NoError(t, err)
		require.Equal(t, "pem", got)
	})

	t.Run("passes a plaintext value through", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{key: "plain"}}
		got, err := sysparamseal.Wrap(inner, openingSealer(), nil).GetAppSetting(ctx, key)
		require.NoError(t, err)
		require.Equal(t, "plain", got)
	})

	t.Run("db error", func(t *testing.T) {
		t.Parallel()

		_, err := sysparamseal.Wrap(&fakeSettingsDB{getErr: errBoom}, openingSealer(), nil).GetAppSetting(ctx, key)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("sealed value without a master key", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{key: sealedSettingJSON}}
		_, err := sysparamseal.Wrap(inner, &fakeSealer{}, nil).GetAppSetting(ctx, key)
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
		require.ErrorContains(t, err, "SP_ENCRYPTION_MASTER_KEY")
	})

	t.Run("wrong master key", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{key: sealedSettingJSON}}
		_, err := sysparamseal.Wrap(inner, &fakeSealer{enabled: true, openErr: errBoom}, nil).GetAppSetting(ctx, key)
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("unexpected sealed content", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{key: sealedSettingJSON}}
		sealer := &fakeSealer{enabled: true, opened: map[string]any{"value": 3}}
		_, err := sysparamseal.Wrap(inner, sealer, nil).GetAppSetting(ctx, key)
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
		require.ErrorContains(t, err, "unexpected sealed content")
	})
}

func TestGetAppSettings(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	sealedKey := sysparamseal.AppSettingSAMLSPPrivateKey
	keys := []string{sealedKey, "webpush.vapid_public_key"}

	t.Run("opens sealed and keeps plain", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{sealedKey: sealedSettingJSON, keys[1]: "pub"}}
		got, err := sysparamseal.Wrap(inner, openingSealer(), nil).GetAppSettings(ctx, keys)
		require.NoError(t, err)
		require.Equal(t, map[string]string{sealedKey: "pem", keys[1]: "pub"}, got)
	})

	t.Run("db error", func(t *testing.T) {
		t.Parallel()

		_, err := sysparamseal.Wrap(&fakeSettingsDB{getErr: errBoom}, openingSealer(), nil).GetAppSettings(ctx, keys)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("unopenable value", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{sealedKey: sealedSettingJSON}}
		_, err := sysparamseal.Wrap(inner, &fakeSealer{}, nil).GetAppSettings(ctx, keys)
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
	})
}

func TestSetAppSettingSealsSecrets(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	secret := sysparamseal.AppSettingVAPIDPrivateKey

	t.Run("seals a secret key", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{}
		require.NoError(t, sysparamseal.Wrap(inner, openingSealer(), nil).SetAppSetting(ctx, secret, "pem"))
		require.JSONEq(t, sealedSettingJSON, inner.written[secret])
	})

	t.Run("leaves a non secret key, an empty value and a sealed value", func(t *testing.T) {
		t.Parallel()

		for key, value := range map[string]string{
			"webpush.vapid_public_key": "pub",
			secret:                     "",
		} {
			inner := &fakeSettingsDB{}
			require.NoError(t, sysparamseal.Wrap(inner, openingSealer(), nil).SetAppSetting(ctx, key, value))
			require.Equal(t, value, inner.written[key])
		}

		inner := &fakeSettingsDB{}
		require.NoError(t, sysparamseal.Wrap(inner, openingSealer(), nil).SetAppSetting(ctx, secret, sealedSettingJSON))
		require.JSONEq(t, sealedSettingJSON, inner.written[secret])
	})

	t.Run("without a master key", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{}
		require.NoError(t, sysparamseal.Wrap(inner, &fakeSealer{}, nil).SetAppSetting(ctx, secret, "pem"))
		require.Equal(t, "pem", inner.written[secret])
	})

	t.Run("seal error", func(t *testing.T) {
		t.Parallel()

		err := sysparamseal.Wrap(&fakeSettingsDB{}, &fakeSealer{enabled: true, sealErr: errBoom}, nil).
			SetAppSetting(ctx, secret, "pem")
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("db error", func(t *testing.T) {
		t.Parallel()

		err := sysparamseal.Wrap(&fakeSettingsDB{setErr: errBoom}, openingSealer(), nil).SetAppSetting(ctx, secret, "pem")
		require.ErrorIs(t, err, errBoom)
	})
}

func TestBulkAppSettingWrites(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	secret := sysparamseal.AppSettingSAMLSPPrivateKey
	input := map[string]string{secret: "pem", "saml.sp_certificate_pem": "cert"}

	writers := map[string]func(*sysparamseal.Service, map[string]string) error{
		"SetAppSettings": func(s *sysparamseal.Service, m map[string]string) error {
			return s.SetAppSettings(ctx, m)
		},
		"CreateAppSettingsIfAbsent": func(s *sysparamseal.Service, m map[string]string) error {
			return s.CreateAppSettingsIfAbsent(ctx, m)
		},
	}

	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inner := &fakeSettingsDB{}
			require.NoError(t, write(sysparamseal.Wrap(inner, openingSealer(), nil), input))
			require.JSONEq(t, sealedSettingJSON, inner.written[secret])
			require.Equal(t, "cert", inner.written["saml.sp_certificate_pem"])
			require.Equal(t, "pem", input[secret], "the caller's map is not modified")

			err := write(sysparamseal.Wrap(&fakeSettingsDB{}, &fakeSealer{enabled: true, sealErr: errBoom}, nil), input)
			require.ErrorIs(t, err, errBoom)

			err = write(sysparamseal.Wrap(&fakeSettingsDB{setErr: errBoom}, openingSealer(), nil), input)
			require.ErrorIs(t, err, errBoom)
		})
	}
}

func TestSealPlaintextAppSettings(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	vapid := sysparamseal.AppSettingVAPIDPrivateKey
	saml := sysparamseal.AppSettingSAMLSPPrivateKey

	t.Run("disabled is a no-op", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{vapid: "plain"}}
		n, err := sysparamseal.Wrap(inner, &fakeSealer{}, nil).SealPlaintextAppSettings(ctx)
		require.NoError(t, err)
		require.Zero(t, n)
		require.Nil(t, inner.written)
	})

	t.Run("read error", func(t *testing.T) {
		t.Parallel()

		_, err := sysparamseal.Wrap(&fakeSettingsDB{getErr: errBoom}, openingSealer(), nil).SealPlaintextAppSettings(ctx)
		require.ErrorIs(t, err, errBoom)
		require.ErrorContains(t, err, "read secret app settings")
	})

	t.Run("seals only the plaintext ones", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{vapid: "plain", saml: sealedSettingJSON}}
		n, err := sysparamseal.Wrap(inner, openingSealer(), nil).SealPlaintextAppSettings(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.JSONEq(t, sealedSettingJSON, inner.written[vapid])
	})

	t.Run("seal error", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{vapid: "plain"}}
		n, err := sysparamseal.Wrap(inner, &fakeSealer{enabled: true, sealErr: errBoom}, nil).SealPlaintextAppSettings(ctx)
		require.ErrorIs(t, err, errBoom)
		require.Zero(t, n)
	})

	t.Run("store error", func(t *testing.T) {
		t.Parallel()

		inner := &fakeSettingsDB{settings: map[string]string{vapid: "plain"}, setErr: errBoom}
		_, err := sysparamseal.Wrap(inner, openingSealer(), nil).SealPlaintextAppSettings(ctx)
		require.ErrorIs(t, err, errBoom)
		require.ErrorContains(t, err, "store sealed app setting")
	})
}

func TestPlaintextAppSettingKeys(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	vapid := sysparamseal.AppSettingVAPIDPrivateKey
	saml := sysparamseal.AppSettingSAMLSPPrivateKey

	inner := &fakeSettingsDB{settings: map[string]string{vapid: "plain", saml: sealedSettingJSON}}
	keys, err := sysparamseal.Wrap(inner, openingSealer(), nil).PlaintextAppSettingKeys(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{vapid}, keys)

	_, err = sysparamseal.Wrap(&fakeSettingsDB{getErr: errBoom}, openingSealer(), nil).PlaintextAppSettingKeys(ctx)
	require.ErrorIs(t, err, errBoom)
}
