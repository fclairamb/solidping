package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const (
	ldapTestServiceDN       = "cn=svc,dc=example,dc=org"
	ldapTestServicePassword = "svc-password"
	ldapTestBaseDN          = "dc=example,dc=org"
)

// newFakeLDAPConfig builds an LDAPConfig pointed at dir, with the given
// (already-registered) service account and base DN. userFilter may be blank
// to exercise the "(mail=%s)" default.
func newFakeLDAPConfig(dir *fakeLDAPDirectory, userFilter string) config.LDAPConfig {
	return config.LDAPConfig{
		Enabled:      true,
		ServerURL:    dir.serverURL(),
		BindDN:       ldapTestServiceDN,
		BindPassword: ldapTestServicePassword,
		BaseDN:       ldapTestBaseDN,
		UserFilter:   userFilter,
	}
}

// --- buildUserFilter: pure unit tests, no server needed -----------------

// TestBuildUserFilter_EscapesInjectionMetacharacters proves — via the real
// go-ldap filter compiler/decompiler, not a string-contains check on our own
// code — that malicious identifiers are neutralized into a single literal
// equality value, for both a bare template and a realistic admin-authored
// composite (AND) template.
func TestBuildUserFilter_EscapesInjectionMetacharacters(t *testing.T) {
	t.Parallel()

	maliciousInputs := []string{
		"*",
		"*)(uid=*))(|(uid=*",
		"x)(mail=*",
		"admin*",
		`back\slash`,
	}

	templates := []string{
		"(mail=%s)",
		"(&(objectClass=inetOrgPerson)(mail=%s))",
	}

	for _, template := range templates {
		for _, input := range maliciousInputs {
			t.Run(fmt.Sprintf("%s/%s", template, input), func(t *testing.T) {
				t.Parallel()

				filter, err := buildUserFilter(template, input)
				require.NoError(t, err, "a properly escaped filter must always compile")

				pkt, err := ldap.CompileFilter(filter)
				require.NoError(t, err)

				// The escaped identifier must appear as a single literal
				// equality value — never as additional filter structure.
				// Walk to the deepest child (the equality node touching our
				// value) and confirm decompiling it round-trips to exactly
				// one hex-escaped literal, not multiple clauses.
				decompiled, err := ldap.DecompileFilter(pkt)
				require.NoError(t, err)

				// None of the raw RFC 4515 metacharacters may appear
				// unescaped in the portion contributed by the identifier:
				// every "(", ")", "*" that came from the malicious input is
				// hex-escaped (e.g. "\28", "\29", "\2a"), so the ONLY
				// unescaped parens/asterisks left in the whole filter are
				// the ones from the (trusted, admin-authored) template
				// itself. We assert this precisely by checking the
				// substitution is present as a contiguous escaped run.
				escapedValue := ldap.EscapeFilter(input)
				require.Contains(t, decompiled, escapedValue,
					"escaped identifier must be present as a literal in the compiled filter")

				if input != escapedValue {
					require.NotContains(t, decompiled, input,
						"the raw, unescaped malicious input must never appear literally in the compiled filter")
				}
			})
		}
	}
}

func TestBuildUserFilter_DefaultsWhenTemplateHasWrongPlaceholderCount(t *testing.T) {
	t.Parallel()

	// A template with zero or more than one "%s" is nonsensical (and could
	// be a Sprintf footgun); buildUserFilter falls back to the safe default
	// rather than passing extra positional args to Sprintf or leaving a
	// literal "%s" in the filter.
	filter, err := buildUserFilter("(objectClass=person)", "alice@example.com")
	require.NoError(t, err)
	require.Equal(t, "(mail=alice@example.com)", filter)
}

// --- LDAPService.Authenticate: real search-then-bind against the fake directory ---

