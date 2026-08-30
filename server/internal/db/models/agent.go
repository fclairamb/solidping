package models

import (
	"time"

	"github.com/google/uuid"
)

// Agent status values.
const (
	// AgentStatusActive is an enrolled, usable agent.
	AgentStatusActive = "active"
	// AgentStatusRevoked is an agent an admin has revoked; it can no longer
	// authenticate and its X25519 key is excluded from future seals.
	AgentStatusRevoked = "revoked"
)

// Agent kinds (spec 2026-07-27-01).
const (
	// AgentKindOrg is a tenant-private deported agent: bound to exactly one
	// organization and one `@<org>/<region>` private region.
	AgentKindOrg = "org"
	// AgentKindSystem is a platform-operated agent (e.g. a fly.io machine)
	// serving a SHARED cloud region across every organization. It has no owning
	// org, and its claim scope is the region alone — the same scope the
	// in-cluster DirectBackend uses.
	AgentKindSystem = "system"
)

// Agent is a deported check agent that connects outbound-only over WebSocket,
// is hard-scoped to exactly one region, and can do nothing but
// claim/execute/submit checks. The DB never holds a usable agent credential —
// only the agent's public keys.
//
// Kind decides the scope: an `org` agent is bound to one organization and one
// private region; a `system` agent has no organization at all and serves one
// shared cloud region for every org. The DB enforces the pairing
// (kind = 'org' <=> organization_uid is not null).
type Agent struct {
	UID string `bun:"uid,pk,type:varchar(36)"`
	// OrganizationUID is nil exactly for system agents.
	OrganizationUID *string `bun:"organization_uid"`
	// Kind is AgentKindOrg or AgentKindSystem.
	Kind string `bun:"kind,notnull"`
	// Region is the region slug the agent is bound to; all its claims are
	// hard-scoped to it. Fully-qualified `@<org>/<region>` for an org agent, a
	// plain cloud region slug for a system agent.
	Region string `bun:"region,notnull"`
	Name   string `bun:"name,notnull"`
	// Ed25519PublicKey is the base64 identity public key used to verify reconnect
	// signatures.
	Ed25519PublicKey string `bun:"ed25519_public_key,notnull"`
	// X25519PublicKey is the age recipient string ("age1…") credentials are
	// sealed to.
	X25519PublicKey string `bun:"x25519_public_key,notnull"`
	// Fingerprint is a short hash of the Ed25519 key, shown in UI/logs.
	Fingerprint string     `bun:"fingerprint,notnull"`
	Status      string     `bun:"status,notnull"`
	LastSeenAt  *time.Time `bun:"last_seen_at"`
	EnrolledAt  time.Time  `bun:"enrolled_at,notnull,default:current_timestamp"`
	RevokedAt   *time.Time `bun:"revoked_at"`
	CreatedAt   time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt   time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt   *time.Time `bun:"deleted_at"`
}

// IsSystem reports whether this is a platform-operated agent (no owning org,
// shared cloud region).
func (a *Agent) IsSystem() bool {
	return a.Kind == AgentKindSystem
}

// OrgUID returns the owning organization UID, or "" for a system agent.
func (a *Agent) OrgUID() string {
	if a.OrganizationUID == nil {
		return ""
	}

	return *a.OrganizationUID
}

// NewAgent builds an active org (tenant-private) agent row from an enrollment.
func NewAgent(orgUID, region, name, ed25519Pub, x25519Pub, fingerprint string) *Agent {
	agent := newAgent(AgentKindOrg, region, name, ed25519Pub, x25519Pub, fingerprint)
	agent.OrganizationUID = &orgUID

	return agent
}

// NewSystemAgent builds an active platform-operated agent row. It carries no
// organization: its claims are scoped by the shared cloud region alone.
func NewSystemAgent(region, name, ed25519Pub, x25519Pub, fingerprint string) *Agent {
	return newAgent(AgentKindSystem, region, name, ed25519Pub, x25519Pub, fingerprint)
}

