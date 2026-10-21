package regionsweep

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
)

// localeSince is when the fixture outage started: 12:00 in Paris.
var localeSince = time.Date(2026, time.July, 5, 10, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // test fixture

func localeFixtures() map[string]*regionCopy {
	return map[string]*regionCopy{
		noticeDark: {
			kind: noticeDark, region: "eu-west", since: localeSince,
			jobs: 419, overdue: 345, blind: 57, orgs: 3, replaced: 2,
		},
		noticeStalled: {kind: noticeStalled, region: "eu-west", since: localeSince, live: 2, overdue: 41},
		noticeRecovered: {
			kind: noticeRecovered, region: "eu-west", since: localeSince,
			phase: regionoutage.PhaseDark, duration: 7*time.Hour + 52*time.Minute, told: 3,
		},
	}
}

// TestRegionNoticesEnglishTextIsUnchanged: the English text is what every
// medium but email carries, and what the shared marker records. These are the
// strings the sweep built before the notices were localized.
func TestRegionNoticesEnglishTextIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fixtures := localeFixtures()
	since := localeSince.Format(noticeTimeLayout)

	darkHeadline := fmt.Sprintf("region %q is DARK (no live worker) since %s: %d job(s) assigned, %d overdue",
		"eu-west", since, 419, 345)
	r.Equal(darkHeadline, fixtures[noticeDark].headline(i18n.English, true))

	dark := fixtures[noticeDark].notice()
	r.Equal(opsnotify.EventWatchdogRegion, dark.Event)
	r.Equal("[SolidPing watchdog] Region eu-west is dark", dark.Subject)
	r.Equal(strings.Join([]string{
		darkHeadline + ".",
		fmt.Sprintf("%d check(s) in %d organization(s) run only from this region and are not running; "+
			"each of those organizations was notified by email.", 57, 3),
		fmt.Sprintf("%d automatically placed check(s) were moved to another region.", 2),
		fmt.Sprintf("Remediation: bring a worker for %q back, or "+
			"POST /api/v1/system/regions/migrate {\"from\":%q,\"to\":\"<live-region>\"}.", "eu-west", "eu-west"),
	}, "\n\n"), dark.Body)

	stalledHeadline := fmt.Sprintf("region %q is stalled: %d live worker(s) but %d job(s) overdue, oldest since %s",
		"eu-west", 2, 41, since)
	stalled := fixtures[noticeStalled].notice()
	r.Equal("[SolidPing watchdog] Region eu-west is stalled", stalled.Subject)
	r.Equal(strings.Join([]string{
		stalledHeadline + ".",
		"The workers beat but do not claim. Organizations were not notified.",
		"Inspect: GET /api/v1/system/regions/health, then the workers' logs.",
	}, "\n\n"), stalled.Body)

	recovered := fixtures[noticeRecovered].notice()
	r.Equal("[SolidPing watchdog] Region eu-west recovered after 7h52m", recovered.Subject)
	r.Equal(fmt.Sprintf("Region %q is healthy again after %s (%s since %s). %d organization(s) were told.",
		"eu-west", "7h52m", regionoutage.PhaseDark, localeSince.Format(time.RFC3339), 3), recovered.Body)
}

// TestRegionNoticesAreWrittenInTheReadersLanguage: the email copy, in every
// language, with the outage start in the reader's zone.
func TestRegionNoticesAreWrittenInTheReadersLanguage(t *testing.T) {
	t.Parallel()

	expected := map[string]map[string][]string{
		"fr": {
			noticeDark: {
				"[SolidPing watchdog] La région eu-west est hors ligne",
				"La région « eu-west » est HORS LIGNE (aucun worker actif) depuis le 5 juil., 12:00 CEST : " +
					"419 tâches assignées, dont 345 en retard.",
				"57 vérifications de 3 organisations ne s'exécutent que depuis cette région", //nolint:misspell // French
				"2 vérifications placées automatiquement ont été déplacées vers une autre région.",
				`Correctif : relancez un worker pour « eu-west », ou appelez POST /api/v1/system/regions/migrate {"from":"eu-west"`,
			},
			noticeStalled: {
				"[SolidPing watchdog] La région eu-west est bloquée",
				"La région « eu-west » est bloquée : 2 workers actifs, mais 41 tâches en retard, " +
					"la plus ancienne depuis le 5 juil., 12:00 CEST.",
			},
			noticeRecovered: {
				"[SolidPing watchdog] La région eu-west est rétablie après 7\u00a0h 52\u00a0min",
				"La région « eu-west » fonctionne de nouveau après 7\u00a0h 52\u00a0min " +
					"(hors ligne depuis le 5 juil., 12:00 CEST). 3 organisations avaient été prévenues.", //nolint:misspell // French
			},
		},
		"de": {
			noticeDark: {
				"[SolidPing watchdog] Region eu-west ist offline",
				"Die Region „eu-west“ ist OFFLINE (kein aktiver Worker) seit dem 5. Juli, 12:00 CEST",
				"57 Prüfungen aus 3 Organisationen laufen nur in dieser Region",
			},
			noticeStalled:   {"Die Region „eu-west“ hängt: 2 aktive Worker, aber 41 Aufträge überfällig"},
			noticeRecovered: {"Die Region „eu-west“ ist nach", "3 Organisationen wurden benachrichtigt."},
		},
		"es": {
			noticeDark: {
				"[SolidPing watchdog] La región eu-west está fuera de línea",
				"La región «eu-west» está FUERA DE LÍNEA (ningún worker activo) desde el 5 jul, 12:00 CEST",
				"57 comprobaciones de 3 organizaciones solo se ejecutan desde esta región",
			},
			noticeStalled:   {"La región «eu-west» está bloqueada: 2 workers activos, pero 41 tareas con retraso"},
			noticeRecovered: {"(fuera de línea desde el 5 jul, 12:00 CEST). Se avisó a 3 organizaciones."},
		},
	}

	for kind, notice := range localeFixtures() {
		for lang, byKind := range expected {
			r := require.New(t)

			loc := i18n.New(lang, "Europe/Paris").WithClock(func() time.Time { return localeSince })
			subject, body := notice.Localize(loc)
			text := subject + "\n" + body

			r.NotContains(text, "notices.", "%s/%s shows a catalog key", kind, lang)
			r.NotContains(text, "—", "%s/%s carries an em dash", kind, lang)

			for _, want := range byKind[kind] {
				r.Contains(text, want, "%s/%s", kind, lang)
			}
		}
	}
}

// TestRegionNoticeSingularForms covers the _one forms (French also uses them
// for zero).
func TestRegionNoticeSingularForms(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	loc := i18n.New("fr", "")

	_, dark := (&regionCopy{
		kind: noticeDark, region: "eu-west", since: localeSince, jobs: 1, overdue: 1, blind: 1, orgs: 1,
	}).Localize(loc)
	r.Contains(dark, ": 1 tâche assignée, dont 1 en retard.")
	r.Contains(dark,
		"1 vérification de 1 organisation ne s'exécute que depuis cette région et est à l'arrêt") //nolint:misspell // French
	r.Contains(dark, "0 vérification placée automatiquement a été déplacée")

	_, stalled := (&regionCopy{kind: noticeStalled, region: "eu-west", since: localeSince, live: 1, overdue: 1}).
		Localize(loc)
	r.Contains(stalled, "1 worker actif, mais 1 tâche en retard depuis le")
}
