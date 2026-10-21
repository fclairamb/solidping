package recipient_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

var errDown = errors.New("database down")

// fakeLookup serves a fixed set of users and org members and counts calls.
type fakeLookup struct {
	users       map[string]*models.User
	members     map[string][]*models.OrganizationMember
	failUsers   bool
	userCalls   int
	memberCalls int
}

func (f *fakeLookup) GetUserByEmail(_ context.Context, email string) (*models.User, error) {
	f.userCalls++

	if f.failUsers {
		return nil, errDown
	}

	if user, ok := f.users[strings.ToLower(email)]; ok {
		return user, nil
	}

	return nil, sql.ErrNoRows
}

func (f *fakeLookup) ListMembersByOrg(_ context.Context, orgUID string) ([]*models.OrganizationMember, error) {
	f.memberCalls++

	return f.members[orgUID], nil
}

func userWith(email, locale, timezone string) *models.User {
	user := models.NewUser(email)
	if locale != "" || timezone != "" {
		user.Preferences = &models.UserPreferences{Locale: locale, Timezone: timezone}
	}

	return user
}

func member(user *models.User, role models.MemberRole, joined time.Time) *models.OrganizationMember {
	m := models.NewOrganizationMember("org-1", user.UID, role)
	m.User = user
	m.CreatedAt = joined

	return m
}

func newFixture() *fakeLookup {
	day := func(n int) time.Time { return time.Date(2026, 1, n, 0, 0, 0, 0, time.UTC) }
	founder := userWith("founder@acme.com", "de", "Europe/Berlin")
	coOwner := userWith("co@acme.com", "es", "Europe/Madrid")
	admin := userWith("admin@acme.com", "fr", "Europe/Paris")

	return &fakeLookup{
		users: map[string]*models.User{
			"lea@acme.com":     userWith("Lea@acme.com", "fr", "Europe/Paris"),
			"tz-only@acme.com": userWith("tz-only@acme.com", "", "Asia/Tokyo"),
			"bogus@acme.com":   userWith("bogus@acme.com", "klingon", "Mars/Olympus"),
			"bare@acme.com":    userWith("bare@acme.com", "", ""),
		},
		members: map[string][]*models.OrganizationMember{
			// Newest first, as ListMembersByOrg returns them.
			"org-1": {
				member(admin, models.MemberRoleAdmin, day(1)),
				member(coOwner, models.MemberRoleOwner, day(3)),
				member(founder, models.MemberRoleOwner, day(2)),
			},
			"org-empty": nil,
		},
	}
}

func TestForAddress_Chain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		orgUID   string
		address  string
		hint     recipient.Hint
		wantLang string
		wantTZ   string
	}{
		{
			"the account wins over everything", "org-1", "LEA@acme.com",
			recipient.Hint{Locale: "es", Timezone: "America/Lima"},
			"fr", "Europe/Paris",
		},
		{
			"the hint fills what the account lacks", "org-1", "tz-only@acme.com",
			recipient.Hint{Locale: "es"},
			"es", "Asia/Tokyo",
		},
		{
			"the owner fills what the account and hint lack", "org-1", "tz-only@acme.com",
			recipient.Hint{},
			"de", "Asia/Tokyo",
		},
		{
			"invalid stored values are skipped", "", "bogus@acme.com",
			recipient.Hint{Locale: "fr-CA"},
			"fr", "UTC",
		},
		{
			"no account: the hint", "org-1", "stranger@acme.com",
			recipient.Hint{Locale: "fr", Timezone: "Europe/Paris"},
			"fr", "Europe/Paris",
		},
		{
			"no account, no hint: the longest-standing owner", "org-1", "stranger@acme.com",
			recipient.Hint{},
			"de", "Europe/Berlin",
		},
		{
			"no account, partial hint: the owner fills the timezone", "org-1", "stranger@acme.com",
			recipient.Hint{Locale: "es"},
			"es", "Europe/Berlin",
		},
		{"not org mail: no owner", "", "stranger@acme.com", recipient.Hint{}, "en", "UTC"},
		{"an org without an owner", "org-empty", "stranger@acme.com", recipient.Hint{}, "en", "UTC"},
		{
			"an account without preferences falls through", "org-1", "bare@acme.com",
			recipient.Hint{},
			"de", "Europe/Berlin",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			loc := recipient.NewResolver(newFixture()).ForAddress(t.Context(), tc.orgUID, tc.address, tc.hint)
			r.Equal(tc.wantLang, loc.Lang())
			r.Equal(tc.wantTZ, loc.Timezone())
		})
	}
}

func TestForAddress_CachesLookups(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	lookup := newFixture()
	resolver := recipient.NewResolver(lookup)

	for range 3 {
		resolver.ForAddress(t.Context(), "org-1", "stranger@acme.com", recipient.Hint{})
		resolver.ForAddress(t.Context(), "org-1", "Stranger@Acme.com ", recipient.Hint{})
	}

	r.Equal(1, lookup.userCalls, "one account lookup per address, misses included")
	r.Equal(1, lookup.memberCalls, "one member listing per organization")

	// A recipient fully resolved by the account never lists the members.
	fresh := newFixture()
	recipient.NewResolver(fresh).ForAddress(t.Context(), "org-1", "lea@acme.com", recipient.Hint{})
	r.Zero(fresh.memberCalls)
}

func TestForAddress_LookupFailureIsNotFatal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	lookup := newFixture()
	lookup.failUsers = true

	loc := recipient.NewResolver(lookup).ForAddress(t.Context(), "", "lea@acme.com", recipient.Hint{Locale: "es"})
	r.Equal("es", loc.Lang())

	loc = recipient.NewResolver(nil).ForAddress(t.Context(), "org-1", "lea@acme.com", recipient.Hint{})
	r.Equal(i18n.LangEnglish, loc.Lang(), "a nil lookup resolves from the hint alone")
}

func TestForUserAndHintFromUser(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	loc := recipient.ForUser(userWith("a@acme.com", "de-CH", "Europe/Zurich"))
	r.Equal("de", loc.Lang())
	r.Equal("Europe/Zurich", loc.Timezone())

	r.Equal(i18n.LangEnglish, recipient.ForUser(nil).Lang())
	r.Equal("UTC", recipient.ForUser(userWith("b@acme.com", "", "")).Timezone())

	r.Equal(recipient.Hint{Locale: "fr", Timezone: "Europe/Paris"},
		recipient.HintFromUser(userWith("c@acme.com", "fr", "Europe/Paris")))
	r.Equal(recipient.Hint{}, recipient.HintFromUser(nil))
}
