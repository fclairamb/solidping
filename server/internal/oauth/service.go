package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// Token lifetimes and grant constants for the embedded authorization server.
const (
	// authCodeTTL is how long an issued authorization code stays redeemable.
	// Codes are single-use and very short-lived (OAuth 2.1 recommends ≤ 60s).
	authCodeTTL = 60 * time.Second
	// accessTokenTTL is the lifetime of an OAuth-issued MCP access token.
	// Short by design — clients refresh with the rotating refresh token.
	accessTokenTTL = 30 * time.Minute
	// refreshTokenTTL is the lifetime of a refresh grant.
	refreshTokenTTL = 30 * 24 * time.Hour

	// tokenEntropyBytes is the size of the random secret backing codes, refresh
	// tokens, and generated client IDs/secrets before base64url encoding.
	tokenEntropyBytes = 32

	// authCodeKeyPrefix namespaces authorization codes within the generic
	// state_entries store (shared with password resets, invites, OAuth sign-in
	// state, etc.) so their random suffixes never collide with another
	// consumer's keys.
	authCodeKeyPrefix = "oauth_auth_code:"

	// authCodeSep separates the embedded organization uid from the random
	// suffix in an issued code. Neither a UUID (hyphens only) nor a
	// base64url token (RFC 4648 §5 alphabet: A-Za-z0-9-_) ever contains a
	// dot, so splitting on the first one is unambiguous.
	authCodeSep = "."
)

// Sentinel errors returned by the service for the token/authorize flows. The
// handler maps these to the appropriate OAuth error response. They are
// package-internal — callers outside the package interact via the HTTP surface.
var (
	// errClientNotFound means no registered client matches the client_id.
	errClientNotFound = errors.New("oauth: client not found")
	// errInvalidGrant means the auth code or refresh token is missing, expired,
	// already used, revoked, or fails a binding check.
	errInvalidGrant = errors.New("oauth: invalid grant")
	// errPKCEFailed means the supplied code_verifier does not match the stored
	// code_challenge.
	errPKCEFailed = errors.New("oauth: pkce verification failed")
)

// Service holds the business logic for the embedded OAuth 2.1 authorization
// server: client validation, authorization-code issuance/redemption, and token
// minting (delegated to the auth service for JWT signing).
type Service struct {
	db      db.Service
	authSvc *auth.Service
	cfg     *config.Config
	clock   clock.Clock
}

// NewService builds the OAuth service. authSvc is reused for JWT signing and
// refresh-token semantics so OAuth-issued credentials are indistinguishable
// from session credentials downstream.
func NewService(dbService db.Service, authSvc *auth.Service, cfg *config.Config) *Service {
	return &Service{
		db:      dbService,
		authSvc: authSvc,
		cfg:     cfg,
		clock:   clock.Real{},
	}
}

// SetClock overrides the service clock. It exists so tests can drive
// authorization-code and refresh-token expiry deterministically.
func (s *Service) SetClock(c clock.Clock) {
	s.clock = c
}

