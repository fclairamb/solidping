package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/oauthstate"
)

// replyMode is how the fake provider answers one of its endpoints.
type replyMode int

const (
	// replyBody answers status and body.
	replyBody replyMode = iota
	// replyTruncated announces more bytes than it sends, then hangs up.
	replyTruncated
	// replyHangUp closes the connection without answering.
	replyHangUp
)

type fakeReply struct {
	mode   replyMode
	status int
	body   string
}

// fakeRoute is one endpoint of the fake provider and its healthy answer.
type fakeRoute struct {
	suffix string
	body   string
}

// fakeProvider is an httptest identity provider. Every endpoint answers its
// healthy body until a test scripts another reply for it.
type fakeProvider struct {
	url string

	mu      sync.Mutex
	routes  []fakeRoute
	replies map[string]fakeReply
}

// newFakeProvider serves routes, matched by path suffix in the given order:
// the most specific suffix goes first.
func newFakeProvider(t *testing.T, routes ...fakeRoute) *fakeProvider {
	t.Helper()

	fake := &fakeProvider{routes: routes, replies: map[string]fakeReply{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)

	fake.url = server.URL

	return fake
}

// script makes the endpoint answer reply from now on.
func (f *fakeProvider) script(suffix string, reply fakeReply) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.replies[suffix] = reply
}

func (f *fakeProvider) replyFor(path string) (fakeReply, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, route := range f.routes {
		if !strings.HasSuffix(path, route.suffix) {
			continue
		}

		if reply, scripted := f.replies[route.suffix]; scripted {
			return reply, true
		}

		return fakeReply{body: route.body}, true
	}

	return fakeReply{}, false
}

func (f *fakeProvider) serve(writer http.ResponseWriter, req *http.Request) {
	reply, known := f.replyFor(req.URL.Path)
	if !known {
		writer.WriteHeader(http.StatusNotFound)

		return
	}

	if reply.mode == replyBody {
		writer.Header().Set("Content-Type", "application/json")

		if reply.status != 0 {
			writer.WriteHeader(reply.status)
		}

		_, _ = writer.Write([]byte(reply.body))

		return
	}

	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		writer.WriteHeader(http.StatusInternalServerError)

		return
	}

	conn, _, err := hijacker.Hijack()
	if err != nil {
		return
	}

	defer func() { _ = conn.Close() }()

	if reply.mode == replyTruncated {
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n" +
			"Content-Length: " + strconv.Itoa(len(reply.body)+64) + "\r\n\r\n" + reply.body))
	}
}

func jsonBody(t *testing.T, value any) string {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return string(raw)
}

// providerRun is one provider wired to a fake identity provider and a store
// that can fail on demand.
type providerRun struct {
	db       db.Service
	fake     *fakeProvider
	fault    *queryFault
	callback func() error
}

func (p *providerRun) userCount(t *testing.T) int {
	t.Helper()

	users, err := p.db.ListUsers(t.Context())
	require.NoError(t, err)

	return len(users)
}

// providerSpec describes a provider to the shared failure tables.
type providerSpec struct {
	name string
	// Sentinels the callback wraps a failed token exchange and a failed API
	// call in.
	tokenErr, profileErr, extraErr error
	// Endpoint suffixes on the fake provider. extra is the second API call
	// (emails or guilds), "" when the provider has none.
	token, profile, extra string
	// workspace: the organization comes from the provider's own workspace
	// (Slack team, Discord guild) instead of the slug of the login page.
	workspace bool
	// relinks: an account that exists without its provider link is linked on
	// the next sign-in, so a failed first sign-in can simply be retried.
	relinks bool
	setup   func(t *testing.T) *providerRun
}

