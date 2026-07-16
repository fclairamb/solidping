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

// Agent is a deported (org-scoped) check agent that connects outbound-only over
// WebSocket, is hard-scoped to exactly one private region, and can do nothing but
// claim/execute/submit checks. The DB never holds a usable agent credential —
// only the agent's public keys.
type Agent struct {
	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	// Region is the fully-qualified private-region slug (`@<org>/<region>`) the
	// agent is bound to; all its claims are hard-scoped to it.
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
	Status      string     `bun:"status,notnull,default:'active'"`
	LastSeenAt  *time.Time `bun:"last_seen_at"`
	EnrolledAt  time.Time  `bun:"enrolled_at,notnull,default:current_timestamp"`
	RevokedAt   *time.Time `bun:"revoked_at"`
	CreatedAt   time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt   time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt   *time.Time `bun:"deleted_at"`
}

// NewAgent builds an active agent row from an enrollment.
func NewAgent(orgUID, region, name, ed25519Pub, x25519Pub, fingerprint string) *Agent {
	now := time.Now()

	return &Agent{
		UID:              uuid.New().String(),
		OrganizationUID:  orgUID,
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

// AgentEnrollmentToken is a one-shot credential minted by an admin that binds a
// future agent to (org, region). Only the SHA-256 hash of the token is stored;
// the token itself is displayed exactly once at creation. Single-use: enrollment
// atomically marks it used while creating the agent row.
type AgentEnrollmentToken struct {
	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	// Region is the fully-qualified private-region slug the enrolled agent will
	// be bound to.
	Region string `bun:"region,notnull"`
	// TokenHash is the SHA-256 hex of the spe_ token — never the token itself.
	TokenHash        string     `bun:"token_hash,notnull"`
	ExpiresAt        time.Time  `bun:"expires_at,notnull"`
	UsedAt           *time.Time `bun:"used_at"`
	UsedByAgentUID   *string    `bun:"used_by_agent_uid"`
	CreatedByUserUID *string    `bun:"created_by_user_uid"`
	CreatedAt        time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	DeletedAt        *time.Time `bun:"deleted_at"`
}

// NewAgentEnrollmentToken builds an enrollment token row bound to (org, region)
// with the given hash and expiry.
func NewAgentEnrollmentToken(
	orgUID, region, tokenHash string, expiresAt time.Time, createdByUserUID *string,
) *AgentEnrollmentToken {
	return &AgentEnrollmentToken{
		UID:              uuid.New().String(),
		OrganizationUID:  orgUID,
		Region:           region,
		TokenHash:        tokenHash,
		ExpiresAt:        expiresAt,
		CreatedByUserUID: createdByUserUID,
		CreatedAt:        time.Now(),
	}
}
