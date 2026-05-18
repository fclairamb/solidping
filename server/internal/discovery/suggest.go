package discovery

import "fmt"

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
			Type:   "ping",
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

// suggestForPort maps a port number to a suggested check type and configuration.
func suggestForPort(ip string, port int) *SuggestedCheck {
	switch port {
	case 80:
		return &SuggestedCheck{
			Type:   "http",
			Config: map[string]any{"url": fmt.Sprintf("http://%s", ip)},
		}
	case 443:
		return &SuggestedCheck{
			Type:   "http",
			Config: map[string]any{"url": fmt.Sprintf("https://%s", ip)},
		}
	case 22, 25, 53, 110, 143, 465, 587, 993, 995, 3306, 5432, 6379, 8080, 8443:
		return &SuggestedCheck{
			Type:   "tcp",
			Config: map[string]any{"host": ip, "port": port},
		}
	}

	return nil
}
