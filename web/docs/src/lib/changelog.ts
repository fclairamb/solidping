/**
 * Transform for the release-please-maintained root CHANGELOG.md into a
 * docs-page-friendly changelog body.
 *
 * See wiki/conventions/changelog.md for the content conventions this
 * transform assumes (user-facing prose, scope naming), and
 * specs/todos/2026-08-29-04-docs-changelog-page.md (or its archived copy)
 * for the original design.
 *
 * Robustness is the top priority: this runs at every docs build, against
 * text nobody proofreads for "will this break a markdown parser". Any line,
 * heading, or bullet this module doesn't recognize is passed through
 * verbatim rather than dropped or thrown on — see the `raw passthrough`
 * tests in changelog.test.ts for the behavior this guarantees.
 */

/** Known scope slugs (as they appear in `**scope:**` bullet prefixes) mapped
 * to a product-facing display name. Deliberately small — an unmapped scope
 * passes through unchanged rather than failing, so growing this table is
 * always optional, never required for the build to succeed. Add an entry
 * here when a scope's raw slug reads poorly to an end user. */
export const SCOPE_LABELS: Record<string, string> = {
  dash0: "Dashboard",
  dashboard: "Dashboard",
  "status pages": "Status pages",
  "status-pages": "Status pages",
  statuspages: "Status pages",
  status0: "Status pages",
  sftp: "SFTP checks",
  api: "API",
  openapi: "API",
  auth: "Authentication",
  checks: "Checks",
  incidents: "Incidents",
  integrations: "Integrations",
  notifications: "Notifications",
  alerting: "Alerting",
  escalation: "Escalation",
  "escalation policies": "Escalation policies",
  "on-call": "On-call",
  oncall: "On-call",
  slo: "SLOs",
  agents: "Agents",
  scheduling: "Scheduling",
  "rate limits": "Rate limits",
  "rate limiting": "Rate limits",
  "rate-limiting": "Rate limits",
  availability: "Availability",
  entitlements: "Billing & entitlements",
  reports: "Reports",
  onboarding: "Onboarding",
  cli: "CLI",
  docs: "Documentation",
  i18n: "Localization",
  slack: "Slack",
  discord: "Discord",
  telegram: "Telegram",
  whatsapp: "WhatsApp",
  zulip: "Zulip",
  smtp: "Email",
  email: "Email",
  webpush: "Web push",
  sms: "SMS",
};

/** The distinct sub-slug of a `#279` / `#282` PR reference found in a bullet. */
interface PrRef {
  number: string;
  url: string;
}

/** One transformed `## [x.y.z](compareUrl) (date)` heading, or `null` if the
 * line doesn't match the release-please heading shape closely enough to
 * transform safely — the caller then keeps the original line verbatim. */
export function transformHeading(
  line: string,
): { version: string; date: string; anchor: string; diffUrl: string } | null {
  const match = /^## \[(\d+\.\d+\.\d+)]\(([^)]+)\) \((\d{4}-\d{2}-\d{2})\)\s*$/.exec(
    line,
  );
  if (!match) {
    return null;
  }
  const [, version, diffUrl, date] = match;
  return { version, date, anchor: version.replace(/\./g, ""), diffUrl };
}

