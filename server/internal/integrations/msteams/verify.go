package msteams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
type BotClaims struct {
	Issuer     string
	Audience   string
	TenantID   string
	ServiceURL string
	ExpiresAt  time.Time
}

// rawClaims mirrors the JWT payload. `aud` may be a string or an array of
// strings depending on the token flavour, hence the custom type.
type rawClaims struct {
	Issuer     string      `json:"iss"`
	Audience   audienceSet `json:"aud"`
	Expiry     int64       `json:"exp"`
	NotBefore  int64       `json:"nbf"`
	TenantID   string      `json:"tid"`
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
type metadataDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// Verifier validates inbound Bot Connector tokens. It is the Teams analogue
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
	// AllowedTenantID, when non-empty, restricts inbound activities to a
	// single Entra tenant (self-hosted single-tenant deployments).
	AllowedTenantID string

	httpClient *http.Client

	mu         sync.Mutex
	cachedURL  string
	issuer     string
	keySet     oidc.KeySet
	fetchedAt  time.Time
	timeSource func() time.Time
}

// NewVerifier builds a Verifier for the given app ID and optional tenant
// allow-list.
func NewVerifier(appID, allowedTenantID string) *Verifier {
	return &Verifier{
		MetadataURL:     DefaultMetadataURL,
		AppID:           appID,
		AllowedTenantID: allowedTenantID,
		httpClient:      &http.Client{Timeout: metadataTimeout},
	}
}

// now returns the current time, overridable in tests.
func (v *Verifier) now() time.Time {
	if v.timeSource != nil {
		return v.timeSource()
	}

	return time.Now()
}

// keys returns the cached (issuer, key set), fetching the metadata document
// when the cache is cold, stale, or the metadata URL changed.
func (v *Verifier) keys(ctx context.Context) (string, oidc.KeySet, error) {
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
		return v.issuer, v.keySet, nil
	}

	doc, err := v.fetchMetadata(ctx, metadataURL)
	if err != nil {
		return "", nil, err
	}

	// The remote key set owns a background refresh loop keyed off the context
	// it is built with, so it must outlive the request that happened to
	// trigger the (re)fetch. WithoutCancel keeps any client/values from the
	// request context while dropping its cancellation.
	v.issuer = doc.Issuer
	v.keySet = oidc.NewRemoteKeySet(context.WithoutCancel(ctx), doc.JWKSURI)
	v.cachedURL = metadataURL
	v.fetchedAt = v.now()

	return v.issuer, v.keySet, nil
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
// to make us call a different (attacker-controlled) connector endpoint. The
// claim is optional in some token flavours; when absent, the check is skipped
// rather than failing closed on a legitimate token shape.
func (v *Verifier) VerifyToken(ctx context.Context, rawToken, serviceURL string) (*BotClaims, error) {
	if v.AppID == "" {
		return nil, ErrNotConfigured
	}

	if rawToken == "" {
		return nil, ErrMissingAuthorization
	}

	issuer, keySet, err := v.keys(ctx)
	if err != nil {
		return nil, err
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
		TenantID:   claims.TenantID,
		ServiceURL: claims.ServiceURL,
		ExpiresAt:  time.Unix(claims.Expiry, 0),
	}, nil
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

	if claims.ServiceURL != "" && serviceURL != "" &&
		normalizeServiceURL(claims.ServiceURL) != normalizeServiceURL(serviceURL) {
		return fmt.Errorf("%w: serviceurl claim does not match the activity", ErrInvalidToken)
	}

	if v.AllowedTenantID != "" && claims.TenantID != v.AllowedTenantID {
		return fmt.Errorf("%w: %q", ErrTenantNotAllowed, claims.TenantID)
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
