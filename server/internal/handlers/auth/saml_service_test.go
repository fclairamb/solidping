package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/xml"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// fakeSAMLIdP is a minimal in-repo SAML identity provider used to drive the
// SP-initiated login flow without any external dependency. It wraps
// saml.IdentityProvider — the crewjam/saml library's own IdP implementation
// — rather than hand-rolling signed XML fixtures, so assertions are
// produced by the same real library code a genuine IdP would use.
type fakeSAMLIdP struct {
	idp *saml.IdentityProvider

	// nextSession is returned by GetSession for the next request driven
	// through the real ServeSSO/redirect path (the happy-path and replay
	// tests). Negative-validation tests build assertions directly via
	// buildSignedAssertionRequest instead, bypassing GetSession entirely.
	nextSession *saml.Session
}

// GetSession implements saml.SessionProvider.
func (f *fakeSAMLIdP) GetSession(_ http.ResponseWriter, _ *http.Request, _ *saml.IdpAuthnRequest) *saml.Session {
	return f.nextSession
}

// newFakeSAMLIdP creates a fake IdP with its own self-signed signing
// identity. ssoURL/metadataURL need not be reachable — none of the tests
// actually fetch them over HTTP; the SP is configured directly with this
// IdP's metadata XML (idp.metadataXML).
func newFakeSAMLIdP(t *testing.T) *fakeSAMLIdP {
	t.Helper()

	key, cert := generateTestSigningIdentity(t, "fake-saml-idp")

	f := &fakeSAMLIdP{
		idp: &saml.IdentityProvider{
			Key:         key,
			Certificate: cert,
		},
	}

	ssoURL, err := url.Parse("https://fake-saml-idp.example.com/sso")
	require.NoError(t, err)

	metadataURL, err := url.Parse("https://fake-saml-idp.example.com/metadata")
	require.NoError(t, err)

	f.idp.SSOURL = *ssoURL
	f.idp.MetadataURL = *metadataURL
	f.idp.SessionProvider = f

	return f
}

// metadataXML returns this IdP's own metadata document, fed to the SP as
// config.SAMLConfig.IDPMetadataXML.
func (f *fakeSAMLIdP) metadataXML(t *testing.T) string {
	t.Helper()

	data, err := xml.MarshalIndent(f.idp.Metadata(), "", "  ")
	require.NoError(t, err)

	return string(data)
}

// generateTestSigningIdentity creates a fresh RSA keypair and a short-lived
// self-signed certificate wrapping it, used both for the fake IdP's trusted
// signing identity and (with a second, independent call) for an
// untrusted/rogue signer in the bad-signature test.
func generateTestSigningIdentity(t *testing.T, cn string) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return key, cert
}

// setupSAMLTestService wires a SAMLService whose trusted IdP is the given
// fake IdP. entitlements may be nil (no-op, matching production default
// when no entitlements service is configured).
func setupSAMLTestService(
	t *testing.T, idp *fakeSAMLIdP, entitlements EntitlementsChecker,
) (*SAMLService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		SAML: config.SAMLConfig{
			Enabled:        true,
			IDPMetadataXML: idp.metadataXML(t),
		},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{
			BaseURL: "http://localhost:4000",
		},
	}

	authService := NewService(dbService, cfg.Auth, cfg, nil, entitlements)
	svc := NewSAMLService(dbService, cfg, authService)

	return svc, ctx
}

func setupSAMLTestOrg(ctx context.Context, t *testing.T, svc *SAMLService) *models.Organization {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	return org
}

// defaultTestSession returns a valid saml.Session asserting the given
// email/name, used as the fake IdP's "logged in user" for a given request.
func defaultTestSession(email, name string) *saml.Session {
	now := time.Now()

	return &saml.Session{
		ID:             "session-" + email,
		CreateTime:     now,
		ExpireTime:     now.Add(time.Hour),
		NameID:         "saml-subject-" + email,
		NameIDFormat:   string(saml.TransientNameIDFormat),
		UserEmail:      email,
		UserCommonName: name,
	}
}

