// parseLabelsParam splits a `key1:value1,key2:value2` URL string into a map.
// Empty/invalid pairs are skipped silently — the URL is user-editable, so we
// favour graceful degradation over hard errors.
export function parseLabelsParam(s: string | undefined | null): Record<string, string> {
  if (!s) return {};
  const out: Record<string, string> = {};
  for (const pair of s.split(",")) {
    const idx = pair.indexOf(":");
    if (idx <= 0) continue;
    const key = pair.slice(0, idx).trim();
    const value = pair.slice(idx + 1).trim();
    if (key && value) out[key] = value;
  }
  return out;
}

// serializeLabelsParam is the inverse of parseLabelsParam. Returns an empty
// string when the map is empty so callers can pass `serializeLabelsParam(x)
// || undefined` to drop the param from the URL entirely.
export function serializeLabelsParam(labels: Record<string, string>): string {
  return Object.entries(labels)
    .filter(([k, v]) => k && v)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}:${v}`)
    .join(",");
}
