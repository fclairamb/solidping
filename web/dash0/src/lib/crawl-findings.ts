// Pure helpers for the website crawl check (spec 2026-10-03-03): read the
// findings a crawl result carries in its output and group them for the check
// page. The result output holds the first 50 findings, the count per type and
// `newFindings` (count + first 20) diffed against the previous run.

export const CRAWL_FINDING_TYPES = [
  "broken_link",
  "broken_external_link",
  "mixed_content_active",
  "mixed_content_passive",
  "sitemap_error",
] as const;

export type CrawlFindingType = (typeof CRAWL_FINDING_TYPES)[number];

export interface CrawlFinding {
  type: string;
  url: string;
  source?: string;
  status?: number;
  error?: string;
  fingerprint: string;
}

export interface CrawlFindingGroup {
  type: string;
  /** Total count of this type in the run (may exceed `findings.length`). */
  count: number;
  findings: (CrawlFinding & { isNew: boolean })[];
}

export interface CrawlSummary {
  groups: CrawlFindingGroup[];
  total: number;
  shown: number;
  newCount: number;
  pagesCrawled: number;
  incomplete: boolean;
}

function asFindings(value: unknown): CrawlFinding[] {
  if (!Array.isArray(value)) return [];
  return value.filter(
    (item): item is CrawlFinding =>
      !!item &&
      typeof item === "object" &&
      typeof (item as CrawlFinding).type === "string" &&
      typeof (item as CrawlFinding).url === "string",
  );
}

function asNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

/** Whether a result output is a crawl result. */
export function isCrawlOutput(output: Record<string, unknown> | undefined): boolean {
  return !!output && typeof output.counts === "object" && Array.isArray(output.findings);
}

/** summarizeCrawlOutput groups a crawl result's findings by type, in the
 * report order, flagging the ones the previous run did not have. Types with
 * no finding are left out. */
export function summarizeCrawlOutput(output: Record<string, unknown> | undefined): CrawlSummary {
  const findings = asFindings(output?.findings);
  const counts = (output?.counts ?? {}) as Record<string, unknown>;
  const newBlock = (output?.newFindings ?? {}) as Record<string, unknown>;
  const newFingerprints = new Set(asFindings(newBlock.items).map((f) => f.fingerprint));
  const newCount = asNumber(newBlock.count);
  // When every finding is new (first run), the 20-item list cannot flag them
  // all: fall back to "all new".
  const allNew = newCount > 0 && newCount === asNumber(output?.findingsTotal);

  const order = [
    ...CRAWL_FINDING_TYPES,
    ...findings.map((f) => f.type).filter((t) => !(CRAWL_FINDING_TYPES as readonly string[]).includes(t)),
  ];
  const groups: CrawlFindingGroup[] = [];

  for (const type of new Set(order)) {
    const ofType = findings.filter((f) => f.type === type);
    const count = Math.max(asNumber(counts[type]), ofType.length);
    if (count === 0) continue;
    groups.push({
      type,
      count,
      findings: ofType.map((f) => ({ ...f, isNew: allNew || newFingerprints.has(f.fingerprint) })),
    });
  }

  return {
    groups,
    total: asNumber(output?.findingsTotal) || findings.length,
    shown: findings.length,
    newCount,
    pagesCrawled: asNumber(output?.pagesCrawled),
    incomplete: output?.incomplete === true,
  };
}

/** crawlProgress reads pagesDone / maxPages out of a run's progress bag. */
export function crawlProgress(progress: Record<string, unknown> | undefined): {
  done: number;
  max: number;
} | null {
  if (!progress) return null;
  const done = asNumber(progress.pagesDone);
  const max = asNumber(progress.maxPages);
  return max > 0 ? { done, max } : null;
}
