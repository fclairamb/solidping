package auth

import (
	"fmt"
	"regexp"
	"strings"
)

// freeMailDomains is the denylist of free webmail domains that must not be
// used as the sole post-@ component of an auto-join regex. Setting one of
// these as the org's registration_email_pattern would inhale every signup
// from that provider.
var freeMailDomains = []string{ //nolint:gochecknoglobals
	"gmail.com", "googlemail.com", "yahoo.com", "outlook.com", "hotmail.com",
	"live.com", "msn.com", "icloud.com", "me.com", "protonmail.com",
	"proton.me", "aol.com", "gmx.com", "mail.com", "yandex.com",
	"tutanota.com", "zoho.com", "qq.com", "163.com", "126.com",
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
	for _, free := range freeMailDomains {
		if postAtLower == free {
			return fmt.Errorf(
				"%w: %q is a free webmail provider — claiming it is unsafe",
				ErrInvalidAutoJoinRegex, free,
			)
		}
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
