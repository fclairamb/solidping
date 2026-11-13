package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// samlWorld is the SAML service wired to the fake IdP, with one organization.
// It is shared by the subtests of a test: every sign-in uses its own address.
type samlWorld struct {
	svc   *SAMLService
	ctx   context.Context //nolint:containedctx // test fixture
	idp   *fakeSAMLIdP
	org   *models.Organization
	fault *queryFault
	seq   int
}

func newSAMLWorld(t *testing.T) *samlWorld {
	t.Helper()

	idp := newFakeSAMLIdP(t)
	svc, ctx := setupSAMLTestService(t, idp, nil)

	return &samlWorld{svc: svc, ctx: ctx, idp: idp, org: setupSAMLTestOrg(ctx, t, svc), fault: installQueryFault(svc.db)}
}

// session is a fresh identity at the IdP.
func (w *samlWorld) session() *saml.Session {
	w.seq++

	return defaultTestSession(fmt.Sprintf("member%d@acme.com", w.seq), "Member")
}

// consume posts a signed assertion for session to the ACS, the way the IdP
// answers a login the service started.
func (w *samlWorld) consume(t *testing.T, session *saml.Session, mutate func(*SAMLRelayState)) (*SAMLResult, error) {
	t.Helper()

	state := mintSAMLState(w.ctx, t, w.svc, w.org.Slug)

	spClient, err := w.svc.serviceProvider(w.ctx)
	require.NoError(t, err)

	req := buildSignedAssertionRequest(t, w.idp, spClient.Metadata(), state.RequestID, session, time.Now(), w.svc.acsURL())

	if mutate != nil {
		mutate(state)
	}

	return w.svc.HandleACS(w.ctx, req, state)
}

func (w *samlWorld) account(email string) *models.User {
	user, err := w.svc.db.GetUserByEmail(w.ctx, email)
	if err != nil {
		return nil
	}

	return user
}

func samlAttribute(name, friendlyName string, values ...string) saml.Attribute {
	attr := saml.Attribute{Name: name, FriendlyName: friendlyName}
	for _, value := range values {
		attr.Values = append(attr.Values, saml.AttributeValue{Value: value})
	}

	return attr
}

func samlAssertion(nameID string, attrs ...saml.Attribute) *saml.Assertion {
	return &saml.Assertion{
		Subject:             &saml.Subject{NameID: &saml.NameID{Value: nameID}},
		AttributeStatements: []saml.AttributeStatement{{Attributes: attrs}},
	}
}

// TestSAMLAssertionMapping pins which attributes of an assertion become the
// address and the name of the account.
func TestSAMLAssertionMapping(t *testing.T) {
	t.Parallel()

	plain := &SAMLService{cfg: &config.Config{}}
	mapped := &SAMLService{cfg: &config.Config{SAML: config.SAMLConfig{
		EmailAttribute: "Contact", NameAttribute: "Nickname",
	}}}

	tests := []struct {
		name      string
		svc       *SAMLService
		assertion *saml.Assertion
		want      samlUserInfo
	}{
		{
			"well-known attributes", plain,
			samlAssertion("subject-1", samlAttribute("mail", "", "alice@acme.com"), samlAttribute("cn", "", "Alice")),
			samlUserInfo{NameID: "subject-1", Email: "alice@acme.com", Name: "Alice"},
		},
		{
			"friendly names, any case", plain,
			samlAssertion("subject-1",
				samlAttribute("urn:x", "Email", "alice@acme.com"), samlAttribute("urn:y", "DisplayName", "Alice")),
			samlUserInfo{NameID: "subject-1", Email: "alice@acme.com", Name: "Alice"},
		},
		{
			"configured attributes win over the well-known ones", mapped,
			samlAssertion("subject-1",
				samlAttribute("mail", "", "old@acme.com"), samlAttribute("contact", "", "alice@acme.com"),
				samlAttribute("cn", "", "A. Lice"), samlAttribute("nickname", "", "Alice")),
			samlUserInfo{NameID: "subject-1", Email: "alice@acme.com", Name: "Alice"},
		},
		{
			"an attribute without value is ignored", plain,
			samlAssertion("subject-1", samlAttribute("mail", ""), samlAttribute("email", "", "alice@acme.com")),
			samlUserInfo{NameID: "subject-1", Email: "alice@acme.com", Name: "alice@acme.com"},
		},
		{
			"a NameID that is an address is the email", plain,
			samlAssertion("alice@acme.com"),
			samlUserInfo{NameID: "alice@acme.com", Email: "alice@acme.com", Name: "alice@acme.com"},
		},
		{
			"an opaque NameID is not an email", plain,
			samlAssertion("subject-1", samlAttribute("cn", "", "Alice")),
			samlUserInfo{NameID: "subject-1", Name: "Alice"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, &tt.want, tt.svc.mapAssertion(tt.assertion))
		})
	}
}

