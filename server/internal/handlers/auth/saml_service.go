package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SAML specific errors.
var (
	ErrSAMLNotConfigured    = errors.New("SAML SP is not configured")
	ErrSAMLIDPMetadata      = errors.New("failed to fetch/parse SAML IdP metadata")
	ErrSAMLInvalidState     = errors.New("invalid or expired SAML relay state")
	ErrSAMLAssertionInvalid = errors.New("SAML assertion validation failed")
	ErrSAMLNoEmail          = errors.New("SAML assertion did not include a usable email address")
	ErrSAMLNoSSOEndpoint    = errors.New("SAML IdP metadata has no HTTP-Redirect SSO endpoint")

	// errSAMLInvalidKeyPEM / errSAMLInvalidCertPEM are wrapped (not returned
	// bare) by parseSAMLKeyPairPEM below.
	errSAMLInvalidKeyPEM  = errors.New("invalid PEM for SP private key")
	errSAMLInvalidCertPEM = errors.New("invalid PEM for SP certificate")
)

const (
	samlStatePrefix          = "saml_state:"
	samlStateTTL             = 10 * time.Minute
	samlMetadataFetchTimeout = 15 * time.Second

	// appSettingKeySPCertificate / appSettingKeySPPrivateKey persist the SP's
	// self-signed keypair across restarts via db.GetAppSetting/SetAppSetting,
	// mirroring the VAPID-key pattern in internal/webpush/vapid.go. The
	// keypair lets the SP publish a certificate in its own metadata (so an
	// IdP can encrypt assertions to it) and decrypt them if it does.
	// AuthnRequests themselves are left unsigned — see serviceProvider's doc.
	appSettingKeySPCertificate = "saml.sp_certificate_pem"
	appSettingKeySPPrivateKey  = "saml.sp_private_key_pem"

	samlSPKeyValidity = 10 * 365 * 24 * time.Hour // 10 years

	samlACSPath      = "/api/v1/auth/saml/acs"
	samlMetadataPath = "/api/v1/auth/saml/metadata"

	// Well-known attribute Name/FriendlyName values (lower-cased) tried, in
	// order, when the configured EmailAttribute/NameAttribute is blank or not
	// present on the assertion. Covers the common "basic" attribute names
	// several IdPs use directly, the LDAP-derived urn:oid names ADFS/AD FS
	// federation commonly emits, and the WS-Federation claim URI.
	samlAttrEmail         = "email"
	samlAttrMail          = "mail"
	samlAttrEmailAddress  = "emailaddress"
	samlAttrMailURI       = "urn:oid:0.9.2342.19200300.100.1.3"
	samlAttrEmailClaimURI = "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"
	samlAttrDisplayName   = "displayname"
	samlAttrCommonName    = "cn"
	samlAttrName          = "name"
	samlAttrCommonNameURI = "urn:oid:2.5.4.3"
)

// SAMLRelayState is the state tracked between GenerateAuthnRequest (the
// login redirect) and HandleACS (the assertion consumer). Stored under a
// random nonce via the same TTL'd db state-entry store the OAuth providers'
// OAuthState uses, and carried as the SAML RelayState parameter — the
// RequestID is what lets ParseResponse enforce InResponseTo (replay/CSRF
// protection): only a request this SP actually issued and hasn't yet
// consumed can complete a login.
type SAMLRelayState struct {
	Nonce       string `json:"nonce"`
	RequestID   string `json:"requestId"`
	RedirectURI string `json:"redirectUri"`
	OrgSlug     string `json:"orgSlug"`
	CreatedAt   int64  `json:"createdAt"`
}

// SAMLResult contains the result of a successful SAML SP-initiated login.
type SAMLResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	OrgSlug      string
	UserUID      string
	// Pending is true when the login succeeded but the org did not admit
	// the user: no membership was created, a membership request is awaiting
	// admin approval, and the tokens above are an org-less session.
	Pending bool
}

// samlUserInfo is the set of identity fields extracted from a validated
// assertion.
type samlUserInfo struct {
	NameID string
	Email  string
	Name   string
}

