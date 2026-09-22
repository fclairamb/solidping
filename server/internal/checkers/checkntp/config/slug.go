package config

// sanitizeSlug turns a host into a slug-friendly fragment.
func sanitizeSlug(host string) string {
	out := make([]rune, 0, len(host))

	for _, r := range host {
		switch r {
		case '.', ':':
			out = append(out, '-')
		default:
			out = append(out, r)
		}
	}

	return string(out)
}