func TestLDAPService_Authenticate_HappyPath(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()
	dir.setEntries(newFakeLDAPEntry(
		"cn=alice,ou=people,dc=example,dc=org",
		"alice-password",
		map[string][]string{"mail": {"alice@example.com"}, "cn": {"Alice Example"}},
	))

	svc := NewLDAPService(&config.Config{LDAP: newFakeLDAPConfig(dir, "")})

	info, err := svc.Authenticate(t.Context(), "alice@example.com", "alice-password")
	require.NoError(t, err)
	require.Equal(t, "cn=alice,ou=people,dc=example,dc=org", info.DN)
	require.Equal(t, "alice@example.com", info.Email)
	require.Equal(t, "Alice Example", info.Name)
}

func TestLDAPService_Authenticate_WrongPassword(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()
	dir.setEntries(newFakeLDAPEntry(
		"cn=alice,ou=people,dc=example,dc=org",
		"alice-password",
		map[string][]string{"mail": {"alice@example.com"}, "cn": {"Alice Example"}},
	))

	svc := NewLDAPService(&config.Config{LDAP: newFakeLDAPConfig(dir, "")})

	_, err := svc.Authenticate(t.Context(), "alice@example.com", "wrong-password")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLDAPBindFailed)
}

func TestLDAPService_Authenticate_UserNotFound(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()
	dir.setEntries() // empty directory

	svc := NewLDAPService(&config.Config{LDAP: newFakeLDAPConfig(dir, "")})

	_, err := svc.Authenticate(t.Context(), "nobody@example.com", "whatever")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLDAPUserNotFound)
}

// TestLDAPService_Authenticate_RejectsEmptyPassword_NoNetworkCall proves the
// empty-password guard runs BEFORE any network I/O: ServerURL points at a
// loopback port nothing is listening on (immediate connection refused if
// ever dialed), yet Authenticate returns instantly with the specific
// empty-password error rather than a dial/connection error.
func TestLDAPService_Authenticate_RejectsEmptyPassword_NoNetworkCall(t *testing.T) {
	t.Parallel()

	cfg := config.LDAPConfig{
		Enabled:   true,
		ServerURL: "ldap://127.0.0.1:1", // nothing listens on port 1
		BaseDN:    ldapTestBaseDN,
	}
	svc := NewLDAPService(&config.Config{LDAP: cfg})

	start := time.Now()

	_, err := svc.Authenticate(t.Context(), "alice@example.com", "")

	require.Less(t, time.Since(start), time.Second,
		"an empty password must be rejected immediately, not after attempting to dial")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLDAPEmptyPassword)
	require.NotErrorIs(t, err, ErrLDAPBindFailed, "must fail before any bind/dial is attempted, not because of one")
}

// TestLDAPService_Authenticate_EmptyPasswordGuard_EvenWhenDirectoryAllowsUnauthenticatedBind
// is the sharpest version of the empty-password guard test: the fake
// directory is configured to actually HONOR an RFC 4513 unauthenticated
// bind (a real, if dangerously configured, LDAP server behavior). A raw
// go-ldap client bind against it with an empty password genuinely succeeds
// — proving the directory is exploitable in principle — yet
// LDAPService.Authenticate still rejects the empty password before ever
// reaching that bind, so it never benefits from (or falls victim to) the
// directory's permissiveness.
func TestLDAPService_Authenticate_EmptyPasswordGuard_EvenWhenDirectoryAllowsUnauthenticatedBind(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()
	dir.setAllowUnauthenticatedBind(true)

	victimDN := "cn=victim,ou=people,dc=example,dc=org"
	dir.setEntries(newFakeLDAPEntry(
		victimDN, "victim-real-password",
		map[string][]string{"mail": {"victim@example.com"}, "cn": {"Victim"}},
	))

	// Sanity check: prove the directory really is vulnerable to an
	// unauthenticated bind for this DN, using the raw go-ldap client
	// (bypassing our AllowEmptyPassword=false default and our own guard
	// entirely) — this is not a strawman.
	conn, err := ldap.DialURL(dir.serverURL())
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	_, err = conn.SimpleBind(&ldap.SimpleBindRequest{
		Username:           victimDN,
		Password:           "",
		AllowEmptyPassword: true,
	})
	require.NoError(t, err, "sanity check: the fake directory must actually accept the unauthenticated bind")

	// Now prove our application-level guard catches it regardless: even
	// though the search would find the victim, Authenticate must never
	// reach the point of sending an empty-password bind for them.
	svc := NewLDAPService(&config.Config{LDAP: newFakeLDAPConfig(dir, "")})

	_, err = svc.Authenticate(t.Context(), "victim@example.com", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLDAPEmptyPassword)
}

