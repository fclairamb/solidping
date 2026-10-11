package auth

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// freeMailDomains is the denylist of free webmail domains that must not be
// used as the sole post-@ component of an auto-join regex. Setting one of
// these as the org's registration_email_pattern would inhale every signup
// from that provider.
//
// Country variants of the providers in freeMailBrands (yahoo.fr, gmx.de,
// hotmail.co.uk) are matched by isFreeMailDomain and need no entry here. This
// list holds the providers that only exist under one name, mostly national
// ISPs and mailbox services.
//
// web/dash0/src/lib/quick-target-suggestion.ts mirrors this list and
// freeMailBrands verbatim. Keep both in sync.
var freeMailDomains = []string{ //nolint:gochecknoglobals
	"gmail.com", "googlemail.com", "yahoo.com", "outlook.com", "hotmail.com",
	"live.com", "msn.com", "icloud.com", "me.com", "mac.com", "protonmail.com",
	"proton.me", "pm.me", "aol.com", "gmx.com", "mail.com", "yandex.com",
	"tutanota.com", "tuta.io", "zoho.com", "qq.com", "163.com", "126.com",
	"yeah.net", "sina.com", "naver.com", "daum.net", "hanmail.net", "rediffmail.com",
	"fastmail.com", "fastmail.fm", "hey.com", "mailbox.org", "posteo.de", "posteo.net",
	// Germany, Austria, Switzerland
	"web.de", "t-online.de", "freenet.de", "arcor.de", "bluewin.ch",
	// France
	"orange.fr", "wanadoo.fr", "free.fr", "laposte.net", "sfr.fr", "neuf.fr", "bbox.fr",
	// Rest of Europe
	"libero.it", "virgilio.it", "tiscali.it", "telenet.be", "skynet.be", "ziggo.nl",
	"seznam.cz", "wp.pl", "o2.pl", "interia.pl", "onet.pl", "sapo.pt",
	"inbox.ru", "list.ru", "bk.ru", "rambler.ru", "ukr.net", "abv.bg",
	// UK, North America, Oceania, Brazil
	"btinternet.com", "sky.com", "virginmedia.com", "comcast.net", "verizon.net",
	"att.net", "sbcglobal.net", "shaw.ca", "rogers.com", "bigpond.com",
	"uol.com.br", "bol.com.br",
}

// freeMailBrands are webmail providers that run the same free service under
// many country domains. A domain made of one of these labels and a country
// suffix (yahoo.fr, gmx.net, hotmail.co.uk, mail.ru) is free webmail.
var freeMailBrands = []string{ //nolint:gochecknoglobals
	"gmail", "googlemail", "yahoo", "ymail", "rocketmail", "hotmail", "outlook",
	"live", "msn", "gmx", "aol", "icloud", "protonmail", "proton", "yandex",
	"mail", "tutanota", "zoho",
}

// countrySecondLevels are the labels that sit between a name and a ccTLD in
// a two-level public suffix, such as co.uk or com.br.
var countrySecondLevels = []string{"co", "com", "net", "org", "ne", "or"} //nolint:gochecknoglobals

// isFreeMailDomain reports whether domain belongs to a free webmail provider:
// an exact entry of freeMailDomains, or a freeMailBrands label followed by a
// single TLD (gmx.de) or a two-level country suffix (yahoo.co.uk). A brand
// label deeper in the name (mail.acme.com, live.team.acme.com) does not count.
func isFreeMailDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if slices.Contains(freeMailDomains, domain) {
		return true
	}

	labels := strings.Split(domain, ".")
	if !slices.Contains(freeMailBrands, labels[0]) {
		return false
	}

	switch len(labels) {
	case 2:
		return labels[1] != ""
	case 3:
		return slices.Contains(countrySecondLevels, labels[1]) && labels[2] != ""
	default:
		return false
	}
}

// permissivePostAtPatterns are post-@ substrings that effectively let the
// trailing domain be arbitrary. We refuse them outright.
var permissivePostAtPatterns = []string{ //nolint:gochecknoglobals
	".*", ".+", ".", "[^@]+", "[^@]*", `\S+`, `\S*`, `.+\..+`, `.*\..*`,
}

// permissivenessProbes are synthetic emails. A pattern that matches any of
// them is too broad to be safe — even if the static structural checks pass.
var permissivenessProbes = []string{ //nolint:gochecknoglobals
	"attacker@evil.example",
	"bob@gmail.com",
	"x@y.z",
}

// validateAutoJoinRegex rejects patterns that would match too broadly. An
// empty string disables the feature and is allowed.
func validateAutoJoinRegex(pattern string) error {
	if pattern == "" {
		return nil
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("%w: cannot compile (%s)", ErrInvalidAutoJoinRegex, err.Error())
	}

	if !strings.Contains(pattern, "@") {
		return fmt.Errorf("%w: must include an explicit @ separator", ErrInvalidAutoJoinRegex)
	}

	postAt := postAtSubpattern(pattern)
	postAtTrimmed := strings.Trim(postAt, "()$ ")
	for _, bad := range permissivePostAtPatterns {
		if postAtTrimmed == bad {
			return fmt.Errorf(
				"%w: domain part %q is too permissive",
				ErrInvalidAutoJoinRegex, postAt,
			)
		}
	}

	postAtLower := strings.ToLower(unescapeRegexLiteral(postAtTrimmed))
	if isFreeMailDomain(postAtLower) {
		return fmt.Errorf(
			"%w: %q is a free webmail provider — claiming it is unsafe",
			ErrInvalidAutoJoinRegex, postAtLower,
		)
	}

	for _, probe := range permissivenessProbes {
		if compiled.MatchString(probe) {
			return fmt.Errorf(
				"%w: pattern matches generic probe %q (too permissive)",
				ErrInvalidAutoJoinRegex, probe,
			)
		}
	}

	return nil
}

// postAtSubpattern extracts the substring that follows the literal @ in a
// pattern. Returns "" if no literal @ is present.
func postAtSubpattern(pattern string) string {
	idx := strings.Index(pattern, "@")
	if idx < 0 {
		return ""
	}
	return pattern[idx+1:]
}

// unescapeRegexLiteral collapses common regex escapes back into the literal
// they represent (e.g. `\.` → `.`). Used purely for the free-webmail equality
// check; not a full regex parser.
func unescapeRegexLiteral(s string) string {
	out := strings.ReplaceAll(s, `\.`, ".")
	out = strings.ReplaceAll(out, `\-`, "-")
	return out
}
