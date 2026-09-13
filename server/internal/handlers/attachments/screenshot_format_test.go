package attachments

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/files"
)

// The tests in this file are the ones whose absence let spec 2026-09-13-01's
// bug ship. Every pre-existing screenshot fixture was a hand-written PNG header
// (see pngBytes), which can never disagree with a sniffer that only knows PNG —
// so the store rejecting every real capture was invisible for the life of the
// feature.
//
// These use the magic bytes real encoders emit, one fixture per accepted
// format, plus a negative control.

// jpegImage is a JFIF-prefixed blob: FF D8 FF is the JPEG SOI + marker every
// real JPEG starts with, and E0 00 10 "JFIF" is what Chrome's encoder produced
// in the live capture that proved this bug.
func jpegImage(marker string) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, []byte(marker)...)
}

// webpImage is a RIFF container declaring the WEBP form type — the shape the
// capture path now produces.
func webpImage(marker string) []byte {
	return append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), []byte(marker)...)
}

// TestScreenshotStoresTheSniffedTypeAndExtension is §4's contract: what gets
// stored is what the BYTES say, and the filename agrees with it.
//
// The extension half matters on its own — a WebP saved as `.png` is a download
// the operating system refuses to open, which is the file-manager twin of the
// broken-image icon on the incident card.
func TestScreenshotStoresTheSniffedTypeAndExtension(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     []byte
		mimeType string
		suffix   string
	}{
		{"png", pngBytes("shot"), "image/png", ".png"},
		{"jpeg", jpegImage("shot"), "image/jpeg", ".jpg"},
		{"webp", webpImage("shot"), "image/webp", ".webp"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, dbService, svc, org := setupAttachmentsTest(t)

			incidentUID := uuid.New().String()

			fileUID, err := svc.PutIncidentScreenshot(ctx, org.UID, incidentUID, tc.body, nil)
			r.NoError(err, "a real %s capture must be accepted", tc.name)
			r.NotEmpty(fileUID)

			stored, err := dbService.GetFile(ctx, org.UID, fileUID)
			r.NoError(err)
			r.Equal(tc.mimeType, stored.MimeType, "the stored type is the sniffed one")
			r.True(strings.HasSuffix(stored.Name, tc.suffix),
				"the filename extension must match the sniffed type, got %q", stored.Name)
			r.Contains(stored.Name, "incident-"+incidentUID+"-screenshot")

			// And the list view, which is what the incident card reads, agrees.
			list, err := svc.ListIncidentAttachments(ctx, org.UID, incidentUID)
			r.NoError(err)
			r.Len(list, 1)
			r.Equal(tc.mimeType, list[0].MimeType)
		})
	}
}

// TestScreenshotSniffFailsClosedOnUnknownMagic is the negative control for the
// widened table: three accepted formats must not become "anything goes".
func TestScreenshotSniffFailsClosedOnUnknownMagic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body []byte
	}{
		{"gif", []byte("GIF89a" + strings.Repeat("x", 32))},
		{"plain text", []byte("this is not an image at all, not even a little")},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"json", []byte(`{"hops":[]}`)},
		// RIFF without the WEBP form type: a WAV file is a RIFF container too,
		// so matching only the first four bytes would let audio through.
		{"riff that is not webp", []byte("RIFF\x00\x00\x00\x00WAVEfmt padding")},
		// Truncated below the 12 bytes the form type lives at.
		{"truncated riff", []byte("RIFF\x00\x00\x00\x00")},
		// One byte short of the PNG signature.
		{"truncated png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, _, svc, org := setupAttachmentsTest(t)

			_, err := svc.PutIncidentScreenshot(ctx, org.UID, uuid.New().String(), tc.body, nil)
			r.ErrorIs(err, ErrUnsupportedMediaType,
				"an unrecognized body must still be refused for the screenshot kind")
		})
	}

	// Positive control, so "everything is refused" cannot pass this test.
	t.Run("positive control", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		ctx, _, svc, org := setupAttachmentsTest(t)

		uid, err := svc.PutIncidentScreenshot(ctx, org.UID, uuid.New().String(), webpImage("ok"), nil)
		r.NoError(err)
		r.NotEmpty(uid)
	})
}

// TestStoredScreenshotServesWithAMatchingContentType is the render proof.
//
// files.WriteContent sets `X-Content-Type-Options: nosniff` on every served
// file, so a browser refuses to decode bytes whose declared type is wrong —
// which is exactly how a JPEG stored as image/png became a broken-image icon on
// the incident card. Asserting the served header against the format of the
// bytes is what proves the icon is gone; it also pins the INLINE disposition,
// without which the card's <img> would download instead of render.
func TestStoredScreenshotServesWithAMatchingContentType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     []byte
		mimeType string
	}{
		{"png", pngBytes("shot"), "image/png"},
		{"jpeg", jpegImage("shot"), "image/jpeg"},
		{"webp", webpImage("shot"), "image/webp"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, dbService, svc, org := setupAttachmentsTest(t)

			fileUID, err := svc.PutIncidentScreenshot(ctx, org.UID, uuid.New().String(), tc.body, nil)
			r.NoError(err)

			stored, err := dbService.GetFile(ctx, org.UID, fileUID)
			r.NoError(err)

			recorder := httptest.NewRecorder()
			r.NoError(files.WriteContent(recorder, stored.MimeType, stored.Name, bytes.NewReader(tc.body)))

			r.Equal(tc.mimeType, recorder.Header().Get("Content-Type"),
				"the served type must describe the bytes, or nosniff blocks the render")
			r.Equal("nosniff", recorder.Header().Get("X-Content-Type-Options"))
			r.True(strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "inline"),
				"a screenshot must render in the incident card, not download; got %q",
				recorder.Header().Get("Content-Disposition"))
			r.Equal(tc.body, recorder.Body.Bytes())
		})
	}
}