// buildSignedAssertionRequest drives the fake IdP's real assertion
// construction/signing path (saml.DefaultAssertionMaker + MakeAssertionEl +
// MakeResponse — exactly what IdentityProvider.ServeSSO calls internally)
// for a request with the given requestID/session/time, and returns a
// ready-to-parse ACS POST *http.Request. spMetadata/signer let tests
// substitute a tampered SP descriptor (wrong audience) or an untrusted
// signing identity (bad signature) without needing to mutate any
// process-wide clock or global state.
func buildSignedAssertionRequest(
	t *testing.T,
	signer *fakeSAMLIdP,
	spMetadata *saml.EntityDescriptor,
	requestID string,
	session *saml.Session,
	now time.Time,
	acsURL string,
) *http.Request {
	t.Helper()

	spSSODescriptor := &spMetadata.SPSSODescriptors[0]
	acsEndpoint := &spSSODescriptor.AssertionConsumerServices[0]

	idpReq := &saml.IdpAuthnRequest{
		IDP:         signer.idp,
		HTTPRequest: httptest.NewRequestWithContext(t.Context(), http.MethodGet, signer.idp.SSOURL.String(), nil),
		Request: saml.AuthnRequest{
			ID:           requestID,
			IssueInstant: now,
			Version:      "2.0",
		},
		ServiceProviderMetadata: spMetadata,
		SPSSODescriptor:         spSSODescriptor,
		ACSEndpoint:             acsEndpoint,
		Now:                     now,
	}

	require.NoError(t, (saml.DefaultAssertionMaker{}).MakeAssertion(idpReq, session))
	require.NoError(t, idpReq.MakeResponse())

	form, err := idpReq.PostBinding()
	require.NoError(t, err)

	body := url.Values{"SAMLResponse": {form.SAMLResponse}, "RelayState": {"unused-in-these-tests"}}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, acsURL, strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.NoError(t, req.ParseForm())

	return req
}

func TestSAMLMetadata_WellFormed(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)

	data, err := svc.Metadata(ctx)
	require.NoError(t, err)

	var descriptor saml.EntityDescriptor
	require.NoError(t, xml.Unmarshal(data, &descriptor))

	assert.Equal(t, "http://localhost:4000/api/v1/auth/saml/metadata", descriptor.EntityID)
	require.Len(t, descriptor.SPSSODescriptors, 1)
	require.NotEmpty(t, descriptor.SPSSODescriptors[0].AssertionConsumerServices)
	assert.Equal(t, "http://localhost:4000/api/v1/auth/saml/acs",
		descriptor.SPSSODescriptors[0].AssertionConsumerServices[0].Location)
}

// TestSAMLMetadata_AvailableBeforeIdPConfigured is the regression test for
// the chicken-and-egg design decision documented on spBase/serviceProvider:
// the SP metadata endpoint only requires SAML.Enabled, not an IdP metadata
// URL/XML, since an admin typically needs this SP's own metadata *first* to
// configure their IdP, before they have IdP metadata to paste back. Login
// and ACS, by contrast, do require the IdP side (see
// TestSAMLGenerateAuthnRequest_NotConfigured).
func TestSAMLMetadata_AvailableBeforeIdPConfigured(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		SAML: config.SAMLConfig{Enabled: true}, // no IDPMetadataURL/XML set
		Auth: config.AuthConfig{JWTSecret: "test-jwt-secret"},
		Server: config.ServerConfig{
			BaseURL: "http://localhost:4000",
		},
	}
	authService := NewService(dbService, cfg.Auth, cfg, nil, nil)
	svc := NewSAMLService(dbService, cfg, authService)

	data, err := svc.Metadata(ctx)
	require.NoError(t, err)

	var descriptor saml.EntityDescriptor
	require.NoError(t, xml.Unmarshal(data, &descriptor))
	assert.Equal(t, "http://localhost:4000/api/v1/auth/saml/metadata", descriptor.EntityID)

	// But login must still refuse until the IdP side is configured too.
	_, err = svc.GenerateAuthnRequest(ctx, "/", "test-org")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSAMLNotConfigured)
}