// randomToken returns a base64url-encoded, unpadded random secret.
func randomToken() (string, error) {
	buf := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GetClient looks up a registered client by its client_id.
func (s *Service) GetClient(ctx context.Context, clientID string) (*models.OAuthClient, error) {
	client, err := s.db.GetOAuthClientByClientID(ctx, clientID)
	if err != nil {
		return nil, errClientNotFound
	}

	return client, nil
}

// AuthCodeGrant captures everything an authorization code binds together. The
// token endpoint replays these bindings (client, redirect, PKCE, resource) to
// stop a stolen code from being redeemed under different parameters.
type AuthCodeGrant struct {
	ClientID            string
	UserUID             string
	OrgUID              string
	OrgSlug             string
	RedirectURI         string
	Scope               string
	Resource            string
	CodeChallenge       string
	CodeChallengeMethod string
}

// IssueAuthCode mints a single-use, short-lived authorization code bound to the
// grant, persisted in the generic state_entries store rather than a dedicated
// table. The returned code is the opaque value handed back to the client via
// the redirect; it embeds the organization uid as a prefix (authCodeSep
// separated) because the token endpoint that redeems it receives no org
// context of its own — the prefix is how redemption resolves the org-scoped
// lookup. The rest of the grant (client, redirect, PKCE, scope, resource,
// user) travels in the entry's JSON value.
func (s *Service) IssueAuthCode(ctx context.Context, grant *AuthCodeGrant) (string, error) {
	random, err := randomToken()
	if err != nil {
		return "", err
	}

	code := grant.OrgUID + authCodeSep + random

	value := models.JSONMap{
		paramClientID:           grant.ClientID,
		"user_uid":              grant.UserUID,
		"redirect_uri":          grant.RedirectURI,
		paramScope:              grant.Scope,
		paramResource:           grant.Resource,
		"code_challenge":        grant.CodeChallenge,
		"code_challenge_method": grant.CodeChallengeMethod,
		"expires_at":            s.clock.Now().Add(authCodeTTL).Format(time.RFC3339Nano),
	}

	ttl := authCodeTTL
	orgUID := grant.OrgUID
	if err := s.db.SetStateEntry(ctx, &orgUID, authCodeKeyPrefix+random, &value, &ttl); err != nil {
		return "", fmt.Errorf("persist auth code: %w", err)
	}

	return code, nil
}

// splitAuthCode extracts the org uid and random suffix embedded in a code
// issued by IssueAuthCode. The final bool is false for anything that isn't in
// that shape (never a valid grant, so the caller rejects it the same as any
// other invalid code).
func splitAuthCode(code string) (string, string, bool) {
	orgUID, random, found := strings.Cut(code, authCodeSep)
	if !found || orgUID == "" || random == "" {
		return "", "", false
	}

	return orgUID, random, true
}

// stringFromValue reads a string field out of a state-entry JSON value,
// returning "" if the key is absent or not a string.
func stringFromValue(v models.JSONMap, key string) string {
	if s, ok := v[key].(string); ok {
		return s
	}

	return ""
}

// TokenResult is the successful output of a token-endpoint exchange.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	Scope        string
	ExpiresIn    int
}

// ExchangeAuthCode redeems an authorization code for an access + refresh token.
// It enforces, in order: the code exists, is unexpired, and is consumed exactly
// once (atomic compare-and-set against the underlying state_entries row); the
// redirect_uri and client_id match the binding; and the PKCE code_verifier
// matches the stored S256 challenge. Any failure returns errInvalidGrant /
// errPKCEFailed without minting a token.
func (s *Service) ExchangeAuthCode(
	ctx context.Context, code, clientID, redirectURI, codeVerifier string,
) (*TokenResult, error) {
	orgUID, random, ok := splitAuthCode(code)
	if !ok {
		return nil, errInvalidGrant
	}

	key := authCodeKeyPrefix + random

	entry, err := s.db.GetStateEntry(ctx, &orgUID, key)
	if err != nil {
		return nil, fmt.Errorf("get auth code: %w", err)
	}

	if entry == nil || entry.Value == nil {
		return nil, errInvalidGrant
	}

	value := *entry.Value

	// Expiry is re-checked here (rather than trusted to state_entries' own
	// expires_at filter, which stamps real wall-clock time on insert) against
	// the service's own clock, so tests can fake time the same way the
	// dedicated-table implementation did.
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, stringFromValue(value, "expires_at"))
	if parseErr != nil || s.clock.Now().After(expiresAt) {
		return nil, errInvalidGrant
	}

	if stringFromValue(value, paramClientID) != clientID || stringFromValue(value, "redirect_uri") != redirectURI {
		return nil, errInvalidGrant
	}

	if !VerifyPKCE(codeVerifier, stringFromValue(value, "code_challenge")) {
		return nil, errPKCEFailed
	}

	// Atomic single-use guard: a replay finds no live row to delete.
	consumed, err := s.db.DeleteStateEntry(ctx, &orgUID, key)
	if err != nil {
		return nil, fmt.Errorf("consume auth code: %w", err)
	}

	if !consumed {
		return nil, errInvalidGrant
	}

	orgSlug, err := s.orgSlug(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	return s.mintTokens(ctx, &mintInput{
		clientID: clientID,
		userUID:  stringFromValue(value, "user_uid"),
		orgUID:   orgUID,
		orgSlug:  orgSlug,
		scope:    stringFromValue(value, paramScope),
		resource: stringFromValue(value, paramResource),
		grant:    grantAuthorizationCode,
	})
}

