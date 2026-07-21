// Status-code pattern helpers for the HTTP check form's expected-status chip
// input (spec 2026-07-21-02). Mirrors the backend's
// checkhttp.validateStatusPattern / MatchStatusCode
// (server/internal/checkers/checkhttp/checker.go, config.go): an exact
// 100-599 status code, or an "NXX" wildcard (N in 1-5), case-insensitive.
const STATUS_PATTERN_RE = /^[1-5]([0-9]{2}|XX)$/i;

// isValidStatusPattern checks a single token (after trimming) against the
// pattern grammar. Case-insensitive, so "4xx" and "4XX" both pass — matching
// the backend's strings.ToUpper before matching.
export function isValidStatusPattern(token: string): boolean {
  return STATUS_PATTERN_RE.test(token.trim());
}

// normalizeStatusPattern trims and uppercases a token, mirroring the
// backend's strings.ToUpper normalization before MatchStatusCode compares it
// ("4xx" -> "4XX").
export function normalizeStatusPattern(token: string): string {
  return token.trim().toUpperCase();
}

// dedupeStatusPatterns normalizes and de-duplicates a raw status-code list,
// dropping empty entries and preserving first-seen order. Used to seed the
// HTTP form's chip list from a stored config, in case it predates
// normalization or was hand-edited via the API with raw duplicates/casing.
export function dedupeStatusPatterns(raw: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const item of raw) {
    const normalized = normalizeStatusPattern(item);
    if (!normalized || seen.has(normalized)) continue;
    seen.add(normalized);
    out.push(normalized);
  }
  return out;
}