func providerSpecs() []providerSpec {
	return []providerSpec{
		{
			name: "github", tokenErr: ErrGitHubTokenExchange, profileErr: ErrGitHubAPI, extraErr: ErrGitHubAPI,
			token: "/login/oauth/access_token", profile: "/user", extra: "/user/emails", relinks: true,
			setup: func(t *testing.T) *providerRun {
				t.Helper()

				svc, ctx := setupGitHubTestService(t)
				org := setupGitHubTestOrg(ctx, t, svc)
				fake := newFakeProvider(t,
					fakeRoute{"/login/oauth/access_token", jsonBody(t, GitHubTokenResponse{AccessToken: "t"})},
					fakeRoute{"/user/emails", jsonBody(t, []GitHubEmail{
						{Email: "member@acme.com", Primary: true, Verified: true},
					})},
					fakeRoute{"/user", jsonBody(t, GitHubUserInfo{ID: 42, Login: "member", Name: "Member"})},
				)

				target, err := url.Parse(fake.url)
				require.NoError(t, err)

				svc.httpClient = &http.Client{
					Timeout:   5 * time.Second,
					Transport: hostRewriter{target: target, next: http.DefaultTransport},
				}

				return &providerRun{db: svc.db, fake: fake, fault: installQueryFault(svc.db), callback: func() error {
					_, cbErr := svc.HandleCallback(ctx, "code", org.Slug)

					return cbErr
				}}
			},
		},
		{
			name: "google", tokenErr: ErrGoogleTokenExchange, profileErr: ErrGoogleAPI,
			token: "/token", profile: "/userinfo", relinks: true,
			setup: func(t *testing.T) *providerRun {
				t.Helper()

				svc, ctx := setupGoogleTestService(t)
				org := setupTestOrg(ctx, t, svc)
				fake := newFakeProvider(t,
					fakeRoute{"/token", jsonBody(t, GoogleTokenResponse{AccessToken: "t"})},
					fakeRoute{"/userinfo", jsonBody(t, GoogleUserInfo{
						Sub: "g-1", Email: "member@acme.com", EmailVerified: true, Name: "Member",
					})},
				)
				svc.tokenURL = fake.url + "/token"
				svc.userInfoURL = fake.url + "/userinfo"

				return &providerRun{db: svc.db, fake: fake, fault: installQueryFault(svc.db), callback: func() error {
					_, cbErr := svc.HandleCallback(ctx, "code", org.Slug)

					return cbErr
				}}
			},
		},
		{
			name: "gitlab", tokenErr: ErrGitLabTokenExchange, profileErr: ErrGitLabAPI, extraErr: ErrGitLabAPI,
			token: "/oauth/token", profile: "/api/v4/user", extra: "/api/v4/user/emails",
			setup: func(t *testing.T) *providerRun {
				t.Helper()

				svc, ctx := setupGitLabTestService(t)
				org := setupGitLabTestOrg(ctx, t, svc)
				fake := newFakeProvider(t,
					fakeRoute{"/oauth/token", jsonBody(t, GitLabTokenResponse{AccessToken: "t"})},
					fakeRoute{"/api/v4/user/emails", jsonBody(t, confirmedEmails("member@acme.com"))},
					fakeRoute{"/api/v4/user", jsonBody(t, gitLabProfile(7, "member@acme.com", gitLabConfirmedAt))},
				)
				svc.cfg.GitLab.BaseURL = fake.url

				return &providerRun{db: svc.db, fake: fake, fault: installQueryFault(svc.db), callback: func() error {
					_, cbErr := svc.HandleCallback(ctx, "code", org.Slug)

					return cbErr
				}}
			},
		},
		{
			name: "microsoft", tokenErr: ErrMicrosoftTokenExchange, profileErr: ErrMicrosoftAPI,
			token: "/token", profile: "/me", relinks: true,
			setup: func(t *testing.T) *providerRun {
				t.Helper()

				svc, ctx := setupMicrosoftTestService(t)
				org := setupMicrosoftTestOrg(ctx, t, svc)
				fake := newFakeProvider(t,
					fakeRoute{"/token", jsonBody(t, MicrosoftTokenResponse{AccessToken: "t"})},
					fakeRoute{"/me", jsonBody(t, MicrosoftUserInfo{
						ID: "ms-1", DisplayName: "Member", Mail: "member@acme.com", UserPrincipalName: "member@acme.com",
					})},
				)
				svc.tokenURL = fake.url + "/token"
				svc.userURL = fake.url + "/me"

				return &providerRun{db: svc.db, fake: fake, fault: installQueryFault(svc.db), callback: func() error {
					_, cbErr := svc.HandleCallback(ctx, "code", org.Slug)

					return cbErr
				}}
			},
		},
		{
			name: "discord", tokenErr: ErrDiscordTokenExchange, profileErr: ErrDiscordAPI, extraErr: ErrDiscordAPI,
			token: "/oauth2/token", profile: "/users/@me", extra: "/users/@me/guilds",
			workspace: true, relinks: true,
			setup: func(t *testing.T) *providerRun {
				t.Helper()

				svc := newDiscordTestService(t, newSQLiteDBService(t))
				fake := newFakeProvider(t,
					fakeRoute{"/oauth2/token", jsonBody(t, DiscordTokenResponse{AccessToken: "t"})},
					fakeRoute{"/users/@me/guilds", jsonBody(t, []DiscordGuild{{ID: "guild-1", Name: "Acme"}})},
					fakeRoute{"/users/@me", jsonBody(t, DiscordUserInfo{
						ID: "du-1", Username: "member", Email: "member@acme.com", Verified: true,
					})},
				)
				svc.tokenURL = fake.url + "/oauth2/token"
				svc.apiBaseURL = fake.url

				return &providerRun{db: svc.db, fake: fake, fault: installQueryFault(svc.db), callback: func() error {
					_, cbErr := svc.HandleCallback(t.Context(), "code")

					return cbErr
				}}
			},
		},
		{
			name: "slack", tokenErr: ErrSlackTokenExchange, profileErr: ErrSlackOAuthFailed,
			token: "/slack-token", profile: "/slack-userinfo", workspace: true, relinks: true,
			setup: func(t *testing.T) *providerRun {
				t.Helper()

				svc, ctx := setupSlackTestService(t)
				fake := newFakeProvider(t,
					fakeRoute{"/slack-token", `{"ok":true,"access_token":"xoxb-bot","team":{"id":"T-1","name":"Acme"},` +
						`"authed_user":{"id":"U-1","access_token":"xoxp-user"}}`},
					fakeRoute{"/slack-userinfo", `{"ok":true,"sub":"U-1","email":"member@acme.com",` +
						`"email_verified":true,"name":"Member","https://slack.com/team_domain":"acme"}`},
				)
				svc.oauthURL = fake.url + "/slack-token"
				svc.userInfoURL = fake.url + "/slack-userinfo"

				return &providerRun{db: svc.db, fake: fake, fault: installQueryFault(svc.db), callback: func() error {
					_, cbErr := svc.HandleCallback(ctx, "code")

					return cbErr
				}}
			},
		},
	}
}

