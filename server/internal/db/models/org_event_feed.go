package models

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// OrgEventFeed is one private Atom feed of an organization's events (spec
// 2026-11-24-12). The URL embeds a secret; only its SHA-256 is stored
// (SecretHash), so a database read never yields a working URL. A revoked feed
// keeps its row with DeletedAt set and stops resolving.
type OrgEventFeed struct {
	bun.BaseModel `bun:"table:org_event_feeds,alias:oef"`

	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	Name            string `bun:"name,notnull"`
	SecretHash      string `bun:"secret_hash,notnull"`
	// EventTypes is the feed's scope: exact event types ("check.created") and
	// families written "incident.*".
	EventTypes       []string   `bun:"event_types,type:jsonb"`
	CreatedByUserUID *string    `bun:"created_by_user_uid"`
	CreatedAt        time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	RotatedAt        *time.Time `bun:"rotated_at"`
	LastUsedAt       *time.Time `bun:"last_used_at"`
	DeletedAt        *time.Time `bun:"deleted_at,soft_delete"`
}

// NewOrgEventFeed builds a feed row with a generated UID. secretHash is
// HashOrgEventFeedSecret of the secret handed to the caller.
func NewOrgEventFeed(orgUID, name, secretHash string, eventTypes []string) *OrgEventFeed {
	return &OrgEventFeed{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Name:            name,
		SecretHash:      secretHash,
		EventTypes:      eventTypes,
		CreatedAt:       time.Now(),
	}
}

// HashOrgEventFeedSecret is the stored form of a feed secret: hex SHA-256. The
// secret carries 256 random bits, so an unsalted fast hash is enough, and an
// indexed lookup on the hash never compares the secret itself.
func HashOrgEventFeedSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))

	return hex.EncodeToString(sum[:])
}
