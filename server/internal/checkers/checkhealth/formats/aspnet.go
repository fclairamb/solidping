package formats

func aspnetStatus(raw string) ComponentStatus {
	switch normalize(raw) {
	case "healthy":
		return StatusOK
	case "degraded":
		return StatusWarning
	case "unhealthy":
		return StatusFailed
	default:
		return StatusUnknown
	}
}

// aspnetFormat is the ASP.NET Core HealthChecks UI response writer: an
// `entries` object.
func aspnetFormat() Format {
	return Format{
		Name: NameASPNet,
		Detect: func(_ string, body map[string]any) bool {
			return asMap(body["entries"]) != nil
		},
		Parse: parseASPNet,
	}
}

func parseASPNet(body map[string]any) (Report, error) {
	entries := asMap(body["entries"])
	if entries == nil {
		return Report{}, ErrNotRecognised
	}

	report := Report{
		Format:  NameASPNet,
		Overall: aspnetStatus(asString(body["status"])),
	}

	for _, key := range sortedKeys(entries) {
		entry := asMap(entries[key])
		if entry == nil {
			continue
		}

		message := asString(entry["description"])
		if message == "" {
			message = asString(entry["exception"])
		}

		report.Components = append(report.Components, Component{
			Name:    key,
			Label:   key,
			Message: message,
			Status:  aspnetStatus(asString(entry["status"])),
			Meta:    primitiveMeta(asMap(entry["data"])),
		})
	}

	return report, nil
}