// release-please renders reference clutter as one or more parenthesized,
// comma-separated groups of `[label](url)` items at the very end of a
// bullet — e.g. ` ([#279](url)) ([4cbcc29](url), [#283](url))`. A PR ref's
// label is `#<digits>`; a commit ref's label is a hex hash. The two kinds
// can share a single `(...)` group (as in that example), so a naive
// "each ref has its own enclosing parens" assumption misses the second
// PR link — hence matching the whole trailing cluster as one unit below.
const REF_ITEM = String.raw`\[(?:#\d+|[0-9a-f]{7,40})]\((?:https?:\/\/[^)\s]+)\)`;
const REF_GROUP = String.raw`\((?:${REF_ITEM})(?:,\s*(?:${REF_ITEM}))*\)`;
const TRAILING_REF_CLUSTER_RE = new RegExp(String.raw`(?:\s*(?:${REF_GROUP}))+$`);
const PR_REF_RE = /\[#(\d+)]\((https?:\/\/[^)\s]+)\)/g;
const SCOPE_PREFIX_RE = /^\* \*\*([^*]+):\*\*\s?(.*)$/;

/**
 * Transform one `* ...` bullet line.
 *
 * Returns `null` when the bullet should be dropped entirely (a `deps`
 * scope). Returns the original line, untouched, when it doesn't look like a
 * recognizable bullet at all (robustness fallback).
 */
export function transformBullet(line: string): string | null {
  const scopeMatch = SCOPE_PREFIX_RE.exec(line);

  const rawScope = scopeMatch?.[1]?.trim();
  const rest = scopeMatch ? scopeMatch[2] : line.replace(/^\*\s+/, "");

  if (!scopeMatch && !line.startsWith("* ")) {
    // Doesn't look like a bullet at all — leave it alone.
    return line;
  }

  if (rawScope && rawScope.toLowerCase() === "deps") {
    return null;
  }

  // Distinct PR numbers referenced anywhere in the trailing cluster (order
  // of first appearance), regardless of how they're grouped with hashes.
  const prRefs: PrRef[] = [];
  const seen = new Set<string>();
  for (const m of rest.matchAll(PR_REF_RE)) {
    const [, number, url] = m;
    if (!seen.has(number)) {
      seen.add(number);
      prRefs.push({ number, url });
    }
  }

  const body = rest.replace(TRAILING_REF_CLUSTER_RE, "").trimEnd();

  const refsSuffix =
    prRefs.length > 0
      ? " (" + prRefs.map((ref) => `[#${ref.number}](${ref.url})`).join(", ") + ")"
      : "";

  const scopeLabel = rawScope
    ? (SCOPE_LABELS[rawScope.toLowerCase()] ?? rawScope)
    : undefined;

  const prefix = scopeLabel ? `**${scopeLabel}:** ` : "";
  return `* ${prefix}${body}${refsSuffix}`;
}

interface Section {
  header: string;
  lines: string[];
}

/**
 * Transform one version's body (everything between one `## [...]` heading
 * and the next, heading line excluded) into kept `### Section` blocks.
 * Sections that end up with no kept lines are dropped entirely.
 */
function transformBody(bodyLines: string[]): Section[] {
  const sections: Section[] = [];
  let current: Section | null = null;

  for (const line of bodyLines) {
    if (/^### /.test(line)) {
      current = { header: line, lines: [] };
      sections.push(current);
      continue;
    }
    if (line.trim() === "") {
      continue;
    }
    if (!current) {
      // Content before any ### header — keep a synthetic section so it
      // isn't silently lost.
      current = { header: "", lines: [] };
      sections.push(current);
    }
    if (line.startsWith("* ")) {
      const transformed = transformBullet(line);
      if (transformed !== null) {
        current.lines.push(transformed);
      }
    } else {
      // Unrecognized non-bullet content — pass through verbatim rather
      // than risk dropping something meaningful.
      current.lines.push(line);
    }
  }

  return sections.filter((section) => section.lines.length > 0);
}

/**
 * Transform one release chunk: a `## [x.y.z](...) (date)` heading line plus
 * everything up to (not including) the next `## ` heading.
 */
function transformChunk(chunkLines: string[]): string {
  const [headingLine, ...bodyLines] = chunkLines;
  const heading = transformHeading(headingLine);

  if (!heading) {
    // Doesn't match the expected release-please shape — emit verbatim so an
    // unexpected heading format can never break the build.
    return chunkLines.join("\n");
  }

  const sections = transformBody(bodyLines);
  const out: string[] = [
    `## ${heading.version} — ${heading.date} {#${heading.anchor}}`,
    "",
    `([diff](${heading.diffUrl}))`,
  ];

  if (sections.length === 0) {
    out.push("", "Maintenance release.");
    return out.join("\n");
  }

  for (const section of sections) {
    out.push("");
    if (section.header) {
      out.push(section.header, "");
    }
    out.push(...section.lines);
  }

  return out.join("\n");
}

/**
 * Transform a full release-please CHANGELOG.md into a docs-friendly body
 * (no frontmatter, no leading `# Changelog` — the caller adds those).
 *
 * Never throws: any content that doesn't parse as a release-please chunk is
 * emitted verbatim.
 */
export function transformChangelog(raw: string): string {
  const lines = raw.replace(/\r\n/g, "\n").split("\n");

  const firstHeadingIdx = lines.findIndex((line) => /^## /.test(line));
  if (firstHeadingIdx === -1) {
    // No version headings found at all — nothing to transform, pass the
    // whole thing through so the build never fails on an empty/odd file.
    return raw.trim();
  }

  const chunks: string[][] = [];
  let current: string[] = [];
  for (const line of lines.slice(firstHeadingIdx)) {
    if (/^## /.test(line)) {
      if (current.length > 0) {
        chunks.push(current);
      }
      current = [line];
    } else {
      current.push(line);
    }
  }
  if (current.length > 0) {
    chunks.push(current);
  }

  return chunks.map(transformChunk).join("\n\n").trim() + "\n";
}
