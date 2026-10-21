package uptimereport

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/slo"
)

// nbsp is the no-break space fr, de and es put between a number and its unit.
const nbsp = " "

// writtenClock is "now" for the reports below: the year of their periods, so
// no day label carries a year.
func writtenClock() time.Time {
	return time.Date(2026, time.August, 3, 9, 0, 0, 0, time.UTC)
}

// measuredReport is a monthly July report for a schedule in Paris, flags and
// figures only, as measure leaves it.
func measuredReport(t *testing.T) *Data {
	t.Helper()

	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	july := time.Date(2026, time.July, 1, 0, 0, 0, 0, paris)
	attainment := 99.95

	return &Data{
		OrgName:                "acme",
		Timezone:               "Europe/Paris",
		HasData:                true,
		CheckCount:             1,
		IncidentCount:          1,
		HasPreviousData:        true,
		PreviousIncidentCount:  3,
		ShowAvailabilityDelta:  true,
		AvailabilityDeltaColor: deltaGoodColor,
		ShowIncidentDelta:      true,
		ShowResponseDelta:      true,
		HasLatency:             true,
		StripsShown:            1,
		ShowStripBudgetNote:    true,
		Checks:                 []CheckRow{{Name: "api", HasData: true, Availability: 99.98}},
		SLOs: []SLORow{{
			Name: "API", HasData: true, State: slo.StateAtRisk,
			Attainment: &attainment, Target: 99.9, Budget: -(12*time.Minute + 30*time.Second),
		}},
		Figures: Figures{
			Period:               slo.Window{Start: july, End: july.AddDate(0, 1, 0)},
			Previous:             slo.Window{Start: july.AddDate(0, -1, 0), End: july},
			ScheduleName:         "Production",
			Availability:         99.95,
			PreviousAvailability: 99.87,
			AvailabilityDelta:    0.08,
			IncidentDelta:        -2,
			ResponseDelta:        8.4,
			LongestIncident:      42 * time.Minute,
			AverageIncident:      21*time.Minute + 40*time.Second,
			TotalDowntime:        time.Hour + 5*time.Minute,
			AvgResponse:          171,
			MinResponse:          42,
			MaxResponse:          3200,
			HasResponseRange:     true,
			PreviousAvgResponse:  158,
			SlowSamples:          1,
			SlowPeaks:            2,
			StripStart:           time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			StripDays:            31,
		},
	}
}

// writtenWant is what one language writes for measuredReport.
type writtenWant struct {
	period, previous, scope, pct, delta, response, responseDelta string
	longest, average, budget, state, slow, strip, note           string
}

func writtenWants() map[string]writtenWant {
	return map[string]writtenWant{
		i18n.LangEnglish: {
			"July 2026", "June 2026", "Production (1 check)", "99.950%", "+0.080 pts", "3.20 s", "+8.4%",
			"42m", "21m 40s", "-12m 30s", "At risk", "1 sample and 2 peaks above 1 s",
			"Daily availability, Jul 1 – Jul 31 (UTC)", "Daily strips are shown for 1 check only",
		},
		i18n.LangFrench: {
			"juillet 2026", "juin 2026", "Production (1 contrôle)", "99,950" + nbsp + "%", "+0,080" + nbsp + "pt",
			"3,20" + nbsp + "s", "+8,4" + nbsp + "%",
			"42" + nbsp + "min", "21" + nbsp + "min 40" + nbsp + "s", "-12" + nbsp + "min 30" + nbsp + "s", "À risque",
			"1 mesure et 2 pics au-dessus de 1" + nbsp + "s",
			"Disponibilité quotidienne, 1 juil. – 31 juil. (UTC)", "ne sont affichées que pour 1 contrôle",
		},
		i18n.LangGerman: {
			"Juli 2026", "Juni 2026", "Production (1 Prüfung)", "99,950" + nbsp + "%", "+0,080" + nbsp + "Prozentpunkte",
			"3,20" + nbsp + "s", "+8,4" + nbsp + "%",
			"42" + nbsp + "Min.", "21" + nbsp + "Min. 40" + nbsp + "Sek.", "-12" + nbsp + "Min. 30" + nbsp + "Sek.", "Gefährdet",
			"1 Messung und 2 Spitzen über 1" + nbsp + "s",
			"Tägliche Verfügbarkeit, 1. Juli – 31. Juli (UTC)", "nur für 1 Prüfung angezeigt",
		},
		i18n.LangSpanish: {
			"julio de 2026", "junio de 2026", "Production (1 comprobación)", "99,950" + nbsp + "%", "+0,080" + nbsp + "puntos",
			"3,20" + nbsp + "s", "+8,4" + nbsp + "%",
			"42" + nbsp + "min", "21" + nbsp + "min 40" + nbsp + "s", "-12" + nbsp + "min 30" + nbsp + "s", "En riesgo",
			"1 muestra y 2 picos por encima de 1" + nbsp + "s",
			"Disponibilidad diaria, 1 jul – 31 jul (UTC)", "solo se muestran para 1 comprobación",
		},
	}
}

