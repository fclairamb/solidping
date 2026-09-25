package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/authhandoff"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// attackerRedirectURI is the redirect_uri an attacker puts in the login link
// they send a victim (spec 2026-09-25-18).
const attackerRedirectURI = "https://evil.example/steal"

// mcpConsentBounce is the redirect_uri the dashboard sends when an MCP client's
// authorize request bounced through the login page (web/dash0
// lib/login-destination.ts buildOAuthLoginUrl): the whole authorize request,
// URL-encoded inside the login page's returnTo. state and redirect_uri are
// chosen by the MCP client and have no length bound of their own.
func mcpConsentBounce(clientRedirectURI, state string) string {
	authorize := "/api/v1/oauth/authorize?" + url.Values{
		"client_id":             {"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"},
		"redirect_uri":          {clientRedirectURI},
		"response_type":         {"code"},
		"scope":                 {"mcp"},
		"state":                 {state},
		"resource":              {"https://solidping.acme.com/mcp"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
	}.Encode()

	return "/d/orgs/acme/login?returnTo=" + url.QueryEscape(authorize)
}

// shortMCPBounce is a CLI-style bounce: loopback redirect, 43-char state.
func shortMCPBounce() string {
	return mcpConsentBounce("http://127.0.0.1:53682/callback", "Zm9vYmFyYmF6cXV4cXV1eGNvcmdlZ3JhdWx0Z2FycGx5")
}

// webMCPBounce is a hosted client's bounce: an https callback on its own
// domain and a longer (signed) state.
func webMCPBounce() string {
	return mcpConsentBounce("https://connectors.acme.com/api/mcp/oauth/callback",
		strings.Repeat("s1gn3d-st4te-", 10))
}

func TestSanitizePostLoginRedirect(t *testing.T) {
	t.Parallel()

	accepted := []string{
		"/d/orgs/acme/checks",
		"/d/orgs/acme/checks?status=down#top",
		"/d/login?returnTo=%2Fd%2Forgs%2Facme%2Fchecks",
		"/",
		// Percent-encoded slashes are path bytes, not an authority.
		"/%2F%2Fevil.example",
		shortMCPBounce(),
		webMCPBounce(),
		"/" + strings.Repeat("a", maxPostLoginRedirectLen-1),
	}

	for _, raw := range accepted {
		t.Run("accepts "+truncateName(raw), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.True(isSafePostLoginRedirect(raw))
			r.Equal(raw, sanitizePostLoginRedirect(t.Context(), raw, "acme"))
		})
	}

	rejected := []string{
		"https://evil.com",
		"https://evil.com/d/orgs/acme",
		"http://localhost:4000/d/orgs/acme", // same-origin absolute: still refused
		"//evil.com",
		"//evil.com/d/orgs/acme",
		"/\\evil.com",
		"/d/orgs/acme\\..\\..\\evil",
		"\\\\evil.com",
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"/\t/evil.com",
		"/\n/evil.com",
		"/\r/evil.com",
		"/d/orgs/acme\x00",
		"d/orgs/acme",
		"evil.com",
		" /d/orgs/acme",
		"/" + strings.Repeat("a", maxPostLoginRedirectLen),
		"/%zz", // unparseable escape
	}

	for _, raw := range rejected {
		t.Run("rejects "+truncateName(raw), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.False(isSafePostLoginRedirect(raw))
			r.Equal("/d/orgs/acme", sanitizePostLoginRedirect(t.Context(), raw, "acme"),
				"a rejection falls back to the org's dashboard home")
			r.Equal("/", sanitizePostLoginRedirect(t.Context(), raw, ""),
				"an org-less login falls back to the root")
		})
	}

	t.Run("empty is the default, not a rejection", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		r.Equal("/d/orgs/acme", sanitizePostLoginRedirect(t.Context(), "", "acme"))
		r.Equal("/", sanitizePostLoginRedirect(t.Context(), "", ""))
	})
}

// TestPostLoginRedirectCapFitsMCPBounce pins why the cap is 2048 rather than
// a deep-link-sized 512: the MCP consent bounce through the login page is
// already ~430 chars with a CLI's short values, and a hosted client's longer
// callback URL and state push it past 512. Refusing it would silently
// dead-end every MCP connect started with SSO on the dashboard home.
func TestPostLoginRedirectCapFitsMCPBounce(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Greater(len(shortMCPBounce()), 400)
	r.True(isSafePostLoginRedirect(shortMCPBounce()))

	r.Greater(len(webMCPBounce()), 512, "a hosted client's bounce exceeds a deep-link-sized cap")
	r.True(isSafePostLoginRedirect(webMCPBounce()))
}

func truncateName(raw string) string {
	const maxName = 40

	if len(raw) > maxName {
		return raw[:maxName] + "…"
	}

	return raw
}

