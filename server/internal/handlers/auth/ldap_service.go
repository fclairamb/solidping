package auth

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// LDAP bind-authentication errors.
var (
	ErrLDAPNotConfigured = errors.New("LDAP bind authentication is not configured")
	// ErrLDAPEmptyPassword is returned when an empty password is submitted.
	// This is a security-critical guard, not a validation nicety — see
	// Authenticate's doc comment.
	ErrLDAPEmptyPassword = errors.New("empty password is never sent to an LDAP bind")
	ErrLDAPUserNotFound  = errors.New("no matching LDAP directory entry")
	ErrLDAPAmbiguousUser = errors.New("more than one matching LDAP directory entry")
	ErrLDAPBindFailed    = errors.New("LDAP bind failed")
	ErrLDAPSearchFailed  = errors.New("LDAP search failed")
	ErrLDAPNoEmail       = errors.New("LDAP entry has no usable email attribute")
)

const (
	ldapDefaultUserFilter     = "(mail=%s)"
	ldapDefaultEmailAttribute = "mail"
	ldapDefaultNameAttribute  = "cn"
	ldapDialTimeout           = 10 * time.Second
)

// LDAPUserInfo is what we learn about a directory entry after a successful
// search-then-bind: the entry's DN (used as the stable UserProvider
// identifier) and the mapped email/name attributes (used as the local
// User's identity — see Service.findOrCreateLDAPUser).
type LDAPUserInfo struct {
	DN    string
	Email string
	Name  string
}

// LDAPService performs LDAP/Active Directory bind authentication for the
// password-login path (spec 2026-07-08-08, part 3 — global-only, single
// connector; see config.LDAPConfig). It carries no state beyond the config
// snapshot it was built with — every Authenticate call opens its own
// connection, so a super-admin config edit takes effect on the very next
// login attempt, with no cache to invalidate.
type LDAPService struct {
	cfg *config.Config
}

// NewLDAPService creates a new LDAP bind-authentication service.
func NewLDAPService(cfg *config.Config) *LDAPService {
	return &LDAPService{cfg: cfg}
}

// Enabled reports whether LDAP bind auth is usable: enabled and minimally
// configured (server + base DN present).
func (s *LDAPService) Enabled() bool {
	c := s.cfg.LDAP

	return c.Enabled && strings.TrimSpace(c.ServerURL) != "" && strings.TrimSpace(c.BaseDN) != ""
}

