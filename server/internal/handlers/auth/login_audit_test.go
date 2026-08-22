package auth

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// ---------------------------------------------------------------------------
// The structural guard
// ---------------------------------------------------------------------------

// funcBodies splits a Go source file into top-level function bodies keyed by
// name. Crude but sufficient and dependency-free: every function in this
// package starts at column 0 with `func `, so a scan for that prefix slices
// them exactly.
func funcBodies(t *testing.T, source string) map[string]string {
	t.Helper()

	nameRE := regexp.MustCompile(`^func (?:\([^)]*\) )?(\w+)`)

	bodies := map[string]string{}
	current := ""

	var builder strings.Builder

	flush := func() {
		if current != "" {
			bodies[current] += builder.String()
		}

		builder.Reset()
	}

	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(line, "func ") {
			flush()

			match := nameRE.FindStringSubmatch(line)
			if match == nil {
				current = ""

				continue
			}

			current = match[1]
		}

		builder.WriteString(line)
		builder.WriteString("\n")
	}

	flush()

	return bodies
}

// authPackageSources reads every non-test .go file in this package.
func authPackageSources(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	out := map[string]string{}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		content, readErr := os.ReadFile(filepath.Clean(name))
		require.NoError(t, readErr)

		out[name] = string(content)
	}

	require.NotEmpty(t, out, "positive control: the package sources must have been read")

	return out
}

// TestEverySessionMintingPathGoesThroughStartSession is the guard the audit
// asked for, and it is deliberately STRUCTURAL rather than behavioral.
//
// The first cut of spec 2026-08-21-09 emitted auth.login_succeeded from
// completeLogin on the belief that it was "the single funnel". It was not:
// federated logins mint their session in GenerateTokensForOAuth, 2FA logins in
// completeLoginAfter2FA, and org switches, registration confirmations,
// invitation acceptances and org creation each mint their own. An SSO-only or
// 2FA-enforcing organization therefore had an audit log containing zero
// logins — and no behavioral test would have caught it, because you cannot
// write a test for a code path you forgot exists.
//
// So this asserts the shape instead: a session IS a `refresh`-type user_tokens
// row, and any function that creates one must route it through startSession.
// A NEW login path added next year fails this test the moment it is written.
func TestEverySessionMintingPathGoesThroughStartSession(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var (
		builders    []string
		offenders   []string
		createCalls []string
		routed      = map[string]bool{}
	)

	for filename, source := range authPackageSources(t) {
		for name, body := range funcBodies(t, source) {
			buildsRefreshRow := strings.Contains(body, "models.TokenTypeRefresh)") &&
				strings.Contains(body, "models.NewUserToken(")

			if buildsRefreshRow {
				builders = append(builders, filename+":"+name)

				// Either it hands the session to startSession itself, or it is
				// a pure builder handing the row back to a caller that will.
				handsOff := strings.Contains(body, "s.startSession(") ||
					strings.Contains(body, ") *models.UserToken {")

				if !handsOff {
					offenders = append(offenders, filename+":"+name)
				}
			}

			if strings.Contains(body, "s.startSession(") {
				routed[name] = true
			}

			// The other half of the same rule: nothing may call
			// CreateUserToken directly except startSession (which is where a
			// session becomes real and the event is written) and CreatePAT
			// (which mints a PAT, not a session, and has its own
			// auth.token_created event).
			if strings.Contains(body, "s.db.CreateUserToken(") &&
				name != "startSession" && name != "CreatePAT" {
				createCalls = append(createCalls, filename+":"+name)
			}
		}
	}

	// Positive controls: the scan really found the paths, so an empty
	// `offenders` cannot mean "the matcher matched nothing". These are the
	// exact funnels the first cut of the spec missed.
	r.GreaterOrEqualf(len(builders), 6,
		"expected every known session builder to be found, got %v", builders)

	for _, required := range []string{
		"completeLogin",
		"GenerateTokensForOAuth",
		"completeLoginAfter2FA",
		"SwitchOrg",
		"AcceptInvite",
		"ConfirmRegistration",
		"mintOrgSession",
	} {
		r.Truef(routed[required],
			"%s mints a session but does not route it through startSession — its logins "+
				"would be invisible in the audit trail", required)
	}

	r.Emptyf(offenders,
		"these functions mint a session (a models.TokenTypeRefresh user_tokens row) without "+
			"going through Service.startSession, so their logins are invisible in the audit "+
			"trail: %v — route them through startSession rather than emitting by hand",
		offenders)

	r.Emptyf(createCalls,
		"these functions call db.CreateUserToken directly: %v — session rows must go through "+
			"Service.startSession so auth.login_succeeded cannot be skipped",
		createCalls)
}