// TestProviderCallbackWhenTheProviderMisbehaves scripts each endpoint of each
// provider to answer garbage, an error status, half a body or nothing. The
// callback must fail with the provider's sentinel, which is what the error
// redirect is built from, and must not create an account.
func TestProviderCallbackWhenTheProviderMisbehaves(t *testing.T) {
	t.Parallel()

	type failure struct {
		name    string
		reply   fakeReply
		wantMsg string
	}

	garbage := failure{"answers garbage", fakeReply{body: "<html>oops</html>"}, "failed to parse response"}
	truncated := failure{"hangs up mid-body", fakeReply{mode: replyTruncated, body: `{"a`}, "failed to read response"}
	silent := failure{"hangs up without answering", fakeReply{mode: replyHangUp}, "failed to"}

	for _, spec := range providerSpecs() {
		endpoints := []struct {
			name     string
			suffix   string
			sentinel error
			failures []failure
		}{
			{"token endpoint", spec.token, spec.tokenErr, []failure{garbage, truncated, silent}},
			{"profile endpoint", spec.profile, spec.profileErr, []failure{
				garbage, truncated, silent,
				{"answers 500", fakeReply{status: http.StatusInternalServerError, body: `{"ok":false}`}, ""},
			}},
			{"second API endpoint", spec.extra, spec.extraErr, []failure{
				garbage, truncated, silent,
				{"answers 500", fakeReply{status: http.StatusInternalServerError, body: `[]`}, "status 500"},
			}},
		}

		for _, endpoint := range endpoints {
			if endpoint.suffix == "" {
				continue
			}

			for _, fail := range endpoint.failures {
				t.Run(spec.name+"/"+endpoint.name+" "+fail.name, func(t *testing.T) {
					t.Parallel()

					run := spec.setup(t)
					users := run.userCount(t)
					run.fake.script(endpoint.suffix, fail.reply)

					err := run.callback()
					require.ErrorIs(t, err, endpoint.sentinel)
					require.ErrorContains(t, err, fail.wantMsg)
					require.Equal(t, users, run.userCount(t), "a failed sign-in creates no account")
				})
			}
		}

		t.Run(spec.name+"/control: a healthy provider signs the user in", func(t *testing.T) {
			t.Parallel()

			run := spec.setup(t)
			users := run.userCount(t)

			require.NoError(t, run.callback())
			require.Equal(t, users+1, run.userCount(t))
		})
	}
}

