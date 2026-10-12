package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrFinalURLInvalid is returned by NormalizeFinalURL for a value that is not
// an absolute http(s) URL.
var ErrFinalURLInvalid = errors.New("must be an absolute http or https URL")

// NormalizeFinalURL canonicalises a URL for the `expectedFinalUrl` comparison.
// It is applied to both the configured value and the URL the last request was
// sent to:
//   - an absolute http or https URL is required;
//   - scheme and host are lowercased;
//   - the default port (:80 for http, :443 for https) is dropped, any other
//     port is kept;
//   - an empty path becomes "/";
//   - the fragment is dropped (it is never sent on the wire);
//   - path and query are kept exactly as sent (no case folding, no parameter
//     reordering, "/x" and "/x/" differ).
func NormalizeFinalURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrFinalURLInvalid, err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Hostname() == "" {
		return "", ErrFinalURLInvalid
	}

	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}

	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}

	if port != "" {
		host += ":" + port
	}

	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}

	out := scheme + "://" + host + path
	if parsed.ForceQuery || parsed.RawQuery != "" {
		out += "?" + parsed.RawQuery
	}

	return out, nil
}