// TestAdvertisedAuthMethodsAreProducible pins the docs to the code. event.go
// and the event catalog both advertise the federated auth_method values; if
// no connector can actually produce one, the documentation is a lie about a
// security control.
func TestAdvertisedAuthMethodsAreProducible(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	joined := strings.Builder{}
	for _, source := range authPackageSources(t) {
		joined.WriteString(source)
	}

	all := joined.String()

	// Keyed by the CONSTANT NAME, valued by the constant itself — so this test
	// restates no literal of its own (which would be a second source of truth,
	// and would drift).
	connectors := map[string]string{
		"signupMethodOIDC":      signupMethodOIDC,
		"signupMethodSAML":      signupMethodSAML,
		"signupMethodGitHub":    signupMethodGitHub,
		"signupMethodGitLab":    signupMethodGitLab,
		"signupMethodGoogle":    signupMethodGoogle,
		"signupMethodMicrosoft": signupMethodMicrosoft,
		"signupMethodDiscord":   signupMethodDiscord,
		"signupMethodSlack":     signupMethodSlack,
	}

	// The docs advertise these auth_method values. Reading the shipped file
	// rather than restating the list is what stops the documentation from
	// promising a value no connector can produce — which is exactly the state
	// the first cut of this spec shipped in.
	catalog, err := os.ReadFile(filepath.Clean(
		filepath.Join("..", "..", "..", "..", "wiki", "api-specification", "events-catalogue.md"),
	))
	r.NoError(err)

	catalogText := string(catalog)
	r.Contains(catalogText, "auth.login_succeeded",
		"positive control: the catalog file really was read")

	for name, value := range connectors {
		// require.Contains on a multi-megabyte string dumps the whole package
		// into the failure message, so compute the boolean first.
		named := strings.Contains(all, "WithLoginMethod("+name+")")
		r.Truef(named,
			"auth_method %s (%q) is advertised but no connector names itself with it — "+
				"its logins would all be recorded as the generic %q",
			name, value, AuthMethodOAuth)

		r.NotEmptyf(value, "%s must carry a value", name)
		r.Containsf(catalogText, value,
			"the event catalog must advertise the %s auth_method it can now produce", name)
	}
}

// ---------------------------------------------------------------------------
// The behavioral table
// ---------------------------------------------------------------------------

// totpTestIssuer is this test's own TOTP issuer label. Deliberately not the
// product's, which the real code owns.
const totpTestIssuer = "SolidPing Test"

type loginAuditFixture struct {
	svc *Service
	db  db.Service
	org *models.Organization
}

func newLoginAuditFixture(t *testing.T) (*loginAuditFixture, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
	}

	org := models.NewOrganization("acmeaudit", "Acme")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	return &loginAuditFixture{
		svc: NewService(dbSvc, cfg.Auth, cfg, nil, nil),
		db:  dbSvc,
		org: org,
	}, ctx
}

func (f *loginAuditFixture) user(ctx context.Context, t *testing.T, email string) *models.User {
	t.Helper()

	user := models.NewUser(email)
	require.NoError(t, f.db.CreateUser(ctx, user))
	require.NoError(t, f.db.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(f.org.UID, user.UID, models.MemberRoleAdmin)))

	return user
}

func (f *loginAuditFixture) events(ctx context.Context, t *testing.T, family string) []*models.Event {
	t.Helper()

	rows, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID:   f.org.UID,
		EventTypePrefixes: []string{family},
		Limit:             100,
	})
	require.NoError(t, err)

	return rows
}

