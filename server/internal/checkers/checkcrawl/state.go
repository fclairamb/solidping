package checkcrawl

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"hash/fnv"
	"slices"
)

// Crawl phases, in order.
const (
	phaseRobots  = "robots"
	phaseSitemap = "sitemap"
	phaseCrawl   = "crawl"
)

// Queue item kinds.
const (
	kindPage     = ""  // internal URL: fetched with GET, parsed when HTML
	kindExternal = "x" // external URL: HEAD, GET fallback
	kindSitemap  = "s" // internal URL listed by a sitemap
)

// Caps that keep the state well under the 1 MiB state limit.
const (
	maxExternalLinks  = 2000
	maxFindings       = 2000
	maxSitemapFetches = 20
)

// Finding is one problem the crawl found (spec 2026-10-03-03 §2.4).
type Finding struct {
	Type        string `json:"type"`
	URL         string `json:"url"`
	Source      string `json:"source,omitempty"`
	Status      int    `json:"status,omitempty"`
	Error       string `json:"error,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

// newFinding builds a finding with its fingerprint = sha1(type|url|source).
func newFinding(findingType, target, source string, status int, errText string) Finding {
	sum := sha1.Sum([]byte(findingType + "|" + target + "|" + source))

	return Finding{
		Type: findingType, URL: target, Source: source, Status: status, Error: errText,
		Fingerprint: hex.EncodeToString(sum[:]),
	}
}

type queueItem struct {
	U   string `json:"u"`
	D   int    `json:"d"`
	Src string `json:"src,omitempty"`
	K   string `json:"k,omitempty"`
}

type sitemapItem struct {
	U string `json:"u"`
	// Optional marks the /sitemap.xml guess of auto mode: missing is fine.
	Optional bool `json:"optional,omitempty"`
}

type sitemapState struct {
	Status  string        `json:"status"`
	URLs    int           `json:"urls"`
	Fetched int           `json:"fetched"`
	Pending []sitemapItem `json:"pending,omitempty"`
}

type robotsState struct {
	Fetched bool         `json:"fetched"`
	Rules   []robotsRule `json:"rules,omitempty"`
}

// crawlState is the checker-owned payload stored between slices.
type crawlState struct {
	Phase       string       `json:"phase"`
	Queue       []queueItem  `json:"queue"`
	Seen        []byte       `json:"seen"` // sorted 8-byte URL hashes
	Sitemap     sitemapState `json:"sitemap"`
	Robots      robotsState  `json:"robots"`
	Findings    []Finding    `json:"findings"`
	Pages       int          `json:"pages"`
	Admitted    int          `json:"admitted"`
	Externals   int          `json:"externals"`
	Units       int          `json:"units"`
	Slices      int          `json:"slices"`
	StartedAtMs int64        `json:"startedAtMs"`
}

// urlHash is the 8-byte FNV-1a hash stored in the seen set.
func urlHash(raw string) uint64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(raw))

	return hasher.Sum64()
}

func decodeSeen(raw []byte) map[uint64]struct{} {
	const width = 8

	seen := make(map[uint64]struct{}, len(raw)/width)

	for i := 0; i+width <= len(raw); i += width {
		seen[binary.BigEndian.Uint64(raw[i:i+width])] = struct{}{}
	}

	return seen
}

func encodeSeen(seen map[uint64]struct{}) []byte {
	const width = 8

	hashes := make([]uint64, 0, len(seen))
	for h := range seen {
		hashes = append(hashes, h)
	}

	slices.Sort(hashes)

	out := make([]byte, len(hashes)*width)
	for i, h := range hashes {
		binary.BigEndian.PutUint64(out[i*width:], h)
	}

	return out
}
