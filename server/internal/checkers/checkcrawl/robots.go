package checkcrawl

import (
	"bufio"
	"bytes"
	"strings"
)

// robotsRule is one Allow / Disallow line of the group that applies to us.
type robotsRule struct {
	Pattern string `json:"p"`
	Allow   bool   `json:"a,omitempty"`
}

// parsedRobots is what a robots.txt says to the crawler.
type parsedRobots struct {
	rules    []robotsRule
	sitemaps []string
}

// parseRobots reads a robots.txt. Rules come from the group naming our agent
// token when there is one, else from the `*` group. Sitemap lines are global.
func parseRobots(body []byte) parsedRobots {
	var (
		out         parsedRobots
		ourRules    []robotsRule
		starRules   []robotsRule
		groupAgents []string
		inRules     bool
		haveOurs    bool
	)

	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		key, value, ok := robotsLine(scanner.Text())
		if !ok {
			continue
		}

		switch key {
		case "sitemap":
			out.sitemaps = append(out.sitemaps, value)
		case "user-agent":
			if inRules {
				groupAgents = nil
				inRules = false
			}

			groupAgents = append(groupAgents, strings.ToLower(value))
		case "allow", "disallow":
			inRules = true

			if value == "" {
				continue
			}

			rule := robotsRule{Pattern: value, Allow: key == "allow"}

			for _, agent := range groupAgents {
				switch {
				case strings.Contains(agent, robotsAgentToken):
					ourRules = append(ourRules, rule)
					haveOurs = true
				case agent == "*":
					starRules = append(starRules, rule)
				}
			}
		}
	}

	out.rules = starRules
	if haveOurs {
		out.rules = ourRules
	}

	return out
}

func robotsLine(line string) (string, string, bool) {
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		line = line[:idx]
	}

	key, value, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}

	return strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value), true
}

// robotsAllowed applies the longest-match rule (Allow wins a tie) to path.
func robotsAllowed(rules []robotsRule, path string) bool {
	bestLen := -1
	allowed := true

	for _, rule := range rules {
		if !robotsMatch(rule.Pattern, path) {
			continue
		}

		length := len(rule.Pattern)
		if length > bestLen || (length == bestLen && rule.Allow) {
			bestLen = length
			allowed = rule.Allow
		}
	}

	return allowed
}

// robotsMatch matches a robots pattern (`*` wildcard, `$` end anchor)
// against the start of path.
func robotsMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")

	return globMatch(pattern, path, anchored)
}

// globMatch reports whether path starts with pattern, where `*` in pattern
// matches any run of characters. With anchored, the whole path must match.
func globMatch(pattern, path string, anchored bool) bool {
	parts := strings.Split(pattern, "*")
	pos := 0

	for i, part := range parts {
		switch i {
		case 0:
			if !strings.HasPrefix(path, part) {
				return false
			}

			pos = len(part)
		default:
			idx := strings.Index(path[pos:], part)
			if idx < 0 {
				return false
			}

			pos += idx + len(part)
		}
	}

	if !anchored {
		return true
	}

	return pos == len(path) || (len(parts) > 1 && parts[len(parts)-1] == "")
}
