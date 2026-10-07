package formats

func springStatus(raw string) ComponentStatus {
	switch normalize(raw) {
	case wordUp:
		return StatusOK
	case wordDown, "out_of_service":
		return StatusFailed
	default:
		return StatusUnknown
	}
}

// springFormat is Spring Boot Actuator's /actuator/health: a top-level
// `status` plus a `components` object (or the legacy `details` object).
func springFormat() Format {
	return Format{
		Name: NameSpring,
		Detect: func(_ string, body map[string]any) bool {
			return springShape(body)
		},
		Parse: parseSpring,
	}
}

func springShape(body map[string]any) bool {
	if _, ok := body["status"].(string); !ok {
		return false
	}

	if _, hasChecks := body["checks"]; hasChecks {
		return false
	}

	return asMap(body["components"]) != nil || asMap(body["details"]) != nil
}

func parseSpring(body map[string]any) (Report, error) {
	status, ok := body["status"].(string)
	if !ok {
		return Report{}, ErrNotRecognised
	}

	group := asMap(body["components"])
	if group == nil {
		group = asMap(body["details"])
	}

	report := Report{Format: NameSpring, Overall: springStatus(status)}
	flattenSpring(&report, "", group)

	return report, nil
}

// flattenSpring appends one component per leaf indicator, naming nested ones
// parent.child. A group is not a component itself: its status is the roll-up
// of its children, which are already listed.
func flattenSpring(report *Report, prefix string, group map[string]any) {
	for _, key := range sortedKeys(group) {
		entry := asMap(group[key])
		if entry == nil {
			continue
		}

		name := key
		if prefix != "" {
			name = prefix + "." + key
		}

		if nested := asMap(entry["components"]); nested != nil {
			flattenSpring(report, name, nested)

			continue
		}

		details := asMap(entry["details"])
		report.Components = append(report.Components, Component{
			Name:    name,
			Label:   key,
			Message: asString(details["error"]),
			Status:  springStatus(asString(entry["status"])),
			Meta:    primitiveMeta(details),
		})
	}
}