// Authenticate performs a search-then-bind against the configured directory:
// bind as the service account (or anonymously, when BindDN is blank), search
// for exactly one entry matching the submitted identifier via a filter built
// with RFC 4515 escaping (see buildUserFilter), then bind as that entry's DN
// with the submitted password — the step that actually verifies it.
//
// SECURITY — empty password / "unauthenticated bind": per RFC 4513 §5.1.2,
// an LDAP bind carrying a valid DN and an EMPTY password is defined as an
// "unauthenticated bind", which servers may accept as a SUCCESS without
// checking any credential at all. If this function ever reached the final
// user-bind step with password == "", an attacker could authenticate as any
// directory entry our search could find, with zero knowledge of its real
// password. The guard below is therefore the load-bearing control, not a
// nicety, and it runs first — before any network I/O at all, i.e. before
// the service-account bind and the search, not just before the user bind.
// It does not depend on: (a) the go-ldap client's own default refusal
// (Conn.Bind already rejects an empty password unless AllowEmptyPassword is
// explicitly set, which this code never does), or (b) the directory
// rejecting it — a misconfigured or permissive directory that *would* honor
// an unauthenticated bind is still safe against this code, because the bind
// is simply never attempted.
func (s *LDAPService) Authenticate(ctx context.Context, identifier, password string) (*LDAPUserInfo, error) {
	if !s.Enabled() {
		return nil, ErrLDAPNotConfigured
	}

	if password == "" {
		return nil, ErrLDAPEmptyPassword
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	conn, err := s.dial()
	if err != nil {
		return nil, fmt.Errorf("%w: dial: %w", ErrLDAPBindFailed, err)
	}
	defer func() { _ = conn.Close() }()

	if err = s.bindServiceAccount(conn); err != nil {
		return nil, err
	}

	entry, err := s.searchUser(conn, identifier)
	if err != nil {
		return nil, err
	}

	// The actual credential check. password is guaranteed non-empty by the
	// guard above, and entry.DN came from our own search, not from
	// unescaped user input — see searchUser/buildUserFilter.
	if err = conn.Bind(entry.DN, password); err != nil {
		return nil, fmt.Errorf("%w: user bind: %w", ErrLDAPBindFailed, err)
	}

	info := s.mapEntry(entry)
	if info.Email == "" {
		return nil, ErrLDAPNoEmail
	}

	return info, nil
}

// dial opens a connection to the configured directory, applying StartTLS
// when requested for a plain "ldap://" URL ("ldaps://" URLs are already TLS
// via DialWithTLSConfig).
func (s *LDAPService) dial() (*ldap.Conn, error) {
	ldapCfg := s.cfg.LDAP

	dialOpts := []ldap.DialOpt{ldap.DialWithDialer(&net.Dialer{Timeout: ldapDialTimeout})}

	isLDAPS := strings.HasPrefix(strings.ToLower(ldapCfg.ServerURL), "ldaps://")
	if isLDAPS {
		dialOpts = append(dialOpts, ldap.DialWithTLSConfig(&tls.Config{
			InsecureSkipVerify: ldapCfg.InsecureSkipVerify,
			ServerName:         hostnameOf(ldapCfg.ServerURL),
		}))
	}

	conn, err := ldap.DialURL(ldapCfg.ServerURL, dialOpts...)
	if err != nil {
		return nil, err
	}

	if ldapCfg.StartTLS && !isLDAPS {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: ldapCfg.InsecureSkipVerify,
			ServerName:         hostnameOf(ldapCfg.ServerURL),
		}

		if err := conn.StartTLS(tlsConfig); err != nil {
			_ = conn.Close()

			return nil, err
		}
	}

	return conn, nil
}

// hostnameOf extracts the host (no port) from a "scheme://host:port" URL,
// used as the TLS ServerName for certificate verification. Returns "" (no
// override — the stdlib falls back to whatever DialURL used) when the URL
// doesn't parse.
func hostnameOf(serverURL string) string {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return ""
	}

	return parsed.Hostname()
}

// bindServiceAccount authenticates the connection used for the directory
// search. When BindDN is blank, the search proceeds unauthenticated
// (some directories explicitly allow anonymous read/search) — a deliberate
// admin choice, and unrelated to the end-user empty-password guard above.
func (s *LDAPService) bindServiceAccount(conn *ldap.Conn) error {
	bindDN := s.cfg.LDAP.BindDN
	if bindDN == "" {
		return nil
	}

	if err := conn.Bind(bindDN, s.cfg.LDAP.BindPassword); err != nil {
		return fmt.Errorf("%w: service account bind: %w", ErrLDAPBindFailed, err)
	}

	return nil
}

// searchUser looks up exactly one directory entry matching identifier under
// the configured base DN, using the configured (or default) filter
// template with the identifier RFC 4515-escaped before substitution — see
// buildUserFilter. Zero or more than one match is treated as "not found"
// (fail closed): an ambiguous filter must never be treated as a match for
// any of the candidates it happens to hit.
func (s *LDAPService) searchUser(conn *ldap.Conn, identifier string) (*ldap.Entry, error) {
	filter, err := buildUserFilter(s.cfg.LDAP.UserFilter, identifier)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLDAPSearchFailed, err)
	}

	emailAttr := s.cfg.LDAP.EmailAttribute
	if emailAttr == "" {
		emailAttr = ldapDefaultEmailAttribute
	}

	nameAttr := s.cfg.LDAP.NameAttribute
	if nameAttr == "" {
		nameAttr = ldapDefaultNameAttribute
	}

	req := ldap.NewSearchRequest(
		s.cfg.LDAP.BaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		filter,
		[]string{emailAttr, nameAttr},
		nil,
	)

	result, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLDAPSearchFailed, err)
	}

	switch len(result.Entries) {
	case 0:
		return nil, ErrLDAPUserNotFound
	case 1:
		return result.Entries[0], nil
	default:
		return nil, ErrLDAPAmbiguousUser
	}
}