// callbackProviders is every federated login driven through its real Login and
// Callback handlers.
func callbackProviders() map[string]func(t *testing.T, opts callbackOpts) callbackRun {
	return map[string]func(t *testing.T, opts callbackOpts) callbackRun{
		"google":    runGoogleCallback,
		"github":    runGitHubCallback,
		"gitlab":    runGitLabCallback,
		"microsoft": runMicrosoftCallback,
		"discord":   runDiscordCallback,
		"slack":     runSlackCallback,
		"oidc":      runOIDCCallback,
		"saml":      runSAMLCallback,
	}
}

// TestProviderLoginsRefuseForeignRedirectURI is the attack: the victim follows
// an attacker-minted login link carrying redirect_uri=<attacker host> and
// completes a normal login. The handoff code must land on our own handoff
// route, and the session it carries must return to the default destination —
// never to the attacker host.
//
// "via login" is the path a real browser takes (Login sanitizes before sealing
// the state). "stale state" seals the attacker URL straight into the state, as
// a deploy without this guard did: the callback must refuse it all the same.
func TestProviderLoginsRefuseForeignRedirectURI(t *testing.T) {
	t.Parallel()

	for name, run := range callbackProviders() {
		for _, viaLogin := range []bool{true, false} {
			label := name + "/stale state"
			if viaLogin {
				label = name + "/via login"
			}

			t.Run(label, func(t *testing.T) {
				t.Parallel()

				result := run(t, callbackOpts{redirectURI: attackerRedirectURI, viaLogin: viaLogin})

				require.NotContains(t, result.rec.Header().Get("Location"), "evil.example")

				// The regular handoff contract, with the default destination
				// sealed instead of the attacker's URL.
				result.returnTo = result.defaultTo
				assertHandoffRedirect(t, result)
			})
		}
	}
}

// TestProviderLoginsKeepDeepLinkThroughLogin is the positive control for the
// test above: a same-origin deep link started through the real Login handler
// survives to the handoff session unchanged, so the guard is not simply
// replacing every value.
func TestProviderLoginsKeepDeepLinkThroughLogin(t *testing.T) {
	t.Parallel()

	for name, run := range callbackProviders() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assertHandoffRedirect(t, run(t, callbackOpts{viaLogin: true}))
		})
	}
}

// TestProviderCallbackErrorsRefuseForeignRedirectURI covers the error path: a
// callback whose provider exchange fails redirects with the error parameters
// to the default destination, not to the attacker's URL — and, as the
// control, to the login's own deep link when that one is safe.
func TestProviderCallbackErrorsRefuseForeignRedirectURI(t *testing.T) {
	t.Parallel()

	for name, run := range callbackProviders() {
		for _, hostile := range []bool{true, false} {
			label := name + "/deep link"
			if hostile {
				label = name + "/attacker url"
			}

			t.Run(label, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)

				opts := callbackOpts{failExchange: true}
				if hostile {
					opts.redirectURI = attackerRedirectURI
				}

				result := run(t, opts)

				r.Equal(http.StatusFound, result.rec.Code, result.rec.Body.String())

				rawLocation := result.rec.Header().Get("Location")
				location, err := url.Parse(rawLocation)
				r.NoError(err)

				r.Empty(location.Scheme, rawLocation)
				r.Empty(location.Host, rawLocation)
				r.NotContains(rawLocation, "evil.example")
				r.NotEmpty(location.Query().Get("error"), "the failure is reported: %s", rawLocation)

				want := result.returnTo
				if hostile {
					want = result.defaultTo
				}

				r.Equal(want, location.Path)
			})
		}
	}
}

