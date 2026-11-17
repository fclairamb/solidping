package models

// LocalizedText is the recipe a generated sentence was built from: a stable
// message key plus the values it interpolates. It is stored next to the
// rendered English (or page-language) text so a reader in another language can
// render the same sentence in theirs, instead of the literal sentence frozen at
// creation time.
//
// The literal column stays the source of truth for every channel that cannot
// translate (mails already sent, webhooks, feeds, older clients). A row whose
// text a person edited has no recipe (nil): their words are shown as written.
//
// Keys are namespaced by producer ("publication.openedTitle",
// "incident.title.down"). Clients that do not know a key fall back to the
// literal text, so adding a key never breaks an older dashboard.
type LocalizedText struct {
	// Key is the message key, e.g. "incident.title.down".
	Key string `json:"key"`
	// Params are the interpolated values. nil when the sentence has none.
	Params *LocalizedParams `json:"params,omitempty"`
}

// LocalizedParams are the values a LocalizedText interpolates. A closed,
// typed set on purpose: these recipes reach public status pages, and a free
// map would be one careless producer away from carrying probe output there.
// Add a field when a new message needs one.
type LocalizedParams struct {
	// Name is the subject: a check slug or name, an SLO name, or the public
	// display name of a status-page resource.
	Name string `json:"name,omitempty"`
	// Failures / Probes / Slow count probes in a degraded-incident window.
	Failures *int `json:"failures,omitempty"`
	Probes   *int `json:"probes,omitempty"`
	Slow     *int `json:"slow,omitempty"`
	// ThresholdMs is the slow-probe threshold in milliseconds.
	ThresholdMs *int `json:"thresholdMs,omitempty"`
	// Availability is a percentage, rounded to one decimal.
	Availability *float64 `json:"availability,omitempty"`
	// Rate is an SLO burn rate, rounded to one decimal.
	Rate *float64 `json:"rate,omitempty"`
}

// NewLocalizedText builds a recipe. params may be nil.
func NewLocalizedText(key string, params *LocalizedParams) *LocalizedText {
	return &LocalizedText{Key: key, Params: params}
}