func TestSAMLHandleACS_HappyPath(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)
	org := setupSAMLTestOrg(ctx, t, svc)

	_, err := svc.GenerateAuthnRequest(ctx, "/dash0/orgs/"+org.Slug, org.Slug)
	require.NoError(t, err)

	spClient, err := svc.serviceProvider(ctx)
	require.NoError(t, err)

	spMetadata := spClient.Metadata()
	session := defaultTestSession("test@example.com", "Test User")

	// The request ID below must match one actually issued by
	// GenerateAuthnRequest for HandleACS's InResponseTo check to pass; we
	// recover it from the state store via a second GenerateAuthnRequest call
	// so the test doesn't need to reach into GenerateAuthnRequest's return
	// value (which only returns the redirect URL, not the raw request ID).
	state := mintSAMLState(ctx, t, svc, org.Slug)

	acsReq := buildSignedAssertionRequest(t, idp, spMetadata, state.RequestID, session, time.Now(), svc.acsURL())

	result, err := svc.HandleACS(ctx, acsReq, state)
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
	assert.NotEmpty(t, result.RefreshToken)
	assert.Equal(t, org.Slug, result.OrgSlug)

	// A User + UserProvider(ProviderTypeSAML) must have been created.
	user, err := svc.db.GetUserByEmail(ctx, "test@example.com")
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.Equal(t, "Test User", user.Name)

	provider, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSAML, session.NameID)
	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.Equal(t, user.UID, provider.UserUID)

	// A session (org membership) must exist too.
	member, err := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	assert.Equal(t, user.UID, member.UserUID)
}

// mintSAMLState issues a real AuthnRequest via GenerateAuthnRequest and
// returns the SAMLRelayState it stored, by minting a nonce ourselves,
// mirroring GenerateAuthnRequest's own bookkeeping so tests can drive
// HandleACS directly without parsing a redirect URL.
func mintSAMLState(ctx context.Context, t *testing.T, svc *SAMLService, orgSlug string) *SAMLRelayState {
	t.Helper()

	redirectURL, err := svc.GenerateAuthnRequest(ctx, "/dash0/orgs/"+orgSlug, orgSlug)
	require.NoError(t, err)

	parsed, err := url.Parse(redirectURL)
	require.NoError(t, err)

	relayState := parsed.Query().Get("RelayState")
	require.NotEmpty(t, relayState)

	state, err := svc.ValidateRelayState(ctx, relayState)
	require.NoError(t, err)

	return state
}

func TestSAMLHandleACS_ExistingUserLogsInAgain(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)
	org := setupSAMLTestOrg(ctx, t, svc)

	spClient, err := svc.serviceProvider(ctx)
	require.NoError(t, err)

	spMetadata := spClient.Metadata()
	session := defaultTestSession("test@example.com", "Test User")

	state1 := mintSAMLState(ctx, t, svc, org.Slug)
	req1 := buildSignedAssertionRequest(t, idp, spMetadata, state1.RequestID, session, time.Now(), svc.acsURL())
	first, err := svc.HandleACS(ctx, req1, state1)
	require.NoError(t, err)

	state2 := mintSAMLState(ctx, t, svc, org.Slug)
	req2 := buildSignedAssertionRequest(t, idp, spMetadata, state2.RequestID, session, time.Now(), svc.acsURL())
	second, err := svc.HandleACS(ctx, req2, state2)
	require.NoError(t, err)

	assert.Equal(t, first.UserUID, second.UserUID)
}