// SAMLService implements the SAML 2.0 Service Provider role for SolidPing.
//
// Exactly one IdP is supported (spec 2026-07-08-08, part 2 — global-only,
// single instance, SP-initiated login only). IdP metadata (fetched from a
// URL or parsed from pasted XML) is cached in-process and only re-parsed
// when the configured source changes, so a super-admin edit takes effect on
// the next login without a restart.
//
// Trust model / why linking by email is safe here: unlike the generic OIDC
// connector (which had to add an explicit `email_verified` gate — see
// oidc_service.go — because an admin-configured OIDC issuer might not itself
// verify the email it asserts), SAML has no standard "unverified claim"
// escape hatch. The entire assertion, including every attribute, is signed
// by the IdP's private key, and the SP only trusts that key because an
// admin explicitly configured it (via IDPMetadataURL/IDPMetadataXML) — the
// same admin-established trust boundary OIDC relies on for its issuer.
// Once ParseResponse has validated the signature, Conditions, Audience, and
// InResponseTo, the asserted email is exactly as trustworthy as an OIDC
// token's `email_verified: true` claim: linking to a pre-existing local user
// by email is therefore acceptable. The residual risk — an IdP that lets
// end users self-assert their own "email" attribute via a self-service
// profile — is an IdP-configuration concern outside what any SP can detect
// from the wire protocol (SAML has nothing equivalent to OIDC's
// email_verified to check), exactly as an OIDC issuer that doesn't verify
// email ownership is a provider-configuration concern. This is called out
// here so a security review evaluates it explicitly rather than assuming an
// oversight.
type SAMLService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client

	mu            sync.Mutex
	cachedIDPKey  string // cache-invalidation key: IDPMetadataXML + "\x00" + IDPMetadataURL
	idpMetadata   *saml.EntityDescriptor
	spKey         *rsa.PrivateKey
	spCertificate *x509.Certificate
}

// NewSAMLService creates a new SAML SP service.
func NewSAMLService(dbService db.Service, cfg *config.Config, authService *Service) *SAMLService {
	return &SAMLService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
}

func (s *SAMLService) baseURL() string {
	return strings.TrimSuffix(s.cfg.Server.BaseURL, "/")
}

func (s *SAMLService) acsURL() string      { return s.baseURL() + samlACSPath }
func (s *SAMLService) metadataURL() string { return s.baseURL() + samlMetadataPath }

func (s *SAMLService) entityID() string {
	if v := strings.TrimSpace(s.cfg.SAML.SPEntityID); v != "" {
		return v
	}

	return s.metadataURL()
}

// spKeyPair returns the SP's signing/decryption keypair, generating and
// persisting one on first use (mirrors GetOrCreateVAPIDKeys in
// internal/webpush/vapid.go). Cached in-process after the first successful
// load so a restart is the only time the DB is consulted again.
func (s *SAMLService) spKeyPair(ctx context.Context) (*rsa.PrivateKey, *x509.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.spKey != nil && s.spCertificate != nil {
		return s.spKey, s.spCertificate, nil
	}

	keyPEM, keyErr := s.db.GetAppSetting(ctx, appSettingKeySPPrivateKey)
	if keyErr != nil && !errors.Is(keyErr, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("saml: read SP private key: %w", keyErr)
	}

	certPEM, certErr := s.db.GetAppSetting(ctx, appSettingKeySPCertificate)
	if certErr != nil && !errors.Is(certErr, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("saml: read SP certificate: %w", certErr)
	}

	if keyPEM != "" && certPEM != "" {
		key, cert, err := parseSAMLKeyPairPEM(keyPEM, certPEM)
		if err == nil {
			s.spKey, s.spCertificate = key, cert

			return key, cert, nil
		}

		slog.WarnContext(ctx, "saml: failed to parse persisted SP keypair, regenerating", "error", err)
	}

	key, cert, keyPEMOut, certPEMOut, err := generateSAMLKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("saml: generate SP keypair: %w", err)
	}

	if err := s.db.SetAppSetting(ctx, appSettingKeySPPrivateKey, keyPEMOut); err != nil {
		slog.WarnContext(ctx, "saml: failed to persist SP private key", "error", err)
	}

	if err := s.db.SetAppSetting(ctx, appSettingKeySPCertificate, certPEMOut); err != nil {
		slog.WarnContext(ctx, "saml: failed to persist SP certificate", "error", err)
	}

	slog.InfoContext(ctx, "saml: generated SP keypair, persisted to app_settings")

	s.spKey, s.spCertificate = key, cert

	return key, cert, nil
}

