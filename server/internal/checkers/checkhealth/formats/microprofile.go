package formats

func microprofileStatus(raw string) ComponentStatus {
	switch normalize(raw) {
	case wordUp:
		return StatusOK
	case wordDown:
		return StatusFailed
	default:
		return StatusUnknown
	}
}

// microprofileFormat is MicroProfile Health (Quarkus, Open Liberty): a
// `checks` array of {name, status}.
func microprofileFormat() Format {
	return Format{
		Name: NameMicroProfile,
		Detect: func(_ string, body map[string]any) bool {
			_, ok := body["checks"].([]any)

			return ok
		},
		Parse: parseMicroProfile,
	}
}

func parseMicroProfile(body map[string]any) (Report, error) {
	checks, ok := body["checks"].([]any)
	if !ok {
		return Report{}, ErrNotRecognised
	}

	report := Report{
		Format:  NameMicroProfile,
		Overall: microprofileStatus(asString(body["status"])),
	}

	for _, item := range checks {
		entry := asMap(item)
		if entry == nil {
			continue
		}

		data := asMap(entry["data"])

		report.Components = append(report.Components, Component{
			Name:    asString(entry["name"]),
			Label:   asString(entry["name"]),
			Message: asString(data["error"]),
			Status:  microprofileStatus(asString(entry["status"])),
			Meta:    primitiveMeta(data),
		})
	}

	return report, nil
}
