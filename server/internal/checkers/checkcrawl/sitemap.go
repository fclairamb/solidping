package checkcrawl

import (
	"encoding/xml"
	"errors"
	"strings"
)

var errSitemapNotXML = errors.New("sitemap is neither a <urlset> nor a <sitemapindex>")

// parsedSitemap is one fetched sitemap: page URLs (urlset) or child
// sitemaps (sitemapindex).
type parsedSitemap struct {
	pages    []string
	children []string
}

type sitemapLoc struct {
	Loc string `xml:"loc"`
}

type sitemapDoc struct {
	XMLName  xml.Name     `xml:""`
	URLs     []sitemapLoc `xml:"url"`
	Sitemaps []sitemapLoc `xml:"sitemap"`
}

func parseSitemap(body []byte) (parsedSitemap, error) {
	var doc sitemapDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return parsedSitemap{}, err
	}

	var out parsedSitemap

	switch doc.XMLName.Local {
	case "urlset":
		for _, loc := range doc.URLs {
			if text := strings.TrimSpace(loc.Loc); text != "" {
				out.pages = append(out.pages, text)
			}
		}
	case "sitemapindex":
		for _, loc := range doc.Sitemaps {
			if text := strings.TrimSpace(loc.Loc); text != "" {
				out.children = append(out.children, text)
			}
		}
	default:
		return parsedSitemap{}, errSitemapNotXML
	}

	return out, nil
}
