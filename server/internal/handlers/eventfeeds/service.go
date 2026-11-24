// Package eventfeeds serves the private Atom feeds of an organization's
// incidents and events (spec 2026-11-24-12, #1014), and their management API.
//
// The feed URL carries a secret and no session: feed readers, NOC walls and
// automation cannot send a bearer token. That makes the secret the whole
// authorization, so:
//
//   - it carries 256 random bits, base64url, prefixed with SecretPrefix so
//     secret scanners can spot a leaked URL;
//   - only its SHA-256 is stored, looked up through a unique index: the secret
//     itself is never compared, and a database read never yields a working URL;
//   - it is shown once, at creation and at rotation;
//   - only an org admin manages feeds (route middleware), and every action is
//     written to the audit trail;
//   - the feed only offers the incident, check, status update and maintenance
//     families. Membership, auth and integration events are closer to the
//     audit log and never reach a URL.
package eventfeeds

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
)

const (
	// SecretPrefix starts every feed secret, so secret scanners can spot one.
	SecretPrefix = "spf_"
	// secretBytes is the entropy of a feed secret.
	secretBytes = 32
	// MaxFeedsPerOrg caps the live feeds of an organization.
	MaxFeedsPerOrg = 10
	// MaxNameLength bounds a feed name, in characters.
	MaxNameLength = 100
	// touchInterval spaces the last_used_at writes of a polled feed.
	touchInterval = time.Minute
	// auditTargetType names a feed in the audit trail.
	auditTargetType = "event_feed"
	// auditKeyAction names the action in the audit payload.
	auditKeyAction = "action"
)

// ScopeChoices lists every scope item a feed may carry: exact event types and
// "family.*" items. Anything else is refused.
//
//nolint:gochecknoglobals // static policy list, treated as a constant.
var ScopeChoices = []string{
	"incident.*",
	"check.created",
	"check.updated",
	"check.deleted",
	"status_update.*",
	"maintenance_window.*",
}

// DefaultScope is the scope of a feed created without one.
//
//nolint:gochecknoglobals // static policy list, treated as a constant.
var DefaultScope = []string{"incident.*", "check.created", "check.updated", "check.deleted"}

// Service errors.
var (
	ErrOrgNotFound   = errors.New("organization not found")
	ErrNotFound      = errors.New("event feed not found")
	ErrInvalidName   = errors.New("invalid feed name")
	ErrInvalidScope  = errors.New("invalid feed scope")
	ErrLimitReached  = errors.New("event feed limit reached")
	ErrUnknownSecret = errors.New("unknown or revoked feed secret")
)

// Service implements the event feed rules over the database.
type Service struct {
	db     db.Service
	events *events.Service
	now    func() time.Time
}

// NewService constructs a Service. eventsSvc runs the feed query, the one the
// events page uses.
func NewService(database db.Service, eventsSvc *events.Service) *Service {
	return &Service{db: database, events: eventsSvc, now: time.Now}
}

