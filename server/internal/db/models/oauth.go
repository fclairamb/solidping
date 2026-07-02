package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// OAuthClient is a registered OAuth 2.1 client for the MCP authorization
// server. Native MCP clients (Claude Desktop, mcp-remote) register dynamically
// (RFC 7591) as public clients (no secret) using PKCE + loopback redirects;
// confidential clients store a hashed secret. Redirect URIs are stored as a
// JSON array and validated on every /authorize.
type OAuthClient struct {
	bun.BaseModel `bun:"table:oauth_clients,alias:oauth_client"`

	UID          string    `bun:"uid,pk,type:varchar(36)"`
	ClientID     string    `bun:"client_id,notnull"`
	SecretHash   *string   `bun:"secret_hash"`
	ClientName   string    `bun:"client_name"`
	RedirectURIs []string  `bun:"redirect_uris,type:jsonb,nullzero"`
	GrantTypes   []string  `bun:"grant_types,type:jsonb,nullzero"`
	Scopes       []string  `bun:"scopes,type:jsonb,nullzero"`
	IsPublic     bool      `bun:"is_public,notnull"`
	CreatedAt    time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt    time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewOAuthClient builds a new client row with a generated UID.
func NewOAuthClient(clientID string) *OAuthClient {
	now := time.Now()

	return &OAuthClient{
		UID:       uuid.New().String(),
		ClientID:  clientID,
		IsPublic:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// OAuthRefreshToken is a rotating refresh grant issued by the token endpoint.
// On each refresh the prior row is marked RevokedAt and a fresh one is issued,
// so a stolen-and-reused refresh token is detectable. Tokens are also revoked
// on logout / PAT-revoke.
type OAuthRefreshToken struct {
	bun.BaseModel `bun:"table:oauth_refresh_tokens,alias:oauth_refresh_token"`

	UID             string     `bun:"uid,pk,type:varchar(36)"`
	Token           string     `bun:"token,notnull"`
	ClientID        string     `bun:"client_id,notnull"`
	UserUID         string     `bun:"user_uid,notnull"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	Scope           string     `bun:"scope,notnull"`
	Resource        string     `bun:"resource,notnull"`
	ExpiresAt       time.Time  `bun:"expires_at,notnull"`
	RevokedAt       *time.Time `bun:"revoked_at"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
}

// NewOAuthRefreshToken builds a new refresh-token row with a generated UID.
func NewOAuthRefreshToken(token, clientID, userUID, orgUID string) *OAuthRefreshToken {
	return &OAuthRefreshToken{
		UID:             uuid.New().String(),
		Token:           token,
		ClientID:        clientID,
		UserUID:         userUID,
		OrganizationUID: orgUID,
		CreatedAt:       time.Now(),
	}
}
