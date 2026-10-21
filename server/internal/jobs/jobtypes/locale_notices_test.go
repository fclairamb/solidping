package jobtypes

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

var (
	// digestNow is the fixture run: 12:00 in Paris.
	digestNow = time.Date(2026, time.July, 5, 10, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // test fixture
	// errDigestFixture is a detector that failed.
	errDigestFixture = errors.New("db timeout")
)

// digestFixtures cover every section and both subject shapes.
func digestFixtures() map[string]*digestCopy {
	failed := &watchdog.Report{
		GeneratedAt: digestNow,
		Failed:      map[string]error{watchdog.DetectorFleetCollapse: errDigestFixture},
	}

	return map[string]*digestCopy{
		"anomalies": {
			generatedAt: digestNow,
			report:      failed,
			transitions: []watchdog.Transition{
				{
					Fingerprint: "stale-incidents:active",
					Kind:        watchdog.TransitionRenotify,
					FirstSeenAt: digestNow.Add(-30 * time.Minute),
					Notify:      true,
					Anomaly: watchdog.Anomaly{
						Severity: watchdog.SeverityWarning,
						Headline: "61 active incident(s) are frozen",
					},
				},
				{
					Fingerprint: "dark-region:eu2",
					Kind:        watchdog.TransitionNew,
					FirstSeenAt: digestNow,
					Notify:      true,
					Anomaly: watchdog.Anomaly{
						Severity:    watchdog.SeverityCritical,
						Headline:    `region "eu2" is DARK (no live worker): 419 job(s) assigned, 345 overdue`,
						Detail:      "liveWorkers=0 checksReferencing=57",
						Remediation: `POST /api/v1/system/regions/migrate {"from":"eu2","to":"<live-region>"}`,
					},
				},
				{
					Fingerprint: "info:x",
					Kind:        watchdog.TransitionNew,
					FirstSeenAt: digestNow,
					Notify:      true,
					Anomaly:     watchdog.Anomaly{Severity: watchdog.SeverityInfo, Headline: "something to know"},
				},
				{Fingerprint: "ongoing:y", Kind: watchdog.TransitionOngoing, Notify: false},
				{
					Fingerprint: "fleet-collapse:instance",
					Kind:        watchdog.TransitionResolved,
					FirstSeenAt: digestNow.Add(-time.Hour),
					Notify:      true,
				},
			},
		},
		"allClear": {
			generatedAt: digestNow,
			report:      &watchdog.Report{GeneratedAt: digestNow},
			transitions: []watchdog.Transition{
				{Fingerprint: "a:1", Kind: watchdog.TransitionResolved, FirstSeenAt: digestNow, Notify: true},
				{Fingerprint: "b:2", Kind: watchdog.TransitionResolved, FirstSeenAt: digestNow, Notify: true},
			},
		},
	}
}

// TestDigestCopyMirrorsTheEnglishDigest holds the localized digest to
// watchdog.BuildDigest, the English text every other medium sends: with the
// same date format, the English writing is the same text, byte for byte.
func TestDigestCopyMirrorsTheEnglishDigest(t *testing.T) {
	t.Parallel()

	rfc3339 := func(instant time.Time) string { return instant.UTC().Format(time.RFC3339) }

	for name, fixture := range digestFixtures() {
		r := require.New(t)

		want := watchdog.BuildDigest(fixture.transitions, fixture.report, fixture.generatedAt)
		subject, body := fixture.write(i18n.English, rfc3339)

		r.Equal(want.Subject, subject, name)
		r.Equal(want.Text, body, name)
	}
}

// TestDigestCopyIsWrittenInEveryLanguage: the digest's own words are
// translated and its dates are in the reader's zone; the detectors'
// diagnostics are quoted as is.
func TestDigestCopyIsWrittenInEveryLanguage(t *testing.T) {
	t.Parallel()

	expected := map[string]map[string][]string{
		"fr": {
			"anomalies": {
				"[SolidPing watchdog] 3 anomalies de la plateforme, dont 1 critique (+1 résolue)\n",
				"Généré le 5 juil., 12:00 CEST\n",
				"\nANOMALIES\n",
				"\n[CRITIQUE] dark-region:eu2 (NOUVELLE)\n",
				"\n[AVERTISSEMENT] stale-incidents:active (TOUJOURS EN COURS depuis le 5 juil., 11:30 CEST)\n",
				"\n[INFO] info:x (NOUVELLE)\n",
				`  correctif : POST /api/v1/system/regions/migrate {"from":"eu2","to":"<live-region>"}`,
				"  61 active incident(s) are frozen\n",
				"\nRÉSOLUES\n  fleet-collapse:instance : résolue (détectée pour la première fois le 5 juil., 11:00 CEST)\n",
				"\nÉCHECS DES DÉTECTEURS (ces anomalies n'ont PAS pu être évaluées lors de cette exécution)\n",
				"  fleet-collapse: db timeout\n",
			},
			"allClear": {"[SolidPing watchdog] Tout est rentré dans l'ordre : 2 anomalies résolues\n"},
		},
		"de": {
			"anomalies": {
				"[SolidPing watchdog] 3 Plattform-Anomalien, davon 1 kritisch (+1 behoben)\n",
				"Erstellt am 5. Juli, 12:00 CEST\n",
				"[KRITISCH] dark-region:eu2 (NEU)",
				"[WARNUNG] stale-incidents:active (BESTEHT WEITERHIN seit dem 5. Juli, 11:30 CEST)",
				"\nBEHOBEN\n",
			},
			"allClear": {"[SolidPing watchdog] Alles in Ordnung: 2 Anomalien behoben\n"},
		},
		"es": {
			"anomalies": {
				"[SolidPing watchdog] 3 anomalías de la plataforma, 1 crítica (+1 resuelta)\n",
				"Generado el 5 jul, 12:00 CEST\n",
				"[CRÍTICO] dark-region:eu2 (NUEVA)",
				"[ADVERTENCIA] stale-incidents:active (SIGUE ACTIVA desde el 5 jul, 11:30 CEST)",
				"\nRESUELTAS\n",
			},
			"allClear": {"[SolidPing watchdog] Todo en orden: 2 anomalías resueltas\n"},
		},
	}

	for name, fixture := range digestFixtures() {
		for lang, byFixture := range expected {
			r := require.New(t)

			loc := i18n.New(lang, "Europe/Paris").WithClock(func() time.Time { return digestNow })
			subject, body := fixture.Localize(loc)

			r.NotContains(subject+body, "notices.", "%s/%s shows a catalog key", name, lang)
			r.Contains(body, subject+"\n", "the body opens with the subject")

			for _, want := range byFixture[name] {
				r.Contains(body, want, "%s/%s", name, lang)
			}
		}
	}
}

// TestDigestCopySingularForms covers the _one forms of the subject.
func TestDigestCopySingularForms(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fixture := &digestCopy{
		generatedAt: digestNow,
		transitions: []watchdog.Transition{{
			Fingerprint: "dark-region:eu2", Kind: watchdog.TransitionNew, FirstSeenAt: digestNow, Notify: true,
			Anomaly: watchdog.Anomaly{Severity: watchdog.SeverityCritical, Headline: "eu2 is dark"},
		}},
	}

	subject, _ := fixture.Localize(i18n.New("fr", ""))
	r.Equal("[SolidPing watchdog] 1 anomalie de la plateforme, dont 1 critique", subject)

	subject, _ = fixture.Localize(i18n.New("es", ""))
	r.Equal("[SolidPing watchdog] 1 anomalía de la plataforma, 1 crítica", subject)
}