// ExchangeRefreshToken rotates a refresh grant: it validates the presented token
// is active and unexpired, atomically revokes it, and issues a fresh access +
// refresh pair. Grants are user_tokens rows (type oauth_refresh, revocation =
// soft delete); because the revoking soft-delete is an atomic compare-and-set,
// a concurrent or replayed use of the same refresh token loses the race and is
// rejected — the rotation invalidates the prior token.
func (s *Service) ExchangeRefreshToken(
	ctx context.Context, refreshToken, clientID string,
) (*TokenResult, error) {
	// Revoked grants are soft-deleted, so a lookup miss covers "never existed"
	// and "already rotated/revoked" alike.
	row, err := s.db.GetUserTokenByToken(ctx, refreshToken)
	if err != nil {
		return nil, errInvalidGrant
	}

	// Only OAuth refresh grants are redeemable here — a PAT or session
	// refresh token presented at the OAuth token endpoint must never mint
	// OAuth credentials (each token type is bound to its own endpoint).
	if row.Type != models.TokenTypeOAuthRefresh {
		return nil, errInvalidGrant
	}

	if row.ExpiresAt == nil || s.clock.Now().After(*row.ExpiresAt) {
		return nil, errInvalidGrant
	}

	grantClientID := stringFromValue(row.Properties, paramClientID)
	if clientID != "" && grantClientID != clientID {
		return nil, errInvalidGrant
	}

	// Atomic rotation guard: a concurrent redemption finds no live row.
	won, err := s.db.DeleteUserToken(ctx, row.UID)
	if err != nil {
		return nil, fmt.Errorf("rotate refresh token: %w", err)
	}

	if !won {
		// Lost the race → already rotated/revoked → reject.
		return nil, errInvalidGrant
	}

	if row.OrganizationUID == nil {
		// OAuth grants are always org-scoped; a NULL org is not a valid grant.
		return nil, errInvalidGrant
	}

	orgSlug, err := s.orgSlug(ctx, *row.OrganizationUID)
	if err != nil {
		return nil, err
	}

	return s.mintTokens(ctx, &mintInput{
		clientID: grantClientID,
		userUID:  row.UserUID,
		orgUID:   *row.OrganizationUID,
		orgSlug:  orgSlug,
		scope:    stringFromValue(row.Properties, paramScope),
		resource: stringFromValue(row.Properties, paramResource),
		grant:    grantRefreshRotation,
	})
}

// RevokeGrant implements RFC 7009 token revocation for an OAuth refresh grant.
// It soft-deletes the oauth_refresh user_tokens row backing refreshToken, but
// ONLY when that row's client_id binding equals the presented (non-empty)
// clientID — a client may revoke only grants issued to itself.
//
// Every "nothing to do" outcome is a silent no-op by design, so the endpoint
// can never be used as an oracle to probe token validity (RFC 7009 mandates a
// 200 either way): an unknown or already-revoked token, an expired grant, a PAT
// or session refresh token (wrong type — each is torn down through its own
// surface), and a grant bound to a different client_id (hence a different
// client/user) all leave the store untouched.
//
// The returned bool reports whether a live row was actually deleted; it exists
// for tests and callers that want the fact — the HTTP handler ignores it and
// always answers 200. A non-nil error is a genuine backend failure, never a
// "token not found" signal.
func (s *Service) RevokeGrant(ctx context.Context, refreshToken, clientID string) (bool, error) {
	if refreshToken == "" || clientID == "" {
		return false, nil
	}

	// A soft-deleted grant is filtered out by GetUserTokenByToken, so a lookup
	// miss covers "never existed" and "already revoked" alike — both no-ops.
	row, err := s.db.GetUserTokenByToken(ctx, refreshToken)
	if err != nil {
		//nolint:nilerr // RFC 7009: an unknown/already-revoked token is a silent
		// no-op, never surfaced as an error (that would make an oracle).
		return false, nil
	}

	// Only OAuth refresh grants are revocable through this endpoint. A PAT or
	// session refresh token presented here is NOT deleted (RFC 7009 says never
	// signal what happened, so this is a silent no-op rather than an error).
	if row.Type != models.TokenTypeOAuthRefresh {
		return false, nil
	}

	// Client binding: revoke only when the grant was issued to the presenting
	// client. A mismatch (or an unbound grant) is a silent no-op to the CALLER
	// — the HTTP answer is an indistinguishable 200 — but it is NOT silent in
	// the audit trail: see recordGrantMisuse.
	grantClientID := stringFromValue(row.Properties, paramClientID)
	if grantClientID == "" || grantClientID != clientID {
		s.recordGrantMisuse(ctx, row, grantClientID, clientID)

		return false, nil
	}

	// Soft-delete is idempotent (compare-and-set on deleted_at): a concurrent
	// rotation/revoke may have won the race, reported as deleted=false — still
	// a 200 to the caller.
	deleted, err := s.db.DeleteUserToken(ctx, row.UID)
	if err != nil {
		return false, fmt.Errorf("revoke grant: %w", err)
	}

	// Only a grant that was actually live gets a revocation event. A lost race
	// (deleted=false) means someone else's revocation already recorded it — a
	// second row would claim a revocation that did not happen.
	if deleted && row.OrganizationUID != nil {
		audit.Record(
			audit.WithUser(ctx, row.UserUID, models.ActorTypeAPIToken),
			s.db, *row.OrganizationUID, models.EventTypeAuthTokenRevoked,
			audit.Target{Type: auditTargetOAuthGrant, UID: row.UID, Name: grantClientID},
			models.JSONMap{
				paramTokenKind: auditTokenKindOAuthGrant,
				paramClientID:  grantClientID,
				paramScope:     stringFromValue(row.Properties, paramScope),
			})
	}

	return deleted, nil
}

