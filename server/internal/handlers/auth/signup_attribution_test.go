package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestNormalizeSignupAttribution pins the contract of the one function that
// decides what a client may park on a user row (spec 2026-09-07-03): bounded
// lengths, a closed set of click-id kinds, and nil when nothing is left.
func TestNormalizeSignupAttribution(t *testing.T) {
	t.Parallel()

	captured := time.Date(2026, 9, 7, 10, 0, 0, 0, time.FixedZone("CEST", 2*3600))

	tests := []struct {
		name string
		in   *models.SignupAttribution
		want *models.SignupAttribution
	}{
		{name: "nil stays nil", in: nil, want: nil},
		{name: "empty becomes nil", in: &models.SignupAttribution{}, want: nil},
		{
			name: "whitespace-only becomes nil",
			in:   &models.SignupAttribution{Source: "  ", Campaign: "\t"},
			want: nil,
		},
		{
			name: "a full google click is kept, kind lower-cased, time in UTC",
			in: &models.SignupAttribution{
				Source: " google ", Medium: "cpc", Campaign: "fr-alternatives", Term: "alternative uptimerobot",
				Content: "creative-1", ClickIDKind: "GCLID", ClickID: "Cj0KCQjw", LandingPath: "/d/login",
				CapturedAt: &captured,
			},
			want: &models.SignupAttribution{
				Source: "google", Medium: "cpc", Campaign: "fr-alternatives", Term: "alternative uptimerobot",
				Content: "creative-1", ClickIDKind: "gclid", ClickID: "Cj0KCQjw", LandingPath: "/d/login",
				CapturedAt: ptr(captured.UTC()),
			},
		},
		{
			name: "an unknown click-id kind drops the id, keeps the campaign",
			in:   &models.SignupAttribution{Campaign: "x", ClickIDKind: "fbclid", ClickID: "abc"},
			want: &models.SignupAttribution{Campaign: "x"},
		},
		{
			name: "a click-id kind without an id is dropped",
			in:   &models.SignupAttribution{Campaign: "x", ClickIDKind: "gclid", ClickID: "  "},
			want: &models.SignupAttribution{Campaign: "x"},
		},
		{
			name: "an id without a kind is dropped, so nothing is left",
			in:   &models.SignupAttribution{ClickID: "abc"},
			want: nil,
		},
		{
			name: "values are capped at 200 characters",
			in:   &models.SignupAttribution{Campaign: strings.Repeat("a", 500)},
			want: &models.SignupAttribution{Campaign: strings.Repeat("a", 200)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			got := normalizeSignupAttribution(tt.in)

			if tt.want == nil {
				r.Nil(got)

				return
			}

			r.NotNil(got)

			if tt.want.CapturedAt == nil {
				// The client said nothing about time, so the server stamped
				// "now". Check it is recent and UTC, then compare the rest.
				r.NotNil(got.CapturedAt)
				r.WithinDuration(time.Now(), *got.CapturedAt, time.Minute)
				r.Equal(time.UTC, got.CapturedAt.Location())
				got.CapturedAt = nil
			}

			r.Equal(tt.want, got)
		})
	}
}

// TestRegisterPersistsSignupAttribution walks the real two-step flow —
// Register stashes a pending entry, ConfirmRegistration creates the account —
// and proves the attribution survives the JSON round-trip through the state
// store and lands on the user row, normalized. Then proves the negative: a
// registration without attribution stores NULL, not an empty object.
func TestRegisterPersistsSignupAttribution(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newLoginAuditFixture(t)
	f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

	confirm := func(email string) *models.User {
		t.Helper()

		entries, err := f.db.ListStateEntries(ctx, nil, registrationKeyPrefix)
		r.NoError(err)

		var token string

		for _, entry := range entries {
			if entry.Value == nil {
				continue
			}

			if got, ok := (*entry.Value)[keyEmail].(string); ok && got == email {
				token, _ = (*entry.Value)[keyToken].(string)
			}
		}

		r.NotEmpty(token, "precondition: the registration token must have been stored")

		_, err = f.svc.ConfirmRegistration(ctx, token)
		r.NoError(err)

		user, err := f.db.GetUserByEmail(ctx, email)
		r.NoError(err)

		return user
	}

	captured := time.Date(2026, 9, 7, 8, 30, 0, 0, time.UTC)

	_, err := f.svc.Register(ctx, RegisterRequest{
		Email: "paid-click@acme.com", Password: "supersecret123",
		Attribution: &models.SignupAttribution{
			Source: "google", Medium: "cpc", Campaign: "en-category",
			ClickIDKind: "gclid", ClickID: "TeSt.ClickId-123", CapturedAt: &captured,
			// Would be dropped by normalization: not a known kind... but the
			// kind IS known here, so this exercises the keep path; the
			// unknown-kind path is covered by the unit test above.
		},
	})
	r.NoError(err)

	user := confirm("paid-click@acme.com")
	r.NotNil(user.SignupAttribution, "the attribution must reach the user row")
	r.Equal("google", user.SignupAttribution.Source)
	r.Equal("cpc", user.SignupAttribution.Medium)
	r.Equal("en-category", user.SignupAttribution.Campaign)
	r.Equal("gclid", user.SignupAttribution.ClickIDKind)
	r.Equal("TeSt.ClickId-123", user.SignupAttribution.ClickID)
	r.NotNil(user.SignupAttribution.CapturedAt)
	r.True(captured.Equal(*user.SignupAttribution.CapturedAt), "the client's capture time is kept")

	// Read it back through a fresh query too: what GetUserByEmail returned
	// above came from the same process, this proves the column survives a
	// round-trip through the database's JSON encoding.
	again, err := f.db.GetUser(ctx, user.UID)
	r.NoError(err)
	r.Equal(user.SignupAttribution, again.SignupAttribution)

	_, err = f.svc.Register(ctx, RegisterRequest{
		Email: "organic@acme.com", Password: "supersecret123",
	})
	r.NoError(err)

	organic := confirm("organic@acme.com")
	r.Nil(organic.SignupAttribution, "an untagged signup stores nothing, not an empty object")
}

func ptr[T any](v T) *T {
	return &v
}
