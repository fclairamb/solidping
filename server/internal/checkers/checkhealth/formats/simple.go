package formats

// simpleFormat is the fallback: a top-level `status` string and nothing else.
func simpleFormat() Format {
	return Format{
		Name: NameSimple,
		Detect: func(_ string, body map[string]any) bool {
			_, ok := body["status"].(string)

			return ok
		},
		Parse: parseSimple,
	}
}

func parseSimple(body map[string]any) (Report, error) {
	status, ok := body["status"].(string)
	if !ok {
		return Report{}, ErrNotRecognised
	}

	return Report{Format: NameSimple, Overall: simpleStatus(status)}, nil
}

// simpleStatus maps a free-form status word: the known good words are ok, the
// known degraded words are warning, anything else is failed.
func simpleStatus(raw string) ComponentStatus {
	switch normalize(raw) {
	case wordOK, wordUp, "pass", "healthy":
		return StatusOK
	case wordWarn, "degraded":
		return StatusWarning
	default:
		return StatusFailed
	}
}