// TestProviderCallbackRefusals covers the answers a provider gives on
// purpose: an error document from the token endpoint, a profile without a
// verified email.
func TestProviderCallbackRefusals(t *testing.T) {
	t.Parallel()

	oauthError := `{"error":"bad_verification_code","error_description":"The code has expired"}`

	tests := []struct {
		provider string
		name     string
		suffix   string
		reply    fakeReply
		want     error
		wantMsg  string
	}{
		{
			"github", "token refused", "/login/oauth/access_token",
			fakeReply{body: oauthError},
			ErrGitHubTokenExchange, "bad_verification_code: The code has expired",
		},
		{
			"google", "token refused", "/token",
			fakeReply{body: oauthError},
			ErrGoogleTokenExchange, "bad_verification_code: The code has expired",
		},
		{
			"gitlab", "token refused", "/oauth/token",
			fakeReply{body: oauthError},
			ErrGitLabTokenExchange, "bad_verification_code: The code has expired",
		},
		{
			"microsoft", "token refused", "/token",
			fakeReply{body: oauthError},
			ErrMicrosoftTokenExchange, "bad_verification_code: The code has expired",
		},
		{
			"discord", "token refused", "/oauth2/token",
			fakeReply{status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`},
			ErrDiscordTokenExchange, "status 400",
		},
		{
			"slack", "user info refused", "/slack-userinfo",
			fakeReply{body: `{"ok":false,"error":"token_revoked"}`},
			ErrSlackOAuthFailed, "token_revoked",
		},
		{
			"github", "no verified primary email", "/user/emails",
			fakeReply{body: `[{"email":"member@acme.com","primary":true,"verified":false}]`},
			ErrEmailNotVerified, "",
		},
		{
			"gitlab", "profile without email", "/api/v4/user",
			fakeReply{body: `{"id":7,"username":"gluser","confirmed_at":"` + gitLabConfirmedAt + `"}`},
			ErrEmailNotVerified, "",
		},
		{
			"gitlab", "profile email missing from the confirmed list", "/api/v4/user/emails",
			fakeReply{body: `[{"id":1,"email":"other@acme.com","confirmed_at":"` + gitLabConfirmedAt + `"}]`},
			ErrEmailNotVerified, "",
		},
		{
			"discord", "unverified email", "/users/@me",
			fakeReply{body: `{"id":"du-1","username":"member","email":"member@acme.com","verified":false}`},
			ErrEmailNotVerified, "",
		},
		{
			"slack", "no email", "/slack-userinfo",
			fakeReply{body: `{"ok":true,"sub":"U-1"}`},
			ErrEmailNotVerified, "",
		},
		{
			"slack", "unverified email", "/slack-userinfo",
			fakeReply{body: `{"ok":true,"sub":"U-1","email":"member@acme.com","email_verified":false}`},
			ErrEmailNotVerified, "",
		},
	}

	specs := map[string]providerSpec{}
	for _, spec := range providerSpecs() {
		specs[spec.name] = spec
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.name, func(t *testing.T) {
			t.Parallel()

			run := specs[tt.provider].setup(t)
			users := run.userCount(t)
			run.fake.script(tt.suffix, tt.reply)

			err := run.callback()
			require.ErrorIs(t, err, tt.want)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Equal(t, users, run.userCount(t))
		})
	}
}

// TestProviderCallbackWhenTheStoreFails fails one store call of a first
// sign-in at a time. The callback answers an error naming the step. For the
// providers that link an existing account by email, the sign-in then simply
// works when retried.
func TestProviderCallbackWhenTheStoreFails(t *testing.T) {
	t.Parallel()

	type step struct {
		name    string
		fault   sqlFault
		wantMsg string
	}

	account := []step{
		{"looking the provider link up", failingSQL(`FROM "user_providers"`), "failed to get user provider"},
		{"looking the account up by email", failingSQL(`FROM "users"`), "failed to get user by email"},
		{"creating the account", failingSQL(`INSERT INTO "users"`), "failed to create user"},
		{"creating the provider link", failingSQL(`INSERT INTO "user_providers"`), "failed to create user provider"},
		{"storing the session", failingSQL(`INSERT INTO "user_tokens"`), ""},
	}
	bySlug := []step{
		{"reading the organization", failingSQL(`FROM "organizations"`), "organization not found"},
	}
	byWorkspace := []step{
		{
			"looking the workspace link up", failingSQL(`FROM "organization_providers"`),
			"failed to get organization provider",
		},
		{"creating the organization", failingSQL(`INSERT INTO "organizations"`), "failed to create organization"},
		{
			"creating the workspace link", failingSQL(`INSERT INTO "organization_providers"`),
			"failed to create organization provider",
		},
	}

	for _, spec := range providerSpecs() {
		steps := append([]step{}, account...)
		if spec.workspace {
			steps = append(steps, byWorkspace...)
		} else {
			steps = append(steps, bySlug...)
		}

		for _, st := range steps {
			t.Run(spec.name+"/"+st.name, func(t *testing.T) {
				t.Parallel()

				run := spec.setup(t)

				var err error

				run.fault.during(t, st.fault, func() { err = run.callback() })
				require.ErrorIs(t, err, context.Canceled)
				require.ErrorContains(t, err, st.wantMsg)

				if spec.relinks {
					require.NoError(t, run.callback(), "the sign-in works once the store is back")
				}
			})
		}

		t.Run(spec.name+"/reading the account behind an existing link", func(t *testing.T) {
			t.Parallel()

			run := spec.setup(t)
			require.NoError(t, run.callback())

			var err error

			run.fault.during(t, failingSQL(`FROM "users"`), func() { err = run.callback() })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, "failed to get user behind provider link")
			require.NoError(t, run.callback())
		})

		if !spec.workspace {
			continue
		}

		t.Run(spec.name+"/reading the organization behind an existing workspace link", func(t *testing.T) {
			t.Parallel()

			run := spec.setup(t)
			require.NoError(t, run.callback())

			var err error

			run.fault.during(t, failingSQL(`FROM "organizations"`), func() { err = run.callback() })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, "failed to get organization behind provider link")
			require.NoError(t, run.callback())
		})
	}
}

// oauthStateStore is the state API the redirect-based providers share.
type oauthStateStore struct {
	name     string
	prefix   string
	db       db.Service
	generate func() (string, error)
	validate func(nonce string) (*OAuthState, error)
}

func oauthStateStores(t *testing.T) []oauthStateStore {
	t.Helper()

	github, ctx := setupGitHubTestService(t)
	google, _ := setupGoogleTestService(t)
	gitlab, _ := setupGitLabTestService(t)
	microsoft, _ := setupMicrosoftTestService(t)
	discord := newDiscordTestService(t, newSQLiteDBService(t))

	return []oauthStateStore{
		{
			"github", gitHubOAuthStatePrefix, github.db,
			func() (string, error) { return github.GenerateOAuthState(ctx, "/d", "acme") },
			func(nonce string) (*OAuthState, error) { return github.ValidateOAuthState(ctx, nonce) },
		},
		{
			"google", googleOAuthStatePrefix, google.db,
			func() (string, error) { return google.GenerateOAuthState(ctx, "/d", "acme") },
			func(nonce string) (*OAuthState, error) { return google.ValidateOAuthState(ctx, nonce) },
		},
		{
			"gitlab", gitLabOAuthStatePrefix, gitlab.db,
			func() (string, error) { return gitlab.GenerateOAuthState(ctx, "/d", "acme") },
			func(nonce string) (*OAuthState, error) { return gitlab.ValidateOAuthState(ctx, nonce) },
		},
		{
			"microsoft", microsoftOAuthStatePrefix, microsoft.db,
			func() (string, error) { return microsoft.GenerateOAuthState(ctx, "/d", "acme") },
			func(nonce string) (*OAuthState, error) { return microsoft.ValidateOAuthState(ctx, nonce) },
		},
		{
			"discord", discordOAuthStatePrefix, discord.db,
			func() (string, error) { return discord.GenerateOAuthState(ctx, "/d") },
			func(nonce string) (*OAuthState, error) { return discord.ValidateOAuthState(ctx, nonce) },
		},
	}
}

// TestOAuthStateIsRefusedWhenUnusable: a state entry that is not a state
// document, or that outlived its TTL, is refused like an unknown one, and a
// state that could not be stored fails the login redirect.
func TestOAuthStateIsRefusedWhenUnusable(t *testing.T) { //nolint:tparallel // subtests share one service per provider
	t.Parallel()

	for _, store := range oauthStateStores(t) {
		runOAuthStateRefusals(t, store)
	}
}

// runOAuthStateRefusals is the state contract of one provider. Its subtests
// run in sequence on the store's database.
func runOAuthStateRefusals(t *testing.T, store oauthStateStore) {
	t.Helper()

	ctx := t.Context()
	stale := jsonBody(t, OAuthState{Nonce: "stale", RedirectURI: "/d", CreatedAt: time.Now().Add(-time.Hour).Unix()})

	entries := map[string]models.JSONMap{
		"state that is not a string":   {keyState: 42},
		"state that is not a document": {keyState: "{"},
		"state older than its TTL":     {keyState: stale},
	}

	for name, value := range entries {
		t.Run(store.name+"/"+name, func(t *testing.T) {
			nonce := "nonce-" + strings.ReplaceAll(name, " ", "-")
			require.NoError(t, store.db.SetStateEntry(ctx, nil, store.prefix+nonce, &value, nil))

			state, err := store.validate(nonce)
			require.ErrorIs(t, err, ErrInvalidOAuthState)
			require.Nil(t, state)

			entry, err := store.db.GetStateEntry(ctx, nil, store.prefix+nonce)
			require.NoError(t, err)
			require.Nil(t, entry, "a refused state is consumed all the same")
		})
	}

	t.Run(store.name+"/unknown state", func(t *testing.T) {
		state, err := store.validate("no-such-nonce")
		require.ErrorIs(t, err, ErrInvalidOAuthState)
		require.Nil(t, state)
	})

	t.Run(store.name+"/state that cannot be stored", func(t *testing.T) {
		fault := installQueryFault(store.db)

		var err error

		fault.during(t, failingSQL(`"state_entries"`), func() { _, err = store.generate() })
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "failed to store state")

		// Control: the same call mints a state that validates once.
		nonce, err := store.generate()
		require.NoError(t, err)

		state, err := store.validate(nonce)
		require.NoError(t, err)
		require.Equal(t, "/d", state.RedirectURI)
	})
}

// TestStaleProviderLinkThatCannotBeCleared: a link to a deleted organization
// or account is cleared on the next sign-in. When the clearing fails, the
// sign-in fails with that cause, and works once the store is back.
func TestStaleProviderLinkThatCannotBeCleared(t *testing.T) {
	t.Parallel()

	specs := map[string]providerSpec{}
	for _, spec := range providerSpecs() {
		specs[spec.name] = spec
	}

	t.Run("workspace link of a deleted organization", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		run := specs["slack"].setup(t)
		ctx := t.Context()
		r.NoError(run.callback())

		link, err := run.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, "T-1")
		r.NoError(err)
		r.NoError(run.db.DeleteOrganization(ctx, link.OrganizationUID))

		run.fault.during(t, failingSQL(`UPDATE "organization_providers"`), func() { err = run.callback() })
		r.ErrorIs(err, context.Canceled)
		r.ErrorContains(err, "failed to clear stale organization provider link")

		r.NoError(run.callback())

		healed, err := run.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, "T-1")
		r.NoError(err)
		r.NotEqual(link.OrganizationUID, healed.OrganizationUID, "the workspace is linked to a fresh organization")
	})

	t.Run("identity link of a deleted account", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		run := specs["google"].setup(t)
		ctx := t.Context()
		r.NoError(run.callback())

		link, err := run.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGoogle, "g-1")
		r.NoError(err)
		r.NoError(run.db.DeleteUser(ctx, link.UserUID))

		run.fault.during(t, failingSQL(`DELETE FROM "user_providers"`), func() { err = run.callback() })
		r.ErrorIs(err, context.Canceled)
		r.ErrorContains(err, "failed to clear stale user provider link")

		r.NoError(run.callback())

		healed, err := run.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGoogle, "g-1")
		r.NoError(err)
		r.NotEqual(link.UserUID, healed.UserUID, "the identity is linked to a fresh account")
	})
}

// TestGitLabCallbackWithAnUnusableBaseURL: the GitLab base URL is operator
// configuration. One that is not a URL fails the token exchange.
func TestGitLabCallbackWithAnUnusableBaseURL(t *testing.T) {
	t.Parallel()

	svc, ctx := setupGitLabTestService(t)
	org := setupGitLabTestOrg(ctx, t, svc)
	svc.cfg.GitLab.BaseURL = "://not-a-url"

	_, err := svc.HandleCallback(ctx, "code", org.Slug)
	require.ErrorIs(t, err, ErrGitLabTokenExchange)
	require.ErrorContains(t, err, "failed to create request")
}

func TestGitLabEmailConfirmedNeedsAPrimaryEmail(t *testing.T) {
	t.Parallel()

	confirmedAt := gitLabConfirmedAt
	emails := []GitLabEmail{{Email: "", ConfirmedAt: &confirmedAt}, {Email: "member@acme.com", ConfirmedAt: &confirmedAt}}

	require.False(t, gitLabEmailConfirmed("", emails))
	require.True(t, gitLabEmailConfirmed(" Member@acme.com ", emails))
}

// TestGoogleEndpointsDefaultToGoogle pins the production endpoints: the URL
// fields only exist so tests can point the service elsewhere.
func TestGoogleEndpointsDefaultToGoogle(t *testing.T) {
	t.Parallel()

	svc, _ := setupGoogleTestService(t)
	require.Equal(t, "https://oauth2.googleapis.com/token", svc.getTokenURL())
	require.Equal(t, "https://www.googleapis.com/oauth2/v3/userinfo", svc.getUserInfoURL())

	svc.tokenURL = "http://127.0.0.1/token"
	svc.userInfoURL = "http://127.0.0.1/userinfo"
	require.Equal(t, "http://127.0.0.1/token", svc.getTokenURL())
	require.Equal(t, "http://127.0.0.1/userinfo", svc.getUserInfoURL())
}

func TestDiscordAvatarURL(t *testing.T) {
	t.Parallel()

	require.Empty(t, (&DiscordUserInfo{ID: "42"}).AvatarURL())
	require.Equal(t, "https://cdn.discordapp.com/avatars/42/abc.png",
		(&DiscordUserInfo{ID: "42", Avatar: "abc"}).AvatarURL())
}

// TestSlackInstallCodeExchange: the single-use code minted at the end of a
// Slack install redeems once for the session it carries, and only when that
// session is complete.
func TestSlackInstallCodeExchange(t *testing.T) {
	t.Parallel()

	svc, ctx := setupSlackTestService(t)

	mint := func(t *testing.T, payload map[string]any) string {
		t.Helper()

		code, err := oauthstate.Generate(ctx, svc.db, "slack-exchange", payload, time.Minute)
		require.NoError(t, err)

		return code
	}

	full := func() map[string]any {
		return map[string]any{
			"accessToken": "access", "refreshToken": "refresh", "orgSlug": "acme", "userUID": "user-1",
			"channelUid": "channel-1", "expiresIn": 3600,
		}
	}

	t.Run("redeems once", func(t *testing.T) {
		t.Parallel()

		code := mint(t, full())

		result, err := svc.ExchangeSlackInstallCode(ctx, code)
		require.NoError(t, err)
		require.Equal(t, &SlackExchangeResult{
			AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600,
			OrgSlug: "acme", UserUID: "user-1", ChannelUID: "channel-1",
		}, result)

		_, err = svc.ExchangeSlackInstallCode(ctx, code)
		require.ErrorIs(t, err, ErrInvalidOAuthState)
	})

	refused := map[string]func() map[string]any{
		"no payload": func() map[string]any { return nil },
		"no access token": func() map[string]any {
			payload := full()
			delete(payload, "accessToken")

			return payload
		},
		"no refresh token": func() map[string]any {
			payload := full()
			payload["refreshToken"] = ""

			return payload
		},
		"no organization": func() map[string]any {
			payload := full()
			delete(payload, "orgSlug")

			return payload
		},
		"no user": func() map[string]any {
			payload := full()
			delete(payload, "userUID")

			return payload
		},
	}
	for name, payload := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result, err := svc.ExchangeSlackInstallCode(ctx, mint(t, payload()))
			require.ErrorIs(t, err, ErrInvalidOAuthState)
			require.Nil(t, result)
		})
	}

	t.Run("unknown code", func(t *testing.T) {
		t.Parallel()

		_, err := svc.ExchangeSlackInstallCode(ctx, "no-such-code")
		require.ErrorIs(t, err, ErrInvalidOAuthState)
	})
}

// TestSlackSignInState: the Slack sign-in state lives in the shared
// oauthstate store.
func TestSlackSignInState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, ctx := setupSlackTestService(t)
	fault := installQueryFault(svc.db)

	var err error

	fault.during(t, failingSQL(`"state_entries"`), func() { _, err = svc.GenerateOAuthState(ctx, "/d") })
	r.ErrorIs(err, context.Canceled)
	r.ErrorContains(err, "failed to generate oauth state")

	_, err = svc.ValidateOAuthState(ctx, "no-such-state")
	r.ErrorIs(err, ErrInvalidOAuthState)

	nonce, err := svc.GenerateOAuthState(ctx, "/d")
	r.NoError(err)

	state, err := svc.ValidateOAuthState(ctx, nonce)
	r.NoError(err)
	r.Equal("/d", state.RedirectURI)
}
