// Package uistate stores small, per-user, per-organization UI preferences
// server-side, so a choice a user makes in the dashboard (today: dismissing
// the getting-started checklist) follows them to their other devices instead
// of living in one browser's localStorage.
//
// The store is deliberately NOT a junk drawer. Two constraints keep it that
// way, and both are enforced here rather than in the handler so they hold for
// every caller:
//
//   - the key must match a short allowlist (v1: `onboarding.<org>` only), and
//   - the stored value is capped at a few kilobytes.
//
// Rows live in the existing `state_entries` table, user-scoped
// (`user_uid` set, `organization_uid` NULL) via the db service's
// Get/Set/DeleteUserStateEntry — which filter on user_uid, so one user can
// never read another's entry.
package uistate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// MaxValueBytes caps the encoded JSON body a single entry may carry. This is
// a preferences store, not a document store: a few KB is generous for the
// flags a dashboard needs and small enough that no client can turn a user row
// into a blob dump.
const MaxValueBytes = 4096

// KeyPrefixOnboarding is the only key namespace v1 accepts. The suffix names
// the organization the preference belongs to.
const KeyPrefixOnboarding = "onboarding."

// maxOrgRefLength bounds the organization reference in a key. An org slug is
// at most 20 characters and a UID is 36; 64 leaves room without letting an
// arbitrary string through.
const maxOrgRefLength = 64

var (
	// ErrInvalidKey is returned for a key outside the v1 allowlist.
	ErrInvalidKey = errors.New("unsupported ui-state key")
	// ErrValueTooLarge is returned when the value exceeds MaxValueBytes.
	ErrValueTooLarge = errors.New("ui-state value too large")
	// ErrOrgNotFound is returned when the organization named by the key does
	// not resolve.
	ErrOrgNotFound = errors.New("organization not found")
	// ErrNotFound is returned when the user has no entry under the key.
	ErrNotFound = errors.New("ui-state entry not found")
)

// Service resolves and stores per-user UI state.
type Service struct {
	db db.Service
}

// NewService constructs a Service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// isOrgRefShape reports whether s could name an organization: a slug
// (alphanumeric plus hyphen) or a UID. Anything else — a slash, a dot, a
// space, an empty string — is rejected before it reaches the database.
func isOrgRefShape(s string) bool {
	if s == "" || len(s) > maxOrgRefLength {
		return false
	}

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-':
		default:
			return false
		}
	}

	return true
}

// ResolveKey validates a client-supplied key and returns the storage key.
//
// v1 accepts `onboarding.<org>` only, where `<org>` is an organization slug
// or UID. The organization is resolved here and the stored key always names
// its UID, so a later org rename does not orphan the preference while the
// dashboard keeps passing the slug it already has in the URL.
//
// There is deliberately no membership gate: the row is the caller's own
// private UI state, and requiring membership would 403 a super admin browsing
// an org they do not belong to. Resolving a caller-supplied slug does reveal
// whether an organization exists (missing resolves to ORGANIZATION_NOT_FOUND),
// but that is the same distinction RequireOrgAccess already draws platform-wide
// (404 for missing vs 403 for existing-but-not-a-member), so this adds no new
// information. A key naming a foreign org only writes the caller's own
// user-scoped row, which is capped and one-per-org.
func (s *Service) ResolveKey(ctx context.Context, key string) (string, error) {
	orgRef, found := strings.CutPrefix(key, KeyPrefixOnboarding)
	if !found || !isOrgRefShape(orgRef) {
		return "", ErrInvalidKey
	}

	org, err := s.resolveOrg(ctx, orgRef)
	if err != nil {
		return "", err
	}

	return KeyPrefixOnboarding + org.UID, nil
}

// resolveOrg looks an organization up by UID, then by slug, then by a
// previous slug (a renamed org keeps answering on its old name everywhere
// else, so it does here too).
func (s *Service) resolveOrg(ctx context.Context, ref string) (*models.Organization, error) {
	if _, parseErr := uuid.Parse(ref); parseErr == nil {
		org, err := s.db.GetOrganization(ctx, ref)
		if err == nil && org != nil {
			return org, nil
		}
	}

	org, err := s.db.GetOrganizationBySlug(ctx, ref)
	if err == nil && org != nil {
		return org, nil
	}

	org, err = s.db.GetOrganizationByPreviousSlug(ctx, ref)
	if err == nil && org != nil {
		return org, nil
	}

	return nil, ErrOrgNotFound
}

// Get returns the value stored by userUID under key, or ErrNotFound.
func (s *Service) Get(ctx context.Context, userUID, key string) (models.JSONMap, error) {
	storageKey, err := s.ResolveKey(ctx, key)
	if err != nil {
		return nil, err
	}

	entry, err := s.db.GetUserStateEntry(ctx, userUID, storageKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read ui-state: %w", err)
	}

	if entry == nil || entry.Value == nil {
		return nil, ErrNotFound
	}

	return *entry.Value, nil
}

// Set stores value for userUID under key. The value never expires — a
// dismissal is a decision, not a cooldown.
func (s *Service) Set(ctx context.Context, userUID, key string, value models.JSONMap) error {
	storageKey, err := s.ResolveKey(ctx, key)
	if err != nil {
		return err
	}

	stored := value
	if err := s.db.SetUserStateEntry(ctx, userUID, storageKey, &stored, nil); err != nil {
		return fmt.Errorf("failed to write ui-state: %w", err)
	}

	return nil
}

// Delete removes the entry. Deleting an entry that was never written is not
// an error: the caller asked for "no value stored", and that is the result.
func (s *Service) Delete(ctx context.Context, userUID, key string) error {
	storageKey, err := s.ResolveKey(ctx, key)
	if err != nil {
		return err
	}

	if _, err := s.db.DeleteUserStateEntry(ctx, userUID, storageKey); err != nil {
		return fmt.Errorf("failed to delete ui-state: %w", err)
	}

	return nil
}