// TestWriteInEveryLanguage pins each language's figures: separators, units,
// plurals, and the period read in the SCHEDULE's zone. The reader sits in New
// York, where July 1 00:00 Paris is still June 30: a label in the reader's
// zone would say June.
func TestWriteInEveryLanguage(t *testing.T) {
	t.Parallel()

	for lang, want := range writtenWants() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			loc := i18n.New(lang, "America/New_York").WithClock(writtenClock)
			data := measuredReport(t).In(loc)

			r.Equal(want.period, data.PeriodLabel)
			r.Equal(want.previous, data.PreviousPeriodLabel)
			r.Equal(want.scope, data.ScopeLabel)
			r.Equal(want.pct, data.AvailabilityPct)
			r.Equal(want.pct, data.SLOs[0].AttainmentPct)
			r.Equal(want.delta, data.AvailabilityDeltaText)
			r.Equal("-2", data.IncidentDeltaText)
			r.Equal(want.response, data.MaxResponseTime)
			r.Equal(want.responseDelta, data.ResponseDeltaText)
			r.Equal(want.longest, data.LongestIncident)
			r.Equal(want.average, data.AverageIncident)
			r.Equal(want.budget, data.SLOs[0].BudgetRemaining)
			r.Equal(want.state, data.SLOs[0].StateLabel)
			r.Equal(want.slow, data.SlowLine)
			r.Equal(want.strip, data.DayStripLabel)
			r.Contains(data.StripBudgetNote, want.note)
			r.NotEmpty(data.SlowNote)
			r.NotEmpty(data.LatencyNote)
		})
	}
}

// TestWriteNumbersAndPlurals covers the forms the table above does not: the
// other side of each plural, "no change", the org-wide scope, and a weekly
// period read as a day range.
func TestWriteNumbersAndPlurals(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	report := measuredReport(t)
	report.CheckCount = 12
	report.Figures.OrgWide = true
	report.Figures.AvailabilityDelta = 0
	report.Figures.IncidentDelta = 0
	report.Figures.SlowSamples = 0
	report.Figures.SlowPeaks = 1
	report.StripsShown = 22

	english := report.In(i18n.English.WithClock(writtenClock))
	r.Equal("All checks (12)", english.ScopeLabel)
	r.Equal("no change", english.AvailabilityDeltaText)
	r.Equal("no change", english.IncidentDeltaText)
	r.Equal("1 peak above 1 s", english.SlowLine)
	r.Contains(english.StripBudgetNote, "shown for the 22 lowest-uptime checks;")

	french := report.In(i18n.New(i18n.LangFrench, "").WithClock(writtenClock))
	r.Equal("Tous les contrôles (12)", french.ScopeLabel)
	r.Equal("stable", french.AvailabilityDeltaText)
	r.Equal("1 pic au-dessus de 1"+nbsp+"s", french.SlowLine)
	r.Contains(french.StripBudgetNote, "pour les 22 contrôles")

	// A scoped schedule with several checks takes the plural.
	report.Figures.OrgWide = false
	r.Equal("Production (12 checks)", report.In(i18n.English).ScopeLabel)
	r.Equal("Production (12 Prüfungen)", report.In(i18n.New(i18n.LangGerman, "")).ScopeLabel)

	// Weekly: the first and last day of the week, in the schedule's zone.
	paris, err := time.LoadLocation("Europe/Paris")
	r.NoError(err)

	monday := time.Date(2026, time.July, 6, 0, 0, 0, 0, paris)
	report.Figures.Weekly = true
	report.Figures.Period = slo.Window{Start: monday, End: monday.AddDate(0, 0, 7)}

	r.Equal("Jul 6 – Jul 12", report.In(i18n.English.WithClock(writtenClock)).PeriodLabel)
	r.Equal("6 juil. – 12 juil.",
		report.In(i18n.New(i18n.LangFrench, "Pacific/Auckland").WithClock(writtenClock)).PeriodLabel)
}

