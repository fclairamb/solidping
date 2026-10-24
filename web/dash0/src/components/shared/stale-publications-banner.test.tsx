/**
 * @vitest-environment jsdom
 *
 * With several stale entries on one page, each one gets its own link: dash0
 * has no list of a page's entries, so linking to the page itself left the
 * operator with nothing to review.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import "@/i18n";
import type { StalePublication } from "@/api/hooks";

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    params,
    className,
    "data-testid": testId,
  }: PropsWithChildren<{
    params: Record<string, string>;
    className?: string;
    "data-testid"?: string;
  }>) => (
    <a
      href={`/orgs/${params.org}/status-pages/${params.statusPageUid}/incidents/${params.uid ?? ""}`}
      className={className}
      data-testid={testId}
    >
      {children}
    </a>
  ),
}));

const { StalePublicationsBanner } = await import("./stale-publications-banner");

function entry(uid: string, title: string): StalePublication {
  return {
    page: { uid: "page-1", name: "Public status" },
    publication: {
      uid,
      statusPageUid: "page-1",
      incidentUid: `inc-${uid}`,
      title,
      state: "investigating",
      autoCreated: true,
      humanTouched: false,
      publishedAt: "2026-10-18T03:09:43Z",
      createdAt: "2026-10-18T03:09:43Z",
      updatedAt: "2026-10-18T03:09:43Z",
      stale: true,
    },
  } as StalePublication;
}

describe("StalePublicationsBanner", () => {
  afterEach(() => {
    cleanup();
  });

  it("links one entry straight to its edit page", () => {
    const { getAllByTestId } = render(
      <StalePublicationsBanner
        org="acme"
        stale={[entry("pub-1", "API down")]}
      />,
    );
    const links = getAllByTestId("stale-publication-link");
    expect(links).toHaveLength(1);
    expect(links[0].getAttribute("href")).toBe(
      "/orgs/acme/status-pages/page-1/incidents/pub-1",
    );
  });

  it("links every entry when there are several", () => {
    const { getAllByTestId } = render(
      <StalePublicationsBanner
        org="acme"
        stale={[entry("pub-1", "API down"), entry("pub-2", "Docs down")]}
      />,
    );
    const links = getAllByTestId("stale-publication-link");
    expect(links.map((link) => link.textContent)).toEqual([
      "API down",
      "Docs down",
    ]);
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "/orgs/acme/status-pages/page-1/incidents/pub-1",
      "/orgs/acme/status-pages/page-1/incidents/pub-2",
    ]);
  });
});
