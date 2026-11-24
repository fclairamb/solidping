package atomfeed_test

import (
	"bytes"
	"encoding/xml"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/atomfeed"
)

func TestEncodeRoundTrips(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.FixedZone("CET", 3600))
	feed := &atomfeed.Feed{
		Title:   "Acme <events>",
		ID:      "urn:test",
		Updated: atomfeed.Timestamp(at),
		Links:   []atomfeed.Link{{Href: "https://acme.test/feed.xml", Rel: "self"}},
		Entries: []atomfeed.Entry{{
			Title: "Incident & co", ID: "urn:uuid:1", Updated: atomfeed.Timestamp(at),
			Published: atomfeed.Timestamp(at), Link: atomfeed.Link{Href: "https://acme.test/i/1"},
			Content: atomfeed.Text{Type: "text", Body: "body"},
		}},
	}

	var buf bytes.Buffer
	r.NoError(atomfeed.Encode(&buf, feed))
	r.True(bytes.HasPrefix(buf.Bytes(), []byte("<?xml")))

	var parsed atomfeed.Feed
	r.NoError(xml.Unmarshal(buf.Bytes(), &parsed))
	r.Equal("Acme <events>", parsed.Title)
	r.Equal("2026-11-24T09:00:00Z", parsed.Updated)
	r.Len(parsed.Entries, 1)
	r.Equal("Incident & co", parsed.Entries[0].Title)
}