// TestSAMLHandleACS_ReplayRejected is the regression test for InResponseTo
// (replay/CSRF) protection: an assertion produced for one AuthnRequest must
// not validate against a *different* request's stored state, even though
// the assertion itself is validly signed and otherwise well-formed.
func TestSAMLHandleACS_ReplayRejected(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)
	org := setupSAMLTestOrg(ctx, t, svc)

	spClient, err := svc.serviceProvider(ctx)
	require.NoError(t, err)

	spMetadata := spClient.Metadata()
	session := defaultTestSession("test@example.com", "Test User")

	stateA := mintSAMLState(ctx, t, svc, org.Slug)
	stateB := mintSAMLState(ctx, t, svc, org.Slug)
	require.NotEqual(t, stateA.RequestID, stateB.RequestID)

	// Assertion is built InResponseTo stateA's request...
	reqA := buildSignedAssertionRequest(t, idp, spMetadata, stateA.RequestID, session, time.Now(), svc.acsURL())

	// ...but presented against stateB (a different, still-live request).
	_, err = svc.HandleACS(ctx, reqA, stateB)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSAMLAssertionInvalid)
}

func TestSAMLHandleACS_RejectsWrongAudience(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)
	org := setupSAMLTestOrg(ctx, t, svc)

	spClient, err := svc.serviceProvider(ctx)
	require.NoError(t, err)

	// Tamper the SP metadata the IdP is told about: same ACS endpoint (so
	// the assertion is still Recipient-valid), but a different EntityID, so
	// the assertion's Audience restriction won't name the real SP.
	tamperedMetadata := *spClient.Metadata()
	tamperedMetadata.EntityID = "https://attacker-controlled-sp.example.com/metadata"

	session := defaultTestSession("test@example.com", "Test User")
	state := mintSAMLState(ctx, t, svc, org.Slug)

	req := buildSignedAssertionRequest(t, idp, &tamperedMetadata, state.RequestID, session, time.Now(), svc.acsURL())

	_, err = svc.HandleACS(ctx, req, state)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSAMLAssertionInvalid)
}

func TestSAMLHandleACS_RejectsExpiredAssertion(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)
	org := setupSAMLTestOrg(ctx, t, svc)

	spClient, err := svc.serviceProvider(ctx)
	require.NoError(t, err)

	spMetadata := spClient.Metadata()
	session := defaultTestSession("test@example.com", "Test User")
	state := mintSAMLState(ctx, t, svc, org.Slug)

	// Anchor the assertion's Conditions/SubjectConfirmationData validity
	// window (computed from `now` inside DefaultAssertionMaker) two hours in
	// the past, well beyond MaxIssueDelay/MaxClockSkew — so by the time
	// HandleACS validates it against the real wall clock, it's expired.
	longAgo := time.Now().Add(-2 * time.Hour)

	req := buildSignedAssertionRequest(t, idp, spMetadata, state.RequestID, session, longAgo, svc.acsURL())

	_, err = svc.HandleACS(ctx, req, state)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSAMLAssertionInvalid)
}

// TestSAMLHandleACS_RejectsUntrustedSigner is the regression test for
// signature verification: an assertion signed by a key/certificate the SP
// was never told to trust (i.e. not present in the configured IdP
// metadata) must be rejected outright, even though every other field
// (audience, recipient, InResponseTo, conditions) is otherwise valid.
func TestSAMLHandleACS_RejectsUntrustedSigner(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)
	org := setupSAMLTestOrg(ctx, t, svc)

	spClient, err := svc.serviceProvider(ctx)
	require.NoError(t, err)

	spMetadata := spClient.Metadata()
	session := defaultTestSession("test@example.com", "Test User")
	state := mintSAMLState(ctx, t, svc, org.Slug)

	// A rogue IdP with its own, unpublished signing identity — its
	// EntityID/SSO URL are irrelevant here since we drive assertion
	// construction directly rather than through ServeSSO/GetServiceProvider;
	// what matters is that its key/certificate never appear in svc's
	// configured IDPMetadataXML.
	rogue := newFakeSAMLIdP(t)

	req := buildSignedAssertionRequest(t, rogue, spMetadata, state.RequestID, session, time.Now(), svc.acsURL())

	_, err = svc.HandleACS(ctx, req, state)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSAMLAssertionInvalid)
}