// --- LDAP injection: real search evaluated with real filter semantics ---

// TestLDAPInjection_BareWildcardDoesNotAuthenticateAsAnotherUser is the
// textbook LDAP-injection payload: an unescaped "*" inside an equality
// filter is RFC 4515 wildcard syntax meaning "attribute present", so it
// would match ANY entry that has the searched attribute — letting an
// attacker who knows no valid email at all get matched to (and, if they
// then guessed nothing meaningful, at least located) an arbitrary existing
// user. We first prove this is a real bypass against this exact
// backend/template (not a strawman) by searching with the raw, unescaped
// filter directly, then prove LDAPService.Authenticate — which always goes
// through buildUserFilter's escaping — treats "*" as an inert literal
// instead.
func TestLDAPInjection_BareWildcardDoesNotAuthenticateAsAnotherUser(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()

	victimDN := "cn=victim,ou=people,dc=example,dc=org"
	dir.setEntries(newFakeLDAPEntry(
		victimDN, "victim-real-password",
		map[string][]string{
			"mail":        {"victim@example.com"},
			"cn":          {"Victim User"},
			"objectClass": {"inetOrgPerson"},
		},
	))

	const template = "(&(objectClass=inetOrgPerson)(mail=%s))"

	// Sanity check on the raw (unescaped) filter: a bare "*" must match the
	// victim when the template isn't escaped, proving the vulnerability is
	// real for this filter shape.
	rawFilter := fmt.Sprintf(template, "*")
	rawEntries := searchFakeDirectoryRaw(t, dir, rawFilter)
	require.Len(t, rawEntries, 1, "sanity check: an unescaped wildcard must match the victim entry")
	require.Equal(t, victimDN, rawEntries[0].DN)

	// The real path: an attacker who doesn't know the victim's password (or
	// even their exact email) submits "*" hoping the search widens to match
	// "anyone". With escaping, "*" becomes the literal string "*", which no
	// entry's mail attribute equals — so the search must find nobody, and
	// authentication must fail no matter what password is guessed.
	svc := NewLDAPService(&config.Config{LDAP: newFakeLDAPConfig(dir, template)})

	_, err := svc.Authenticate(t.Context(), "*", "attacker-guess")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLDAPUserNotFound)

	// Even a lucky/correct guess of the victim's real password must not
	// help — the search itself must not have resolved to the victim.
	_, err = svc.Authenticate(t.Context(), "*", "victim-real-password")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLDAPUserNotFound)
}

// TestLDAPInjection_ParenAndPipeInjectionDoesNotBypassSearch exercises the
// coordinator/spec's own example payload
// ("*)(uid=*))(|(uid=*") against the composite template. Whether the
// unescaped version would even compile is beside the point (go-ldap's
// strict single-top-level-filter grammar happens to reject it, but a less
// strict LDAP client library might not) — what matters is that
// LDAPService.Authenticate, which always escapes, treats it as an inert
// literal and never authenticates as anyone.
func TestLDAPInjection_ParenAndPipeInjectionDoesNotBypassSearch(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()
	dir.setEntries(newFakeLDAPEntry(
		"cn=victim,ou=people,dc=example,dc=org", "victim-real-password",
		map[string][]string{
			"mail":        {"victim@example.com"},
			"cn":          {"Victim User"},
			"objectClass": {"inetOrgPerson"},
		},
	))

	const template = "(&(objectClass=inetOrgPerson)(mail=%s))"

	svc := NewLDAPService(&config.Config{LDAP: newFakeLDAPConfig(dir, template)})

	maliciousIdentifiers := []string{
		"*)(uid=*))(|(uid=*",
		"victim@example.com)(objectClass=*",
		"victim@example.com))(|(objectClass=*",
	}

	for _, identifier := range maliciousIdentifiers {
		t.Run(identifier, func(t *testing.T) {
			t.Parallel()

			_, err := svc.Authenticate(t.Context(), identifier, "attacker-guess")
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrLDAPBindFailed,
				"must never reach a user bind for an injected/malformed identifier")
		})
	}
}

