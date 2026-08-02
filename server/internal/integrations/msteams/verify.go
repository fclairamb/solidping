package msteams

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// DefaultMetadataURL is Microsoft's Bot Framework OpenID metadata document.
// It is NOT a standard OIDC issuer URL (the document lives on
// login.botframework.com while the issuer it advertises is
// api.botframework.com), so we fetch it directly instead of going through
// oidc.NewProvider, then hand the advertised jwks_uri to a remote key set.
const DefaultMetadataURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"

const (
	// metadataTTL is how long a fetched metadata document (issuer + jwks_uri)
	// is reused before being re-fetched. Microsoft rotates signing keys, but
	// the remote key set handles kid rotation on its own; this TTL only
	// covers a change of the jwks_uri itself, which is very rare.
	metadataTTL = 24 * time.Hour
	// metadataTimeout bounds the metadata fetch.
	metadataTimeout = 10 * time.Second
	// clockSkew is the tolerance applied to exp/nbf so a small clock drift
	// between this instance and Microsoft doesn't reject valid traffic.
	clockSkew = 5 * time.Minute
	// bearerPrefix is the expected Authorization scheme.
	bearerPrefix = "Bearer "
)

var (
	// ErrMissingAuthorization is returned when the Authorization header is
	// absent or not a Bearer token.
	ErrMissingAuthorization = errors.New("missing or malformed Authorization header")
	// ErrNotConfigured is returned when the Teams bot has no app ID, so no
	// audience can be checked and no token can be trusted.
	ErrNotConfigured = errors.New("microsoft teams bot is not configured")
	// ErrInvalidToken is returned when signature verification or claim
	// validation fails.
	ErrInvalidToken = errors.New("invalid bot framework token")
	// ErrMetadataFetch is returned when the OpenID metadata document cannot
	// be retrieved.
	ErrMetadataFetch = errors.New("failed to fetch bot framework metadata")
	// ErrTenantNotAllowed is returned when the token's tenant is outside the
	// configured single-tenant allow-list.
	ErrTenantNotAllowed = errors.New("tenant not allowed")
)

// BotClaims is the validated subset of an inbound Bot Connector token.
//
// There is deliberately NO tenant field here. Microsoft documents the
// Connector-to-Bot token payload as exactly `aud` / `iss` / `nbf` / `exp`
// (plus the `serviceurl` binding claim) — it carries no `tid`, because the
// token authenticates *the Bot Connector service calling this bot*, not the
// Microsoft 365 tenant the activity originated in. See
// https://learn.microsoft.com/en-us/azure/bot-service/rest-api/bot-framework-rest-connector-authentication
// ("Connector to Bot: example JWT components").
//
// Tenant identity therefore only ever comes from the activity body
// (`channelData.tenant.id`), which is populated by Bot Framework itself and
// is trustworthy exactly because the request carrying it passed this
// signature check. What the body cannot prove is *which SolidPing org owns
// that tenant* — that is what the linking-code proof-of-possession flow in
// linkcode.go / Service.LinkTenant exists for. Nothing here silently
// discards a claim: there is no tenant claim to check.
type BotClaims struct {
	Issuer     string
	Audience   string
	ServiceURL string
	ExpiresAt  time.Time
}

// rawClaims mirrors the JWT payload. `aud` may be a string or an array of
// strings depending on the token flavor, hence the custom type.
type rawClaims struct {
	Issuer     string      `json:"iss"`
	Audience   audienceSet `json:"aud"`
	Expiry     int64       `json:"exp"`
	NotBefore  int64       `json:"nbf"`
	ServiceURL string      `json:"serviceurl"`
}

// audienceSet decodes the `aud` claim from either JSON shape.
type audienceSet []string

// UnmarshalJSON accepts both `"aud": "x"` and `"aud": ["x","y"]`.
func (a *audienceSet) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = audienceSet{single}

		return nil
	}

	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("decoding aud claim: %w", err)
	}

	*a = many

	return nil
}

func (a audienceSet) contains(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}

	return false
}

// metadataDocument is the subset of the OpenID metadata we need.
//
//nolint:tagliatelle // snake_case is the OpenID discovery wire format
type metadataDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
	// SigningAlgs is `id_token_signing_alg_values_supported`. Microsoft's
	// verification step 6 requires the signature to be checked *using the
	// algorithm advertised here*, so we pin `alg` against this list rather
	// than accepting whatever the token header asks for.
	SigningAlgs []string `json:"id_token_signing_alg_values_supported"`
}

