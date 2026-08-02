package msteams

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	josejwk "github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const (
	testAppID      = "11111111-2222-3333-4444-555555555555"
	testTenantID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testServiceURL = "https://smba.trafficmanager.net/emea/"
	testKeyID      = "bf-test-key-1"
)

// fakeBotFramework is an in-repo stand-in for Microsoft's Bot Framework
// OpenID metadata + JWKS endpoints. Nothing in this repo can hold a real
// Azure/Entra credential, so the full verification path (metadata fetch →
// JWKS → signature → claims) is exercised against this fake instead of a
// live Microsoft round-trip.
type fakeBotFramework struct {
	server  *httptest.Server
	privKey *rsa.PrivateKey
	// otherKey signs "wrong key" tokens; it is never published in the JWKS.
	otherKey *rsa.PrivateKey
}

func newFakeBotFramework(t *testing.T) *fakeBotFramework {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	fake := &fakeBotFramework{privKey: priv, otherKey: other}

	mux := http.NewServeMux()
	mux.HandleFunc("/metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                fake.issuer(),
			"jwks_uri":                              fake.server.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		set := josejwk.JSONWebKeySet{Keys: []josejwk.JSONWebKey{{
			Key:       &fake.privKey.PublicKey,
			KeyID:     testKeyID,
			Algorithm: "RS256",
			Use:       "sig",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

// issuer is the constant Bot Framework issuer value; the real one is
// https://api.botframework.com and is deliberately NOT the metadata URL's
// own host, which is exactly why this package fetches metadata by hand
// instead of using oidc.NewProvider.
func (f *fakeBotFramework) issuer() string { return "https://api.botframework.test" }

func (f *fakeBotFramework) metadataURL() string { return f.server.URL + "/metadata" }

func (f *fakeBotFramework) baseClaims() jwt.MapClaims {
	now := time.Now()

	return jwt.MapClaims{
		"iss":        f.issuer(),
		"aud":        testAppID,
		"tid":        testTenantID,
		"serviceurl": testServiceURL,
		"iat":        now.Unix(),
		"nbf":        now.Add(-time.Minute).Unix(),
		"exp":        now.Add(time.Hour).Unix(),
	}
}

// sign issues a token with the published key, applying an optional claim
// mutation first.
func (f *fakeBotFramework) sign(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()

	return f.signWith(t, f.privKey, mutate)
}

func (f *fakeBotFramework) signWith(t *testing.T, key *rsa.PrivateKey, mutate func(jwt.MapClaims)) string {
	t.Helper()

	claims := f.baseClaims()
	if mutate != nil {
		mutate(claims)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = testKeyID

	signed, err := token.SignedString(key)
	require.NoError(t, err)

	return signed
}

func newTestVerifier(fake *fakeBotFramework) *Verifier {
	v := NewVerifier(testAppID)
	v.MetadataURL = fake.metadataURL()

	return v
}

func TestVerifyToken_AcceptsWellFormedToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	claims, err := verifier.VerifyToken(t.Context(), fake.sign(t, nil), testServiceURL)
	r.NoError(err)
	r.Equal(testAppID, claims.Audience)
	r.Equal(fake.issuer(), claims.Issuer)
	r.Equal(testServiceURL, claims.ServiceURL)
}

// TestVerifyToken_AcceptsArrayAudience pins that the `aud` claim decodes from
// both the string and the array JSON shape — real Bot Framework tokens have
// been observed in both.
func TestVerifyToken_AcceptsArrayAudience(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	raw := fake.sign(t, func(c jwt.MapClaims) {
		c["aud"] = []string{"some-other-app", testAppID}
	})

	_, err := verifier.VerifyToken(t.Context(), raw, testServiceURL)
	r.NoError(err)
}

func TestVerifyToken_RejectsWrongAudience(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	raw := fake.sign(t, func(c jwt.MapClaims) { c["aud"] = "someone-elses-app-id" })

	_, err := verifier.VerifyToken(t.Context(), raw, testServiceURL)
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "audience")
}

func TestVerifyToken_RejectsWrongIssuer(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	raw := fake.sign(t, func(c jwt.MapClaims) { c["iss"] = "https://evil.example.com" })

	_, err := verifier.VerifyToken(t.Context(), raw, testServiceURL)
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "issuer")
}

func TestVerifyToken_RejectsExpiredToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	raw := fake.sign(t, func(c jwt.MapClaims) {
		c["exp"] = time.Now().Add(-2 * time.Hour).Unix()
	})

	_, err := verifier.VerifyToken(t.Context(), raw, testServiceURL)
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "expired")
}

// TestVerifyToken_RejectsNotYetValidToken guards the nbf branch: a token
// minted for the future must not be accepted early.
func TestVerifyToken_RejectsNotYetValidToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	raw := fake.sign(t, func(c jwt.MapClaims) {
		c["nbf"] = time.Now().Add(2 * time.Hour).Unix()
		c["exp"] = time.Now().Add(3 * time.Hour).Unix()
	})

	_, err := verifier.VerifyToken(t.Context(), raw, testServiceURL)
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "not yet valid")
}

