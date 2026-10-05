package formats

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func ietfStatus(raw string) ComponentStatus {
	switch normalize(raw) {
	case "pass", wordOK, wordUp:
		return StatusOK
	case wordWarn:
		return StatusWarning
	case "fail", wordDown:
		return StatusFailed
	default:
		return StatusUnknown
	}
}

// ietfFormat is draft-inadarei-api-health-check: `Content-Type:
// application/health+json`, or a `checks` object whose values are arrays.
func ietfFormat() Format {
	return Format{
		Name: NameIETF,
		Detect: func(contentType string, body map[string]any) bool {
			if strings.Contains(strings.ToLower(contentType), "application/health+json") {
				return true
			}

			return ietfChecks(body)
		},
		Parse: parseIETF,
	}
}

// ietfChecks reports whether body has a non-empty `checks` object of arrays.
func ietfChecks(body map[string]any) bool {
	checks := asMap(body["checks"])
	if len(checks) == 0 {
		return false
	}

	for _, value := range checks {
		if _, ok := value.([]any); !ok {
			return false
		}
	}

	return true
}

func parseIETF(body map[string]any) (Report, error) {
	_, hasStatus := body["status"].(string)
	checks := asMap(body["checks"])

	if !hasStatus && checks == nil {
		return Report{}, ErrNotRecognised
	}

	report := Report{
		Format:  NameIETF,
		Overall: ietfStatus(asString(body["status"])),
	}

	var latest *time.Time

	for _, key := range sortedKeys(checks) {
		entries := asSlice(checks[key])

		for index, item := range entries {
			entry := asMap(item)
			if entry == nil {
				continue
			}

			name := key
			if len(entries) > 1 {
				suffix := asString(entry["componentId"])
				if suffix == "" {
					suffix = strconv.Itoa(index)
				}

				name = key + "#" + suffix
			}

			report.Components = append(report.Components, Component{
				Name:    name,
				Label:   key,
				Message: asString(entry["output"]),
				Summary: ietfSummary(entry),
				Status:  ietfStatus(asString(entry["status"])),
				Meta:    primitiveMeta(entry),
			})

			if at := parseTime(entry["time"]); at != nil && (latest == nil || at.After(*latest)) {
				latest = at
			}
		}
	}

	report.FinishedAt = parseTime(body["time"])
	if report.FinishedAt == nil {
		report.FinishedAt = latest
	}

	return report, nil
}

// ietfSummary renders observedValue + observedUnit.
func ietfSummary(entry map[string]any) string {
	value, ok := entry["observedValue"]
	if !ok || value == nil {
		return ""
	}

	text := fmt.Sprint(value)
	if unit := asString(entry["observedUnit"]); unit != "" {
		text += " " + unit
	}

	return text
}
