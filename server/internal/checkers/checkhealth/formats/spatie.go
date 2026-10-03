package formats

func spatieStatus(raw string) ComponentStatus {
	switch normalize(raw) {
	case wordOK:
		return StatusOK
	case "warning":
		return StatusWarning
	case "failed", "crashed":
		return StatusFailed
	case "skipped":
		return StatusSkipped
	default:
		return StatusUnknown
	}
}

// spatieFormat is the spatie health format produced by spatie/laravel-health
//: a `checkResults` array.
func spatieFormat() Format {
	return Format{
		Name: NameSpatie,
		Detect: func(_ string, body map[string]any) bool {
			_, ok := body["checkResults"].([]any)

			return ok
		},
		Parse: parseSpatie,
	}
}

func parseSpatie(body map[string]any) (Report, error) {
	results, ok := body["checkResults"].([]any)
	if !ok {
		return Report{}, ErrNotRecognised
	}

	report := Report{Format: NameSpatie, FinishedAt: parseTime(body["finishedAt"])}

	for _, item := range results {
		entry := asMap(item)
		if entry == nil {
			continue
		}

		name := asString(entry["name"])
		label := asString(entry["label"])

		if name == "" {
			name = label
		}

		report.Components = append(report.Components, Component{
			Name:    name,
			Label:   label,
			Message: asString(entry["notificationMessage"]),
			Summary: asString(entry["shortSummary"]),
			Status:  spatieStatus(asString(entry["status"])),
			Meta:    primitiveMeta(asMap(entry["meta"])),
		})
	}

	return report, nil
}
