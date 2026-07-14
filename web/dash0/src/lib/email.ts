// isValidEmail is a pragmatic, HTML5-input-style check — not a full RFC 5322
// validator. It rejects the obviously-wrong (missing @, missing domain dot,
// embedded whitespace) without trying to be a perfect email grammar.
export function isValidEmail(value: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value);
}

// parseEmailList splits free-form recipient text on any run of whitespace,
// commas, or semicolons — covering space/comma/semicolon/newline and any
// mix thereof (e.g. pasting "a@x.com, b@x.com"). Entries are trimmed, empty
// strings dropped, and exact-string duplicates collapsed (case is preserved:
// the local part of an email address is technically case-sensitive, so we
// never lowercase for comparison).
export function parseEmailList(raw: string): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const token of raw.split(/[\s,;]+/)) {
    const trimmed = token.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    result.push(trimmed);
  }
  return result;
}