func TestSAMLHandleACS_EnforcesMaxSSOUsers(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, &stubEntitlementsChecker{err: errSSOQuotaTest})
	org := setupSAMLTestOrg(ctx, t, svc)

	spClient, err := svc.serviceProvider(ctx)
	require.NoError(t, err)

	spMetadata := spClient.Metadata()
	session := defaultTestSession("test@example.com", "Test User")
	state := mintSAMLState(ctx, t, svc, org.Slug)

	req := buildSignedAssertionRequest(t, idp, spMetadata, state.RequestID, session, time.Now(), svc.acsURL())

	_, err = svc.HandleACS(ctx, req, state)
	require.Error(t, err)
	require.ErrorIs(t, err, errSSOQuotaTest)

	// The membership must not have been created since the quota check
	// (CheckSSOSlot -> entitlements.CheckSSOMembership) failed before
	// CreateOrganizationMember ran.
	members, err := svc.db.ListMembersByOrg(ctx, org.UID)
	require.NoError(t, err)
	assert.Empty(t, members)
}

// createExistingSAMLLocalUser simulates a pre-existing, password-based
// account (e.g. the documented default super-admin) that a SAML assertion
// might legitimately want to link to once fully validated.
func createExistingSAMLLocalUser(ctx context.Context, t *testing.T, svc *SAMLService, email string) *models.User {
	t.Helper()

	hash := "not-a-real-hash"
	user := models.NewUser(email)
	user.Name = "Existing Local User"
	user.PasswordHash = &hash

	require.NoError(t, svc.db.CreateUser(ctx, user))

	return user
}

// TestSAMLHandleACS_ValidatedAssertionLinksToExistingUser exercises the
// linking-by-email path explicitly, since it's the security-sensitive
// behavior a review will focus on (see SAMLService's doc comment on why
// this is safe once ParseResponse has validated the assertion).
func TestSAMLHandleACS_ValidatedAssertionLinksToExistingUser(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)
	org := setupSAMLTestOrg(ctx, t, svc)

	const email = "legit-user@example.com"

	existing := createExistingSAMLLocalUser(ctx, t, svc, email)

	spClient, err := svc.serviceProvider(ctx)
	require.NoError(t, err)

	spMetadata := spClient.Metadata()
	session := defaultTestSession(email, "Legit User")
	state := mintSAMLState(ctx, t, svc, org.Slug)

	req := buildSignedAssertionRequest(t, idp, spMetadata, state.RequestID, session, time.Now(), svc.acsURL())

	result, err := svc.HandleACS(ctx, req, state)
	require.NoError(t, err)
	assert.Equal(t, existing.UID, result.UserUID)

	provider, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSAML, session.NameID)
	require.NoError(t, err)
	assert.Equal(t, existing.UID, provider.UserUID)

	member, err := svc.db.GetMemberByUserAndOrg(ctx, existing.UID, org.UID)
	require.NoError(t, err)
	assert.Equal(t, existing.UID, member.UserUID)
}

func TestSAMLHandleACS_RejectsInvalidRelayState(t *testing.T) {
	t.Parallel()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)

	_, err := svc.ValidateRelayState(ctx, "not-a-real-nonce")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSAMLInvalidState)
}

func TestSAMLGenerateAuthnRequest_NotConfigured(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		SAML: config.SAMLConfig{Enabled: false},
		Auth: config.AuthConfig{JWTSecret: "test-jwt-secret"},
		Server: config.ServerConfig{
			BaseURL: "http://localhost:4000",
		},
	}
	authService := NewService(dbService, cfg.Auth, cfg, nil, nil)
	svc := NewSAMLService(dbService, cfg, authService)

	_, err = svc.GenerateAuthnRequest(ctx, "/", "test-org")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSAMLNotConfigured)
}
