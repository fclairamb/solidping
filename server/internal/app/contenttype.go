package app

import (
	"mime"
	"path"
	"strings"
)

// pinnedContentType is consulted BEFORE Go's mime table, on purpose.
//
// Go seeds that table from the host's mime database, so the answer for the same
// extension depends on the machine the binary runs on: ".yaml" resolves to
// "application/yaml" on a typical Linux image and to nothing at all on macOS.
// Applying a pin only when the OS says nothing therefore does not remove the
// variance, it just hides it on one platform — the served header still differs
// between a laptop and CI. Pinning first makes it identical everywhere.
func pinnedContentType(ext string) (string, bool) {
	switch ext {
	// RFC 9512. YAML is UTF-8 by definition, so the charset is stated rather
	// than left for the client to assume.
	case ".yaml", ".yml":
		return "application/yaml; charset=utf-8", true
	case ".md":
		return "text/markdown; charset=utf-8", true
	default:
		return "", false
	}
}

// contentTypeForFile resolves the Content-Type to serve an embedded file with,
// or "" when it cannot say.
//
// The trap this exists to close: mime.TypeByExtension takes an EXTENSION
// (".html"), but the embed paths handed around here are whole paths
// ("openapi/index.html"). Passing the path returns "" for every file, and
// setting that empty string as the header is worse than setting nothing at all
// — an explicitly-set Content-Type suppresses net/http's own content sniffing,
// so the response goes out genuinely untyped rather than sniffed.
//
// Callers must therefore treat "" as "do not set the header" and let net/http
// sniff, rather than writing the empty value through.
func contentTypeForFile(fileName string) string {
	ext := strings.ToLower(path.Ext(fileName))

	if pinned, ok := pinnedContentType(ext); ok {
		return pinned
	}

	// Everything else comes from Go's table, whose built-in entries (.html,
	// .css, .js, .json…) are compiled in and so do not vary by host.
	return mime.TypeByExtension(ext)
}
