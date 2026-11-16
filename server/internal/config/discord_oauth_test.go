package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The two halves of "Discord" need different things, and production proved it
// the hard way: it carried the client id/secret pair (login worked, a user
// signed up through it) and neither the bot token nor the application public
// key, while the bot was advertised anyway (spec 2026-09-19-01).
func TestDiscordConfiguredPredicates(t *testing.T) {
	t.Parallel()

	full := DiscordOAuthConfig{
		Enabled:      true,
		ClientID:     "id",
		ClientSecret: "secret",
		BotToken:     "token",
		PublicKey:    "pubkey",
	}

	production := full
	production.BotToken = ""
	production.PublicKey = ""

	withWhitespace := full
	withWhitespace.BotToken = "   "

	disabled := full
	disabled.Enabled = false

	for name, testCase := range map[string]struct {
		cfg     DiscordOAuthConfig
		login   bool
		bot     bool
		missing []string
	}{
		"fully configured": {cfg: full, login: true, bot: true},
		"the production state: login values only": {
			cfg: production, login: true, bot: false,
			missing: []string{EnvDiscordBotToken, EnvDiscordPublicKey},
		},
		"bot token missing": {
			cfg:   func() DiscordOAuthConfig { c := full; c.BotToken = ""; return c }(),
			login: true, bot: false, missing: []string{EnvDiscordBotToken},
		},
		"public key missing": {
			cfg:   func() DiscordOAuthConfig { c := full; c.PublicKey = ""; return c }(),
			login: true, bot: false, missing: []string{EnvDiscordPublicKey},
		},
		"client secret missing": {
			cfg:   func() DiscordOAuthConfig { c := full; c.ClientSecret = ""; return c }(),
			login: false, bot: false, missing: []string{EnvDiscordClientSecret},
		},
		"whitespace is not a value": {
			cfg: withWhitespace, login: true, bot: false,
			missing: []string{EnvDiscordBotToken},
		},
		// Nothing is "missing" from a feature the operator never asked for:
		// a self-hosted install with Discord off must log no warning at all.
		"disabled entirely": {cfg: disabled, login: false, bot: false},
		"empty":             {cfg: DiscordOAuthConfig{}, login: false, bot: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := testCase.cfg
			r.Equal(testCase.login, cfg.LoginConfigured(), "LoginConfigured")
			r.Equal(testCase.bot, cfg.BotConfigured(), "BotConfigured")
			r.Equal(testCase.missing, cfg.MissingBotConfigKeys(), "MissingBotConfigKeys")
		})
	}
}

// The bot rule is strictly stronger than the login rule. If that inverts, a
// deployment could mount the bot without being able to sign anybody in.
func TestDiscordBotConfiguredImpliesLoginConfigured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := DiscordOAuthConfig{
		Enabled:      true,
		ClientID:     "id",
		ClientSecret: "secret",
		BotToken:     "token",
		PublicKey:    "pubkey",
	}

	r.True(cfg.BotConfigured())
	r.True(cfg.LoginConfigured())

	cfg.ClientSecret = ""
	r.False(cfg.BotConfigured(), "the bot must need everything login needs")
}

func TestWarnIgnoredDiscordRedirectEnv(t *testing.T) {
	var buf bytes.Buffer

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	t.Setenv(EnvDiscordRedirectURL, "")
	warnIgnoredDiscordRedirectEnv()
	require.Empty(t, buf.String())

	t.Setenv(EnvDiscordRedirectURL, "https://x.acme.com/cb")
	warnIgnoredDiscordRedirectEnv()
	require.Contains(t, buf.String(), "SP_DISCORD_REDIRECT_URL is ignored")
}

// TestIsPlaceholderJWTSecret pins the one predicate every JWT-secret guard
// shares: empty and both publicly-known constants are placeholders, anything
// else is a real secret. A new known-unsafe constant added to the switch must
// be refused everywhere at once (startup generation, OVH DLR derivation).
func TestIsPlaceholderJWTSecret(t *testing.T) {
	t.Parallel()

	for _, secret := range []string{
		"", DefaultJWTSecret, DevJWTSecret,
		"your-secure-random-secret", "your-secure-random-secret-at-least-32-chars",
	} {
		require.True(t, IsPlaceholderJWTSecret(secret), "placeholder: %q", secret)
	}

	random := make([]byte, 48)
	_, err := rand.Read(random)
	require.NoError(t, err)

	for _, secret := range []string{
		"a-real-secret", "change-me-in-production2", "dev-secret",
		base64.StdEncoding.EncodeToString(random),
	} {
		require.False(t, IsPlaceholderJWTSecret(secret), "real secret: %q", secret)
	}

	for _, secret := range KnownPlaceholderJWTSecrets() {
		require.True(t, IsPlaceholderJWTSecret(secret), "listed placeholder: %q", secret)
	}
}

func TestIsShortJWTSecret(t *testing.T) {
	t.Parallel()

	require.True(t, IsShortJWTSecret("short-secret"))
	require.False(t, IsShortJWTSecret(strings.Repeat("x", MinJWTSecretLength)))
	// Placeholders are replaced, not warned about as short.
	require.False(t, IsShortJWTSecret(""))
	require.False(t, IsShortJWTSecret(DocsJWTSecret))
}

// TestDocsHaveNoPlaceholderJWTSecretExample keeps the published docs from
// offering a known placeholder as a copy-paste JWT secret: anyone who reads
// the docs knows it, so it must never become a signing key.
func TestDocsHaveNoPlaceholderJWTSecretExample(t *testing.T) {
	t.Parallel()

	const docsDir = "../../../web/docs/docs"

	_, err := os.Stat(docsDir)
	require.NoError(t, err, "docs directory must exist")

	assignment := regexp.MustCompile(`(?:jwt_secret:|SP_AUTH_JWT_SECRET=)\s*["']?([^\s"'#]+)`)
	placeholders := map[string]bool{}

	for _, p := range KnownPlaceholderJWTSecrets() {
		placeholders[p] = true
	}

	scanned := 0

	err = filepath.WalkDir(docsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() || (!strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".mdx")) {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		scanned++

		for i, line := range strings.Split(string(content), "\n") {
			for _, m := range assignment.FindAllStringSubmatch(line, -1) {
				require.False(t, placeholders[m[1]],
					"%s:%d publishes the placeholder JWT secret %q", path, i+1, m[1])
			}
		}

		return nil
	})
	require.NoError(t, err)
	require.Positive(t, scanned)
}