// generateSAMLKeyPair creates a fresh RSA-2048 keypair and a long-lived
// self-signed certificate wrapping its public half, returning both the
// parsed values and their PEM encodings (for persistence).
func generateSAMLKeyPair() (*rsa.PrivateKey, *x509.Certificate, string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("generate RSA key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)

	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("generate certificate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "solidping-saml-sp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(samlSPKeyValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("create self-signed certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("parse generated certificate: %w", err)
	}

	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))

	return key, cert, keyPEM, certPEM, nil
}

func parseSAMLKeyPairPEM(keyPEM, certPEM string) (*rsa.PrivateKey, *x509.Certificate, error) {
	keyBlock, _ := pem.Decode([]byte(keyPEM))
	if keyBlock == nil {
		return nil, nil, errSAMLInvalidKeyPEM
	}

	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse SP private key: %w", err)
	}

	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil {
		return nil, nil, errSAMLInvalidCertPEM
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse SP certificate: %w", err)
	}

	return key, cert, nil
}

// spBase builds a ServiceProvider carrying this SP's own identity (entity
// ID, ACS/metadata URLs, keypair) but no IdP metadata attached — enough to
// serve GET /auth/saml/metadata. It requires only Enabled: unlike
// serviceProvider (used for login/ACS), it does not require IdP metadata to
// already be configured, since an admin typically needs *our* metadata
// first, to configure their IdP, before they have IdP metadata to paste
// back.
func (s *SAMLService) spBase(ctx context.Context) (*saml.ServiceProvider, error) {
	if !s.cfg.SAML.Enabled {
		return nil, ErrSAMLNotConfigured
	}

	key, cert, err := s.spKeyPair(ctx)
	if err != nil {
		return nil, err
	}

	acsURL, err := url.Parse(s.acsURL())
	if err != nil {
		return nil, fmt.Errorf("saml: invalid ACS URL: %w", err)
	}

	metadataURLParsed, err := url.Parse(s.metadataURL())
	if err != nil {
		return nil, fmt.Errorf("saml: invalid metadata URL: %w", err)
	}

	return &saml.ServiceProvider{
		EntityID:          s.entityID(),
		Key:               key,
		Certificate:       cert,
		AcsURL:            *acsURL,
		MetadataURL:       *metadataURLParsed,
		AuthnNameIDFormat: saml.EmailAddressNameIDFormat,
	}, nil
}

// serviceProvider builds a fully configured ServiceProvider, including the
// IdP's metadata (fetched from IDPMetadataURL or parsed from
// IDPMetadataXML, cached until the configured source changes). Used by the
// login and ACS paths, which need to know the IdP's SSO endpoint and
// signing certificate(s).
func (s *SAMLService) serviceProvider(ctx context.Context) (*saml.ServiceProvider, error) {
	idpMetadataURL := strings.TrimSpace(s.cfg.SAML.IDPMetadataURL)
	idpMetadataXML := strings.TrimSpace(s.cfg.SAML.IDPMetadataXML)

	if !s.cfg.SAML.Enabled || (idpMetadataURL == "" && idpMetadataXML == "") {
		return nil, ErrSAMLNotConfigured
	}

	spClient, err := s.spBase(ctx)
	if err != nil {
		return nil, err
	}

	metadata, err := s.idpMetadataFor(ctx, idpMetadataXML, idpMetadataURL)
	if err != nil {
		return nil, err
	}

	spClient.IDPMetadata = metadata

	return spClient, nil
}

func (s *SAMLService) idpMetadataFor(
	ctx context.Context, idpMetadataXML, idpMetadataURL string,
) (*saml.EntityDescriptor, error) {
	cacheKey := idpMetadataXML + "\x00" + idpMetadataURL

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.idpMetadata != nil && s.cachedIDPKey == cacheKey {
		return s.idpMetadata, nil
	}

	metadata, err := s.fetchIDPMetadata(ctx, idpMetadataXML, idpMetadataURL)
	if err != nil {
		return nil, err
	}

	s.idpMetadata = metadata
	s.cachedIDPKey = cacheKey

	return metadata, nil
}

func (s *SAMLService) fetchIDPMetadata(
	ctx context.Context, idpMetadataXML, idpMetadataURL string,
) (*saml.EntityDescriptor, error) {
	if idpMetadataXML != "" {
		metadata, err := samlsp.ParseMetadata([]byte(idpMetadataXML))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSAMLIDPMetadata, err)
		}

		return metadata, nil
	}

	metadataURL, err := url.Parse(idpMetadataURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid metadata URL: %w", ErrSAMLIDPMetadata, err)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, samlMetadataFetchTimeout)
	defer cancel()

	metadata, err := samlsp.FetchMetadata(fetchCtx, s.httpClient, *metadataURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSAMLIDPMetadata, err)
	}

	return metadata, nil
}

