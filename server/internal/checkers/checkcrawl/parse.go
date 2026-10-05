package checkcrawl

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// mixedClass says whether an http:// reference on an https page is active
// mixed content, passive mixed content, or not mixed content at all.
type mixedClass uint8

const (
	mixedNone mixedClass = iota
	mixedActive
	mixedPassive
)

// pageRef is one URL reference found on a page.
type pageRef struct {
	url   *url.URL
	check bool // the target is link-checked
	mixed mixedClass
}

// rawRef is a reference before resolution against the page / <base> URL.
type rawRef struct {
	value string
	check bool
	mixed mixedClass
}

// extractRefs tokenizes an HTML document and returns every reference it
// carries, resolved against <base href> and the page URL, fragments dropped
// and non-fetchable schemes (mailto:, tel:, javascript:, data:) removed.
// Order is document order, which is what keeps the crawl deterministic.
func extractRefs(body []byte, pageURL *url.URL) []pageRef {
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	base := pageURL

	var raws []rawRef

	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			break // io.EOF or a malformed document: keep what was read
		}

		if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
			continue
		}

		token := tokenizer.Token()

		if token.Data == "base" {
			if href := attr(token, "href"); href != "" {
				if parsed, err := pageURL.Parse(href); err == nil {
					base = parsed
				}
			}

			continue
		}

		raws = append(raws, refsOf(token)...)
	}

	return resolveRefs(raws, base)
}

func resolveRefs(raws []rawRef, base *url.URL) []pageRef {
	out := make([]pageRef, 0, len(raws))

	for _, raw := range raws {
		resolved, ok := resolveRef(base, raw.value)
		if !ok {
			continue
		}

		out = append(out, pageRef{url: resolved, check: raw.check, mixed: raw.mixed})
	}

	return out
}

// refsOf returns the references one start tag carries.
func refsOf(token html.Token) []rawRef {
	switch token.Data {
	case "a":
		return single(attr(token, "href"), true, mixedNone)
	case "link":
		return linkRefs(token)
	case "script":
		return single(attr(token, "src"), true, mixedActive)
	case "iframe":
		return single(attr(token, "src"), true, mixedActive)
	case "object":
		return single(attr(token, "data"), true, mixedActive)
	case "form":
		// A form action is not link-checked (a GET on a POST endpoint is a
		// 405 that would read as broken) but it is mixed content.
		return single(attr(token, "action"), false, mixedActive)
	case "img", "source":
		refs := single(attr(token, "src"), true, mixedPassive)

		return append(refs, srcsetRefs(attr(token, "srcset"))...)
	case "video", "audio":
		return single(attr(token, "src"), true, mixedPassive)
	default:
		return nil
	}
}

func linkRefs(token html.Token) []rawRef {
	rels := strings.Fields(strings.ToLower(attr(token, "rel")))
	mixed := mixedNone

	for _, rel := range rels {
		switch rel {
		case "preconnect", "dns-prefetch":
			return nil
		case "stylesheet":
			mixed = mixedActive
		}
	}

	return single(attr(token, "href"), true, mixed)
}

func srcsetRefs(srcset string) []rawRef {
	if srcset == "" {
		return nil
	}

	candidates := strings.Split(srcset, ",")
	out := make([]rawRef, 0, len(candidates))

	for _, candidate := range candidates {
		fields := strings.Fields(candidate)
		if len(fields) == 0 {
			continue
		}

		out = append(out, rawRef{value: fields[0], check: true, mixed: mixedPassive})
	}

	return out
}

func single(value string, check bool, mixed mixedClass) []rawRef {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	return []rawRef{{value: value, check: check, mixed: mixed}}
}

func attr(token html.Token, name string) string {
	for i := range token.Attr {
		if token.Attr[i].Key == name {
			return strings.TrimSpace(token.Attr[i].Val)
		}
	}

	return ""
}

// resolveRef resolves value against base, keeping only http(s) URLs and
// dropping the fragment.
func resolveRef(base *url.URL, value string) (*url.URL, bool) {
	lower := strings.ToLower(value)
	for _, prefix := range []string{"mailto:", "tel:", "javascript:", "data:", "#"} {
		if strings.HasPrefix(lower, prefix) {
			return nil, false
		}
	}

	resolved, err := base.Parse(value)
	if err != nil {
		return nil, false
	}

	if resolved.Scheme != schemeHTTP && resolved.Scheme != schemeHTTPS {
		return nil, false
	}

	resolved.Fragment = ""
	resolved.RawFragment = ""

	return resolved, true
}