// TestEverySessionFunnelEmitsLoginSucceeded drives each session-minting funnel
// for real and asserts an attributed auth.login_succeeded lands with the right
// auth_method.
//
// The structural guard above proves nothing was FORGOTTEN; this proves the
// events that do get emitted are correct and attributed. Both are needed: a
// path could route through startSession and still record the wrong method.
func TestEverySessionFunnelEmitsLoginSucceeded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wantMethod string
		mint       func(ctx context.Context, t *testing.T, f *loginAuditFixture, user *models.User) error
	}{
		{
			name:       "password login",
			wantMethod: AuthMethodPassword,
			mint: func(ctx context.Context, t *testing.T, f *loginAuditFixture, user *models.User) error {
				t.Helper()
				_, err := f.svc.completeLogin(ctx, user, f.org, string(models.MemberRoleAdmin),
					LoginActionDefault, nil, AuthMethodPassword, Context{RemoteAddr: "203.0.113.7"})

				return err
			},
		},
		{
			// The bug this whole punch-list item exists for: every federated
			// connector lands here, and it used to emit nothing at all.
			name:       "federated login names its provider",
			wantMethod: signupMethodOIDC,
			mint: func(ctx context.Context, t *testing.T, f *loginAuditFixture, user *models.User) error {
				t.Helper()
				_, err := f.svc.GenerateTokensForOAuth(ctx, user, f.org,
					string(models.MemberRoleUser), signupMethodOIDC, Context{RemoteAddr: "203.0.113.7"})

				return err
			},
		},
		{
			// A connector that forgets WithLoginMethod still produces an
			// event — just a less specific one. Silence is never acceptable.
			name:       "federated login with no named provider still records",
			wantMethod: AuthMethodOAuth,
			mint: func(ctx context.Context, t *testing.T, f *loginAuditFixture, user *models.User) error {
				t.Helper()
				_, err := f.svc.GenerateTokensForOAuth(ctx, user, f.org,
					string(models.MemberRoleUser), "", Context{})

				return err
			},
		},
		{
			// A TOTP user's sign-ins were entirely unaudited, and the first
			// factor was lost at the hand-off.
			name:       "2FA login records both factors",
			wantMethod: "password+totp",
			mint: func(ctx context.Context, t *testing.T, f *loginAuditFixture, user *models.User) error {
				t.Helper()

				_, err := f.svc.completeLoginAfter2FA(ctx, user, f.org.Slug,
					string(models.MemberRoleAdmin),
					withSecondFactor(AuthMethodPassword, SecondFactorTOTP),
					Context{RemoteAddr: "203.0.113.7"})

				return err
			},
		},
		{
			name:       "2FA via recovery code records the recovery path",
			wantMethod: "ldap+recovery_code",
			mint: func(ctx context.Context, t *testing.T, f *loginAuditFixture, user *models.User) error {
				t.Helper()

				_, err := f.svc.completeLoginAfter2FA(ctx, user, f.org.Slug,
					string(models.MemberRoleAdmin),
					withSecondFactor(AuthMethodLDAP, SecondFactorRecoveryCode),
					Context{})

				return err
			},
		},
		{
			name:       "switching organization mints an audited session",
			wantMethod: AuthMethodSwitchOrg,
			mint: func(ctx context.Context, t *testing.T, f *loginAuditFixture, user *models.User) error {
				t.Helper()
				_, err := f.svc.SwitchOrg(ctx, user.UID, f.org.Slug, Context{RemoteAddr: "203.0.113.7"})

				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f, ctx := newLoginAuditFixture(t)
			user := f.user(ctx, t, "alice@acme.com")

			r.NoError(tc.mint(ctx, t, f, user))

			rows := f.events(ctx, t, "auth")
			r.Len(rows, 1, "exactly one auth.login_succeeded per minted session")
			r.Equal(models.EventTypeAuthLoginSucceeded, rows[0].EventType)
			r.Equal(tc.wantMethod, rows[0].Payload["auth_method"])
			r.Equal("alice@acme.com", rows[0].Payload["email"])

			// Attributed to the person, not to "system".
			r.Equal(models.ActorTypeUser, rows[0].ActorType)
			r.NotNil(rows[0].ActorUID)
			r.Equal(user.UID, *rows[0].ActorUID)
		})
	}
}