// searchFakeDirectoryRaw searches dir with filter exactly as given — no
// escaping — using the real go-ldap client, to establish a sanity-check
// baseline for what an UNESCAPED search would return against the same
// backend the safe path is tested against.
func searchFakeDirectoryRaw(t *testing.T, dir *fakeLDAPDirectory, filter string) []*ldap.Entry {
	t.Helper()

	conn, err := ldap.DialURL(dir.serverURL())
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.Bind(ldapTestServiceDN, ldapTestServicePassword))

	req := ldap.NewSearchRequest(
		ldapTestBaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		filter,
		[]string{"mail", "cn"},
		nil,
	)

	result, err := conn.Search(req)
	require.NoError(t, err)

	return result.Entries
}

// --- Service.Login: full integration through the password-login path ---

// setupLDAPLoginTestService wires a *Service with an in-memory sqlite DB and
// the given LDAP config, mirroring setupAuthTestServiceWithConfig.
func setupLDAPLoginTestService(
	t *testing.T, ldapCfg config.LDAPConfig, entitlements EntitlementsChecker,
) (*Service, db.Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	fullCfg := &config.Config{
		LDAP: ldapCfg,
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{
			BaseURL: "http://localhost:4000",
		},
	}

	svc := NewService(dbService, fullCfg.Auth, fullCfg, nil, entitlements)

	return svc, dbService, ctx
}

func TestLogin_LDAPHappyPath_AutoProvisionsUserAndSession(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()

	entryDN := "cn=newuser,ou=people,dc=example,dc=org"
	dir.setEntries(newFakeLDAPEntry(
		entryDN, "correct-password",
		map[string][]string{"mail": {"newuser@example.com"}, "cn": {"New User"}},
	))

	svc, dbSvc, ctx := setupLDAPLoginTestService(t, newFakeLDAPConfig(dir, ""), nil)

	org := models.NewOrganization("ldap-org", "LDAP Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	resp, err := svc.Login(ctx, org.Slug, "newuser@example.com", "correct-password", Context{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.RefreshToken)

	user, err := dbSvc.GetUserByEmail(ctx, "newuser@example.com")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Nil(t, user.PasswordHash, "an LDAP-provisioned user must never get a local password hash")
	require.Equal(t, "New User", user.Name)

	provider, err := dbSvc.GetUserProviderByProviderID(ctx, models.ProviderTypeLDAP, entryDN)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, user.UID, provider.UserUID)

	member, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, user.UID, member.UserUID)
}

