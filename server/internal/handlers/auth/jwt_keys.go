package auth

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// jwtPurposeKeyLen is the length of every key derived by purposeKey.
const jwtPurposeKeyLen = 32

// jwtPurposeKeyInfoPrefix namespaces the HKDF info string, so a purpose key
// can never collide with a key another package derives from the same secret.
const jwtPurposeKeyInfoPrefix = "solidping-jwt:"

// errEmptyJWTPurpose guards purposeKey against being asked for the bearer key.
var errEmptyJWTPurpose = errors.New("jwt purpose key requires a non-empty purpose")

// bearerKey is the raw auth.jwt_secret. Only bearer tokens (session access,
// impersonation, MCP access) are signed with it, and ValidateToken is the only
// verifier that uses it. It is read live from fullCfg because the system
// config overlay and ensureJWTSecret mutate it after NewService.
func (s *Service) bearerKey() []byte {
	return []byte(s.fullCfg.Auth.JWTSecret)
}

// purposeKey derives the signing key of a non-bearer JWT (2FA temp token,
// passkey ceremony sessions, ...) from auth.jwt_secret. Each purpose gets its
// own key, so ValidateToken (which verifies with the raw secret) cannot even
// check the signature of such a token, and one purpose's token cannot be
// replayed as another's.
func (s *Service) purposeKey(purpose string) ([]byte, error) {
	if purpose == "" {
		return nil, errEmptyJWTPurpose
	}

	key, err := hkdf.Key(sha256.New, s.bearerKey(), nil, jwtPurposeKeyInfoPrefix+purpose, jwtPurposeKeyLen)
	if err != nil {
		return nil, fmt.Errorf("derive jwt purpose key: %w", err)
	}

	return key, nil
}

// jwtKey returns the HS256 key for a purpose: the raw secret for a bearer
// token (purpose == ""), a derived key for anything else.
func (s *Service) jwtKey(purpose string) ([]byte, error) {
	if purpose == "" {
		return s.bearerKey(), nil
	}

	return s.purposeKey(purpose)
}

// signJWT is the one place this package signs a JWT. purpose == "" means a
// bearer token; any other value signs with that purpose's derived key, so a
// new token type cannot land on the bearer key by accident.
func (s *Service) signJWT(purpose string, claims jwt.Claims) (string, error) {
	key, err := s.jwtKey(purpose)
	if err != nil {
		return "", err
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}

	return signed, nil
}

// parsePurposeJWT parses a non-bearer JWT signed by signJWT(purpose, ...).
// It accepts HS256 only.
func (s *Service) parsePurposeJWT(tokenString, purpose string, claims jwt.Claims) (*jwt.Token, error) {
	key, err := s.purposeKey(purpose)
	if err != nil {
		return nil, err
	}

	return jwt.ParseWithClaims(tokenString, claims, func(_ *jwt.Token) (any, error) {
		return key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
}
