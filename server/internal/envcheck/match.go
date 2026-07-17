package envcheck

import "strings"

// suggest returns the best did-you-mean recognized name for an unrecognized
// SP_* variable, or "" when nothing is close enough. Two ranked rules:
//
//  1. Segment subsequence — split both names on '_'. If the unknown's segments
//     are an ordered subsequence of a known name's segments (a missing segment,
//     e.g. SP_RATE_LIMITING_TRUSTED_PROXIES vs the real
//     SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES) or vice versa (an extra
//     segment), the known name is a candidate. This catches the missing/extra
//     whole-segment class that plain edit distance cannot: inserting "SERVER_"
//     is a Levenshtein distance of 7, far past any sane typo threshold.
//  2. Levenshtein <= maxSuggestDistance — for genuine character typos
//     (SP_LOG_LEVL → SP_LOG_LEVEL).
//
// Rule 1 wins over rule 2. Within a rule, candidates are ranked by closeness
// (fewest differing segments / smallest distance), then lexicographically, and
// at most one suggestion is returned. known must be sorted for determinism.
func suggest(unknown string, known []string) string {
	if s, ok := bestSubsequenceMatch(unknown, known); ok {
		return s
	}

	if s, ok := bestLevenshteinMatch(unknown, known); ok {
		return s
	}

	return ""
}

// bestSubsequenceMatch implements rule 1. A candidate is any known name whose
// underscore-split segments contain the unknown's as an ordered subsequence, or
// are themselves an ordered subsequence of the unknown's. Ranked by the absolute
// segment-count difference, then lexicographically.
func bestSubsequenceMatch(unknown string, known []string) (string, bool) {
	unknownSegs := strings.Split(unknown, "_")

	best := ""
	bestDiff := 0
	for _, candidate := range known {
		candidateSegs := strings.Split(candidate, "_")
		if !isSubsequence(unknownSegs, candidateSegs) && !isSubsequence(candidateSegs, unknownSegs) {
			continue
		}

		diff := len(candidateSegs) - len(unknownSegs)
		if diff < 0 {
			diff = -diff
		}

		if best == "" || diff < bestDiff || (diff == bestDiff && candidate < best) {
			best = candidate
			bestDiff = diff
		}
	}

	return best, best != ""
}

// bestLevenshteinMatch implements rule 2: the known name at the smallest
// Levenshtein distance from unknown, provided that distance is within
// maxSuggestDistance. Ties break lexicographically.
func bestLevenshteinMatch(unknown string, known []string) (string, bool) {
	best := ""
	bestDist := 0
	for _, candidate := range known {
		dist := levenshtein(unknown, candidate)
		if dist > maxSuggestDistance {
			continue
		}

		if best == "" || dist < bestDist || (dist == bestDist && candidate < best) {
			best = candidate
			bestDist = dist
		}
	}

	return best, best != ""
}

// isSubsequence reports whether a is an ordered subsequence of b: every element
// of a appears in b in the same order (segments compared for exact equality).
// The empty slice is a subsequence of anything.
func isSubsequence(a, b []string) bool {
	i := 0
	for j := 0; i < len(a) && j < len(b); j++ {
		if a[i] == b[j] {
			i++
		}
	}

	return i == len(a)
}

// levenshtein returns the byte-level edit distance between a and b (SP_* names
// are ASCII) using the standard two-row dynamic-programming algorithm.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}

			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}

		prev, curr = curr, prev
	}

	return prev[len(b)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}

	return m
}