// TestInLeavesTheSharedReportAlone: one reader's copy must never change what
// the next reader gets.
func TestInLeavesTheSharedReportAlone(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	report := measuredReport(t)
	report.write(i18n.English)

	french := report.In(i18n.New(i18n.LangFrench, ""))
	french.UnsubscribeURL = "https://acme.example/unsubscribe?token=fr"

	r.Equal("July 2026", report.PeriodLabel)
	r.Equal("99.980%", report.Checks[0].AvailabilityPct)
	r.Equal("At risk", report.SLOs[0].StateLabel)
	r.Empty(report.UnsubscribeURL)
	r.Equal("juillet 2026", french.PeriodLabel)
	r.Equal("99,980"+nbsp+"%", french.Checks[0].AvailabilityPct)
}

// readerLookup is a recipient.Lookup over a fixed set of accounts.
type readerLookup struct {
	users   map[string]*models.User
	owner   *models.User
	lookups int
}

func (l *readerLookup) GetUserByEmail(_ context.Context, email string) (*models.User, error) {
	l.lookups++

	if user, ok := l.users[strings.ToLower(email)]; ok {
		return user, nil
	}

	return nil, sql.ErrNoRows
}

func (l *readerLookup) ListMembersByOrg(_ context.Context, _ string) ([]*models.OrganizationMember, error) {
	return []*models.OrganizationMember{{Role: models.MemberRoleOwner, User: l.owner}}, nil
}

func reader(email, locale, timezone string) *models.User {
	user := models.NewUser(email)
	user.Preferences = &models.UserPreferences{Locale: locale, Timezone: timezone}

	return user
}

// TestForReaderGroupsRecipientsByLocale: each recipient reads the report in
// their own language (an address with no account in the org owner's), and
// recipients who share a language and timezone share one written copy.
func TestForReaderGroupsRecipientsByLocale(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	lookup := &readerLookup{
		users: map[string]*models.User{
			"lea@acme.com":   reader("lea@acme.com", "fr", "Europe/Paris"),
			"marc@acme.com":  reader("marc@acme.com", "fr-FR", "Europe/Paris"),
			"jonas@acme.com": reader("jonas@acme.com", "de", "Europe/Berlin"),
			"bare@acme.com":  reader("bare@acme.com", "", ""),
		},
		owner: reader("owner@acme.com", "es", "America/Lima"),
	}

	report := measuredReport(t)
	report.readers = recipient.NewResolver(lookup)
	report.write(i18n.English)

	for _, tc := range []struct {
		address, lang, timezone, period string
	}{
		{"lea@acme.com", "fr", "Europe/Paris", "juillet 2026"},
		{"marc@acme.com", "fr", "Europe/Paris", "juillet 2026"},
		{"jonas@acme.com", "de", "Europe/Berlin", "Juli 2026"},
		// A member without preferences reads English, never the owner's.
		{"bare@acme.com", "en", "UTC", "July 2026"},
		// A list address with no account reads the owner's language.
		{"ops-list@acme.com", "es", "America/Lima", "julio de 2026"},
	} {
		written, loc := report.ForReader(ctx, "org-1", tc.address)

		r.Equal(tc.lang, loc.Lang(), tc.address)
		r.Equal(tc.timezone, loc.Timezone(), tc.address)
		r.Equal(tc.period, written.PeriodLabel, tc.address)
	}

	// Five recipients, four locales: Lea and Marc share one French copy.
	r.Len(report.written, 4)

	// The returned value is the caller's own: setting one recipient's link
	// does not reach the next one of the same language.
	first, _ := report.ForReader(ctx, "org-1", "lea@acme.com")
	first.UnsubscribeURL = "https://acme.example/unsubscribe?token=lea"

	second, _ := report.ForReader(ctx, "org-1", "marc@acme.com")
	r.Empty(second.UnsubscribeURL)

	// The resolver caches each address: no second lookup for Lea or Marc.
	r.Equal(5, lookup.lookups)

	// The shared report stays English.
	r.Equal("July 2026", report.PeriodLabel)
}

// TestForReaderWithoutAResolverIsEnglish: a report not made by Build (no
// resolver) still goes out, in English.
func TestForReaderWithoutAResolverIsEnglish(t *testing.T) {
	t.Parallel()

	written, loc := measuredReport(t).ForReader(t.Context(), "org-1", "lea@acme.com")

	require.Equal(t, i18n.LangEnglish, loc.Lang())
	require.Equal(t, "July 2026", written.PeriodLabel)
}
