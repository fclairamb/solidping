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
	CreatedAt       time.Time    `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time    `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time   `bun:"deleted_at"`

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
	ProviderName *string
	Metadata     *JSONMap
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
	TOTPEnabled       bool       `bun:"totp_enabled,notnull,default:false"`
	TOTPRecoveryCodes []string   `bun:"totp_recovery_codes,type:jsonb"`
	LastActiveAt      *time.Time `bun:"last_active_at"`
	CreatedAt         time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt         time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt         *time.Time `bun:"deleted_at"`
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
	LastActiveAt      *time.Time
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

// Member roles.
const (
	MemberRoleAdmin  MemberRole = "admin"
	MemberRoleUser   MemberRole = "user"
	MemberRoleViewer MemberRole = "viewer"
)

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
)

// UserToken represents an authentication token (PAT or refresh token).
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