// TestCompleteLoginAfter2FAIsReachableThroughVerify2FA closes the loop between
// the table above (which calls completeLoginAfter2FA directly) and the real
// entry point, including that the FIRST factor survives the temp token.
func TestCompleteLoginAfter2FAIsReachableThroughVerify2FA(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newLoginAuditFixture(t)
	user := f.user(ctx, t, "bob@acme.com")

	key, err := totp.Generate(totp.GenerateOpts{Issuer: totpTestIssuer, AccountName: user.Email})
	r.NoError(err)

	secret := key.Secret()
	enabled := true
	r.NoError(f.db.UpdateUser(ctx, user.UID, &models.UserUpdate{
		TOTPSecret:  &secret,
		TOTPEnabled: &enabled,
	}))

	// The temp token is minted by Login with the first factor recorded on it.
	tempToken, err := f.svc.generate2FATempToken(user.UID, f.org.Slug,
		string(models.MemberRoleAdmin), AuthMethodLDAP)
	r.NoError(err)

	code, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	_, err = f.svc.Verify2FA(ctx, tempToken, code, Context{RemoteAddr: "203.0.113.7"})
	r.NoError(err)

	rows := f.events(ctx, t, "auth")
	r.Len(rows, 1)
	r.Equal("ldap+totp", rows[0].Payload["auth_method"],
		"the first factor must survive the 2FA hand-off, not be assumed to be a password")
	r.NotNil(rows[0].SourceIP)
}

// TestFederatedAutoJoinRecordsMemberJoined — an SSO org's membership must not
// appear in the trail from nowhere. The positive control is the login event in
// the same run: if CompleteOrgLogin had failed outright, neither would exist.
func TestFederatedAutoJoinRecordsMemberJoined(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newLoginAuditFixture(t)

	// Seed a member so the org is past the zero-member bootstrap rule, then
	// let the email pattern admit the newcomer.
	f.user(ctx, t, "seed@acme.com")
	r.NoError(f.db.SetOrgParameter(ctx, f.org.UID, registrationEmailPatternKey, `@acme\.com$`, false))

	newcomer := models.NewUser("carol@acme.com")
	r.NoError(f.db.CreateUser(ctx, newcomer))

	result, err := f.svc.CompleteOrgLogin(ctx, f.org, newcomer, WithLoginMethod(signupMethodSAML))
	r.NoError(err)
	r.False(result.Pending, "the email pattern must have admitted this user")

	memberEvents := f.events(ctx, t, "member")
	r.Len(memberEvents, 1)
	r.Equal(models.EventTypeMemberJoined, memberEvents[0].EventType)
	r.Equal("carol@acme.com", memberEvents[0].Payload["email"])
	r.Equal(joinSourceEmailPattern, memberEvents[0].Payload["source"])
	r.Equal(string(models.MemberRoleUser), memberEvents[0].Payload["role"])

	// Positive control + the federated login half.
	authEvents := f.events(ctx, t, "auth")
	r.Len(authEvents, 1)
	r.Equal(signupMethodSAML, authEvents[0].Payload["auth_method"])
}

// TestPendingFederatedLoginRecordsNoSession — a user who authenticated but was
// NOT admitted gets no org-scoped session, so there is no login to record
// against that org. Recording one would claim access that was never granted.
func TestPendingFederatedLoginRecordsNoSession(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newLoginAuditFixture(t)

	f.user(ctx, t, "seed@acme.com")

	stranger := models.NewUser("mallory@evil.example")
	r.NoError(f.db.CreateUser(ctx, stranger))

	result, err := f.svc.CompleteOrgLogin(ctx, f.org, stranger, WithLoginMethod(signupMethodGitHub))
	r.NoError(err)
	r.True(result.Pending, "an unmatched stranger must not be admitted")

	r.Empty(f.events(ctx, t, "auth"), "no session was minted, so no login may be recorded")
	r.Empty(f.events(ctx, t, "member"), "no membership was created either")
}

// TestLoginAuditRedactsNothingSensitive — the payload assembled by this
// package must not carry a credential under any key.
func TestLoginAuditRedactsNothingSensitive(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newLoginAuditFixture(t)
	user := f.user(ctx, t, "alice@acme.com")

	_, err := f.svc.GenerateTokensForOAuth(ctx, user, f.org,
		string(models.MemberRoleUser), signupMethodOIDC, Context{})
	r.NoError(err)

	rows := f.events(ctx, t, "auth")
	r.Len(rows, 1)

	// Positive control: the fields the trail is FOR are present.
	r.NotEmpty(rows[0].Payload["auth_method"])

	for key := range rows[0].Payload {
		r.Falsef(audit.IsSensitiveKey(key), "payload key %q would carry a secret", key)
	}
}
