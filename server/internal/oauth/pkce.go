package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// VerifyPKCE checks an RFC 7636 code_verifier against a stored S256
// code_challenge. Only the S256 method is supported (OAuth 2.1 forbids the
// `plain` method); callers must reject any other method before reaching here.
//
// The verifier is hashed with SHA-256 and base64url-encoded (no padding); the
// result is compared to the stored challenge in constant time so a timing
// side-channel cannot be used to forge a matching verifier.
func VerifyPKCE(codeVerifier, codeChallenge string) bool {
	if codeVerifier == "" || codeChallenge == "" {
		return false
	}

	sum := sha256.Sum256([]byte(codeVerifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])

	return subtle.ConstantTimeCompare([]byte(computed), []byte(codeChallenge)) == 1
}

// PKCE verifier length bounds from RFC 7636 §4.1: 43–128 characters.
const (
	pkceVerifierMinLen = 43
	pkceVerifierMaxLen = 128
)

// validCodeChallenge reports whether a code_challenge is plausibly a base64url
// S256 challenge (length within the RFC bounds for the encoded 32-byte hash).
func validCodeChallenge(challenge string) bool {
	// A SHA-256 digest base64url-encoded without padding is 43 chars; we allow
	// the full verifier range as a permissive upper bound rather than rejecting
	// a well-formed challenge on a strict equality check.
	return len(challenge) >= pkceVerifierMinLen && len(challenge) <= pkceVerifierMaxLen
}