// mintInput carries the parameters needed to mint a token pair.
type mintInput struct {
	clientID string
	userUID  string
	orgUID   string
	orgSlug  string
	scope    string
	resource string
	// grant distinguishes the FIRST issuance of a grant from a rotation of one
	// that already exists. Only the first is an access-granting event worth
	// recording: rotation happens every access-token TTL for the life of the
	// grant, so auditing it would bury the trail under machine noise while
	// telling a reader nothing they did not already learn at grant time.
	grant string
}

// Grant types recorded on (or deliberately excluded from) the audit trail.
const (
	grantAuthorizationCode = "authorization_code"
	grantRefreshRotation   = "refresh_token"
)

// mintTokens issues an audience-bound JWT access token (via the auth service's
// signing key) and a persisted rotating refresh grant (a user_tokens row of
// type oauth_refresh; the client/scope/resource bindings ride in properties).
//
// The refresh grant is persisted FIRST so its UID can be embedded in the access
// token's Claims.RefreshUID: that back-link is what lets a client holding only
// the access token identify (isCurrent) and self-revoke the grant it rides on.
// Rotation re-links for free because ExchangeRefreshToken funnels through here.
func (s *Service) mintTokens(ctx context.Context, input *mintInput) (*TokenResult, error) {
	scopes := ParseScopes(input.scope)

	refreshValue, err := randomToken()
	if err != nil {
		return nil, err
	}

	refreshRow := models.NewUserToken(
		input.userUID, &input.orgUID, refreshValue, models.TokenTypeOAuthRefresh,
	)
	expiresAt := s.clock.Now().Add(refreshTokenTTL)
	refreshRow.ExpiresAt = &expiresAt
	refreshRow.Properties = models.JSONMap{
		paramClientID: input.clientID,
		paramScope:    input.scope,
		paramResource: input.resource,
	}

	if err = s.db.CreateUserToken(ctx, refreshRow); err != nil {
		return nil, fmt.Errorf("persist refresh token: %w", err)
	}

	s.recordGrantIssued(ctx, input, refreshRow.UID)

	accessToken, err := s.authSvc.GenerateMCPAccessToken(
		ctx, input.userUID, input.orgSlug, scopes, input.resource, accessTokenTTL, refreshRow.UID,
	)
	if err != nil {
		return nil, fmt.Errorf("mint access token: %w", err)
	}

	return &TokenResult{
		AccessToken:  accessToken,
		RefreshToken: refreshValue,
		Scope:        input.scope,
		ExpiresIn:    int(accessTokenTTL.Seconds()),
	}, nil
}

// recordGrantIssued writes auth.token_created for a NEWLY issued OAuth grant
// (spec 2026-08-21-09).
//
// An OAuth grant is an access grant: it hands an external client the right to
// act as this user, in this org, for the listed scopes, until it is revoked.
// That is a sibling of "a personal access token was minted", and the spec
// exists so an org can answer "who was granted access" — so it belongs in the
// same trail, under the same event type.
//
// ROTATIONS ARE DELIBERATELY SILENT. ExchangeRefreshToken re-mints a pair every
// access-token TTL for the entire life of the grant; recording each one would
// bury the grant that matters under thousands of rows saying nothing new. The
// grant and its revocation are the two facts a reader needs.
//
// The payload carries the client, scope and resource — never the token values,
// which are the credential itself.
func (s *Service) recordGrantIssued(ctx context.Context, input *mintInput, grantUID string) {
	if input.grant != grantAuthorizationCode {
		return
	}

	// The token endpoint carries no session, so the actor is named explicitly:
	// the user who consented at the authorize step. Request provenance (source
	// IP, user agent) is already on the context from the request-meta
	// middleware and is preserved by WithUser.
	actorCtx := audit.WithUser(ctx, input.userUID, models.ActorTypeUser)

	audit.Record(actorCtx, s.db, input.orgUID, models.EventTypeAuthTokenCreated,
		audit.Target{Type: auditTargetOAuthGrant, UID: grantUID, Name: input.clientID},
		models.JSONMap{
			paramTokenKind: auditTokenKindOAuthGrant,
			paramClientID:  input.clientID,
			paramScope:     input.scope,
			paramResource:  input.resource,
			"grant_type":   input.grant,
		})
}

