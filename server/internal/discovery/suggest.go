package discovery

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

const (
	// checkTypePing is the suggest-engine alias the urlparse layer uses; it is
	// normalized to "icmp" at promotion time.
	checkTypePing = "ping"
	checkTypeHTTP = "http"
	checkTypeTCP  = "tcp"
	// checkTypeDocker is the promoted check type for a discovered container; it
	// passes through normalizeCheckType unchanged.
	checkTypeDocker = "docker"
)

// schemeICMP is the scheme label used when building an ICMP check name/slug.
const schemeICMP = "icmp"

// Config keys shared across suggested-check builders.
const (
	cfgKeyHost = "host"
	cfgKeyURL  = "url"
	cfgKeyPort = "port"
)

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
			Config:     mustJSON(map[string]any{cfgKeyHost: ip}),
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
				Config:     mustJSON(map[string]any{cfgKeyURL: fmt.Sprintf(spec.URLTmpl, ip)}),
			}
		}

		return &SuggestedCheck{
			GroupKey:   ip,
			GroupLabel: groupLabel,
			Name:       fmt.Sprintf("%s/%d", checkName(groupLabel, scheme), port),
			Slug:       dedupSlug(seen, checkSlug(groupLabel, scheme, port)),
			Type:       checkTypeTCP,
			Config:     mustJSON(map[string]any{cfgKeyHost: ip, cfgKeyPort: port}),
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
		parts = append(parts, strconv.Itoa(port))
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

// ContainerPort is one port mapping of a discovered container, mirroring the
// Docker SDK's container.Port. Only ports with PublicPort != 0 (published to the
// host) are worker-reachable and become HTTP/TCP suggestions.
type ContainerPort struct {
	PrivatePort uint16 // container-side port (decides the scheme)
	PublicPort  uint16 // host-published port (what a worker connects to); 0 = not published
	Type        string // "tcp" | "udp"
}

// ContainerSuggestInput carries the fields a container contributes to its
// suggested-check group.
type ContainerSuggestInput struct {
	ContainerID  string // group_key; the stable upsert identity
	Name         string // group_label; the container name (no leading slash)
	Image        string // metadata
	State        string // metadata (e.g. "running")
	HealthStatus string // metadata (from the status string, may be empty)
	DockerHost   string // the endpoint; the promoted docker check needs it
	HostAddress  string // host IP/hostname a worker reaches published ports on
	Ports        []ContainerPort
}

// schemeDocker/schemeHTTP/schemeHTTPS/schemeTCP are the scheme labels used when
// building suggested-check names/slugs for a container.
const (
	schemeDocker = "docker"
	schemeHTTP   = "HTTP"
	schemeHTTPS  = "HTTPS"
	schemeTCP    = "TCP"
)

// SuggestContainerChecks returns the grouped suggested checks for one discovered
// container. Every row shares group_key=containerID, group_label=name, and
// metadata={image, state, healthStatus, dockerHost}. A `docker` check is always
// emitted (the HEALTHCHECK mirror); each PUBLISHED port additionally yields an
// http/https/tcp check on the host-published port. Slugs are deduped within the
// group.
func SuggestContainerChecks(input *ContainerSuggestInput) []SuggestedCheck {
	groupLabel := input.Name
	if groupLabel == "" {
		groupLabel = input.ContainerID
	}

	meta := containerMetadata(input)
	seen := make(map[string]struct{})

	var suggestions []SuggestedCheck

	// Primary: the docker check. Always emitted — it covers containers that
	// publish no ports (the "internal service behind a reverse proxy" case).
	dockerCfg := map[string]any{"containerName": input.Name}
	if input.DockerHost != "" {
		dockerCfg[cfgKeyHost] = input.DockerHost
	}

	suggestions = append(suggestions, SuggestedCheck{
		GroupKey:   input.ContainerID,
		GroupLabel: groupLabel,
		Name:       checkName(groupLabel, schemeDocker),
		Slug:       dedupSlug(seen, checkSlug(groupLabel, schemeDocker, 0)),
		Type:       checkTypeDocker,
		Config:     mustJSON(dockerCfg),
		Metadata:   meta,
	})

	// Secondary: one check per published port, reachable on the host address.
	host := input.HostAddress
	if host == "" {
		host = dockerHostAddress(input.DockerHost)
	}

	for i := range input.Ports {
		sc := suggestForContainerPort(input.ContainerID, groupLabel, host, input.Ports[i], seen)
		if sc != nil {
			sc.Metadata = meta
			suggestions = append(suggestions, *sc)
		}
	}

	return suggestions
}

// containerMetadata builds the denormalized group-display metadata shared across
// a container's suggested-check rows.
func containerMetadata(input *ContainerSuggestInput) json.RawMessage {
	return mustJSON(map[string]any{
		"image":        input.Image,
		"state":        input.State,
		"healthStatus": input.HealthStatus,
		"dockerHost":   input.DockerHost,
	})
}

// suggestForContainerPort maps one PUBLISHED container port to a suggested check
// on the host-published port. The scheme is decided by the container-side port:
// 80/8080 → http, 443/8443 → https, anything else → tcp. Unpublished ports
// (PublicPort == 0) yield no suggestion — they are not worker-reachable.
func suggestForContainerPort(
	containerID, groupLabel, host string, port ContainerPort, seen map[string]struct{},
) *SuggestedCheck {
	if port.PublicPort == 0 {
		return nil
	}

	public := int(port.PublicPort)
	hostPort := net.JoinHostPort(host, strconv.Itoa(public))

	switch port.PrivatePort {
	case 80, 8080:
		return &SuggestedCheck{
			GroupKey:   containerID,
			GroupLabel: groupLabel,
			Name:       checkName(groupLabel, schemeHTTP),
			Slug:       dedupSlug(seen, checkSlug(groupLabel, schemeHTTP, public)),
			Type:       checkTypeHTTP,
			Config:     mustJSON(map[string]any{cfgKeyURL: "http://" + hostPort}),
		}
	case 443, 8443:
		return &SuggestedCheck{
			GroupKey:   containerID,
			GroupLabel: groupLabel,
			Name:       checkName(groupLabel, schemeHTTPS),
			Slug:       dedupSlug(seen, checkSlug(groupLabel, schemeHTTPS, public)),
			Type:       checkTypeHTTP,
			Config:     mustJSON(map[string]any{cfgKeyURL: "https://" + hostPort}),
		}
	default:
		return &SuggestedCheck{
			GroupKey:   containerID,
			GroupLabel: groupLabel,
			Name:       fmt.Sprintf("%s/%d", checkName(groupLabel, schemeTCP), public),
			Slug:       dedupSlug(seen, checkSlug(groupLabel, schemeTCP, public)),
			Type:       checkTypeTCP,
			Config:     mustJSON(map[string]any{cfgKeyHost: host, cfgKeyPort: public}),
		}
	}
}

// dockerHostAddress extracts a worker-reachable host from a Docker endpoint. For
// a tcp:// endpoint it returns the host part; for unix:// (and anything else) it
// returns localhost — a published port on a local socket is reachable on the
// loopback of the worker that shares it.
func dockerHostAddress(endpoint string) string {
	const tcpPrefix = "tcp://"
	if strings.HasPrefix(endpoint, tcpPrefix) {
		hostPort := strings.TrimPrefix(endpoint, tcpPrefix)
		if host, _, err := net.SplitHostPort(hostPort); err == nil && host != "" {
			return host
		}

		if hostPort != "" {
			return hostPort
		}
	}

	return "127.0.0.1"
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
