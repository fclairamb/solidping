package app

import (
	"mime"
	"path"
	"strings"
)

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

	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType
	}

	// Go's table is seeded from the OS mime database, so what it knows varies
	// by machine — .yaml resolves on a typical Linux image and not on macOS,
	// which is exactly the kind of difference that makes a header appear in CI
	// and vanish locally. Pin the ones we actually embed.
	switch ext {
	case ".yaml", ".yml":
		// RFC 9512.
		return "application/yaml; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	default:
		return ""
	}
}
