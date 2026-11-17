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

import (
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// templateKey names one public narrative template. The value is also the
// message key suffix dash0 and status0 render it from ("publication." + key),
// so a visitor reading the page in another language than the page's own gets
// the same sentence in theirs. Never rename one: it is stored in
// incident_publications.public_title_i18n and status_updates.*_i18n.
type templateKey string

// The `%s` slot of every template is the resource's public display name —
// never a check slug, never an incident title, never probe output.
const (
	// keyOpenedTitle is the publication's public title, e.g. "Payments API is
	// experiencing issues".
	keyOpenedTitle templateKey = "openedTitle"
	// keyOpenedBody is the first `investigating` update.
	keyOpenedBody templateKey = "openedBody"
	// keyGroupOpenedTitle is used when the affected component could not be
	// named (a group resource with no public name, or a multi-resource
	// incident). It also stands in for the name in the other templates then.
	keyGroupOpenedTitle templateKey = "groupOpenedTitle"
	// keyResolvedTitle / keyResolvedBody close an untouched auto-publication.
	keyResolvedTitle templateKey = "resolvedTitle"
	keyResolvedBody  templateKey = "resolvedBody"
	// keyMonitoringTitle / keyMonitoringBody are posted instead of a resolve
	// when a human has taken the narrative over (`if_untouched` policy): the
	// automation reports the recovery and leaves the final word to them.
	keyMonitoringTitle templateKey = "monitoringTitle"
	keyMonitoringBody  templateKey = "monitoringBody"
	// keyRelapseTitle / keyRelapseBody reopen a publication after a relapse.
	keyRelapseTitle templateKey = "relapseTitle"
	keyRelapseBody  templateKey = "relapseBody"
	// keyAlsoAffectingTitle / keyAlsoAffectingBody announce a new member
	// joining a group incident.
	keyAlsoAffectingTitle templateKey = "alsoAffectingTitle"
	keyAlsoAffectingBody  templateKey = "alsoAffectingBody"
	// keyRemovedTitle / keyRemovedBody close a publication whose check was
	// deleted: there is nothing left to recover, so the entry is resolved
	// whatever the page's auto_resolve policy says. keyRemovedGenericTitle
	// replaces keyRemovedTitle when the resource can no longer be named (its
	// page row usually leaves with the check).
	keyRemovedTitle        templateKey = "removedTitle"
	keyRemovedGenericTitle templateKey = "removedGenericTitle"
	keyRemovedBody         templateKey = "removedBody"
)

// I18nKeyPrefix prefixes every templateKey in the stored recipes and in the
// frontends' locale files.
const I18nKeyPrefix = "publication."

// templateSet is one language's worth of public narrative strings.
type templateSet map[templateKey]string

// renderedText is one templated sentence: the literal text in the page
// language (what mails, feeds and webhooks carry) and the recipe a client
// renders in its reader's language.
type renderedText struct {
	Value string
	I18n  *models.LocalizedText
}

// render fills template key with name. An empty name means the component
// could not be named: the group title stands in for it, and the recipe
// carries no name so the client substitutes its own translation of that
// title.
func (t templateSet) render(key templateKey, name string) renderedText {
	format := t[key]

	var params *models.LocalizedParams

	if strings.Contains(format, "%s") {
		if name == "" {
			name = t[keyGroupOpenedTitle]
		} else {
			params = &models.LocalizedParams{Name: name}
		}

		format = fmt.Sprintf(format, name)
	}

	return renderedText{Value: format, I18n: models.NewLocalizedText(I18nKeyPrefix+string(key), params)}
}

// The Spanish entries below trip the English-only spell checker on words like
// "problemas"; the table is translated copy, not prose.
//
//nolint:gochecknoglobals,misspell // static translation table, treated as a constant.
var publicTemplates = map[string]templateSet{
	"en": {
		keyOpenedTitle:         "%s is experiencing issues",
		keyOpenedBody:          "We are investigating an issue affecting %s.",
		keyGroupOpenedTitle:    "Some services are experiencing issues",
		keyResolvedTitle:       "%s has recovered",
		keyResolvedBody:        "%s is operating normally again. This incident is resolved.",
		keyMonitoringTitle:     "%s has recovered",
		keyMonitoringBody:      "The affected component has recovered. We are monitoring the situation.",
		keyRelapseTitle:        "%s is experiencing issues again",
		keyRelapseBody:         "%s is affected again. We are investigating.",
		keyAlsoAffectingTitle:  "This incident is also affecting %s",
		keyAlsoAffectingBody:   "%s is now affected by this incident as well.",
		keyRemovedTitle:        "%s is no longer monitored",
		keyRemovedGenericTitle: "The affected service is no longer monitored",
		keyRemovedBody:         "The monitored resource was removed. This incident is resolved.",
	},
	"fr": {
		keyOpenedTitle:         "%s rencontre des difficultés",
		keyOpenedBody:          "Nous investiguons un incident affectant %s.",
		keyGroupOpenedTitle:    "Certains services rencontrent des difficultés",
		keyResolvedTitle:       "%s a été rétabli",
		keyResolvedBody:        "%s fonctionne de nouveau normalement. L'incident est résolu.",
		keyMonitoringTitle:     "%s a été rétabli",
		keyMonitoringBody:      "Le composant affecté a été rétabli. Nous surveillons la situation.",
		keyRelapseTitle:        "%s rencontre de nouveau des difficultés",
		keyRelapseBody:         "%s est de nouveau affecté. Nous investiguons.",
		keyAlsoAffectingTitle:  "Cet incident affecte également %s",
		keyAlsoAffectingBody:   "%s est désormais également affecté par cet incident.",
		keyRemovedTitle:        "%s n'est plus surveillé",
		keyRemovedGenericTitle: "Le service affecté n'est plus surveillé",
		keyRemovedBody:         "La ressource surveillée a été retirée. L'incident est résolu.",
	},
	"de": {
		keyOpenedTitle:         "%s hat Probleme",
		keyOpenedBody:          "Wir untersuchen eine Störung, die %s betrifft.",
		keyGroupOpenedTitle:    "Einige Dienste haben Probleme",
		keyResolvedTitle:       "%s ist wiederhergestellt",
		keyResolvedBody:        "%s funktioniert wieder normal. Die Störung ist behoben.",
		keyMonitoringTitle:     "%s ist wiederhergestellt",
		keyMonitoringBody:      "Die betroffene Komponente ist wiederhergestellt. Wir beobachten die Lage.",
		keyRelapseTitle:        "%s hat erneut Probleme",
		keyRelapseBody:         "%s ist erneut betroffen. Wir untersuchen das Problem.",
		keyAlsoAffectingTitle:  "Diese Störung betrifft auch %s",
		keyAlsoAffectingBody:   "%s ist nun ebenfalls von dieser Störung betroffen.",
		keyRemovedTitle:        "%s wird nicht mehr überwacht",
		keyRemovedGenericTitle: "Der betroffene Dienst wird nicht mehr überwacht",
		keyRemovedBody:         "Die überwachte Ressource wurde entfernt. Die Störung ist behoben.",
	},
	"es": {
		keyOpenedTitle:         "%s está experimentando problemas",
		keyOpenedBody:          "Estamos investigando una incidencia que afecta a %s.",
		keyGroupOpenedTitle:    "Algunos servicios están experimentando problemas",
		keyResolvedTitle:       "%s se ha recuperado",
		keyResolvedBody:        "%s vuelve a funcionar con normalidad. La incidencia está resuelta.",
		keyMonitoringTitle:     "%s se ha recuperado",
		keyMonitoringBody:      "El componente afectado se ha recuperado. Estamos supervisando la situación.",
		keyRelapseTitle:        "%s vuelve a experimentar problemas",
		keyRelapseBody:         "%s está afectado de nuevo. Estamos investigando.",
		keyAlsoAffectingTitle:  "Esta incidencia también afecta a %s",
		keyAlsoAffectingBody:   "%s también está afectado por esta incidencia.",
		keyRemovedTitle:        "%s ya no se supervisa",
		keyRemovedGenericTitle: "El servicio afectado ya no se supervisa",
		keyRemovedBody:         "El recurso supervisado se ha eliminado. La incidencia está resuelta.",
	},
}

// defaultLanguage is the fallback for any page whose language is unset or not
// translated here. English, matching the subscriber-mail templates.
const defaultLanguage = i18n.LangEnglish

// templatesFor resolves a page language ("fr", "fr-FR", "" …) to a template
// set, falling back to English. Matching is on the primary subtag and is
// case-insensitive, so "FR-ca" resolves to French rather than silently
// dropping to English.
func templatesFor(language *string) templateSet {
	if language == nil {
		return publicTemplates[defaultLanguage]
	}

	if set, ok := publicTemplates[i18n.Normalize(*language)]; ok {
		return set
	}

	return publicTemplates[defaultLanguage]
}
