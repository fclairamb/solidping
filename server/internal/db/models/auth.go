package models

import (
	"time"

	"github.com/google/uuid"
)

// OrganizationProvider links an organization to an external provider identity.
// This is the single source of truth for org↔provider mapping (e.g., Slack team, Google Workspace).
type OrganizationProvider struct {
	UID             string       `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string       `bun:"organization_uid,notnull"`
	ProviderType    ProviderType `bun:"provider_type,notnull"`
	ProviderID      string       `bun:"provider_id,notnull"` // e.g., Slack Team ID T0123456789
	ProviderName    string       `bun:"provider_name"`       // e.g., "Acme Corp Slack Workspace"
	Metadata        JSONMap      `bun:"metadata,type:jsonb,nullzero"`
	// MetadataPrivate / MetadataPrivateKeys mirror the credential-encryption
	// shape used on Check.Config — OAuth client secrets and similar live
	// here as an AES-GCM envelope at rest.
	MetadataPrivate     *string    `bun:"metadata_private,type:text,nullzero"`
	MetadataPrivateKeys *string    `bun:"metadata_private_keys,type:text,nullzero"`
	CreatedAt           time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt           time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt           *time.Time `bun:"deleted_at"`

	// Relations (for eager loading)
	Organization *Organization `bun:"rel:belongs-to,join:organization_uid=uid"`
}

// NewOrganizationProvider creates a new organization provider with generated UID.
func NewOrganizationProvider(orgUID string, providerType ProviderType, providerID string) *OrganizationProvider {
	now := time.Now()

	return &OrganizationProvider{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		ProviderType:    providerType,
		ProviderID:      providerID,
		Metadata:        make(JSONMap),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// OrganizationProviderUpdate represents fields that can be updated.
type OrganizationProviderUpdate struct {
	ProviderName         *string
	Metadata             *JSONMap
	MetadataPrivate      *string
	MetadataPrivateKeys  *string
	ClearMetadataPrivate bool
}

// User represents a global user account.
type User struct {
	UID               string     `bun:"uid,pk,type:varchar(36)"`
	Email             string     `bun:"email,notnull"`
	Name              string     `bun:"name"`
	AvatarURL         string     `bun:"avatar_url"`
	PasswordHash      *string    `bun:"password_hash"`
	EmailVerifiedAt   *time.Time `bun:"email_verified_at"`
	SuperAdmin        bool       `bun:"super_admin"`
	TOTPSecret        *string    `bun:"totp_secret"`
	TOTPEnabled       bool       `bun:"totp_enabled,notnull"`
	TOTPRecoveryCodes []string   `bun:"totp_recovery_codes,type:jsonb"`
	// MustChangePassword forces a password rotation before the account can do
	// anything else. It is a GENERAL user-level capability, not a property of
	// the seeded bootstrap admin: an operator-initiated reset, an invited user
	// or a compromised-credential response all set the same flag, and every
	// consumer reads this field rather than keying on who the user is.
	//
	// While it is true, a session authenticated as this user reaches only the
	// rotation endpoint, /auth/me and /auth/logout — enforced centrally in the
	// auth layer (see internal/handlers/auth/password_rotation.go), so the API,
	// the dashboard, the CLI, PAT creation and the realtime socket are all
	// covered by one rule.
	//
	// Defaults to false, which is what keeps OAuth/SSO/LDAP users — who may
	// carry a nil PasswordHash and could not satisfy a rotation — unaffected.
	MustChangePassword bool       `bun:"must_change_password,notnull"`
	LastActiveAt       *time.Time `bun:"last_active_at"`
	CreatedAt          time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt          time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt          *time.Time `bun:"deleted_at"`
}

// NewUser creates a new user with generated UID.
func NewUser(email string) *User {
	now := time.Now()

	return &User{
		UID:       uuid.New().String(),
		Email:     email,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// UserUpdate represents fields that can be updated.
type UserUpdate struct {
	Email             *string
	Name              *string
	AvatarURL         *string
	PasswordHash      *string
	EmailVerifiedAt   *time.Time
	SuperAdmin        *bool
	TOTPSecret        *string
	TOTPEnabled       *bool
	TOTPRecoveryCodes *[]string
	// MustChangePassword sets or clears the forced-rotation flag. Nil leaves it
	// alone — so an unrelated profile update can never silently un-force a
	// pending rotation.
	MustChangePassword *bool
	LastActiveAt       *time.Time
}

// ProviderType represents an external auth provider type.
type ProviderType string

// Provider types.
const (
	ProviderTypeGoogle    ProviderType = "google"
	ProviderTypeGitHub    ProviderType = "github"
	ProviderTypeGitLab    ProviderType = "gitlab"
	ProviderTypeMicrosoft ProviderType = "microsoft"
	ProviderTypeTwitter   ProviderType = "twitter"
	ProviderTypeSlack     ProviderType = "slack"
	ProviderTypeDiscord   ProviderType = "discord"
	ProviderTypeSAML      ProviderType = "saml"
	ProviderTypeOIDC      ProviderType = "oidc"
	// ProviderTypeLDAP identifies a user auto-provisioned or linked via an
	// LDAP/Active Directory bind (spec 2026-07-08-08, part 3). Users linked
	// this way always have a nil User.PasswordHash — see
	// Service.findOrCreateLDAPUser in internal/handlers/auth/ldap_service.go.
	ProviderTypeLDAP ProviderType = "ldap"
)

// UserProvider links a user to an external auth provider.
type UserProvider struct {
	UID          string       `bun:"uid,pk,type:varchar(36)"`
	UserUID      string       `bun:"user_uid,notnull"`
	ProviderType ProviderType `bun:"provider_type,notnull"`
	ProviderID   string       `bun:"provider_id,notnull"`
	Metadata     JSONMap      `bun:"metadata,type:jsonb,nullzero"`
	CreatedAt    time.Time    `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt    time.Time    `bun:"updated_at,notnull,default:current_timestamp"`

	// Relations (for eager loading)
	User *User `bun:"rel:belongs-to,join:user_uid=uid"`
}

