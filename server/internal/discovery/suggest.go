package discovery

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	// checkTypePing is the suggest-engine alias the urlparse layer uses; it is
	// normalized to "icmp" at promotion time.
	checkTypePing = "ping"
	checkTypeHTTP = "http"
	checkTypeTCP  = "tcp"
	// checkTypeICMP is the registered check type an ICMP suggestion maps to.
	checkTypeICMP = "icmp"
)

// schemeICMP is the scheme label used when building an ICMP check name/slug.
const schemeICMP = "icmp"

// nonSlugChars matches any run of characters that are not lowercase
// alphanumerics, for slug normalization.
var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// SuggestedCheck is one fully-formed suggested check within a group. A suggester
// emits these so promotion is "create these checks" rather than "pick from a
// host's suggestions". GroupKey/GroupLabel/Metadata are identical across every
// SuggestedCheck of the same group (the group is a render-time concern).
type SuggestedCheck struct {
	GroupKey   string          `json:"groupKey"`
	GroupLabel string          `json:"groupLabel"`
	Name       string          `json:"name"`
	Slug       string          `json:"slug"`
	Type       string          `json:"type"`
	Config     json.RawMessage `json:"config"`
	Metadata   json.RawMessage `json:"metadata,omitempty"` // group hints (denormalized)
}

// SuggestChecks returns suggested checks for one LAN host as grouped rows. Every
// row shares groupKey=ip, groupLabel=hostname||ip, and metadata={openPorts,
// icmpReachable}. An ICMP check is emitted when reachable; one check is emitted
// per open port that maps to a known scheme (via defaultPorts). Slugs are deduped
// within the group.
func SuggestChecks(ip, hostname string, icmpReachable bool, openPorts []int) []SuggestedCheck {
	groupLabel := hostname
	if groupLabel == "" {
		groupLabel = ip
	}

	meta := lanMetadata(openPorts, icmpReachable)

	seen := make(map[string]struct{})

	var suggestions []SuggestedCheck

	if icmpReachable {
		suggestions = append(suggestions, SuggestedCheck{
			GroupKey:   ip,
			GroupLabel: groupLabel,
			Name:       checkName(groupLabel, schemeICMP),
			Slug:       dedupSlug(seen, checkSlug(groupLabel, schemeICMP, 0)),
			Type:       checkTypePing,
			Config:     mustJSON(map[string]any{"host": ip}),
			Metadata:   meta,
		})
	}

	for _, port := range openPorts {
		sc := suggestForPort(ip, groupLabel, port, seen)
		if sc != nil {
			sc.Metadata = meta
			suggestions = append(suggestions, *sc)
		}
	}

	return suggestions
}

// lanMetadata builds the denormalized group-display metadata shared across a LAN
// host's suggested-check rows.
func lanMetadata(openPorts []int, icmpReachable bool) json.RawMessage {
	if openPorts == nil {
		openPorts = []int{}
	}

	return mustJSON(map[string]any{
		"openPorts":     openPorts,
		"icmpReachable": icmpReachable,
	})
}

// suggestForPort maps a port number to a suggested check, driven by the
// authoritative defaultPorts table (ports.go). A port absent from that table
// produces no suggestion.
func suggestForPort(ip, groupLabel string, port int, seen map[string]struct{}) *SuggestedCheck {
	for i := range defaultPorts {
		spec := defaultPorts[i]
		if spec.Port != port {
			continue
		}

		scheme := schemeForPort(spec, port)

		if spec.CheckType == checkTypeHTTP {
			return &SuggestedCheck{
				GroupKey:   ip,
				GroupLabel: groupLabel,
				Name:       checkName(groupLabel, scheme),
				Slug:       dedupSlug(seen, checkSlug(groupLabel, scheme, port)),
				Type:       checkTypeHTTP,
				Config:     mustJSON(map[string]any{"url": fmt.Sprintf(spec.URLTmpl, ip)}),
			}
		}

		return &SuggestedCheck{
			GroupKey:   ip,
			GroupLabel: groupLabel,
			Name:       fmt.Sprintf("%s/%d", checkName(groupLabel, scheme), port),
			Slug:       dedupSlug(seen, checkSlug(groupLabel, scheme, port)),
			Type:       checkTypeTCP,
			Config:     mustJSON(map[string]any{"host": ip, "port": port}),
		}
	}

	return nil
}

// schemeForPort returns the human scheme label for a port (e.g. "HTTP", "HTTPS",
// "TCP"). HTTP/HTTPS are distinguished by the URL template; everything else is a
// plain TCP port (the port number is carried separately, in the name suffix and
// slug).
func schemeForPort(spec portSpec, _ int) string {
	if spec.CheckType == checkTypeHTTP {
		if strings.HasPrefix(spec.URLTmpl, "https") {
			return "HTTPS"
		}

		return "HTTP"
	}

	return "TCP"
}

// checkName builds a human suggested-check name like "192.168.1.5 · HTTP".
func checkName(groupLabel, scheme string) string {
	return fmt.Sprintf("%s · %s", groupLabel, strings.ToUpper(scheme))
}

// checkSlug builds a URL-friendly slug like "http-192-168-1-5" or
// "tcp-192-168-1-5-22". The scheme leads so the slug always begins with a letter
// (the checks service requires `^[a-z][a-z0-9-]{2,49}$`) — group labels are often
// IPs, which start with a digit. A non-zero port is appended to disambiguate
// ports that would otherwise share a scheme.
func checkSlug(groupLabel, scheme string, port int) string {
	parts := []string{slugify(scheme), slugify(groupLabel)}
	if port != 0 {
		parts = append(parts, fmt.Sprintf("%d", port))
	}

	slug := strings.Trim(strings.Join(parts, "-"), "-")

	// Guard: a scheme that slugifies to empty (shouldn't happen) would leave a
	// leading digit. Prefix a stable letter to keep the slug valid.
	if slug == "" || (slug[0] >= '0' && slug[0] <= '9') {
		slug = "d-" + slug
	}

	// Cap at the checks-service maximum (50 chars) to keep promotion valid.
	const maxSlugLen = 50
	if len(slug) > maxSlugLen {
		slug = strings.Trim(slug[:maxSlugLen], "-")
	}

	return slug
}

// dedupSlug ensures slug uniqueness within a group, appending -2, -3… on
// collision. The chosen slug is recorded in seen.
func dedupSlug(seen map[string]struct{}, base string) string {
	candidate := base

	for i := 2; ; i++ {
		if _, taken := seen[candidate]; !taken {
			seen[candidate] = struct{}{}

			return candidate
		}

		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

// slugify lowercases and replaces non-alphanumeric runs with a single hyphen.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonSlugChars.ReplaceAllString(s, "-")

	return strings.Trim(s, "-")
}

// mustJSON marshals v to json.RawMessage, falling back to "{}" on the
// (practically impossible) marshal error of a map[string]any of scalars.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}

	return b
}
