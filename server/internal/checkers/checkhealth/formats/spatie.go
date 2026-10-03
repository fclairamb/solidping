package formats

var spatieStatuses = map[string]ComponentStatus{
	"ok":      StatusOK,
	"warning": StatusWarning,
	"failed":  StatusFailed,
	"crashed": StatusFailed,
	"skipped": StatusSkipped,
}

// spatieFormat is the Oh Dear health format produced by spatie/laravel-health
// and ohdearapp/health-check-results: a `checkResults` array.
var spatieFormat = Format{
	Name: NameSpatie,
	Detect: func(_ string, body map[string]any) bool {
		_, ok := body["checkResults"].([]any)

		return ok
	},
	Parse: parseSpatie,
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
			Status:  mapStatus(asString(entry["status"]), spatieStatuses),
			Meta:    primitiveMeta(asMap(entry["meta"])),
		})
	}

	return report, nil
}