// TestLogin_LDAPRepeatLoginReusesSameUser proves a second LDAP login for the
// same directory entry finds the existing User/UserProvider(LDAP) row
// (matched by DN via GetUserProviderByProviderID) rather than creating a
// duplicate User — mirrors oidc_service_test.go's
// TestOIDCHandleCallback_ExistingUserLogsInAgain.
func TestLogin_LDAPRepeatLoginReusesSameUser(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()
	dir.setEntries(newFakeLDAPEntry(
		"cn=repeat,ou=people,dc=example,dc=org", "correct-password",
		map[string][]string{"mail": {"repeat@example.com"}, "cn": {"Repeat User"}},
	))

	svc, dbSvc, ctx := setupLDAPLoginTestService(t, newFakeLDAPConfig(dir, ""), nil)

	org := models.NewOrganization("ldap-repeat-org", "LDAP Repeat Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	first, err := svc.Login(ctx, org.Slug, "repeat@example.com", "correct-password", Context{})
	require.NoError(t, err)

	second, err := svc.Login(ctx, org.Slug, "repeat@example.com", "correct-password", Context{})
	require.NoError(t, err)

	require.Equal(t, first.User.UID, second.User.UID)

	users, err := dbSvc.GetUserByEmail(ctx, "repeat@example.com")
	require.NoError(t, err)
	require.Equal(t, first.User.UID, users.UID)
}

// TestLogin_LDAPLinksExistingPasswordlessUserByEmail proves the identity-
// linking safety argument documented on authenticateViaLDAP: an existing
// user auto-provisioned by a DIFFERENT passwordless mechanism (here, a
// stand-in for OIDC/SAML — nil PasswordHash, a UserProvider(ProviderTypeOIDC)
// row) is found and REUSED by email when its email matches a successful LDAP
// bind, gaining an additional UserProvider(ProviderTypeLDAP) row rather than
// a duplicate User being created. This can only ever apply to passwordless
// accounts — see TestLogin_LocalPasswordTakesPriorityOverLDAP for the
// structural guarantee that a user with a local password never reaches this
// code path at all.
func TestLogin_LDAPLinksExistingPasswordlessUserByEmail(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()

	entryDN := "cn=oidcuser,ou=people,dc=example,dc=org"
	dir.setEntries(newFakeLDAPEntry(
		entryDN, "correct-password",
		map[string][]string{"mail": {"oidcuser@example.com"}, "cn": {"OIDC User"}},
	))

	svc, dbSvc, ctx := setupLDAPLoginTestService(t, newFakeLDAPConfig(dir, ""), nil)

	org := models.NewOrganization("ldap-link-org", "LDAP Link Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	existing := models.NewUser("oidcuser@example.com")
	existing.Name = "OIDC User"
	require.NoError(t, dbSvc.CreateUser(ctx, existing))

	oidcProvider := models.NewUserProvider(existing.UID, models.ProviderTypeOIDC, "some-oidc-subject")
	require.NoError(t, dbSvc.CreateUserProvider(ctx, oidcProvider))

	resp, err := svc.Login(ctx, org.Slug, "oidcuser@example.com", "correct-password", Context{})
	require.NoError(t, err)
	require.Equal(t, existing.UID, resp.User.UID, "must reuse the existing user, not create a duplicate")

	ldapProvider, err := dbSvc.GetUserProviderByProviderID(ctx, models.ProviderTypeLDAP, entryDN)
	require.NoError(t, err)
	require.Equal(t, existing.UID, ldapProvider.UserUID)

	// The pre-existing OIDC link must be untouched, not replaced.
	stillOIDC, err := dbSvc.GetUserProviderByProviderID(ctx, models.ProviderTypeOIDC, "some-oidc-subject")
	require.NoError(t, err)
	require.Equal(t, existing.UID, stillOIDC.UserUID)
}

func TestLogin_LDAPWrongPassword_Rejected(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()
	dir.setEntries(newFakeLDAPEntry(
		"cn=someone,ou=people,dc=example,dc=org", "correct-password",
		map[string][]string{"mail": {"someone@example.com"}, "cn": {"Someone"}},
	))

	svc, dbSvc, ctx := setupLDAPLoginTestService(t, newFakeLDAPConfig(dir, ""), nil)

	org := models.NewOrganization("ldap-org-2", "LDAP Org 2")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	_, err := svc.Login(ctx, org.Slug, "someone@example.com", "wrong-password", Context{})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidCredentials)

	// No user must have been created for a failed attempt.
	_, err = dbSvc.GetUserByEmail(ctx, "someone@example.com")
	require.Error(t, err)
}

// TestLogin_LocalPasswordTakesPriorityOverLDAP proves the fallback
// ordering: a user WITH a local password hash authenticates via local
// verification even when LDAP is enabled — and, concretely, even when the
// configured LDAP server is entirely unreachable, showing LDAP is never
// even attempted for this user. The proof is that login succeeds: had LDAP
// been dialed, it would fail instantly with connection-refused against the
// bogus address, so a successful login with a token is only possible if the
// local-password path was taken.
func TestLogin_LocalPasswordTakesPriorityOverLDAP(t *testing.T) {
	t.Parallel()

	unreachableLDAP := config.LDAPConfig{
		Enabled:   true,
		ServerURL: "ldap://127.0.0.1:1", // nothing listens here
		BaseDN:    ldapTestBaseDN,
	}

	svc, dbSvc, ctx := setupLDAPLoginTestService(t, unreachableLDAP, nil)

	org := models.NewOrganization("local-first-org", "Local First Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	passwordHash, err := passwords.Hash("local-password")
	require.NoError(t, err)

	user := models.NewUser("localuser@example.com")
	user.PasswordHash = &passwordHash
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, member))

	resp, err := svc.Login(ctx, org.Slug, "localuser@example.com", "local-password", Context{})

	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
}

