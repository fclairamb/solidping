// Package recipient picks the language and timezone one email is written in
// (spec 2026-10-21-01, decision 6).
//
// The chain, for the language and the timezone independently:
//  1. the recipient's own account (matched by address, case-insensitive);
//  2. a hint from the call site, for what the account does not set (or a
//     recipient with no account): the inviter's preferences, the status
//     page's language, the signup form's;
//  3. only for organization mail to an address with NO account (email
//     integration lists, uptime report lists), the organization owner's
//     preferences. A member without preferences never reads the owner's;
//  4. English, UTC.
package recipient

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// Lookup is the part of db.Service the resolver reads. db.Service satisfies it.
type Lookup interface {
	// GetUserByEmail finds an account by address, case-insensitively.
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	// ListMembersByOrg lists an organization's members, User loaded.
	ListMembersByOrg(ctx context.Context, orgUID string) ([]*models.OrganizationMember, error)
}

// Hint is what a call site knows about a recipient who may have no account.
// Both fields are optional and validated like a stored preference.
type Hint struct {
	Locale   string
	Timezone string
}

// HintFromUser is the hint a user gives about someone else (an inviter about
// the invitee).
func HintFromUser(user *models.User) Hint {
	if user == nil || user.Preferences == nil {
		return Hint{}
	}

	return Hint{Locale: user.Preferences.Locale, Timezone: user.Preferences.Timezone}
}

// ForUser is the locale of a recipient whose account is at hand.
func ForUser(user *models.User) i18n.Locale {
	var chain chooser

	chain.consider(prefsOf(user))

	return chain.locale()
}

// Resolver resolves addresses, caching every lookup. Make one per job run (or
// request) and drop it after: a cached preference is not refreshed. Not safe
// for concurrent use.
type Resolver struct {
	lookup Lookup
	// users caches the account of an address (lowercased); nil = none.
	users map[string]*models.User
	// owners caches the owner of an organization; nil = none.
	owners map[string]*models.User
}

// NewResolver returns a resolver reading from lookup. A nil lookup resolves
// from the hint alone.
func NewResolver(lookup Lookup) *Resolver {
	return &Resolver{
		lookup: lookup,
		users:  map[string]*models.User{},
		owners: map[string]*models.User{},
	}
}

// ForAddress resolves the locale of one recipient address. orgUID is the
// organization the mail is about, "" for mail that is not (registration,
// password reset). The org owner is consulted only for an address with no
// account: a member who has not set a preference reads English/UTC (as
// ForUser gives them), never the owner's. Lookup failures are logged and
// skipped, never returned: a lookup must not cost a mail.
func (r *Resolver) ForAddress(ctx context.Context, orgUID, address string, hint Hint) i18n.Locale {
	var chain chooser

	account := r.user(ctx, address)

	if chain.consider(prefsOf(account)) {
		return chain.locale()
	}

	if chain.consider(models.UserPreferences{Locale: hint.Locale, Timezone: hint.Timezone}) {
		return chain.locale()
	}

	if orgUID != "" && account == nil {
		chain.consider(prefsOf(r.owner(ctx, orgUID)))
	}

	return chain.locale()
}

func (r *Resolver) user(ctx context.Context, address string) *models.User {
	key := strings.ToLower(strings.TrimSpace(address))
	if key == "" || r.lookup == nil {
		return nil
	}

	if user, cached := r.users[key]; cached {
		return user
	}

	user, err := r.lookup.GetUserByEmail(ctx, key)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.DebugContext(ctx, "recipient locale: user lookup failed", "error", err)
		}

		user = nil
	}

	r.users[key] = user

	return user
}

// owner is the organization's longest-standing owner.
func (r *Resolver) owner(ctx context.Context, orgUID string) *models.User {
	if r.lookup == nil {
		return nil
	}

	if user, cached := r.owners[orgUID]; cached {
		return user
	}

	members, err := r.lookup.ListMembersByOrg(ctx, orgUID)
	if err != nil {
		slog.DebugContext(ctx, "recipient locale: member lookup failed", "error", err)
	}

	var owner *models.OrganizationMember

	for _, member := range members {
		if member.Role != models.MemberRoleOwner || member.User == nil {
			continue
		}

		if owner == nil || member.CreatedAt.Before(owner.CreatedAt) {
			owner = member
		}
	}

	var user *models.User
	if owner != nil {
		user = owner.User
	}

	r.owners[orgUID] = user

	return user
}

func prefsOf(user *models.User) models.UserPreferences {
	if user == nil || user.Preferences == nil {
		return models.UserPreferences{}
	}

	return *user.Preferences
}

// chooser keeps the first valid language and the first valid timezone it is
// shown, independently.
type chooser struct {
	lang     string
	timezone string
}

// consider takes what prefs adds and reports whether both are now known.
func (c *chooser) consider(prefs models.UserPreferences) bool {
	if c.lang == "" {
		c.lang = i18n.Normalize(prefs.Locale)
	}

	if c.timezone == "" {
		if _, err := i18n.ParseTimezone(prefs.Timezone); err == nil {
			c.timezone = prefs.Timezone
		}
	}

	return c.lang != "" && c.timezone != ""
}

func (c *chooser) locale() i18n.Locale {
	return i18n.New(c.lang, c.timezone)
}