// Verifier validates inbound Bot Connector tokens. It is the Teams analog
// of slack/verify.go's signing-secret check, but asymmetric: Microsoft signs
// with a rotating key set we fetch from their published JWKS, and there is no
// shared secret to compare against.
//
// A single Verifier is shared by the whole process; the metadata document and
// the remote key set are cached in-process (the key set refreshes itself on
// an unknown kid).
type Verifier struct {
	// MetadataURL is the OpenID metadata document to trust. Defaults to
	// DefaultMetadataURL; overridden in tests to point at an httptest fake.
	MetadataURL string
	// AppID is the expected audience — the Entra application (client) ID.
	AppID string

	httpClient *http.Client

	mu          sync.Mutex
	cachedURL   string
	issuer      string
	signingAlgs []string
	keySet      oidc.KeySet
	fetchedAt   time.Time
	timeSource  func() time.Time
}

// NewVerifier builds a Verifier for the given app ID.
//
// It deliberately takes no tenant allow-list: the Connector-to-Bot token has
// no tenant claim (see BotClaims), so a tenant restriction enforced here
// would compare against an always-empty value and reject every legitimate
// activity. The single-tenant pin (`msteams.tenant_id`) is enforced one layer
// up, in Service.checkTenantAllowed, against the activity body's tenant —
// after this signature check has established that the request really came
// from Bot Framework.
func NewVerifier(appID string) *Verifier {
	return &Verifier{
		MetadataURL: DefaultMetadataURL,
		AppID:       appID,
		httpClient:  &http.Client{Timeout: metadataTimeout},
	}
}

// now returns the current time, overridable in tests.
func (v *Verifier) now() time.Time {
	if v.timeSource != nil {
		return v.timeSource()
	}

	return time.Now()
}

// keys returns the cached (issuer, signing algorithms, key set), fetching the
// metadata document when the cache is cold, stale, or the metadata URL
// changed.
func (v *Verifier) keys(ctx context.Context) (string, []string, oidc.KeySet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	metadataURL := v.MetadataURL
	if metadataURL == "" {
		metadataURL = DefaultMetadataURL
	}

	fresh := v.keySet != nil &&
		v.cachedURL == metadataURL &&
		v.now().Sub(v.fetchedAt) < metadataTTL

	if fresh {
		return v.issuer, v.signingAlgs, v.keySet, nil
	}

	doc, err := v.fetchMetadata(ctx, metadataURL)
	if err != nil {
		return "", nil, nil, err
	}

	// The remote key set owns a background refresh loop keyed off the context
	// it is built with, so it must outlive the request that happened to
	// trigger the (re)fetch. WithoutCancel keeps any client/values from the
	// request context while dropping its cancellation.
	v.issuer = doc.Issuer
	v.signingAlgs = doc.SigningAlgs
	v.keySet = oidc.NewRemoteKeySet(context.WithoutCancel(ctx), doc.JWKSURI)
	v.cachedURL = metadataURL
	v.fetchedAt = v.now()

	return v.issuer, v.signingAlgs, v.keySet, nil
}

// fetchMetadata retrieves and validates the OpenID metadata document.
func (v *Verifier) fetchMetadata(ctx context.Context, metadataURL string) (*metadataDocument, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMetadataFetch, err)
	}

	client := v.httpClient
	if client == nil {
		client = &http.Client{Timeout: metadataTimeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMetadataFetch, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrMetadataFetch, resp.StatusCode)
	}

	var doc metadataDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMetadataFetch, err)
	}

	if doc.JWKSURI == "" || doc.Issuer == "" {
		return nil, fmt.Errorf("%w: metadata is missing issuer or jwks_uri", ErrMetadataFetch)
	}

	return &doc, nil
}

