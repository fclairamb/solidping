package email

// Branding view-model keys. base.html reads these three (through the
// nil-tolerant `field` helper — see formatter.go) to decide what the header
// wears. They are constants because they are referenced from a dozen call
// sites across handlers, jobs and notifiers, and a typo in one of them is a
// silently unbranded email rather than a compile error.
const (
	// KeyOrgName is the organization's display name. Also drives the existing
	// footer attribution ("<org> — sent by SolidPing").
	KeyOrgName = "OrgName"
	// KeyBrandName is the alt text / wordmark shown for the primary logo. It is
	// the org name for operator mail and the status PAGE name for subscriber
	// mail, which is why it is a key of its own rather than reusing OrgName.
	KeyBrandName = "BrandName"
	// KeyOrgLogoURL is the primary logo, stored either as an external https URL
	// or as a site-relative "/pub/assets/<uid>" path. The formatter's absURL
	// helper makes it absolute.
	KeyOrgLogoURL = "OrgLogoURL"
	// KeyHideBranding is the white-label opt-in: no logo, and no SolidPing
	// attribution, anywhere in the message.
	KeyHideBranding = "HideBranding"
)

// ApplyOrgBranding stamps an organization's branding onto an email view model.
//
// Empty values are written as empty strings rather than omitted: base.html
// treats "" as "no org logo" and falls back to the SolidPing logo, so a
// partially-branded org degrades instead of rendering a broken image.
//
// orgName falls back to orgSlug when blank. An org with an empty `name`
// column (observed on the long-lived production `default` org) would
// otherwise leave a hole in every templated sentence that reads OrgName —
// "has asked to join **on** SolidPing", "you're an admin of **.**" — and in
// the subject line. Every org has a non-empty slug, so this is a strict
// improvement over rendering nothing.
func ApplyOrgBranding(viewModel map[string]any, orgName, orgSlug string, logoURL *string) {
	if viewModel == nil {
		return
	}

	name := orgName
	if name == "" {
		name = orgSlug
	}

	viewModel[KeyOrgName] = name
	viewModel[KeyBrandName] = name
	viewModel[KeyOrgLogoURL] = derefString(logoURL)
}

// ApplyStatusPageBranding stamps a STATUS PAGE's branding onto an email view
// model — used by the status-subscriber templates only.
//
// A subscriber opted into a status page, not into the organization behind it,
// so org branding must never leak into these emails: this deliberately sets
// no OrgName, which is also what keeps the "<org> — sent by SolidPing" footer
// line out of subscriber mail. When the page has hide_branding set, no logo
// and no SolidPing attribution is rendered at all.
func ApplyStatusPageBranding(viewModel map[string]any, pageName string, logoURL *string, hideBranding bool) {
	if viewModel == nil {
		return
	}

	viewModel[KeyBrandName] = pageName
	viewModel[KeyHideBranding] = hideBranding

	if hideBranding {
		viewModel[KeyOrgLogoURL] = ""

		return
	}

	viewModel[KeyOrgLogoURL] = derefString(logoURL)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}
