// Package identitylink resolves the provider-side account a member has
// declared for THEMSELVES, as opposed to the one an admin mapped for them.
//
// Two write paths already collect exactly the handle the on-call mention
// resolver needs, and neither used to be consulted: a `slack_user` notification
// contact (Account → Notifications) and a Slack sign-in (`user_providers`).
// This package is the one place that reads them, so the sender and the admin
// "Member mapping" table can never disagree about who a member is.
//
// Every function here is best-effort and returns nil on any uncertainty — a
// mention is a nicety and must never break a send.
package identitylink

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SlackIdentity is one resolved Slack account for a member.
type SlackIdentity struct {
	// ExternalID is the Slack user id (`U…`), the thing `<@…>` needs.
	ExternalID string
	// DisplayName is the provider-side label when the source knows one. Empty
	// for sources that only carry an id — the caller then keeps the SolidPing
	// display name, which is what it would have rendered anyway.
	DisplayName string
	// Source names where the id came from, for logs and tests:
	// SourceContact or SourceAuthProvider.
	Source string
}

// Declared-identity sources, in the order they are consulted.
const (
	// SourceContact is a `slack_user` notification contact the member added.
	SourceContact = "contact"
	// SourceAuthProvider is a Slack sign-in on the member's account.
	SourceAuthProvider = "authProvider"
)

// DeclaredSlackIdentity returns the Slack user id this member declared for
// themselves, valid for the workspace `integration` posts into, or nil.
//
// Resolution order, all of it workspace-scoped:
//
//  1. a `slack_user` contact in this org whose `team_id` IS the integration's
//     team — proof, not inference;
//  2. a `slack_user` contact with a NULL `team_id` (created before the column
//     existed, so its workspace is unknown) — usable ONLY when the org has
//     exactly one live Slack integration, because then there is no other
//     workspace it could have belonged to;
//  3. the member's Slack sign-in, but only when the org's Slack
//     `organization_providers` row names the SAME team id.
//
// It NEVER crosses workspaces: the same Slack user id in another workspace
// addresses a different human, and pinging them is worse than pinging nobody.
// An integration whose settings carry no team id can therefore only ever match
// through rule 2.
//
// Deliberately does NOT consider `user_integration_identities` — that row is
// the admin's answer and always outranks this one, so layering the two is the
// caller's job (and lets SyncIdentities use this as a pure fallback).
func DeclaredSlackIdentity(
	ctx context.Context, dbSvc db.Service, integration *models.Integration, userUID string,
) *SlackIdentity {
	if dbSvc == nil || integration == nil || userUID == "" {
		return nil
	}

	if integration.Type != models.ConnectionTypeSlack {
		return nil
	}

	settings, err := models.SlackSettingsFromJSONMap(integration.Settings)
	if err != nil {
		return nil
	}

	teamID := settings.TeamID
	orgUID := integration.OrganizationUID

	if id := contactIdentity(ctx, dbSvc, orgUID, userUID, teamID); id != nil {
		return id
	}

	return authProviderIdentity(ctx, dbSvc, orgUID, userUID, teamID)
}

// contactIdentity implements rules 1 and 2.
func contactIdentity(
	ctx context.Context, dbSvc db.Service, orgUID, userUID, teamID string,
) *SlackIdentity {
	routes, err := dbSvc.ListUserContactsWithRoutes(ctx, userUID, orgUID)
	if err != nil {
		return nil
	}

	var unknownWorkspace string

	for _, route := range routes {
		contact := route.Contact
		if contact == nil || contact.Type != models.UserContactTypeSlackUser || contact.Value == "" {
			continue
		}

		if contact.TeamID != nil && *contact.TeamID != "" {
			// An exact workspace match is the only evidence that needs nothing
			// else, so it wins immediately.
			if teamID != "" && *contact.TeamID == teamID {
				return &SlackIdentity{ExternalID: contact.Value, Source: SourceContact}
			}

			continue
		}

		if unknownWorkspace == "" {
			unknownWorkspace = contact.Value
		}
	}

	if unknownWorkspace == "" || !orgHasSingleSlackIntegration(ctx, dbSvc, orgUID) {
		return nil
	}

	return &SlackIdentity{ExternalID: unknownWorkspace, Source: SourceContact}
}

// authProviderIdentity implements rule 3.
func authProviderIdentity(
	ctx context.Context, dbSvc db.Service, orgUID, userUID, teamID string,
) *SlackIdentity {
	if teamID == "" || !orgSignsInWithTeam(ctx, dbSvc, orgUID, teamID) {
		return nil
	}

	providers, err := dbSvc.ListUserProvidersByUser(ctx, userUID)
	if err != nil {
		return nil
	}

	for _, provider := range providers {
		if provider.ProviderType == models.ProviderTypeSlack && provider.ProviderID != "" {
			return &SlackIdentity{ExternalID: provider.ProviderID, Source: SourceAuthProvider}
		}
	}

	return nil
}

// orgSignsInWithTeam reports whether the org's Slack sign-in provider is bound
// to this very workspace. A Slack sign-in in workspace B says nothing about who
// a member is in workspace A.
func orgSignsInWithTeam(ctx context.Context, dbSvc db.Service, orgUID, teamID string) bool {
	providers, err := dbSvc.ListOrganizationProviders(ctx, orgUID)
	if err != nil {
		return false
	}

	for _, provider := range providers {
		if provider.ProviderType != models.ProviderTypeSlack || provider.DeletedAt != nil {
			continue
		}

		if provider.ProviderID == teamID {
			return true
		}
	}

	return false
}

// orgHasSingleSlackIntegration reports whether the org has exactly one live
// Slack integration, which is what makes a workspace-less contact unambiguous.
func orgHasSingleSlackIntegration(ctx context.Context, dbSvc db.Service, orgUID string) bool {
	slackType := models.ConnectionTypeSlack

	conns, err := dbSvc.ListChannels(ctx, &models.ListIntegrationsFilter{
		OrganizationUID: orgUID,
		Type:            &slackType,
	})
	if err != nil {
		return false
	}

	live := 0

	for _, conn := range conns {
		if conn != nil && conn.DeletedAt == nil {
			live++
		}
	}

	return live == 1
}