// VerifyToken validates a raw Bot Connector JWT and returns its claims.
//
// serviceURL is the `serviceUrl` of the activity carried in the same request.
// Bot Framework binds a token to the service URL it was minted for, so
// checking it stops a token captured from one tenant's traffic being replayed
// to make us call a different (attacker-controlled) connector endpoint.
//
// The check is UNCONDITIONAL (Microsoft's verification requirement 7): a
// missing claim, or an activity with no serviceUrl, is a rejection. Treating
// either as "not applicable" would be fail-open, since the request body is
// chosen by whoever replays the token.
func (v *Verifier) VerifyToken(ctx context.Context, rawToken, serviceURL string) (*BotClaims, error) {
	if v.AppID == "" {
		return nil, ErrNotConfigured
	}

	if rawToken == "" {
		return nil, ErrMissingAuthorization
	}

	issuer, signingAlgs, keySet, err := v.keys(ctx)
	if err != nil {
		return nil, err
	}

	// Microsoft's verification step 6 requires the signature to be checked
	// with the algorithm advertised by the metadata document. go-oidc's
	// RemoteKeySet.VerifySignature documents that it does NOT validate `alg`
	// itself, so pin it here before handing the token over.
	if algErr := checkTokenAlg(rawToken, signingAlgs); algErr != nil {
		return nil, algErr
	}

	payload, err := keySet.VerifySignature(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("%w: signature: %w", ErrInvalidToken, err)
	}

	var claims rawClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("%w: claims: %w", ErrInvalidToken, err)
	}

	if err := v.validateClaims(&claims, issuer, serviceURL); err != nil {
		return nil, err
	}

	return &BotClaims{
		Issuer:     claims.Issuer,
		Audience:   v.AppID,
		ServiceURL: claims.ServiceURL,
		ExpiresAt:  time.Unix(claims.Expiry, 0),
	}, nil
}

// tokenHeader is the JWS header subset we inspect before verification.
type tokenHeader struct {
	Alg string `json:"alg"`
}

// checkTokenAlg pins the token's signing algorithm to the set advertised by
// the OpenID metadata document (`id_token_signing_alg_values_supported`,
// which is ["RS256"] for Bot Framework). Without this the library would
// accept any algorithm in its own internal allow-list.
func checkTokenAlg(rawToken string, allowed []string) error {
	if len(allowed) == 0 {
		return fmt.Errorf("%w: metadata advertises no signing algorithms", ErrMetadataFetch)
	}

	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: malformed JWT", ErrInvalidToken)
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("%w: malformed JWT header: %w", ErrInvalidToken, err)
	}

	var header tokenHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return fmt.Errorf("%w: malformed JWT header: %w", ErrInvalidToken, err)
	}

	if !slices.Contains(allowed, header.Alg) {
		return fmt.Errorf("%w: signing algorithm %q is not advertised by the metadata document",
			ErrInvalidToken, header.Alg)
	}

	return nil
}

// validateClaims enforces issuer, audience, validity window, service URL
// binding and the optional tenant allow-list.
func (v *Verifier) validateClaims(claims *rawClaims, issuer, serviceURL string) error {
	if claims.Issuer != issuer {
		return fmt.Errorf("%w: issuer %q is not %q", ErrInvalidToken, claims.Issuer, issuer)
	}

	if !claims.Audience.contains(v.AppID) {
		return fmt.Errorf("%w: audience does not include the configured app ID", ErrInvalidToken)
	}

	now := v.now()

	if claims.Expiry == 0 || now.After(time.Unix(claims.Expiry, 0).Add(clockSkew)) {
		return fmt.Errorf("%w: token expired", ErrInvalidToken)
	}

	if claims.NotBefore != 0 && now.Before(time.Unix(claims.NotBefore, 0).Add(-clockSkew)) {
		return fmt.Errorf("%w: token not yet valid", ErrInvalidToken)
	}

	// Microsoft's verification requirement 7 is unconditional: the token's
	// `serviceUrl` claim must match the activity's `serviceUrl`. Treating a
	// missing value as "not applicable" would be fail-open — the body is
	// attacker-chosen, so anyone replaying a captured token could simply omit
	// serviceUrl to skip the binding and redirect our outbound calls.
	if claims.ServiceURL == "" {
		return fmt.Errorf("%w: token carries no serviceurl claim", ErrInvalidToken)
	}

	if serviceURL == "" {
		return fmt.Errorf("%w: activity carries no serviceUrl to bind the token to", ErrInvalidToken)
	}

	if normalizeServiceURL(claims.ServiceURL) != normalizeServiceURL(serviceURL) {
		return fmt.Errorf("%w: serviceurl claim does not match the activity", ErrInvalidToken)
	}

	return nil
}

// normalizeServiceURL makes the serviceUrl comparison insensitive to the
// trailing slash Microsoft is inconsistent about.
func normalizeServiceURL(raw string) string {
	return strings.TrimSuffix(strings.TrimSpace(raw), "/")
}

// TokenFromHeader extracts the raw JWT from an Authorization header value.
func TokenFromHeader(header string) (string, error) {
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", ErrMissingAuthorization
	}

	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", ErrMissingAuthorization
	}

	return token, nil
}
