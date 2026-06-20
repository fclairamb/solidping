package oauth

// JWKS publication.
//
// SECURITY NOTE — v1 reuses the existing HS256 (symmetric) JWT signing key.
// For a symmetric key, the verification key *is* the signing secret, so it must
// NEVER be published: anyone holding it could forge tokens. RFC 8414 still
// requires a jwks_uri, so we advertise the endpoint and serve a well-formed but
// empty key set. MCP clients do not need to verify tokens locally — the MCP
// resource server (SolidPing itself) validates every access token against the
// secret it holds. Migrating to an asymmetric keyset (RS256/ES256) and
// publishing the public half here is the documented follow-on hardening step
// (spec "Remaining open questions" #4).

// JWK is a single JSON Web Key (RFC 7517). Only the public-safe fields are
// modeled; we never emit symmetric key material.
type JWK struct {
	KeyType   string `json:"kty"`
	Use       string `json:"use,omitempty"`
	Algorithm string `json:"alg,omitempty"`
	KeyID     string `json:"kid,omitempty"`
}

// JWKS is a JSON Web Key Set (RFC 7517).
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// BuildJWKS returns the published key set. With the symmetric v1 key this is an
// empty set by design (see the security note above) — never the HMAC secret.
func BuildJWKS() JWKS {
	return JWKS{Keys: []JWK{}}
}