// TestSAMLServiceProviderKeyPair: the keypair of the service provider is
// generated once and persisted, so every replica and every restart publish
// the certificate the IdP was configured with.
func TestSAMLServiceProviderKeyPair(t *testing.T) { //nolint:tparallel // subtests share one database
	t.Parallel()

	w := newSAMLWorld(t)

	_, err := w.svc.Metadata(w.ctx)
	require.NoError(t, err)

	first := w.svc.spCertificate.Raw
	require.NotEmpty(t, first)

	restarted := func() *SAMLService {
		return NewSAMLService(w.svc.db, w.svc.cfg, w.svc.authService)
	}

	// A restarted service loads the persisted keypair.
	{
		svc := restarted()

		_, err := svc.Metadata(w.ctx)
		require.NoError(t, err)
		require.Equal(t, first, svc.spCertificate.Raw)
	}

	reads := map[string]string{
		appSettingKeySPPrivateKey:  "saml: read SP private key",
		appSettingKeySPCertificate: "saml: read SP certificate",
	}
	for key, wantMsg := range reads { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run("a keypair that cannot be read is an error: "+key, func(t *testing.T) {
			svc := restarted()

			var err error

			w.fault.during(t, failingSQL(`FROM "app_settings"`, key), func() { _, err = svc.Metadata(w.ctx) })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, wantMsg)
			require.Nil(t, svc.spCertificate, "no throwaway keypair replaces the persisted one")
		})
	}

	// An unreadable persisted keypair is replaced.
	{
		require.NoError(t, w.svc.db.SetAppSetting(w.ctx, appSettingKeySPPrivateKey, "not a PEM block"))

		svc := restarted()

		_, err := svc.Metadata(w.ctx)
		require.NoError(t, err)
		require.NotEqual(t, first, svc.spCertificate.Raw)

		again := restarted()

		_, err = again.Metadata(w.ctx)
		require.NoError(t, err)
		require.Equal(t, svc.spCertificate.Raw, again.spCertificate.Raw, "the replacement was persisted")
	}

	// A keypair that cannot be persisted still serves.
	{
		require.NoError(t, w.svc.db.SetAppSetting(w.ctx, appSettingKeySPPrivateKey, "not a PEM block"))

		svc := restarted()

		var (
			metadata []byte
			err      error
		)

		w.fault.during(t, failingSQL(`INTO "app_settings"`), func() { metadata, err = svc.Metadata(w.ctx) })
		require.NoError(t, err)
		require.Contains(t, string(metadata), "EntityDescriptor")
	}
}

// TestSAMLMetadataIdentity covers the identity the service provider
// publishes and the configurations it refuses.
func TestSAMLMetadataIdentity(t *testing.T) {
	t.Parallel()

	w := newSAMLWorld(t)

	// The entity ID defaults to the metadata URL.
	{
		metadata, err := w.svc.Metadata(w.ctx)
		require.NoError(t, err)
		require.Contains(t, string(metadata), `entityID="http://localhost:4000`+samlMetadataPath+`"`)
	}

	// A configured entity ID is published as is.
	{
		w.svc.cfg.SAML.SPEntityID = " urn:acme:solidping "

		metadata, err := w.svc.Metadata(w.ctx)
		require.NoError(t, err)
		require.Contains(t, string(metadata), `entityID="urn:acme:solidping"`)
	}

	// A base URL that is not a URL is refused.
	{
		healthy := w.svc.cfg.Server.BaseURL
		w.svc.cfg.Server.BaseURL = "://not-a-url"

		_, err := w.svc.Metadata(w.ctx)
		w.svc.cfg.Server.BaseURL = healthy

		require.ErrorContains(t, err, "saml: invalid ACS URL")
	}

	// Switched off.
	{
		w.svc.cfg.SAML.Enabled = false

		_, err := w.svc.Metadata(w.ctx)
		w.svc.cfg.SAML.Enabled = true

		require.ErrorIs(t, err, ErrSAMLNotConfigured)
	}
}