// recordGrantMisuse records an OAuth client presenting a grant that belongs to
// somebody else (spec 2026-08-21-09).
//
// The two OTHER silent branches of RevokeGrant — an unknown token and an
// already-revoked one — genuinely cannot be recorded: there is no row, so
// there is no organization to scope an org-scoped event to, and inventing one
// would let an unauthenticated caller write into an org's trail by guessing.
// That is the whole of the argument for their silence.
//
// This branch is different on every count. The row exists, so the org is
// known. "Client A presented client B's grant" is a genuine security signal —
// a leaked token being tried, or a confused-deputy bug — and it is exactly
// what a compliance reader is looking for. And the oracle worry does not
// transfer: the SUCCESS path already writes an event, so anyone who can both
// call revoke and read this trail can already tell a live grant from a dead
// one. Staying quiet here would buy no secrecy and cost a real signal.
//
// The caller still gets the same indistinguishable 200 that RFC 7009 requires.
// The trail is an internal, admin-only surface; the HTTP response is not.
//
// The actor is deliberately SYSTEM, not the grant's owner: that user did not
// do this, and attributing it to them would be a false accusation in a record
// people are meant to act on. The presenting client is named in the payload,
// and the source IP rides in from the request-meta middleware.
func (s *Service) recordGrantMisuse(
	ctx context.Context, row *models.UserToken, grantClientID, presentedClientID string,
) {
	if row.OrganizationUID == nil {
		return
	}

	audit.Record(ctx, s.db, *row.OrganizationUID, models.EventTypeAuthTokenMisuse,
		audit.Target{Type: auditTargetOAuthGrant, UID: row.UID, Name: grantClientID},
		models.JSONMap{
			paramTokenKind:        auditTokenKindOAuthGrant,
			"reason":              misuseReasonWrongClient,
			paramClientID:         grantClientID,
			"presented_client_id": presentedClientID,
		})
}

// misuseReasonWrongClient is the machine reason on auth.token_misuse.
const misuseReasonWrongClient = "wrong_client"

// Audit labels owned by this package.
const (
	auditTargetOAuthGrant    = "oauth_grant"
	auditTokenKindOAuthGrant = "oauth_grant"

	// Grant property / audit payload keys, named because they appear on both
	// the persisted user_tokens row and the audit event.
	paramScope     = "scope"
	paramResource  = "resource"
	paramTokenKind = "token_kind"
)

// orgSlug resolves an organization UID to its slug for the access-token claims.
func (s *Service) orgSlug(ctx context.Context, orgUID string) (string, error) {
	org, err := s.db.GetOrganization(ctx, orgUID)
	if err != nil {
		return "", fmt.Errorf("resolve org slug: %w", err)
	}

	return org.Slug, nil
}

// RegisterClient creates a new OAuth client (RFC 7591). Public clients (native /
// loopback, PKCE) get no secret; confidential clients get a generated secret
// returned once in the registration response. The caller has already validated
// the redirect URIs.
func (s *Service) RegisterClient(
	ctx context.Context, name string, redirectURIs, grantTypes, scopes []string, isPublic bool,
) (*models.OAuthClient, string, error) {
	clientID, err := randomToken()
	if err != nil {
		return nil, "", err
	}

	now := s.clock.Now()
	client := &models.OAuthClient{
		UID:          uuid.New().String(),
		ClientID:     clientID,
		ClientName:   name,
		RedirectURIs: redirectURIs,
		GrantTypes:   grantTypes,
		Scopes:       scopes,
		IsPublic:     isPublic,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	var plainSecret string

	if !isPublic {
		plainSecret, err = randomToken()
		if err != nil {
			return nil, "", err
		}

		hash, hashErr := passwords.Hash(plainSecret)
		if hashErr != nil {
			return nil, "", fmt.Errorf("hash client secret: %w", hashErr)
		}

		client.SecretHash = &hash
	}

	if err := s.db.CreateOAuthClient(ctx, client); err != nil {
		return nil, "", fmt.Errorf("persist client: %w", err)
	}

	return client, plainSecret, nil
}
