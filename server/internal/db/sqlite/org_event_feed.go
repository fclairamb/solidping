package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateOrgEventFeed inserts a new org event feed.
func (s *Service) CreateOrgEventFeed(ctx context.Context, feed *models.OrgEventFeed) error {
	if _, err := s.db.NewInsert().Model(feed).Exec(ctx); err != nil {
		return fmt.Errorf("create org event feed: %w", err)
	}

	return nil
}

// ListOrgEventFeeds returns the org's live (not revoked) feeds, oldest first.
func (s *Service) ListOrgEventFeeds(ctx context.Context, orgUID string) ([]*models.OrgEventFeed, error) {
	var feeds []*models.OrgEventFeed

	err := s.db.NewSelect().
		Model(&feeds).
		Where("organization_uid = ?", orgUID).
		OrderExpr("created_at ASC, uid ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list org event feeds: %w", err)
	}

	return feeds, nil
}

// GetOrgEventFeed returns one live feed of the org; sql.ErrNoRows otherwise.
func (s *Service) GetOrgEventFeed(ctx context.Context, orgUID, uid string) (*models.OrgEventFeed, error) {
	feed := new(models.OrgEventFeed)

	err := s.db.NewSelect().
		Model(feed).
		Where("organization_uid = ?", orgUID).
		Where("uid = ?", uid).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return feed, nil
}

// GetOrgEventFeedBySecretHash resolves the unauthenticated feed URL; a revoked
// feed or an unknown hash returns sql.ErrNoRows.
func (s *Service) GetOrgEventFeedBySecretHash(ctx context.Context, secretHash string) (*models.OrgEventFeed, error) {
	feed := new(models.OrgEventFeed)

	if err := s.db.NewSelect().Model(feed).Where("secret_hash = ?", secretHash).Scan(ctx); err != nil {
		return nil, err
	}

	return feed, nil
}

// RotateOrgEventFeedSecret replaces the secret hash of a live feed.
func (s *Service) RotateOrgEventFeedSecret(ctx context.Context, uid, secretHash string, at time.Time) error {
	_, err := s.db.NewUpdate().
		Model((*models.OrgEventFeed)(nil)).
		Set("secret_hash = ?", secretHash).
		Set("rotated_at = ?", at).
		Where("uid = ?", uid).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("rotate org event feed: %w", err)
	}

	return nil
}

// TouchOrgEventFeed records when the feed was last read.
func (s *Service) TouchOrgEventFeed(ctx context.Context, uid string, at time.Time) error {
	_, err := s.db.NewUpdate().
		Model((*models.OrgEventFeed)(nil)).
		Set("last_used_at = ?", at).
		Where("uid = ?", uid).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("touch org event feed: %w", err)
	}

	return nil
}

// RevokeOrgEventFeed soft-deletes a feed.
func (s *Service) RevokeOrgEventFeed(ctx context.Context, uid string) error {
	_, err := s.db.NewDelete().
		Model((*models.OrgEventFeed)(nil)).
		Where("uid = ?", uid).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke org event feed: %w", err)
	}

	return nil
}