// TestPendingHandoffRefusesForeignReturnTo is the pending-membership branch of
// the shared tail (join_policy.go): an org that did not admit the user still
// redirects through the handoff route, and the sealed returnTo is the default
// destination, never the attacker's URL. RedirectWithHandoff is covered on its
// own too, since the Slack app-install callback calls it directly.
func TestPendingHandoffRefusesForeignReturnTo(t *testing.T) {
	t.Parallel()

	tails := map[string]func(rec *httptest.ResponseRecorder, req *http.Request, svc *Service, outcome *ProviderOutcome) error{
		"finishProviderCallback": func(rec *httptest.ResponseRecorder, req *http.Request, svc *Service, outcome *ProviderOutcome) error {
			return finishProviderCallback(rec, req, svc.db, "google", attackerRedirectURI, outcome)
		},
		"RedirectWithHandoff": func(rec *httptest.ResponseRecorder, req *http.Request, svc *Service, outcome *ProviderOutcome) error {
			return RedirectWithHandoff(rec, req, svc.db, "https://solidping.acme.com", outcome, attackerRedirectURI)
		},
	}

	for name, tail := range tails {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			svc, dbSvc, ctx := setupAuthTestService(t)
			user := joinTestUser(ctx, t, dbSvc, "outsider-"+nextFixture()+"@elsewhere.example")

			pending, err := svc.pendingSession(ctx, user)
			r.NoError(err)

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/auth/google/callback", nil)

			r.NoError(tail(rec, req, svc, &ProviderOutcome{
				AccessToken:    pending.AccessToken,
				ExpiresIn:      pending.ExpiresIn,
				OrgSlug:        "acme",
				UserUID:        user.UID,
				Pending:        true,
				PendingOrgSlug: "acme",
			}))

			r.Equal(http.StatusFound, rec.Code)

			rawLocation := rec.Header().Get("Location")
			r.NotContains(rawLocation, "evil.example")

			location, err := url.Parse(rawLocation)
			r.NoError(err)
			r.Equal(handoffCompletePath, location.Path)
			r.Equal("acme", location.Query().Get(pendingMembershipParam))

			session, err := authhandoff.Redeem(ctx, dbSvc, location.Query().Get(handoffCodeParam))
			r.NoError(err)
			r.Equal("/d/orgs/acme", session.ReturnTo)
			r.Equal("acme", session.MembershipPending)
		})
	}
}

// TestFinishProviderCallbackStorageFailureRefusesForeignReturnTo: when the
// handoff cannot be stored, the error redirect goes to the default
// destination, not to the attacker's URL.
func TestFinishProviderCallbackStorageFailureRefusesForeignReturnTo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, dbSvc, ctx := setupAuthTestService(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/auth/google/callback", nil)

	r.NoError(finishProviderCallback(rec, req, dbSvc, "google", attackerRedirectURI, &ProviderOutcome{
		AccessToken: "at-secret", RefreshToken: "rt-secret", ExpiresIn: 3600,
		OrgSlug: "does-not-exist", UserUID: models.NewUser("ghost@acme.com").UID,
	}))

	r.Equal(http.StatusFound, rec.Code)

	rawLocation := rec.Header().Get("Location")
	r.NotContains(rawLocation, "evil.example")

	location, err := url.Parse(rawLocation)
	r.NoError(err)
	r.Equal("/d/orgs/does-not-exist", location.Path)
	r.Equal(OAuthCodeFailed, location.Query().Get("error"))
}

// TestRedirectOAuthErrorRefusesForeignBase is the last line of defence every
// provider's redirectWithError ends in.
func TestRedirectOAuthErrorRefusesForeignBase(t *testing.T) {
	t.Parallel()

	for _, base := range []string{attackerRedirectURI, "//evil.example", "/\\evil.example"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

		redirectOAuthError(rec, req, base, OAuthCodeFailed, "boom")

		location, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		require.Equal(t, "/", location.Path, base)
		require.Empty(t, location.Host, base)
		require.Equal(t, OAuthCodeFailed, location.Query().Get("error"))
	}
}

// TestRedirectLinkedRefusesForeignURI: the Discord account-link round trip
// only returns to a same-origin path.
func TestRedirectLinkedRefusesForeignURI(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		attackerRedirectURI:                      "/",
		"//evil.example":                         "/",
		"":                                       "/",
		"/d/orgs/acme/account/notifications":     "/d/orgs/acme/account/notifications",
		"/d/orgs/acme/account/notifications?x=1": "/d/orgs/acme/account/notifications",
	}

	for raw, wantPath := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

		require.NoError(t, redirectLinked(rec, req, raw))

		location, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		require.Empty(t, location.Host, raw)
		require.Equal(t, wantPath, location.Path, raw)
		require.Equal(t, "1", location.Query().Get(discordLinkedParam))
	}
}

// TestExchangeHandoffDropsForeignReturnTo: a sealed returnTo that is not a
// same-origin path (nothing this deploy mints) is not handed to the dashboard.
func TestExchangeHandoffDropsForeignReturnTo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, ctx := setupAuthTestService(t)
	user := joinTestUser(ctx, t, dbSvc, "sealed-"+nextFixture()+"@acme.com")

	pending, err := svc.pendingSession(ctx, user)
	r.NoError(err)

	for returnTo, want := range map[string]string{
		attackerRedirectURI: "",
		"/d/orgs/acme":      "/d/orgs/acme",
	} {
		code, err := authhandoff.Issue(ctx, dbSvc, &authhandoff.Session{
			AccessToken: pending.AccessToken,
			ExpiresIn:   pending.ExpiresIn,
			UserUID:     user.UID,
			ReturnTo:    returnTo,
		})
		r.NoError(err)

		resp, err := svc.ExchangeHandoffCode(ctx, code)
		r.NoError(err)
		r.Equal(want, resp.ReturnTo, returnTo)
	}
}