// TestVerifyToken_RejectsUnknownSigningKey is the core negative control: a
// structurally perfect token signed by a key that is not in the published
// JWKS must be rejected. Without this, every other assertion here would pass
// against a verifier that never checked the signature at all.
func TestVerifyToken_RejectsUnknownSigningKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	raw := fake.signWith(t, fake.otherKey, nil)

	_, err := verifier.VerifyToken(t.Context(), raw, testServiceURL)
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "signature")
}

func TestVerifyToken_RejectsServiceURLMismatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	_, err := verifier.VerifyToken(t.Context(), fake.sign(t, nil), "https://attacker.example.com/")
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "serviceurl")
}

// TestVerifyToken_ServiceURLTrailingSlashIsIgnored pins the normalization:
// Microsoft is inconsistent about the trailing slash between the claim and
// the activity body, and that must not reject legitimate traffic.
func TestVerifyToken_ServiceURLTrailingSlashIsIgnored(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	_, err := verifier.VerifyToken(t.Context(), fake.sign(t, nil), "https://smba.trafficmanager.net/emea")
	r.NoError(err)
}

// TestVerifyToken_RejectsMissingServiceURLClaim and its sibling below pin
// Microsoft's verification requirement 7 as UNCONDITIONAL. Skipping the
// binding whenever either side is empty would be fail-open: the activity body
// is attacker-chosen, so replaying a captured token with no serviceUrl would
// bypass the check entirely and let the attacker steer our outbound calls.
func TestVerifyToken_RejectsMissingServiceURLClaim(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	raw := fake.sign(t, func(c jwt.MapClaims) { delete(c, "serviceurl") })

	_, err := verifier.VerifyToken(t.Context(), raw, testServiceURL)
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "serviceurl")
}

func TestVerifyToken_RejectsMissingActivityServiceURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	_, err := verifier.VerifyToken(t.Context(), fake.sign(t, nil), "")
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "serviceUrl")
}

// TestVerifyToken_RejectsUnadvertisedAlg pins Microsoft's verification
// requirement 6: the signature must be checked with an algorithm the metadata
// document advertises. go-oidc's VerifySignature documents that it does not
// check `alg` itself, so without our own pin the token header could name any
// algorithm the library happens to support.
func TestVerifyToken_RejectsUnadvertisedAlg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)

	verifier := newTestVerifier(fake)
	// Warm the cache, then narrow the advertised set so RS256 is no longer
	// acceptable — the same effect as a token naming an unadvertised alg.
	_, err := verifier.VerifyToken(t.Context(), fake.sign(t, nil), testServiceURL)
	r.NoError(err)

	verifier.mu.Lock()
	verifier.signingAlgs = []string{"PS512"}
	verifier.mu.Unlock()

	_, err = verifier.VerifyToken(t.Context(), fake.sign(t, nil), testServiceURL)
	r.ErrorIs(err, ErrInvalidToken)
	r.Contains(err.Error(), "signing algorithm")
}

// TestVerifyToken_TenantClaimIsNotRequired documents the finding that drove
// removing the JWT-level tenant check: Microsoft's Connector-to-Bot token
// carries no `tid`. A verifier that demanded one would reject every real
// activity, silently disabling the whole integration.
func TestVerifyToken_TenantClaimIsNotRequired(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	raw := fake.sign(t, func(c jwt.MapClaims) { delete(c, "tid") })

	claims, err := verifier.VerifyToken(t.Context(), raw, testServiceURL)
	r.NoError(err)
	r.Equal(testAppID, claims.Audience)
}

func TestVerifyToken_RejectsWhenNotConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)

	verifier := NewVerifier("")
	verifier.MetadataURL = fake.metadataURL()

	_, err := verifier.VerifyToken(t.Context(), fake.sign(t, nil), testServiceURL)
	r.ErrorIs(err, ErrNotConfigured)
}

func TestVerifyToken_RejectsEmptyToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newFakeBotFramework(t)
	verifier := newTestVerifier(fake)

	_, err := verifier.VerifyToken(t.Context(), "", testServiceURL)
	r.ErrorIs(err, ErrMissingAuthorization)
}

func TestVerifyToken_SurfacesMetadataFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	verifier := NewVerifier(testAppID)
	verifier.MetadataURL = server.URL

	_, err := verifier.VerifyToken(t.Context(), "not-even-a-jwt", testServiceURL)
	r.ErrorIs(err, ErrMetadataFetch)
}

func TestTokenFromHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{name: "valid", header: "Bearer abc.def.ghi", want: "abc.def.ghi"},
		{name: "case insensitive scheme", header: "bearer abc.def.ghi", want: "abc.def.ghi"},
		{name: "empty", header: "", wantErr: true},
		{name: "no scheme", header: "abc.def.ghi", wantErr: true},
		{name: "wrong scheme", header: "Basic abc.def.ghi", wantErr: true},
		{name: "scheme only", header: "Bearer    ", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, err := TokenFromHeader(tc.header)
			if tc.wantErr {
				r.ErrorIs(err, ErrMissingAuthorization)

				return
			}

			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}