// mapEntry extracts the email/name attributes from a found entry using the
// configured (or default) attribute names.
func (s *LDAPService) mapEntry(entry *ldap.Entry) *LDAPUserInfo {
	emailAttr := s.cfg.LDAP.EmailAttribute
	if emailAttr == "" {
		emailAttr = ldapDefaultEmailAttribute
	}

	nameAttr := s.cfg.LDAP.NameAttribute
	if nameAttr == "" {
		nameAttr = ldapDefaultNameAttribute
	}

	info := &LDAPUserInfo{
		DN:    entry.DN,
		Email: entry.GetAttributeValue(emailAttr),
		Name:  entry.GetAttributeValue(nameAttr),
	}

	if info.Name == "" {
		info.Name = info.Email
	}

	return info
}

// buildUserFilter substitutes the RFC 4515-escaped identifier into the
// filter template's single "%s" placeholder.
//
// SECURITY — LDAP injection: identifier is attacker-controlled (it's
// whatever was submitted in the login form's email/username field). Naively
// string-formatting it into the filter would let an attacker inject filter
// syntax — e.g. a value like "*)(uid=*))(|(uid=*" — to widen or redirect the
// search to match an entry other than the one they're supposed to be
// authenticating as, the LDAP analog of SQL injection. ldap.EscapeFilter
// neutralizes every RFC 4515 metacharacter (parentheses, "*", backslash, and
// NUL) by hex-escaping it, so the identifier can only ever be matched as a
// single literal value — it can never introduce new filter clauses,
// wildcards, or boolean structure, regardless of the template (a bare
// "(mail=%s)" or an admin-authored composite
// "(&(objectClass=...)(mail=%s))" alike).
func buildUserFilter(template, identifier string) (string, error) {
	if strings.Count(template, "%s") != 1 {
		template = ldapDefaultUserFilter
	}

	filter := fmt.Sprintf(template, ldap.EscapeFilter(identifier))

	// Defense in depth: confirm the result is actually a single well-formed
	// filter before it's ever sent to the directory. This should always
	// succeed given the escaping above; if it somehow doesn't (e.g. a
	// malformed admin-authored template), fail closed rather than send a
	// filter we haven't validated.
	if _, err := ldap.CompileFilter(filter); err != nil {
		return "", fmt.Errorf("built an invalid LDAP filter: %w", err)
	}

	return filter, nil
}

// verifyLocalPassword checks a user's local password hash against the
// submitted password, transparently rehashing on success if the stored
// hash's policy is stale. Returns ErrInvalidCredentials on any mismatch.
// Extracted from Login so the local-password-first branch and the LDAP
// fallback branch (authenticateViaLDAP) read as siblings.
func (s *Service) verifyLocalPassword(ctx context.Context, user *models.User, password string) error {
	// Support plaintext passwords for development (prefix: $plaintext$).
	if plaintextPassword, found := strings.CutPrefix(*user.PasswordHash, "$plaintext$"); found {
		if plaintextPassword != password {
			return ErrInvalidCredentials
		}

		return nil
	}

	// Verify the stored hash (algorithm is read from the stored marker).
	if !passwords.Verify(password, *user.PasswordHash) {
		return ErrInvalidCredentials
	}

	// Rehash-on-login: best-effort, never affects the login result.
	s.maybeRehashPassword(ctx, user, password)

	return nil
}

// ldapEnabled reports whether the LDAP fallback should be attempted.
// fullCfg is nil in some unit tests that construct Service directly (see
// setupAuthTestService) — treat that the same as "LDAP not configured"
// rather than panicking.
func (s *Service) ldapEnabled() bool {
	return s.fullCfg != nil && s.fullCfg.LDAP.Enabled
}

