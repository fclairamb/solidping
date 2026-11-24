// Package atomfeed serializes Atom 1.0 documents (RFC 4287). It is shared by
// the public status-page feed and the private org event feed, so both write
// the same shape the same way.
package atomfeed

import (
	"encoding/xml"
	"io"
	"time"
)

// ContentType is the media type of an Atom document.
const ContentType = "application/atom+xml; charset=utf-8"

// Feed is the root Atom document.
type Feed struct {
	XMLName  xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	Title    string   `xml:"title"`
	ID       string   `xml:"id"`
	Updated  string   `xml:"updated"`
	Links    []Link   `xml:"link"`
	Entries  []Entry  `xml:"entry"`
	Subtitle string   `xml:"subtitle,omitempty"`
}

// Link is an Atom link element.
type Link struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

// Entry is one Atom entry.
type Entry struct {
	Title     string `xml:"title"`
	ID        string `xml:"id"`
	Updated   string `xml:"updated"`
	Published string `xml:"published"`
	Link      Link   `xml:"link"`
	Content   Text   `xml:"content"`
}

// Text is an Atom text construct.
type Text struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

// Timestamp formats a time the way Atom expects it (RFC 3339, UTC).
func Timestamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339)
}

// Encode writes the XML declaration and the indented feed.
func Encode(writer io.Writer, feed *Feed) error {
	if _, err := writer.Write([]byte(xml.Header)); err != nil {
		return err
	}

	enc := xml.NewEncoder(writer)
	enc.Indent("", "  ")

	return enc.Encode(feed)
}