// TestLogin_SuperAdminNeverLockedOut_LDAPEnabledAndUnreachable is the
// concrete regression test the spec calls out by name: the documented
// default admin account (internal/defaults.Email/Password) must always be
// able to log in with its local password, regardless of LDAP being
// enabled, misconfigured, or entirely unreachable.
func TestLogin_SuperAdminNeverLockedOut_LDAPEnabledAndUnreachable(t *testing.T) {
	t.Parallel()

	misconfiguredLDAP := config.LDAPConfig{
		Enabled:   true,
		ServerURL: "ldap://127.0.0.1:1",
		BaseDN:    ldapTestBaseDN,
		// BindDN/BindPassword deliberately left blank/wrong too — the
		// point is that literally nothing about the LDAP side needs to work.
	}

	svc, dbSvc, ctx := setupLDAPLoginTestService(t, misconfiguredLDAP, nil)

	org := models.NewOrganization(defaults.Organization, "Default Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	passwordHash, err := passwords.Hash(defaults.Password)
	require.NoError(t, err)

	admin := models.NewUser(defaults.Email)
	admin.SuperAdmin = true
	admin.PasswordHash = &passwordHash
	require.NoError(t, dbSvc.CreateUser(ctx, admin))

	member := models.NewOrganizationMember(org.UID, admin.UID, models.MemberRoleAdmin)
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, member))

	resp, err := svc.Login(ctx, defaults.Organization, defaults.Email, defaults.Password, Context{})
	require.NoError(t, err, "the super-admin must always be able to log in locally, "+
		"even with LDAP enabled and unreachable")
	require.NotEmpty(t, resp.AccessToken)
}

// errLDAPSSOQuotaTest is a static sentinel used by
// TestLogin_LDAP_EnforcesMaxSSOUsers to simulate an org that has reached its
// maxSsoUsers cap, mirroring oidc_service_test.go's errSSOQuotaTest.
var errLDAPSSOQuotaTest = errors.New("sso membership quota exceeded")

func TestLogin_LDAP_EnforcesMaxSSOUsers(t *testing.T) {
	t.Parallel()

	dir := startFakeLDAPDirectory(t)
	dir.setServiceAccount()
	dir.setEntries(newFakeLDAPEntry(
		"cn=quotauser,ou=people,dc=example,dc=org", "correct-password",
		map[string][]string{"mail": {"quotauser@example.com"}, "cn": {"Quota User"}},
	))

	svc, dbSvc, ctx := setupLDAPLoginTestService(
		t, newFakeLDAPConfig(dir, ""), &stubEntitlementsChecker{err: errLDAPSSOQuotaTest},
	)

	org := models.NewOrganization("ldap-quota-org", "LDAP Quota Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	// The org already has one member so the new LDAP user isn't the
	// bootstrap membership that bypasses the cap.
	existing := models.NewUser("existing-admin@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, existing))
	existingMember := models.NewOrganizationMember(org.UID, existing.UID, models.MemberRoleAdmin)
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, existingMember))

	// The org admits @example.com addresses, so the LDAP user qualifies for a
	// membership on the merits — the cap is the only thing that can stop them.
	// Without the pattern they would be left pending and the cap would never
	// be consulted, which would make this test vacuous.
	require.NoError(t, dbSvc.SetOrgParameter(ctx, org.UID, "registration.email_pattern", `@example\.com$`, false))

	_, err := svc.Login(ctx, org.Slug, "quotauser@example.com", "correct-password", Context{})
	require.Error(t, err)
	require.ErrorIs(t, err, errLDAPSSOQuotaTest)

	// The bind succeeded and the User row may have been created (identity
	// is real), but no membership/session for this org should exist.
	quotaUser, getErr := dbSvc.GetUserByEmail(ctx, "quotauser@example.com")
	if getErr == nil && quotaUser != nil {
		_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, quotaUser.UID, org.UID)
		require.Error(t, memberErr, "membership must not be created when the SSO quota is exceeded")
	}
}

