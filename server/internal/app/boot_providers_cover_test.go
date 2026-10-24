package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func bootWithConfig(t *testing.T, mutate func(cfg *config.Config)) (*Server, *httptest.Server) {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "boot-cover-secret"
	cfg.Server.BaseURL = "https://solidping.example"
	mutate(cfg)

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return server, ts
}

// Every identity provider and chat integration is only mounted when its
// credentials are configured. A server with all of them configured must route
// each login/callback endpoint, and the supervisors must be built.
func TestBootWithEveryProviderMountsItsRoutes(t *testing.T) {
	t.Parallel()

	server, ts := bootWithConfig(t, func(cfg *config.Config) {
		cfg.Node.Role = "all"
		cfg.Slack = config.SlackConfig{
			Enabled: true, ClientID: "sc", ClientSecret: "ss",
			SocketModeEnabled: true, AppToken: "xapp-1",
		}
		cfg.Google = config.GoogleOAuthConfig{Enabled: true, ClientID: "g", ClientSecret: "s"}
		cfg.GitHub = config.GitHubOAuthConfig{Enabled: true, ClientID: "g", ClientSecret: "s"}
		cfg.Microsoft = config.MicrosoftOAuthConfig{Enabled: true, ClientID: "m", ClientSecret: "s"}
		cfg.GitLab = config.GitLabOAuthConfig{Enabled: true, ClientID: "g", ClientSecret: "s"}
		cfg.OIDC = config.OIDCOAuthConfig{
			Enabled: true, IssuerURL: "https://idp.acme.com", ClientID: "o", ClientSecret: "s",
		}
		cfg.SAML = config.SAMLConfig{Enabled: true}
		cfg.Discord = config.DiscordOAuthConfig{
			Enabled: true, ClientID: "d", ClientSecret: "s", GatewayEnabled: true,
			BotToken: "bot", PublicKey: "00112233445566778899aabbccddeeff",
		}
		cfg.WhatsApp = config.WhatsAppConfig{
			Enabled: true, AccessToken: "tok", PhoneNumberID: "123", AppSecret: "sec",
		}
	})

	require.NotNil(t, server.slackSocketSupervisor)
	require.NotNil(t, server.discordGatewaySupervisor)

	for _, path := range []string{
		"/api/v1/auth/slack/login",
		"/api/v1/auth/google/login",
		"/api/v1/auth/github/login",
		"/api/v1/auth/microsoft/login",
		"/api/v1/auth/gitlab/login",
		"/api/v1/auth/oidc/login",
		"/api/v1/auth/saml/metadata",
		"/api/v1/integrations/whatsapp/webhook",
	} {
		status := discordRouteStatus(t, ts, http.MethodGet, path)
		require.Falsef(t, notRouted(status), "%s should be mounted, got %d", path, status)
	}

	// Control: a bare server mounts none of them.
	_, bare := bootWithConfig(t, func(*config.Config) {})
	for _, path := range []string{"/api/v1/auth/google/login", "/api/v1/integrations/whatsapp/webhook"} {
		status := discordRouteStatus(t, bare, http.MethodGet, path)
		require.Truef(t, notRouted(status), "%s should not be mounted, got %d", path, status)
	}
}

func TestNewServerRejectsUnsupportedDatabaseAndBadDirectory(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Database.Type = "oracle"
	_, err := NewServer(context.Background(), cfg)
	require.ErrorIs(t, err, ErrUnsupportedDatabaseType)

	// A regular file where the data directory should be cannot be created.
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	cfg = &config.Config{}
	cfg.Database.Type = "sqlite"
	cfg.Database.Dir = filepath.Join(file, "sub")
	_, err = NewServer(context.Background(), cfg)
	require.ErrorContains(t, err, "failed to create SQLite service")
}

func TestNewServerRejectsUnknownPasswordAlgorithm(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.Password.Algorithm = "rot13"

	_, err := NewServer(context.Background(), cfg)
	require.ErrorContains(t, err, "password hashing policy")
}

func TestEncryptionMasterKeyLoading(t *testing.T) {
	t.Parallel()

	key := base64.StdEncoding.EncodeToString(make([]byte, 32))

	cfg := &config.Config{}
	got, err := loadEncryptionMasterKey(cfg)
	require.NoError(t, err)
	require.Nil(t, got)

	cfg.Encryption.MasterKey = key
	got, err = loadEncryptionMasterKey(cfg)
	require.NoError(t, err)
	require.Len(t, got, 32)

	path := filepath.Join(t.TempDir(), "kek")
	require.NoError(t, os.WriteFile(path, []byte(key+"\n"), 0o600))
	cfg.Encryption.MasterKey = ""
	cfg.Encryption.MasterKeyFile = path
	got, err = loadEncryptionMasterKey(cfg)
	require.NoError(t, err)
	require.Len(t, got, 32)

	cfg.Encryption.MasterKeyFile = filepath.Join(t.TempDir(), "missing")
	_, err = loadEncryptionMasterKey(cfg)
	require.ErrorContains(t, err, "read master key file")

	cfg.Encryption.MasterKeyFile = ""
	cfg.Encryption.MasterKey = "!!!not-base64!!!"
	_, err = loadEncryptionMasterKey(cfg)
	require.Error(t, err)
}