// Feed is the API projection of a feed. The secret is never part of it.
type Feed struct {
	UID              string     `json:"uid"`
	Name             string     `json:"name"`
	EventTypes       []string   `json:"eventTypes"`
	CreatedByUserUID *string    `json:"createdByUserUid,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	RotatedAt        *time.Time `json:"rotatedAt,omitempty"`
	LastUsedAt       *time.Time `json:"lastUsedAt,omitempty"`
}

// ListResponse is the GET response: the feeds plus the choices the dashboard
// offers.
type ListResponse struct {
	Data         []*Feed  `json:"data"`
	MaxFeeds     int      `json:"maxFeeds"`
	ScopeChoices []string `json:"scopeChoices"`
	DefaultScope []string `json:"defaultScope"`
}

// SecretResponse is returned by create and rotate, the only two times the
// secret is shown.
type SecretResponse struct {
	*Feed

	Secret string `json:"secret"`
	// Path is the feed path under the server; the handler adds URL.
	Path string `json:"path"`
	URL  string `json:"url"`
}

// CreateRequest is the POST body.
type CreateRequest struct {
	Name       string   `json:"name"`
	EventTypes []string `json:"eventTypes,omitempty"`
}

// FeedPath is the path of a feed URL for a secret.
func FeedPath(secret string) string {
	return "/api/v1/org-feeds/" + secret + "/feed.xml"
}

// List returns the org's live feeds.
func (s *Service) List(ctx context.Context, orgSlug string) (*ListResponse, error) {
	org, err := s.org(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListOrgEventFeeds(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("list event feeds: %w", err)
	}

	out := make([]*Feed, 0, len(rows))
	for _, row := range rows {
		out = append(out, project(row))
	}

	return &ListResponse{
		Data: out, MaxFeeds: MaxFeedsPerOrg,
		ScopeChoices: slices.Clone(ScopeChoices), DefaultScope: slices.Clone(DefaultScope),
	}, nil
}

// Create mints a feed and returns its secret, once.
func (s *Service) Create(
	ctx context.Context, orgSlug, userUID string, req *CreateRequest,
) (*SecretResponse, error) {
	org, err := s.org(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > MaxNameLength {
		return nil, ErrInvalidName
	}

	scope, err := normalizeScope(req.EventTypes)
	if err != nil {
		return nil, err
	}

	existing, err := s.db.ListOrgEventFeeds(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("count event feeds: %w", err)
	}

	if len(existing) >= MaxFeedsPerOrg {
		return nil, ErrLimitReached
	}

	secret, err := newSecret()
	if err != nil {
		return nil, err
	}

	row := models.NewOrgEventFeed(org.UID, name, models.HashOrgEventFeedSecret(secret), scope)
	row.CreatedAt = s.now()

	if userUID != "" {
		row.CreatedByUserUID = &userUID
	}

	if err := s.db.CreateOrgEventFeed(ctx, row); err != nil {
		return nil, fmt.Errorf("create event feed: %w", err)
	}

	s.record(ctx, org.UID, row, "event_feed_created")

	return &SecretResponse{Feed: project(row), Secret: secret, Path: FeedPath(secret)}, nil
}

// Rotate replaces a feed's secret: the old URL answers 410 from now on.
func (s *Service) Rotate(ctx context.Context, orgSlug, uid string) (*SecretResponse, error) {
	org, row, err := s.feed(ctx, orgSlug, uid)
	if err != nil {
		return nil, err
	}

	secret, err := newSecret()
	if err != nil {
		return nil, err
	}

	now := s.now()
	if err := s.db.RotateOrgEventFeedSecret(ctx, row.UID, models.HashOrgEventFeedSecret(secret), now); err != nil {
		return nil, err
	}

	row.RotatedAt = &now

	s.record(ctx, org.UID, row, "event_feed_rotated")

	return &SecretResponse{Feed: project(row), Secret: secret, Path: FeedPath(secret)}, nil
}

// Revoke deletes a feed: its URL answers 410 from now on.
func (s *Service) Revoke(ctx context.Context, orgSlug, uid string) error {
	org, row, err := s.feed(ctx, orgSlug, uid)
	if err != nil {
		return err
	}

	if err := s.db.RevokeOrgEventFeed(ctx, row.UID); err != nil {
		return err
	}

	s.record(ctx, org.UID, row, "event_feed_revoked")

	return nil
}

// Resolve maps a feed secret to its live feed, and stamps last_used_at at
// most once per touchInterval. Every failure is ErrUnknownSecret, so an
// unknown, malformed and revoked secret cannot be told apart.
func (s *Service) Resolve(ctx context.Context, secret string) (*models.OrgEventFeed, error) {
	if !strings.HasPrefix(secret, SecretPrefix) || len(secret) > 128 {
		return nil, ErrUnknownSecret
	}

	row, err := s.db.GetOrgEventFeedBySecretHash(ctx, models.HashOrgEventFeedSecret(secret))
	if err != nil {
		return nil, ErrUnknownSecret
	}

	now := s.now()
	if row.LastUsedAt == nil || now.Sub(*row.LastUsedAt) >= touchInterval {
		// Best effort: a failed stamp must not fail the read.
		if err := s.db.TouchOrgEventFeed(ctx, row.UID, now); err == nil {
			row.LastUsedAt = &now
		}
	}

	return row, nil
}

func (s *Service) org(ctx context.Context, orgSlug string) (*models.Organization, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return nil, ErrOrgNotFound
	}

	return org, nil
}

func (s *Service) feed(ctx context.Context, orgSlug, uid string) (*models.Organization, *models.OrgEventFeed, error) {
	org, err := s.org(ctx, orgSlug)
	if err != nil {
		return nil, nil, err
	}

	row, err := s.db.GetOrgEventFeed(ctx, org.UID, uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}

	if err != nil {
		return nil, nil, fmt.Errorf("get event feed: %w", err)
	}

	return org, row, nil
}

func (s *Service) record(ctx context.Context, orgUID string, row *models.OrgEventFeed, action string) {
	audit.Record(ctx, s.db, orgUID, models.EventTypeOrgSettingsUpdated,
		audit.Target{Type: auditTargetType, UID: row.UID, Name: row.Name},
		models.JSONMap{auditKeyAction: action, "event_types": row.EventTypes})
}

// normalizeScope validates a requested scope, drops duplicates and keeps the
// ScopeChoices order. An empty request gets DefaultScope.
func normalizeScope(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return slices.Clone(DefaultScope), nil
	}

	for _, item := range requested {
		if !slices.Contains(ScopeChoices, item) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidScope, item)
		}
	}

	out := make([]string, 0, len(requested))

	for _, choice := range ScopeChoices {
		if slices.Contains(requested, choice) {
			out = append(out, choice)
		}
	}

	return out, nil
}

func newSecret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate feed secret: %w", err)
	}

	return SecretPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func project(row *models.OrgEventFeed) *Feed {
	eventTypes := row.EventTypes
	if eventTypes == nil {
		eventTypes = []string{}
	}

	return &Feed{
		UID: row.UID, Name: row.Name, EventTypes: eventTypes, CreatedByUserUID: row.CreatedByUserUID,
		CreatedAt: row.CreatedAt, RotatedAt: row.RotatedAt, LastUsedAt: row.LastUsedAt,
	}
}