// Metadata returns this SP's metadata document (entity ID, ACS URL,
// certificate) as XML, for IdP admins to configure their side.
func (s *SAMLService) Metadata(ctx context.Context) ([]byte, error) {
	spClient, err := s.spBase(ctx)
	if err != nil {
		return nil, err
	}

	data, err := xml.MarshalIndent(spClient.Metadata(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("saml: failed to marshal SP metadata: %w", err)
	}

	return data, nil
}

// GenerateAuthnRequest builds an AuthnRequest for the configured IdP and
// returns the URL to redirect the browser to (HTTP-Redirect binding).
// Stores the request ID plus redirectURI/orgSlug under a random nonce
// (RelayState) so HandleACS can validate the response against a request
// this SP actually issued and carry the original returnTo through,
// mirroring the OAuthState pattern used by the OAuth-shaped providers.
func (s *SAMLService) GenerateAuthnRequest(ctx context.Context, redirectURI, orgSlug string) (string, error) {
	spClient, err := s.serviceProvider(ctx)
	if err != nil {
		return "", err
	}

	ssoURL := spClient.GetSSOBindingLocation(saml.HTTPRedirectBinding)
	if ssoURL == "" {
		return "", ErrSAMLNoSSOEndpoint
	}

	authnRequest, err := spClient.MakeAuthenticationRequest(ssoURL, saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", fmt.Errorf("saml: failed to build AuthnRequest: %w", err)
	}

	nonceBytes := make([]byte, 32)
	if _, randErr := rand.Read(nonceBytes); randErr != nil {
		return "", fmt.Errorf("saml: failed to generate nonce: %w", randErr)
	}

	// RawURLEncoding (no "=" padding): authnRequest.Redirect below appends
	// RelayState to the query string verbatim, without percent-encoding, so
	// the nonce must only ever contain characters that are already safe
	// unescaped in a URL query component.
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	state := SAMLRelayState{
		Nonce:       nonce,
		RequestID:   authnRequest.ID,
		RedirectURI: redirectURI,
		OrgSlug:     orgSlug,
		CreatedAt:   time.Now().Unix(),
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("saml: failed to marshal relay state: %w", err)
	}

	stateValue := &models.JSONMap{keyState: string(stateJSON)}
	ttl := samlStateTTL

	if storeErr := s.db.SetStateEntry(ctx, nil, samlStatePrefix+nonce, stateValue, &ttl); storeErr != nil {
		return "", fmt.Errorf("saml: failed to store relay state: %w", storeErr)
	}

	redirectURL, err := authnRequest.Redirect(nonce, spClient)
	if err != nil {
		return "", fmt.Errorf("saml: failed to build redirect URL: %w", err)
	}

	return redirectURL.String(), nil
}

// ValidateRelayState validates and consumes (one-time use) the RelayState
// nonce carried on the ACS POST, returning the state stored by
// GenerateAuthnRequest.
func (s *SAMLService) ValidateRelayState(ctx context.Context, relayState string) (*SAMLRelayState, error) {
	entry, err := s.db.GetStateEntry(ctx, nil, samlStatePrefix+relayState)
	if err != nil || entry == nil {
		return nil, ErrSAMLInvalidState
	}

	// Delete state (one-time use) — a replayed RelayState value cannot be
	// redeemed twice even before we get to the InResponseTo check below.
	_, _ = s.db.DeleteStateEntry(ctx, nil, samlStatePrefix+relayState)

	stateJSON, ok := (*entry.Value)[keyState].(string)
	if !ok {
		return nil, ErrSAMLInvalidState
	}

	var state SAMLRelayState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrSAMLInvalidState
	}

	if time.Now().Unix()-state.CreatedAt > int64(samlStateTTL.Seconds()) {
		return nil, ErrSAMLInvalidState
	}

	return &state, nil
}

