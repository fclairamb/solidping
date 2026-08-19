// Package incidentpublications implements the incident publication overlay:
// the object that says "this incident is visible on this status page, under
// this customer-readable title, in this state" (spec 2026-08-19-08).
//
// Everything this package writes is customer-facing. The single hard rule it
// exists to enforce is that NOTHING derived from a probe — a check's error
// string, a response body, `results.output`, an incident's `details` blob —
// may reach any field it produces. Those routinely carry internal hostnames,
// IPs and stack fragments. The templates below are the only source of public
// narrative text, and they interpolate exactly one value: the resource's
// PUBLIC display name, which the page already renders.
package incidentpublications

import "strings"

// templateSet is one language's worth of public narrative strings. The `%s`
// slot is always the resource's public display name — never a check slug,
// never an incident title, never probe output.
type templateSet struct {
	// OpenedTitle is the publication's public title, e.g. "Payments API is
	// experiencing issues".
	OpenedTitle string
	// OpenedBody is the first `investigating` update.
	OpenedBody string
	// GroupOpenedTitle is used when the affected component could not be named
	// (a group resource with no public name, or a multi-resource incident).
	GroupOpenedTitle string
	// ResolvedTitle / ResolvedBody close an untouched auto-publication.
	ResolvedTitle string
	ResolvedBody  string
	// MonitoringTitle / MonitoringBody are posted instead of a resolve when a
	// human has taken the narrative over (`if_untouched` policy): the
	// automation reports the recovery and leaves the final word to them.
	MonitoringTitle string
	MonitoringBody  string
	// RelapseTitle / RelapseBody reopen a publication after a relapse.
	RelapseTitle string
	RelapseBody  string
	// AlsoAffectingTitle / AlsoAffectingBody announce a new member joining a
	// group incident.
	AlsoAffectingTitle string
	AlsoAffectingBody  string
}

//nolint:gochecknoglobals // static translation table, treated as a constant.
var publicTemplates = map[string]templateSet{
	"en": {
		OpenedTitle:        "%s is experiencing issues",
		OpenedBody:         "We are investigating an issue affecting %s.",
		GroupOpenedTitle:   "Some services are experiencing issues",
		ResolvedTitle:      "%s has recovered",
		ResolvedBody:       "%s is operating normally again. This incident is resolved.",
		MonitoringTitle:    "%s has recovered",
		MonitoringBody:     "The affected component has recovered. We are monitoring the situation.",
		RelapseTitle:       "%s is experiencing issues again",
		RelapseBody:        "%s is affected again. We are investigating.",
		AlsoAffectingTitle: "This incident is also affecting %s",
		AlsoAffectingBody:  "%s is now affected by this incident as well.",
	},
	"fr": {
		OpenedTitle:        "%s rencontre des difficultés",
		OpenedBody:         "Nous investiguons un incident affectant %s.",
		GroupOpenedTitle:   "Certains services rencontrent des difficultés",
		ResolvedTitle:      "%s a été rétabli",
		ResolvedBody:       "%s fonctionne de nouveau normalement. L'incident est résolu.",
		MonitoringTitle:    "%s a été rétabli",
		MonitoringBody:     "Le composant affecté a été rétabli. Nous surveillons la situation.",
		RelapseTitle:       "%s rencontre de nouveau des difficultés",
		RelapseBody:        "%s est de nouveau affecté. Nous investiguons.",
		AlsoAffectingTitle: "Cet incident affecte également %s",
		AlsoAffectingBody:  "%s est désormais également affecté par cet incident.",
	},
	"de": {
		OpenedTitle:        "%s hat Probleme",
		OpenedBody:         "Wir untersuchen eine Störung, die %s betrifft.",
		GroupOpenedTitle:   "Einige Dienste haben Probleme",
		ResolvedTitle:      "%s ist wiederhergestellt",
		ResolvedBody:       "%s funktioniert wieder normal. Die Störung ist behoben.",
		MonitoringTitle:    "%s ist wiederhergestellt",
		MonitoringBody:     "Die betroffene Komponente ist wiederhergestellt. Wir beobachten die Lage.",
		RelapseTitle:       "%s hat erneut Probleme",
		RelapseBody:        "%s ist erneut betroffen. Wir untersuchen das Problem.",
		AlsoAffectingTitle: "Diese Störung betrifft auch %s",
		AlsoAffectingBody:  "%s ist nun ebenfalls von dieser Störung betroffen.",
	},
	"es": {
		OpenedTitle:        "%s está experimentando problemas",
		OpenedBody:         "Estamos investigando una incidencia que afecta a %s.",
		GroupOpenedTitle:   "Algunos servicios están experimentando problemas",
		ResolvedTitle:      "%s se ha recuperado",
		ResolvedBody:       "%s vuelve a funcionar con normalidad. La incidencia está resuelta.",
		MonitoringTitle:    "%s se ha recuperado",
		MonitoringBody:     "El componente afectado se ha recuperado. Estamos supervisando la situación.",
		RelapseTitle:       "%s vuelve a experimentar problemas",
		RelapseBody:        "%s está afectado de nuevo. Estamos investigando.",
		AlsoAffectingTitle: "Esta incidencia también afecta a %s",
		AlsoAffectingBody:  "%s también está afectado por esta incidencia.",
	},
}

// defaultLanguage is the fallback for any page whose language is unset or not
// translated here. English, matching the subscriber-mail templates.
const defaultLanguage = "en"

// templatesFor resolves a page language ("fr", "fr-FR", "" …) to a template
// set, falling back to English. Matching is on the primary subtag and is
// case-insensitive, so "FR-ca" resolves to French rather than silently
// dropping to English.
func templatesFor(language *string) templateSet {
	if language == nil || *language == "" {
		return publicTemplates[defaultLanguage]
	}

	lang := strings.ToLower(strings.TrimSpace(*language))
	if idx := strings.IndexAny(lang, "-_"); idx > 0 {
		lang = lang[:idx]
	}

	if set, ok := publicTemplates[lang]; ok {
		return set
	}

	return publicTemplates[defaultLanguage]
}