// TestSAMLIdPMetadataSources: the IdP metadata comes from the configured
// document or URL. A source that cannot be used fails the login with
// ErrSAMLIDPMetadata, which the login page reports as a configuration error.
func TestSAMLIdPMetadataSources(t *testing.T) {
	t.Parallel()

	w := newSAMLWorld(t)
	document := w.idp.metadataXML(t)

	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/samlmetadata+xml")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(document))
	}))
	t.Cleanup(server.Close)

	login := func() (string, error) { return w.svc.GenerateAuthnRequest(w.ctx, "/d", w.org.Slug) }

	// Document that is not metadata.
	{
		w.svc.cfg.SAML.IDPMetadataXML = "<EntityDescriptor"

		_, err := login()
		require.ErrorIs(t, err, ErrSAMLIDPMetadata)
	}

	// Document without a redirect sign-on endpoint.
	{
		w.svc.cfg.SAML.IDPMetadataXML = strings.ReplaceAll(document, saml.HTTPRedirectBinding, saml.HTTPArtifactBinding)

		_, err := login()
		require.ErrorIs(t, err, ErrSAMLNoSSOEndpoint)
	}

	// URL that is not a URL.
	{
		w.svc.cfg.SAML.IDPMetadataXML = ""
		w.svc.cfg.SAML.IDPMetadataURL = "://not-a-url"

		_, err := login()
		require.ErrorIs(t, err, ErrSAMLIDPMetadata)
		require.ErrorContains(t, err, "invalid metadata URL")
	}

	// URL answering an error.
	{
		status = http.StatusInternalServerError
		w.svc.cfg.SAML.IDPMetadataXML = ""
		w.svc.cfg.SAML.IDPMetadataURL = server.URL + "/failing"

		_, err := login()
		require.ErrorIs(t, err, ErrSAMLIDPMetadata)
	}

	// URL serving the metadata.
	{
		status = http.StatusOK
		w.svc.cfg.SAML.IDPMetadataXML = ""
		w.svc.cfg.SAML.IDPMetadataURL = server.URL + "/metadata"

		redirect, err := login()
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(redirect, w.idp.idp.SSOURL.String()), redirect)
	}
}

// TestSAMLRelayState is the relay state contract, the SAML twin of
// TestOAuthStateIsRefusedWhenUnusable.
func TestSAMLRelayState(t *testing.T) { //nolint:tparallel // subtests share one service
	t.Parallel()

	w := newSAMLWorld(t)
	stale := jsonBody(t, SAMLRelayState{OrgSlug: w.org.Slug, CreatedAt: time.Now().Add(-time.Hour).Unix()})

	entries := map[string]models.JSONMap{
		"state that is not a string":   {keyState: 42},
		"state that is not a document": {keyState: "{"},
		"state older than its TTL":     {keyState: stale},
	}
	for name, value := range entries { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(name, func(t *testing.T) {
			nonce := "nonce-" + strings.ReplaceAll(name, " ", "-")
			require.NoError(t, w.svc.db.SetStateEntry(w.ctx, nil, samlStatePrefix+nonce, &value, nil))

			state, err := w.svc.ValidateRelayState(w.ctx, nonce)
			require.ErrorIs(t, err, ErrSAMLInvalidState)
			require.Nil(t, state)
		})
	}

	// State that cannot be stored.
	{
		var err error

		w.fault.during(t, failingSQL(`"state_entries"`), func() {
			_, err = w.svc.GenerateAuthnRequest(w.ctx, "/d", w.org.Slug)
		})
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "saml: failed to store relay state")
	}
}

