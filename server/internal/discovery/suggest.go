package discovery

import "fmt"

const (
	checkTypePing = "ping"
	checkTypeHTTP = "http"
	checkTypeTCP  = "tcp"
)

// SuggestedCheck is a suggested check type and config for a discovered host.
type SuggestedCheck struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// SuggestChecks returns a list of suggested checks for a host based on its open ports and ICMP reachability.
func SuggestChecks(ip string, icmpReachable bool, openPorts []int) []SuggestedCheck {
	var suggestions []SuggestedCheck

	if icmpReachable {
		suggestions = append(suggestions, SuggestedCheck{
			Type:   checkTypePing,
			Config: map[string]any{"host": ip},
		})
	}

	for _, port := range openPorts {
		s := suggestForPort(ip, port)
		if s != nil {
			suggestions = append(suggestions, *s)
		}
	}

	return suggestions
}

// suggestForPort maps a port number to a suggested check type and
// configuration, driven by the authoritative defaultPorts table (ports.go).
// A port absent from that table produces no suggestion.
func suggestForPort(ip string, port int) *SuggestedCheck {
	for i := range defaultPorts {
		spec := defaultPorts[i]
		if spec.Port != port {
			continue
		}

		if spec.CheckType == checkTypeHTTP {
			return &SuggestedCheck{
				Type:   checkTypeHTTP,
				Config: map[string]any{"url": fmt.Sprintf(spec.URLTmpl, ip)},
			}
		}

		return &SuggestedCheck{
			Type:   checkTypeTCP,
			Config: map[string]any{"host": ip, "port": port},
		}
	}

	return nil
}