// HandleACS validates the SAML assertion delivered to the ACS endpoint and
// resolves/creates the local user. ParseResponse performs the
// security-critical validation: signature (against the IdP's metadata
// certificate), Conditions NotBefore/NotOnOrAfter, Audience restriction
// (must name this SP), SubjectConfirmationData Recipient (must be our ACS
// URL) and NotOnOrAfter, and InResponseTo matched against the request ID we
// actually issued (replay/CSRF protection) — see SAMLService's doc comment
// for why linking the resulting identity by email is safe once that
// validation has passed.
func (s *SAMLService) HandleACS(
	ctx context.Context, httpReq *http.Request, state *SAMLRelayState,
) (*SAMLResult, error) {
	spClient, err := s.serviceProvider(ctx)
	if err != nil {
		return nil, err
	}

	assertion, err := spClient.ParseResponse(httpReq, []string{state.RequestID})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSAMLAssertionInvalid, err)
	}

	userInfo := s.mapAssertion(assertion)
	if userInfo.Email == "" {
		return nil, ErrSAMLNoEmail
	}

	org, err := s.db.GetOrganizationBySlug(ctx, state.OrgSlug)
	if err != nil {
		return nil, fmt.Errorf("organization not found: %w", err)
	}

	user, err := s.findOrCreateUser(ctx, userInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending and an org-less session instead of a membership.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user, WithLoginMethod(signupMethodSAML))
	if err != nil {
		return nil, err
	}

	return &SAMLResult{
		AccessToken:  login.AccessToken,
		RefreshToken: login.RefreshToken,
		ExpiresIn:    login.ExpiresIn,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
		Pending:      login.Pending,
	}, nil
}

// mapAssertion extracts email/name/NameID from a validated assertion, using
// the configured attribute mappings with well-known fallbacks.
func (s *SAMLService) mapAssertion(assertion *saml.Assertion) *samlUserInfo {
	info := &samlUserInfo{}

	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		info.NameID = assertion.Subject.NameID.Value
	}

	attrs := make(map[string]string)

	for _, stmt := range assertion.AttributeStatements {
		for i := range stmt.Attributes {
			attr := &stmt.Attributes[i]
			if len(attr.Values) == 0 {
				continue
			}

			val := attr.Values[0].Value
			if attr.Name != "" {
				attrs[strings.ToLower(attr.Name)] = val
			}

			if attr.FriendlyName != "" {
				attrs[strings.ToLower(attr.FriendlyName)] = val
			}
		}
	}

	info.Email = firstAttr(attrs,
		strings.ToLower(s.cfg.SAML.EmailAttribute),
		samlAttrEmail, samlAttrMail, samlAttrEmailAddress,
		samlAttrMailURI, samlAttrEmailClaimURI,
	)

	// Many IdPs put the email directly in NameID (e.g. NameIDFormat
	// "emailAddress"); only trust it as an email when it looks like one.
	if info.Email == "" && strings.Contains(info.NameID, "@") {
		info.Email = info.NameID
	}

	info.Name = firstAttr(attrs,
		strings.ToLower(s.cfg.SAML.NameAttribute),
		samlAttrDisplayName, samlAttrCommonName, samlAttrName, samlAttrCommonNameURI,
	)
	if info.Name == "" {
		info.Name = info.Email
	}

	return info
}

// firstAttr returns the first non-empty value found in attrs for the given
// candidate keys (already lower-cased), skipping blank candidates (e.g. an
// unconfigured mapping).
func firstAttr(attrs map[string]string, candidates ...string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}

		if v, ok := attrs[c]; ok && v != "" {
			return v
		}
	}

	return ""
}

// findOrCreateUser finds or creates a user by SAML identity (NameID, falling
// back to email when NameID is empty).
func (s *SAMLService) findOrCreateUser(ctx context.Context, userInfo *samlUserInfo) (*models.User, error) {
	providerID := userInfo.NameID
	if providerID == "" {
		providerID = userInfo.Email
	}

	// Check by SAML NameID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSAML, providerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user provider: %w", err)
	}

	if err == nil && provider != nil {
		// A link pointing at a soft-deleted user is stale: it is cleared
		// and we fall through to the email lookup / create path rather
		// than failing this login (and every later one) forever.
		user, resolveErr := resolveLinkedUser(ctx, s.db, models.ProviderTypeSAML, providerID, provider)
		if resolveErr != nil {
			return nil, resolveErr
		}

		if user != nil {
			return user, nil
		}

		provider = nil
	}

	// Check by email
	user, err := s.db.GetUserByEmail(ctx, userInfo.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}

	// Create new user if not found. The email is trusted here because the
	// assertion has already passed full signature/Conditions/Audience
	// validation against an admin-configured IdP — see SAMLService's doc
	// comment for the full rationale.
	if user == nil {
		user = models.NewUser(userInfo.Email)
		user.Name = userInfo.Name

		now := time.Now()
		user.EmailVerifiedAt = &now

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodSAML); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link SAML provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeSAML, providerID)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}