// NewUserProvider creates a new user provider with generated UID.
func NewUserProvider(userUID string, providerType ProviderType, providerID string) *UserProvider {
	now := time.Now()

	return &UserProvider{
		UID:          uuid.New().String(),
		UserUID:      userUID,
		ProviderType: providerType,
		ProviderID:   providerID,
		Metadata:     make(JSONMap),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// MemberRole represents a user's role in an organization.
type MemberRole string

// Member roles, from most to least privileged. The ordering is meaningful:
// owner > admin > user > viewer. Use MemberRole.AtLeast rather than equality
// whenever a call site gates on "at least this much privilege" — an owner must
// pass every admin gate.
const (
	MemberRoleOwner  MemberRole = "owner"
	MemberRoleAdmin  MemberRole = "admin"
	MemberRoleUser   MemberRole = "user"
	MemberRoleViewer MemberRole = "viewer"
)

// Privilege ranks for the member roles. Higher is more privileged; an unknown
// role ranks 0, below every real role, so a garbage value can never satisfy a
// gate.
const (
	memberRoleRankUnknown = 0
	memberRoleRankViewer  = 1
	memberRoleRankUser    = 2
	memberRoleRankAdmin   = 3
	memberRoleRankOwner   = 4
)

// Rank returns the role's privilege rank. Unknown roles rank below every valid
// role.
func (r MemberRole) Rank() int {
	switch r {
	case MemberRoleOwner:
		return memberRoleRankOwner
	case MemberRoleAdmin:
		return memberRoleRankAdmin
	case MemberRoleUser:
		return memberRoleRankUser
	case MemberRoleViewer:
		return memberRoleRankViewer
	default:
		return memberRoleRankUnknown
	}
}

// AtLeast reports whether the role carries at least the privilege of min. An
// unknown role never satisfies any gate; an unknown min is never satisfied
// either (it ranks 0 but a role must still be valid to pass).
func (r MemberRole) AtLeast(minRole MemberRole) bool {
	if !r.IsValid() || !minRole.IsValid() {
		return false
	}

	return r.Rank() >= minRole.Rank()
}

// IsValid reports whether the role is one of the known member roles.
func (r MemberRole) IsValid() bool {
	return r.Rank() != memberRoleRankUnknown
}

// OrganizationMember links a user to an organization with a role.
type OrganizationMember struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	UserUID         string     `bun:"user_uid,notnull"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	Role            MemberRole `bun:"role,notnull"`
	InvitedByUID    *string    `bun:"invited_by_uid"`
	InvitedAt       *time.Time `bun:"invited_at"`
	JoinedAt        *time.Time `bun:"joined_at"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`

	// Relations (for eager loading)
	User         *User         `bun:"rel:belongs-to,join:user_uid=uid"`
	Organization *Organization `bun:"rel:belongs-to,join:organization_uid=uid"`
	InvitedBy    *User         `bun:"rel:belongs-to,join:invited_by_uid=uid"`
}

// NewOrganizationMember creates a new membership with generated UID.
func NewOrganizationMember(orgUID, userUID string, role MemberRole) *OrganizationMember {
	now := time.Now()

	return &OrganizationMember{
		UID:             uuid.New().String(),
		UserUID:         userUID,
		OrganizationUID: orgUID,
		Role:            role,
		JoinedAt:        &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// OrganizationMemberUpdate represents fields that can be updated.
type OrganizationMemberUpdate struct {
	Role     *MemberRole
	JoinedAt *time.Time
}

// TokenType represents the type of user token.
type TokenType string

const (
	// TokenTypePAT represents a Personal Access Token.
	TokenTypePAT TokenType = "pat"
	// TokenTypeRefresh represents a refresh token for session management.
	TokenTypeRefresh TokenType = "refresh"
	// TokenTypeOAuthRefresh represents a rotating OAuth 2.1 refresh grant for
	// the MCP resource (spec 2026-06-20-03). The grant's client_id, scope, and
	// resource bindings ride in Properties; revocation is the row's soft
	// delete. Each redemption endpoint validates its own type, so the three
	// types can never be exchanged for one another.
	TokenTypeOAuthRefresh TokenType = "oauth_refresh"
)

// UserToken represents an authentication token (PAT, session refresh token,
// or OAuth refresh grant).
type UserToken struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	UserUID         string     `bun:"user_uid,notnull"`
	OrganizationUID *string    `bun:"organization_uid"`
	Token           string     `bun:"token,notnull"`
	Type            TokenType  `bun:"type,notnull"`
	Properties      JSONMap    `bun:"properties,type:jsonb,nullzero"`
	ExpiresAt       *time.Time `bun:"expires_at"`
	LastActiveAt    *time.Time `bun:"last_active_at"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`

	// Relations (for eager loading)
	User         *User         `bun:"rel:belongs-to,join:user_uid=uid"`
	Organization *Organization `bun:"rel:belongs-to,join:organization_uid=uid"`
}

// NewUserToken creates a new user token with generated UID.
// orgUID can be nil for global refresh tokens.
func NewUserToken(userUID string, orgUID *string, token string, tokenType TokenType) *UserToken {
	now := time.Now()

	return &UserToken{
		UID:             uuid.New().String(),
		UserUID:         userUID,
		OrganizationUID: orgUID,
		Token:           token,
		Type:            tokenType,
		Properties:      make(JSONMap),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// UserTokenUpdate represents fields that can be updated.
type UserTokenUpdate struct {
	Properties   *JSONMap
	ExpiresAt    *time.Time
	LastActiveAt *time.Time
}

// UserPasskey is a registered WebAuthn credential. The public key is not
// a secret, so no encryption-at-rest envelope is needed. SignCount is a
// monotonically-increasing replay guard reported by the authenticator;
// regressions indicate a cloned credential and should reject the assertion.
type UserPasskey struct {
	UID               string     `bun:"uid,pk,type:varchar(36)"`
	UserUID           string     `bun:"user_uid,notnull"`
	Name              string     `bun:"name,notnull"`
	CredentialID      []byte     `bun:"credential_id,notnull"`
	PublicKey         []byte     `bun:"public_key,notnull"`
	AAGUID            *string    `bun:"aaguid"`
	SignCount         uint32     `bun:"sign_count,notnull"`
	Transports        []string   `bun:"transports,type:jsonb,nullzero"`
	BackupEligible    bool       `bun:"backup_eligible,notnull"`
	BackupState       bool       `bun:"backup_state,notnull"`
	UserVerified      bool       `bun:"user_verified,notnull"`
	AttestationFormat *string    `bun:"attestation_format"`
	LastUsedAt        *time.Time `bun:"last_used_at"`
	CreatedAt         time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt         time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt         *time.Time `bun:"deleted_at"`

	User *User `bun:"rel:belongs-to,join:user_uid=uid"`
}

// NewUserPasskey builds a new passkey row with a generated UID.
func NewUserPasskey(userUID, name string, credentialID, publicKey []byte) *UserPasskey {
	now := time.Now()

	return &UserPasskey{
		UID:          uuid.New().String(),
		UserUID:      userUID,
		Name:         name,
		CredentialID: credentialID,
		PublicKey:    publicKey,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// UserPasskeyUpdate carries the mutable subset of UserPasskey. SignCount
// and LastUsedAt update on every successful assertion; Name updates via
// the rename endpoint.
type UserPasskeyUpdate struct {
	Name        *string
	SignCount   *uint32
	LastUsedAt  *time.Time
	BackupState *bool
}