// newAgent is the shared constructor; callers set OrganizationUID per kind.
func newAgent(kind, region, name, ed25519Pub, x25519Pub, fingerprint string) *Agent {
	now := time.Now()

	return &Agent{
		UID:              uuid.New().String(),
		Kind:             kind,
		Region:           region,
		Name:             name,
		Ed25519PublicKey: ed25519Pub,
		X25519PublicKey:  x25519Pub,
		Fingerprint:      fingerprint,
		Status:           AgentStatusActive,
		EnrolledAt:       now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// AgentEnrollmentToken binds a future agent to a region. Only the SHA-256 hash
// of the token is stored; the token itself is readable exactly once.
//
// An `org` token is minted by an org admin, bound to (org, private region), and
// strictly ONE-SHOT: enrollment atomically marks it used while creating the
// agent row. A `system` token is platform-operator material seeded from
// SP_SYSTEM_AGENT_ENROLLMENT_TOKENS, bound to a cloud region slug and to no org,
// and MULTI-USE (bounded by MaxUses, nil = unlimited): every machine of a fly
// fleet generates its own keypair and enrolls on boot, so no private key is ever
// shared between machines.
type AgentEnrollmentToken struct {
	UID string `bun:"uid,pk,type:varchar(36)"`
	// OrganizationUID is nil exactly for system tokens.
	OrganizationUID *string `bun:"organization_uid"`
	// Kind is AgentKindOrg or AgentKindSystem.
	Kind string `bun:"kind,notnull"`
	// Region is the region the enrolled agent will be bound to:
	// `@<org>/<region>` for an org token, a cloud region slug for a system one.
	Region string `bun:"region,notnull"`
	// TokenHash is the SHA-256 hex of the spe_ token — never the token itself.
	TokenHash string    `bun:"token_hash,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
	// MaxUses bounds a system token's enrollments (nil = unlimited). Never set
	// on an org token, which is one-shot by construction.
	MaxUses *int `bun:"max_uses"`
	// UseCount is how many agents enrolled with this token (0 or 1 for org).
	UseCount int `bun:"use_count,notnull"`
	// UsedAt / UsedByAgentUID record the LAST enrollment. For an org token they
	// are also the single-use marker.
	UsedAt           *time.Time `bun:"used_at"`
	UsedByAgentUID   *string    `bun:"used_by_agent_uid"`
	CreatedByUserUID *string    `bun:"created_by_user_uid"`
	CreatedAt        time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	DeletedAt        *time.Time `bun:"deleted_at"`
}

// IsSystem reports whether this is a platform (multi-use) enrollment token.
func (t *AgentEnrollmentToken) IsSystem() bool {
	return t.Kind == AgentKindSystem
}

// OrgUID returns the owning organization UID, or "" for a system token.
func (t *AgentEnrollmentToken) OrgUID() string {
	if t.OrganizationUID == nil {
		return ""
	}

	return *t.OrganizationUID
}

// HasUsesLeft reports whether the token may still enroll an agent. Org tokens
// are one-shot (the atomic consume is the real guard); system tokens are
// unlimited unless MaxUses is set.
func (t *AgentEnrollmentToken) HasUsesLeft() bool {
	if !t.IsSystem() {
		return t.UsedAt == nil
	}

	return t.MaxUses == nil || t.UseCount < *t.MaxUses
}

// NewAgentEnrollmentToken builds a one-shot org enrollment token row bound to
// (org, private region) with the given hash and expiry.
func NewAgentEnrollmentToken(
	orgUID, region, tokenHash string, expiresAt time.Time, createdByUserUID *string,
) *AgentEnrollmentToken {
	return &AgentEnrollmentToken{
		UID:              uuid.New().String(),
		OrganizationUID:  &orgUID,
		Kind:             AgentKindOrg,
		Region:           region,
		TokenHash:        tokenHash,
		ExpiresAt:        expiresAt,
		CreatedByUserUID: createdByUserUID,
		CreatedAt:        time.Now(),
	}
}

// NewSystemAgentEnrollmentToken builds a multi-use platform enrollment token
// bound to a cloud region slug (no owning org). maxUses nil = unlimited.
func NewSystemAgentEnrollmentToken(
	region, tokenHash string, expiresAt time.Time, maxUses *int,
) *AgentEnrollmentToken {
	return &AgentEnrollmentToken{
		UID:       uuid.New().String(),
		Kind:      AgentKindSystem,
		Region:    region,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
		MaxUses:   maxUses,
		CreatedAt: time.Now(),
	}
}

// AgentNonce is one consumed reconnect nonce. The table is the cluster-wide
// replay guard: the per-process cache it replaces was only sound with a single
// API replica, which a multi-machine fly fleet reconnecting through a load
// balancer is not.
type AgentNonce struct {
	AgentUID string    `bun:"agent_uid,pk"`
	Nonce    string    `bun:"nonce,pk"`
	SeenAt   time.Time `bun:"seen_at,notnull"`
}