// authenticateViaLDAP is the LDAP fallback branch of Login, reached only
// when the submitted user has no usable local password hash (see the
// switch in Login). It performs a search-then-bind against the configured
// directory and, on success, finds or auto-provisions the matching local
// User plus its org membership (best-effort — see below).
//
// Identity-linking safety: unlike the federated OIDC/SAML flows (where an
// external IdP unilaterally asserts an email that might not match anything
// the end user actually typed — see oidc_service.go's email_verified gate),
// here the submitted identifier IS the value the user just typed into the
// login form, and a successful bind proves they know the password of the
// exact directory entry our (escaped) search matched against it — the
// directory itself is the trust anchor the admin configured, the same
// reasoning saml_service.go documents for its own email-linking step.
// Structurally, this path can only ever run for a user with NO local
// PasswordHash (see Login's switch): a user who already has a local
// password is authenticated by verifyLocalPassword and never reaches this
// function, so a successful LDAP bind can never be used to hijack an
// existing locally-secured account. It can only create a brand-new user, or
// attach an LDAP identity to an existing passwordless (OIDC/SAML/LDAP)
// account whose email matches the directory's authoritative email
// attribute for the entry just proven to belong to the caller.
func (s *Service) authenticateViaLDAP(ctx context.Context, orgSlug, identifier, password string) (*models.User, error) {
	if !s.ldapEnabled() {
		return nil, ErrInvalidCredentials
	}

	info, err := NewLDAPService(s.fullCfg).Authenticate(ctx, identifier, password)
	if err != nil {
		// Every Authenticate failure (LDAP not configured, empty password,
		// directory unreachable, user not found/ambiguous, wrong password)
		// is a credential-verification failure from the caller's point of
		// view. Map it to the same generic ErrInvalidCredentials a wrong
		// local password would produce: this is anti-enumeration (don't
		// reveal whether a given identifier exists in the directory, or
		// that the directory is unreachable/misconfigured) and it lets the
		// existing 401 mapping in handler.go's handleAuthError apply
		// uniformly across every credential path, local or LDAP.
		return nil, ErrInvalidCredentials
	}

	// From here on the bind has already succeeded — the caller's identity
	// is verified. A failure below is a real internal/quota failure, not a
	// credential problem, so it propagates as-is rather than being folded
	// into ErrInvalidCredentials.
	user, err := s.findOrCreateLDAPUser(ctx, info)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create LDAP user: %w", err)
	}

	// orgSlug is treated as a preference here exactly like Login treats it
	// for existing users: if it doesn't resolve to a real org, we simply
	// don't create a membership, and the generic resolveOrgPreference call
	// in Login handles the resulting zero-membership case the same way it
	// does for any other user.
	//
	// Admission itself goes through the shared JoinOrgViaLogin policy, exactly
	// like every OAuth/SAML connector: a directory bind proves who the caller
	// is, it does not prove the org wants them. A user the org does not admit
	// gets a membership request instead of a membership, and Login's
	// resolveOrgPreference then renders the usual no-org response.
	if orgSlug != "" {
		if org, orgErr := s.db.GetOrganizationBySlug(ctx, orgSlug); orgErr == nil {
			if _, _, err := s.JoinOrgViaLogin(ctx, org, user); err != nil {
				return nil, fmt.Errorf("failed to ensure membership: %w", err)
			}
		}
	}

	return user, nil
}

// findOrCreateLDAPUser finds or creates a user by LDAP identity (the
// entry's DN, which is stable and unique within the directory). On first
// successful bind for a not-yet-provisioned identity, the created User gets
// a nil PasswordHash — see models.ProviderTypeLDAP's doc comment for why:
// an LDAP-provisioned account must only ever be able to authenticate via a
// directory bind, never via a locally-set password.
func (s *Service) findOrCreateLDAPUser(ctx context.Context, info *LDAPUserInfo) (*models.User, error) {
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeLDAP, info.DN)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user provider: %w", err)
	}

	if err == nil && provider != nil {
		// A link pointing at a soft-deleted user is stale: it is cleared
		// and we fall through to the email lookup / create path rather
		// than failing this login (and every later one) forever.
		user, resolveErr := ResolveLinkedUser(ctx, s.db, models.ProviderTypeLDAP, info.DN, provider)
		if resolveErr != nil {
			return nil, resolveErr
		}

		if user != nil {
			return user, nil
		}

		provider = nil
	}

	user, err := s.db.GetUserByEmail(ctx, info.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}

	if user == nil {
		user = models.NewUser(info.Email)
		user.Name = info.Name

		now := time.Now()
		user.EmailVerifiedAt = &now

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodLDAP); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeLDAP, info.DN)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}