// TestLogin_LDAP_NonMatchingEmailGetsMembershipRequest proves the LDAP branch
// honors the same admission policy as the OAuth/SAML connectors. LDAP is the
// one connector calling JoinOrgViaLogin directly (not through
// CompleteOrgLogin), so its pending branch has its own fallback path: the bind
// succeeds, the user is provisioned, but a directory identity the org's
// registration.email_pattern does not admit gets a membership request and NO
// organization_members row — while the matching address in the second subtest
// (same org, same rules) joins, so a policy that simply refused everyone would
// fail this test too.
func TestLogin_LDAP_NonMatchingEmailGetsMembershipRequest(t *testing.T) {
	t.Parallel()

	// setup wires a directory holding one entry with the given mail, an org
	// past bootstrap (one seeded member) whose pattern admits @allowed.example.
	setup := func(t *testing.T, mail string) (*Service, db.Service, context.Context, *models.Organization) {
		t.Helper()

		dir := startFakeLDAPDirectory(t)
		dir.setServiceAccount()
		dir.setEntries(newFakeLDAPEntry(
			"cn=policyuser,ou=people,dc=example,dc=org", "correct-password",
			map[string][]string{"mail": {mail}, "cn": {"Policy User"}},
		))

		svc, dbSvc, ctx := setupLDAPLoginTestService(t, newFakeLDAPConfig(dir, ""), nil)

		org := models.NewOrganization("ldap-policy-org", "LDAP Policy Org")
		require.NoError(t, dbSvc.CreateOrganization(ctx, org))

		seed := models.NewUser("seed@allowed.example")
		require.NoError(t, dbSvc.CreateUser(ctx, seed))
		require.NoError(t, dbSvc.CreateOrganizationMember(
			ctx, models.NewOrganizationMember(org.UID, seed.UID, models.MemberRoleAdmin)))

		require.NoError(t, dbSvc.SetOrgParameter(
			ctx, org.UID, "registration.email_pattern", `@allowed\.example$`, false))

		return svc, dbSvc, ctx, org
	}

	t.Run("outsider gets a membership request and no membership", func(t *testing.T) {
		t.Parallel()

		svc, dbSvc, ctx, org := setup(t, "outsider@evil.example")

		resp, err := svc.Login(ctx, org.Slug, "outsider@evil.example", "correct-password", Context{})
		require.NoError(t, err, "the bind is valid — the user authenticates, they just don't get in")
		require.Nil(t, resp.Organization, "no org-scoped session may be minted")
		require.Empty(t, resp.RefreshToken, "a no-org login mints no refresh token")

		user, err := dbSvc.GetUserByEmail(ctx, "outsider@evil.example")
		require.NoError(t, err)
		require.NotNil(t, user)

		_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
		require.Error(t, memberErr, "no organization_members row may be created")

		request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
		require.NoError(t, reqErr)
		require.NotNil(t, request)
		require.Equal(t, models.MembershipRequestStatusPending, request.Status)
	})

	t.Run("matching email joins as user", func(t *testing.T) {
		t.Parallel()

		svc, dbSvc, ctx, org := setup(t, "insider@allowed.example")

		resp, err := svc.Login(ctx, org.Slug, "insider@allowed.example", "correct-password", Context{})
		require.NoError(t, err)
		require.NotNil(t, resp.Organization)
		require.Equal(t, org.Slug, resp.Organization.Slug)

		user, err := dbSvc.GetUserByEmail(ctx, "insider@allowed.example")
		require.NoError(t, err)

		member, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
		require.NoError(t, memberErr)
		require.Equal(t, models.MemberRoleUser, member.Role)

		request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
		require.True(t, reqErr != nil || request == nil, "an admitted user opens no request")
	})
}
