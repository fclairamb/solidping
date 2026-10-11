import { describe, expect, it } from "vitest";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";

// Docs links (components/shared/docs-link.tsx) are wired only where a
// genuinely relevant docs page exists, never as a forced link to /docs/intro.
// PageHeader rendering a `docsHref` as a link is pinned by
// components/shared/page-header.test.tsx; this file pins WHICH page links
// WHERE, and that every docs path the dashboard links to is a real page of the
// docs site (web/docs/docs). The per-type check detail link is pinned by
// server/internal/checkers/registry/docs_anchor_test.go and exercised in the
// browser by e2e/docs-links.spec.ts.

const SRC = __dirname;
const ROUTES = join(SRC, "routes");
const DOCS = join(SRC, "..", "..", "docs", "docs");

function read(path: string): string {
  return readFileSync(join(ROUTES, path), "utf8");
}

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...sourceFiles(path));
    } else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      out.push(path);
    }
  }
  return out;
}

describe("docs links per page", () => {
  // [route file, the docs link it must render]
  const pages: [string, string][] = [
    ["orgs/$org/status-pages.index.tsx", 'docsHref="/docs/features/status-pages"'],
    ["orgs/$org/slos.index.tsx", 'docsHref="/docs/features/slos"'],
    ["orgs/$org/checks.$checkUid.badges.tsx", 'docsHref="/docs/features/status-badges"'],
    ["orgs/$org/organization.discovery.index.tsx", 'docsHref="/docs/features/discovery"'],
    ["orgs/$org/events.tsx", 'docsHref="/docs/features/events"'],
    ["orgs/$org/organization.audit.tsx", '<DocsLink href="/docs/features/audit-log" />'],
  ];

  for (const [file, link] of pages) {
    it(`${file} links to its docs page`, () => {
      expect(read(file)).toContain(link);
    });
  }

  it("the checks list puts its check-types docs entry in the more-actions menu, not the header", () => {
    const source = read("orgs/$org/checks.index.tsx");
    expect(source).toContain(
      '<a href="/docs/features/check-types" target="_blank" rel="noreferrer" data-testid="check-types-docs-link">',
    );
    expect(source).not.toContain("docsHref=");
    expect(source).not.toContain("<DocsLink");
  });

  it("the jobs pages, which have no docs page, render no docs link", () => {
    for (const file of ["orgs/$org/jobs.tsx", "orgs/$org/jobs.index.tsx"]) {
      const source = read(file);
      expect(source, file).not.toContain("docsHref");
      expect(source, file).not.toContain("DocsLink");
    }
  });
});

describe("docs link targets", () => {
  // Generated at docs build time rather than written as markdown (see the
  // root AGENTS.md "Docs site" section).
  const GENERATED = new Set(["/docs/api", "/docs/changelog"]);

  const links = new Map<string, string>();
  for (const file of sourceFiles(SRC)) {
    const source = readFileSync(file, "utf8");
    for (const match of source.matchAll(/["'`](\/docs\/[^"'`$#]*)/g)) {
      links.set(match[1].replace(/\/$/, ""), relative(SRC, file));
    }
  }

  it("finds the dashboard's docs links", () => {
    // Anti-vacuity: an extraction regex that matched nothing would pass below.
    expect(links.size).toBeGreaterThan(10);
  });

  for (const [link, file] of links) {
    it(`${link} (${file}) is a page of the docs site`, () => {
      if (GENERATED.has(link)) return;
      const path = join(DOCS, link.replace(/^\/docs\//, ""));
      const exists = [".md", ".mdx", "/index.md", "/index.mdx"].some((ext) =>
        existsSync(path + ext),
      );
      expect(exists, `${link} has no markdown under web/docs/docs`).toBe(true);
    });
  }
});
