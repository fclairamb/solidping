package regionsweep_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/regionsweep"
)

// queuedEmail is the part of an email job config these tests read, decoded
// from the stored job the way the email job decodes it (numbers become
// float64, times RFC 3339 strings).
type queuedEmail struct {
	Template     string         `json:"template"`
	TemplateData map[string]any `json:"templateData"`
	Locale       string         `json:"locale"`
	Timezone     string         `json:"timezone"`
}

// renderQueued renders the single email job queued for address with template.
func (e *testEnv) renderQueued(t *testing.T, address, template string) (queuedEmail, string, string) {
	t.Helper()

	r := require.New(t)

	jobs := e.emails(t, address, template)
	r.Len(jobs, 1)

	raw, err := json.Marshal(jobs[0].Config)
	r.NoError(err)

	var queued queuedEmail
	r.NoError(json.Unmarshal(raw, &queued))

	formatter, err := email.NewFormatter()
	r.NoError(err)

	subject, _, text, err := formatter.Format(queued.Template,
		i18n.New(queued.Locale, queued.Timezone), queued.TemplateData)
	r.NoError(err)

	return queued, subject, text
}

// adminWithPreferences is admin with a language and a timezone on the account.
func (e *testEnv) adminWithPreferences(t *testing.T, org *models.Organization, address, locale, timezone string) {
	t.Helper()

	user := models.NewUser(address)
	user.Preferences = &models.UserPreferences{Locale: locale, Timezone: timezone}
	require.NoError(t, e.db.CreateUser(t.Context(), user))
	require.NoError(t, e.db.CreateOrganizationMember(t.Context(),
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))
}

// TestRegionNotices_SpeakEachAdminsLanguage: the offline and recovered mails
// are queued once per admin with that admin's language and zone, and render
// after the job's JSON round trip. One blind check reads in the singular: the
// count arrives as a float64, which the old {{if eq .BlindCount 1}} could not
// compare, losing the whole mail.
func TestRegionNotices_SpeakEachAdminsLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	worker := env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")
	env.adminWithPreferences(t, env.acme, "lea@acme.com", "fr", "Europe/Paris")
	env.operator(t, true)

	env.sweep(t)

	queued, subject, text := env.renderQueued(t, "alice@acme.com", email.TemplateRegionOffline)
	r.Equal(i18n.LangEnglish, queued.Locale, "an admin without preferences reads English, never the owner's")
	r.Equal("UTC", queued.Timezone)
	r.Equal("🔴 Region Lauterbourg (lauterbourg) is offline: 1 of your checks is not running", subject)
	r.Contains(text, "1 of your checks in Acme runs only from this region, so it is not being checked right now:")
	r.Contains(text, "Until then this check will show no data rather than a status.")

	queued, subject, text = env.renderQueued(t, "lea@acme.com", email.TemplateRegionOffline)
	r.Equal("fr", queued.Locale)
	r.Equal("Europe/Paris", queued.Timezone)
	r.Equal("🔴 La région Lauterbourg (lauterbourg) est hors ligne : 1 de vos contrôles ne s'exécute plus", subject)
	r.Contains(text, "1 de vos contrôles dans l'organisation Acme s'exécute uniquement depuis cette région")
	r.Contains(text, "- Pinned API : https://solidping.acme.com/")
	r.Contains(text, "D'ici là, ce contrôle n'affichera pas de statut, faute de données.")

	since := env.marker(t, darkRegion).Since
	r.Contains(text, "est hors ligne depuis le "+i18n.New("fr", "Europe/Paris").Date(since)+".")

	// The worker comes back: two healthy sweeps make a recovery.
	r.NoError(env.db.UpdateWorkerHeartbeat(t.Context(), worker.UID, []string{}, ""))
	env.sweep(t)
	env.sweep(t)

	queued, subject, text = env.renderQueued(t, "lea@acme.com", email.TemplateRegionRecovered)
	r.Equal("fr", queued.Locale)
	r.Equal("🟢 La région Lauterbourg (lauterbourg) est rétablie, vos contrôles ont repris", subject)
	r.Contains(text, "Hors ligne pendant : 10 min")
	r.Contains(text, "Passée hors ligne : "+i18n.New("fr", "Europe/Paris").Date(since))

	_, subject, text = env.renderQueued(t, "alice@acme.com", email.TemplateRegionRecovered)
	r.Equal("🟢 Region Lauterbourg (lauterbourg) is back, your checks resumed", subject)
	r.Contains(text, "Offline for: 10m")
}

// TestOfflineEmailData_CountsWhatItDoesNotList: MoreCount is what the listed
// checks leave out, and a count of one reads in the singular in every
// language after the job's JSON round trip.
func TestOfflineEmailData_CountsWhatItDoesNotList(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	since := time.Date(2026, 9, 24, 13, 41, 0, 0, time.UTC)
	data := regionsweep.OfflineEmailData("Tokyo (tokyo)", since, 22, 1,
		[]regionsweep.NoticeCheck{{Name: "API"}, {Name: "Web", URL: "https://acme.com/web"}}, "")
	r.Equal(20, data["MoreCount"])

	raw, err := json.Marshal(data)
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(raw, &decoded))

	formatter, err := email.NewFormatter()
	r.NoError(err)

	want := map[string][2]string{
		"en": {"…and 20 more.", "1 other check keeps running from its other regions."},
		"fr": {"…et 20 autres.", "1 autre contrôle continue de s'exécuter depuis ses régions restantes."},
		"de": {"…und 20 weitere.", "1 weitere Prüfung läuft in ihren übrigen Regionen weiter."},
		"es": {"…y 20 más.", "1 comprobación más sigue ejecutándose desde sus otras"},
	}

	for lang, sentences := range want {
		_, html, text, err := formatter.Format(email.TemplateRegionOffline, i18n.New(lang, "Asia/Tokyo"), decoded)
		r.NoError(err, lang)

		for _, sentence := range sentences {
			r.Contains(text, sentence, lang)
		}

		r.Contains(html, "<li>API</li>", "a check without a URL is listed unlinked")
		if lang == i18n.LangEnglish {
			r.Contains(text, "Until then these checks will show no data", "22 blind checks read in the plural")
		}
		r.Contains(text, "22:41 JST", "%s: the outage start is in the reader's zone", lang)
	}
}
