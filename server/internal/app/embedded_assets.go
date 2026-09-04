package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
)

// sniffLen is how many bytes http.DetectContentType looks at. Reading exactly
// this much is what lets an unknown extension still be sniffed without pulling
// the whole file into memory first.
const sniffLen = 512

// errEmbeddedIsDirectory signals a request that resolved to a directory inside
// an embedded filesystem. Serving one would either 500 later or leak a listing;
// callers treat it as "not found" and fall through to their own 404 path.
var errEmbeddedIsDirectory = errors.New("embedded path is a directory")

// embeddedFileExists reports whether name is a regular file in fsys.
//
// A directory must answer **false**. `fs.Stat` happily succeeds on one, and the
// callers here resolve request paths that routinely land on directories — "/"
// maps to "dash0res", "/docs/features" to "docsres/features". Treating those as
// hits is what breaks the SPA index fallback and the docs candidate walk, so the
// directory check lives in the one helper every caller uses.
func embeddedFileExists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)

	return err == nil && !info.IsDir()
}

// writeEmbeddedFile streams a file out of an embedded filesystem to the client
// **without copying it onto the Go heap**.
//
// The pattern it replaces was `data, _ := fsys.ReadFile(name); w.Write(data)`.
// embed.FS.ReadFile is `[]byte(string)` — a fresh heap allocation the size of
// the file, per request. Serving the embedded docs that way makes every crawl
// of the site allocate tens of megabytes of anonymous memory that then takes
// the GC and the scavenger minutes to hand back (measured: +57 MB anon on a
// full docs crawl, still elevated three minutes later).
//
// Streaming through io.Copy's 32 KB buffer costs a bounded, reusable
// allocation instead. The file's own pages still become resident — but as
// *file-backed* pages of the binary's rodata, which are clean, reclaimable
// under pressure, and can never cause an OOM kill. That is the entire point:
// the bytes were always going to be touched; what changes is whether a copy of
// them also lands in anonymous memory.
//
// contentType may be empty, in which case it is sniffed from the first 512
// bytes exactly as http.DetectContentType would have done on the full buffer —
// the sniffer never looks past 512 bytes, so the result is identical.
//
// Content-Length is set from the file's size, so responses stay
// non-chunked and clients keep their progress bars. Any header the caller
// already set (Cache-Control, and anything else) is preserved.
func writeEmbeddedFile(writer http.ResponseWriter, fsys fs.FS, name, contentType string, status int) error {
	file, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	if info.IsDir() {
		return fmt.Errorf("%w: %s", errEmbeddedIsDirectory, name)
	}

	// Sniffing needs the first bytes in hand; they are then written out ahead of
	// the rest, so nothing is read twice and nothing is buffered whole.
	var head []byte

	if contentType == "" {
		head = make([]byte, sniffLen)

		read, readErr := io.ReadFull(file, head)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return readErr
		}

		head = head[:read]
		contentType = http.DetectContentType(head)
	}

	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	writer.WriteHeader(status)

	if len(head) > 0 {
		if _, writeErr := writer.Write(head); writeErr != nil {
			return writeErr
		}
	}

	_, err = io.Copy(writer, file)

	return err
}