func TestMaybeAutoMigrateEncryption(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))

	t.Run("encrypted sweep", func(t *testing.T) {
		t.Parallel()
		server, _ := bootWithConfig(t, func(cfg *config.Config) {
			cfg.Encryption.MasterKey = key
			cfg.Encryption.AutoMigrate = true
		})
		require.True(t, server.services.Credentials.Enabled())

		org := models.NewOrganization("enc-sweep", "")
		require.NoError(t, server.dbService.CreateOrganization(ctx, org))
		conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "leaky")
		conn.Settings = models.JSONMap{"webhook_url": "https://x.test/hook", "auth_token": "TOK"}
		require.NoError(t, server.dbService.CreateChannel(ctx, conn))

		require.NoError(t, server.MaybeAutoMigrateEncryption(ctx))

		got, err := server.dbService.GetChannel(ctx, conn.UID)
		require.NoError(t, err)
		require.NotNil(t, got.SettingsPrivate)
		require.NotEmpty(t, *got.SettingsPrivate)
		require.NotContains(t, got.Settings, "auth_token")
	})

	t.Run("encrypted but auto-migrate disabled", func(t *testing.T) {
		t.Parallel()
		server, _ := bootWithConfig(t, func(cfg *config.Config) {
			cfg.Encryption.MasterKey = key
			cfg.Encryption.AutoMigrate = false
		})
		require.NoError(t, server.MaybeAutoMigrateEncryption(ctx))
	})

	t.Run("plaintext split without a key", func(t *testing.T) {
		t.Parallel()
		server, _ := bootWithConfig(t, func(cfg *config.Config) { cfg.Encryption.AutoMigrate = true })
		require.False(t, server.services.Credentials.Enabled())

		org := models.NewOrganization("plain-split", "")
		require.NoError(t, server.dbService.CreateOrganization(ctx, org))
		check := models.NewCheck(org.UID, "leaky", "ssh")
		check.Config = models.JSONMap{"host": "lan.internal", "private_key": "SSH-SECRET"}
		require.NoError(t, server.dbService.CreateCheck(ctx, check))

		// An encrypted blob left behind by a removed key triggers the warning path.
		blob := "opaque"
		conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "enc")
		conn.SettingsPrivate = &blob
		require.NoError(t, server.dbService.CreateChannel(ctx, conn))

		require.NoError(t, server.MaybeAutoMigrateEncryption(ctx))

		got, err := server.dbService.GetCheck(ctx, org.UID, check.UID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.ConfigPrivate)
		require.NotContains(t, got.Config, "private_key")
	})

	t.Run("no key and auto-migrate off", func(t *testing.T) {
		t.Parallel()
		server, _ := bootWithConfig(t, func(cfg *config.Config) { cfg.Encryption.AutoMigrate = false })
		require.NoError(t, server.MaybeAutoMigrateEncryption(ctx))
	})
}

func TestFreeboxConnectionResolver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, _ := bootWithConfig(t, func(*config.Config) {})

	org := models.NewOrganization("freebox-res", "")
	require.NoError(t, server.dbService.CreateOrganization(ctx, org))

	resolve := newFreeboxConnectionResolver(server.dbService, server.services.Credentials)

	_, err := resolve(ctx, "00000000-0000-0000-0000-000000000000")
	require.ErrorContains(t, err, "get channel")

	wrong := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "wh")
	require.NoError(t, server.dbService.CreateChannel(ctx, wrong))
	_, err = resolve(ctx, wrong.UID)
	require.ErrorIs(t, err, errFreeboxWrongType)

	pairing := models.NewIntegration(org.UID, models.ConnectionTypeFreebox, "fb-pairing")
	pairing.Settings = models.JSONMap{"baseUrl": "https://fb.test", "appId": "app"}
	require.NoError(t, server.dbService.CreateChannel(ctx, pairing))
	_, err = resolve(ctx, pairing.UID)
	require.ErrorIs(t, err, errFreeboxNoAppToken)

	plain := models.NewIntegration(org.UID, models.ConnectionTypeFreebox, "fb-plain")
	plain.Settings = models.JSONMap{"baseUrl": "https://fb.test", "appId": "app", "appToken": "tok"}
	require.NoError(t, server.dbService.CreateChannel(ctx, plain))
	got, err := resolve(ctx, plain.UID)
	require.NoError(t, err)
	require.Equal(t, "tok", got.AppToken)
	require.True(t, strings.HasPrefix(got.BaseURL, "https://fb.test"))

	// A private blob with encryption disabled cannot be read.
	blob := "opaque"
	sealed := models.NewIntegration(org.UID, models.ConnectionTypeFreebox, "fb-sealed")
	sealed.Settings = models.JSONMap{"baseUrl": "https://fb.test", "appId": "app"}
	sealed.SettingsPrivate = &blob
	require.NoError(t, server.dbService.CreateChannel(ctx, sealed))
	_, err = resolve(ctx, sealed.UID)
	require.ErrorIs(t, err, errFreeboxEncryptionDisabled)
}
