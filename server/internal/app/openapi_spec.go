package app

import (
	"embed"
	"net/http"
	"strings"
)

// openAPIServersKey is the top-level YAML key whose block this file rewrites.
const openAPIServersKey = "servers:"

// serveOpenAPISpec serves the embedded OpenAPI document with its static
// `servers:` block replaced by the origin the request actually arrived on.
//
// The explorer at /openapi already overrides the list client-side with
// window.location.origin, but the raw spec is also consumed directly — by code
// generators, Postman, `curl | yq`, agents reading /openapi.yaml — and those
// clients have no such hook. Serving them a hardcoded https://solidping.io
// means a spec fetched from a self-hosted instance, from a custom domain, or
// from localhost points every generated client at the wrong host.
//
// The embedded file itself is untouched: the docs site generates its API
// reference from it at build time and should keep advertising the cloud server,
// which is the right answer for a document read outside any instance.
func (s *Server) serveOpenAPISpec(files embed.FS, fileName string) func(http.ResponseWriter, *http.Request) error {
	return func(writer http.ResponseWriter, req *http.Request) error {
		fileData, err := files.ReadFile(fileName)
		if err != nil {
			http.Error(writer, "File not found", http.StatusNotFound)

			return err
		}

		// Set explicitly rather than via mime.TypeByExtension: that helper takes
		// an EXTENSION, and serveFile hands it a whole path ("openapi/x.yaml"),
		// so it returns "" and the spec goes out untyped. RFC 9512 registers
		// application/yaml.
		writer.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		// The body varies with the origin the client used, and X-Forwarded-Proto
		// is the one request header that can change it (requestScheme). Without
		// this a shared cache could hand an http:// spec to an https:// client.
		writer.Header().Set("Vary", "X-Forwarded-Proto")
		writer.WriteHeader(http.StatusOK)

		if _, err := writer.Write(rewriteOpenAPIServers(fileData, req)); err != nil {
			return err
		}

		return nil
	}
}

// rewriteOpenAPIServers returns the spec with its `servers:` block rebuilt
// around the request's own origin, falling back to the document unchanged
// whenever it cannot do so safely.
func rewriteOpenAPIServers(spec []byte, req *http.Request) []byte {
	origin := requestOrigin(req)

	// A Host we cannot vouch for is never spliced into the document. The header
	// is client-controlled, and a value carrying a newline (or a quote) would
	// let a caller inject arbitrary YAML into a document other tools consume.
	// Falling back to the embedded list is always safe — it is what every
	// caller got before this function existed.
	if !isSafeOriginForYAML(origin) {
		return spec
	}

	lines := strings.Split(string(spec), "\n")

	start := -1

	for i, line := range lines {
		if line == openAPIServersKey {
			start = i

			break
		}
	}

	if start == -1 {
		return spec
	}

	// The block runs until the next top-level key: the first non-empty line
	// that carries no indentation. Blank lines inside the span are kept as a
	// trailing separator so the rewritten document keeps the original spacing.
	end := len(lines)

	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			end = i

			break
		}
	}

	blanks := 0

	for i := end - 1; i > start && lines[i] == ""; i-- {
		blanks++
	}

	rebuilt := make([]string, 0, len(lines))
	rebuilt = append(rebuilt, lines[:start+1]...)
	rebuilt = append(rebuilt,
		"  - url: "+origin,
		"    description: This server",
	)

	// Keep the cloud reachable in the served document, but never list it twice
	// — on solidping.io itself the request origin already IS the cloud.
	if !strings.EqualFold(origin, openAPICloudURL) {
		rebuilt = append(rebuilt,
			"  - url: "+openAPICloudURL,
			"    description: SolidPing cloud",
		)
	}

	for range blanks {
		rebuilt = append(rebuilt, "")
	}

	rebuilt = append(rebuilt, lines[end:]...)

	return []byte(strings.Join(rebuilt, "\n"))
}

// openAPICloudURL is the public cloud instance, kept in the served list so a
// spec downloaded from a self-hosted instance still documents it.
const openAPICloudURL = "https://solidping.io"

// isSafeOriginForYAML accepts only what a scheme plus a Host header may legally
// contain, so the result can be emitted as a plain YAML scalar. Anything else —
// whitespace, quotes, control characters, a stray "#" — falls back to the
// embedded document rather than being escaped, because a Host that odd is far
// likelier to be an injection attempt than a real deployment.
func isSafeOriginForYAML(origin string) bool {
	scheme, host, found := strings.Cut(origin, "://")
	if !found || host == "" {
		return false
	}

	if scheme != schemeHTTP && scheme != schemeHTTPS {
		return false
	}

	return strings.IndexFunc(host, func(r rune) bool {
		return !strings.ContainsRune(hostAllowedChars, r)
	}) == -1
}

// hostAllowedChars is every character a scheme's authority may legally carry
// here: letters, digits, the label separators, and ":"/"[]" for a port or an
// IPv6 literal.
const hostAllowedChars = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"0123456789" +
	".-_:[]"
