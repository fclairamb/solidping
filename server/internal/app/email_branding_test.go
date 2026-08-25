package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// TestEmailFormatterUsesThePostOverlayBaseURL pins the WIRING at
// server.go's `email.NewFormatter(email.WithBaseURLFunc(...))`, not just the
// option's behavior.
//
// config.Server.BaseURL is NOT final when NewServer builds the formatter: the
// systemconfig overlay — which is what actually applies SP_BASE_URL and the
// DB-stored `server.base_url` parameter — runs afterwards, in
// InitializeSystemConfig. Capturing the value at construction
// (`WithBaseURL(cfg.Server.BaseURL)`) therefore pins every email to the
// pre-overlay default, which in a real deployment means every transactional
// email ships a localhost logo URL. That regression is invisible to the
// email package's own tests, so it has to be caught here, against a real
// server holding the real *config.Config the overlay mutates.
//
// The pre-overlay assertion is the positive control: without it this test
// would still pass against a formatter that rendered no logo at all.
func TestEmailFormatterUsesThePostOverlayBaseURL(t *testing.T) {
	const (
		preOverlayBaseURL  = "http://localhost:4000"
		postOverlayBaseURL = "https://monitoring.acme.example"
	)

	// Hermetic: an SP_BASE_URL inherited from the developer's shell would win
	// over the stored parameter (env > db > default) and mask the assertion.
	// Empty reads exactly like unset in systemconfig's overlay loop. Not
	// t.Parallel() — t.Setenv forbids it.
	t.Setenv("SP_BASE_URL", "")

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "email-branding-secret"
	cfg.Server.BaseURL = preOverlayBaseURL

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))

	_, before, _, err := server.services.EmailFormatter.Format("welcome.html", map[string]any{})
	r.NoError(err)
	r.Contains(before, preOverlayBaseURL+"/dash0/logo.png",
		"the formatter must render the configured base URL before the overlay")

	// The overlay, exactly as boot performs it — and strictly AFTER NewServer
	// already built the formatter.
	r.NoError(server.dbService.SetSystemParameter(ctx, "server.base_url", postOverlayBaseURL, false))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	r.Equal(postOverlayBaseURL, cfg.Server.BaseURL, "the overlay must have moved the base URL")

	_, after, _, err := server.services.EmailFormatter.Format("welcome.html", map[string]any{})
	r.NoError(err)
	r.Contains(after, postOverlayBaseURL+"/dash0/logo.png",
		"emails must use the post-overlay base URL; a captured (non-late-bound) base URL fails here")
	r.NotContains(after, preOverlayBaseURL,
		"no trace of the pre-overlay base URL may survive in a rendered email")
}
