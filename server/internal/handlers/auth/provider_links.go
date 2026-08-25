package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Provider links (`organization_providers` / `user_providers`) can outlive the
// row they point at: both organizations and users are SOFT-deleted, and the
// `on delete cascade` on the link tables only fires for hard deletes. A link
// left pointing at a soft-deleted organization used to brick every subsequent
// SSO login for that workspace/guild permanently — the link wins the partial
// unique lookup (`… where deleted_at is null`) on every retry, and the bare
// `GetOrganization` behind it answers `sql.ErrNoRows` forever.
//
// The two helpers below are the single place that decides what a link resolving
// to nothing means: the link is stale, it gets cleared, and the caller falls
// through to its normal create path. Deliberately NOT a resurrection of the
// soft-deleted org/user — the deletion was an explicit act; the workspace gets a
// fresh org and the person a fresh account. Every heal logs a WARN naming the
// provider identity and the dangling UID so an operator can see it happened.
//
// `DeleteOrg` also releases an org's provider links now (see
// Service.releaseOrgProviderLinks), which prevents the class going forward;
// these helpers are what repairs rows that already went stale in the field.

// resolveLinkedOrganization returns the organization behind a live
// organization_providers row.
//
// It returns (org, nil) when the link resolves, (nil, nil) when the link was
// stale and has been cleared — the caller must then fall through to creating a
// fresh organization — and (nil, err) for anything else.
func resolveLinkedOrganization(
	ctx context.Context,
	dbSvc db.Service,
	providerType models.ProviderType,
	providerID string,
	link *models.OrganizationProvider,
) (*models.Organization, error) {
	org, err := dbSvc.GetOrganization(ctx, link.OrganizationUID)
	if err == nil {
		return org, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get organization behind provider link: %w", err)
	}

	slog.WarnContext(ctx, "clearing stale organization provider link: linked organization no longer resolves",
		"providerType", providerType,
		"providerId", providerID,
		"organizationUid", link.OrganizationUID,
		"organizationProviderUid", link.UID,
	)

	// Soft delete: the partial unique index `idx_org_providers_type_id … where
	// deleted_at is null` then frees the (provider_type, provider_id) pair so
	// the caller's create path can re-link it.
	if delErr := dbSvc.DeleteOrganizationProvider(ctx, link.UID); delErr != nil {
		// Falling through with the row still live would trip the unique index
		// inside the create path and report a constraint violation instead of
		// the real cause.
		return nil, fmt.Errorf("failed to clear stale organization provider link: %w", delErr)
	}

	return nil, nil //nolint:nilnil // (nil, nil) means "link cleared, fall through to create"
}

// resolveLinkedUser returns the user behind a live user_providers row.
//
// It returns (user, nil) when the link resolves, (nil, nil) when the link was
// stale and has been cleared — the caller must then fall through to its email
// lookup / create path — and (nil, err) for anything else.
func resolveLinkedUser(
	ctx context.Context,
	dbSvc db.Service,
	providerType models.ProviderType,
	providerID string,
	link *models.UserProvider,
) (*models.User, error) {
	user, err := dbSvc.GetUser(ctx, link.UserUID)
	if err == nil {
		return user, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user behind provider link: %w", err)
	}

	slog.WarnContext(ctx, "clearing stale user provider link: linked user no longer resolves",
		"providerType", providerType,
		"providerId", providerID,
		"userUid", link.UserUID,
		"userProviderUid", link.UID,
	)

	// Hard delete: `user_providers` has no deleted_at column, and its unique
	// index `user_providers_provider_idx` is NOT partial — the row has to go
	// for the caller to be able to re-link the same provider identity.
	if delErr := dbSvc.DeleteUserProvider(ctx, link.UID); delErr != nil {
		return nil, fmt.Errorf("failed to clear stale user provider link: %w", delErr)
	}

	return nil, nil //nolint:nilnil // (nil, nil) means "link cleared, fall through to create"
}