// TestSAMLAssertionConsumerFailures mirrors
// TestProviderCallbackWhenTheStoreFails for SAML, plus the assertions an
// account cannot be built from.
func TestSAMLAssertionConsumerFailures(t *testing.T) { //nolint:tparallel // subtests share one IdP
	t.Parallel()

	w := newSAMLWorld(t)

	// Assertion without an address.
	{
		session := w.session()
		session.UserEmail = ""
		session.NameID = "opaque-subject"

		result, err := w.consume(t, session, nil)
		require.ErrorIs(t, err, ErrSAMLNoEmail)
		require.Nil(t, result)
	}

	// Login started for an organization that is gone.
	{
		session := w.session()

		_, err := w.consume(t, session, func(state *SAMLRelayState) { state.OrgSlug = "no-such-org" })
		require.ErrorContains(t, err, "organization not found")
		require.Nil(t, w.account(session.UserEmail))
	}

	// No NameID: linked by the address.
	{
		session := w.session()
		session.NameID = ""

		_, err := w.consume(t, session, nil)
		require.NoError(t, err)

		link, err := w.svc.db.GetUserProviderByProviderID(w.ctx, models.ProviderTypeSAML, session.UserEmail)
		require.NoError(t, err)
		require.Equal(t, w.account(session.UserEmail).UID, link.UserUID)
	}

	steps := []struct {
		name    string
		fault   sqlFault
		wantMsg string
	}{
		{"looking the provider link up", failingSQL(`FROM "user_providers"`), "failed to get user provider"},
		{"looking the account up by email", failingSQL(`FROM "users"`), "failed to get user by email"},
		{"creating the account", failingSQL(`INSERT INTO "users"`), "failed to create user"},
		{"creating the provider link", failingSQL(`INSERT INTO "user_providers"`), "failed to create user provider"},
	}
	for _, st := range steps { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(st.name, func(t *testing.T) {
			session := w.session()

			var err error

			w.fault.during(t, st.fault, func() { _, err = w.consume(t, session, nil) })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, st.wantMsg)

			result, err := w.consume(t, session, nil)
			require.NoError(t, err, "the sign-in works once the store is back")
			require.NotEmpty(t, result.AccessToken)
		})
	}

	// Reading the account behind an existing link.
	{
		session := w.session()

		_, err := w.consume(t, session, nil)
		require.NoError(t, err)

		w.fault.during(t, failingSQL(`FROM "users"`), func() { _, err = w.consume(t, session, nil) })
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "failed to get user behind provider link")
	}
}

// TestSAMLRoutes covers the answers of the SAML routes that are not the
// happy path.
func TestSAMLRoutes(t *testing.T) {
	t.Parallel()

	w := newSAMLWorld(t)
	handler := NewSAMLHandler(w.svc, w.svc.cfg)
	login := "/api/v1/auth/saml/login?org=" + w.org.Slug

	acs := func(t *testing.T, form string) *httptest.ResponseRecorder {
		t.Helper()

		req := httptest.NewRequestWithContext(w.ctx, http.MethodPost, "/api/v1/auth/saml/acs", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rec := httptest.NewRecorder()
		require.NoError(t, handler.ACS(rec, req))

		return rec
	}

	// Login without an organization.
	{
		_, code, _ := errorRedirect(t, serveRoute(t, handler.Login, "/api/v1/auth/saml/login"))
		require.Equal(t, "VALIDATION_ERROR", code)
	}

	// Login for an unknown organization.
	{
		_, code, _ := errorRedirect(t, serveRoute(t, handler.Login, "/api/v1/auth/saml/login?org=no-such-org"))
		require.Equal(t, "ORGANIZATION_NOT_FOUND", code)
	}

	// Assertion posted without a relay state.
	{
		_, code, _ := errorRedirect(t, acs(t, "SAMLResponse=abc"))
		require.Equal(t, OAuthCodeInvalidState, code)
	}

	// Assertion posted with an unknown relay state.
	{
		_, code, _ := errorRedirect(t, acs(t, "SAMLResponse=abc&RelayState=no-such-state"))
		require.Equal(t, OAuthCodeInvalidState, code)
	}

	// Assertion posted as a form that cannot be parsed.
	{
		rec := acs(t, "SAMLResponse=%zz")
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "VALIDATION_ERROR", errorBody(t, rec)["code"])
	}

	// A store failure is a 500 on login and on metadata.
	{
		var rec *httptest.ResponseRecorder

		w.fault.during(t, failingSQL(`"state_entries"`), func() { rec = serveRoute(t, handler.Login, login) })
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Empty(t, rec.Header().Get("Location"))

		restarted := NewSAMLHandler(NewSAMLService(w.svc.db, w.svc.cfg, w.svc.authService), w.svc.cfg)

		w.fault.during(t, failingSQL(`FROM "app_settings"`), func() {
			rec = serveRoute(t, restarted.Metadata, "/api/v1/auth/saml/metadata")
		})
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Equal(t, "INTERNAL_ERROR", errorBody(t, rec)["code"])
	}

	// Changes the configuration, so it runs last.
	// Switched off: login and metadata are not found.
	{
		w.svc.cfg.SAML.Enabled = false

		routes := map[string]httpHandlerFunc{login: handler.Login, "/api/v1/auth/saml/metadata": handler.Metadata}
		for target, route := range routes {
			rec := serveRoute(t, route, target)
			require.Equal(t, http.StatusNotFound, rec.Code, target)
			require.Equal(t, "NOT_FOUND", errorBody(t, rec)["code"], target)
		}
	}
}
