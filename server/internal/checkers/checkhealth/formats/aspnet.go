package formats

var aspnetStatuses = map[string]ComponentStatus{
	"healthy":   StatusOK,
	"degraded":  StatusWarning,
	"unhealthy": StatusFailed,
}

// aspnetFormat is the ASP.NET Core HealthChecks UI response writer: an
// `entries` object.
var aspnetFormat = Format{
	Name: NameASPNet,
	Detect: func(_ string, body map[string]any) bool {
		return asMap(body["entries"]) != nil
	},
	Parse: parseASPNet,
}

func parseASPNet(body map[string]any) (Report, error) {
	entries := asMap(body["entries"])
	if entries == nil {
		return Report{}, ErrNotRecognised
	}

	report := Report{
		Format:  NameASPNet,
		Overall: mapStatus(asString(body["status"]), aspnetStatuses),
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
			Status:  mapStatus(asString(entry["status"]), aspnetStatuses),
			Meta:    primitiveMeta(asMap(entry["data"])),
		})
	}

	return report, nil
}
