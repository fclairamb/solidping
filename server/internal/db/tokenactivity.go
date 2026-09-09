package db

import (
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// FoldTokenActivity folds raw user_tokens rows (as read by
// TokenActivityByUsers — every row for the given users across the refresh,
// pat and oauth_refresh types, soft-deleted included) into per-user
// SessionAt/TokenAt activity.
//
// This lives in the shared db package, not in postgres/ or sqlite/, so both
// backends run the IDENTICAL Go-side MAX logic: SQLite stores timestamps as
// text, so letting either engine do the aggregation in SQL risks a
// lexical-vs-temporal mismatch, and duplicating this fold per-backend would
// risk the two silently drifting apart.
//
// Rules (spec 2026-09-09-05):
//   - type=refresh: SessionAt is the max last_active_at.
//   - type=pat: TokenAt candidate is the max last_active_at; a row with
//     last_active_at = nil (minted, never used) contributes nothing.
//   - type=oauth_refresh: TokenAt candidate is the max created_at
//     (last_active_at is never set for this type and is not consulted).
//   - A user with no rows at all is simply absent from the returned map.
func FoldTokenActivity(tokens []*models.UserToken) map[string]UserTokenActivity {
	activity := make(map[string]UserTokenActivity, len(tokens))

	for _, token := range tokens {
		userActivity := activity[token.UserUID]

		switch token.Type {
		case models.TokenTypeRefresh:
			if token.LastActiveAt != nil &&
				(userActivity.SessionAt == nil || token.LastActiveAt.After(*userActivity.SessionAt)) {
				userActivity.SessionAt = token.LastActiveAt
			}
		case models.TokenTypePAT:
			if token.LastActiveAt != nil &&
				(userActivity.TokenAt == nil || token.LastActiveAt.After(*userActivity.TokenAt)) {
				userActivity.TokenAt = token.LastActiveAt
			}
		case models.TokenTypeOAuthRefresh:
			createdAt := token.CreatedAt
			if userActivity.TokenAt == nil || createdAt.After(*userActivity.TokenAt) {
				userActivity.TokenAt = &createdAt
			}
		}

		activity[token.UserUID] = userActivity
	}

	return activity
}
