/**
 * @vitest-environment jsdom
 *
 * Spec 2026-09-02-05: the checks list mounts
 * `<StalePublicationsBanner stale={useStalePublications(org)} />`. This drives
 * that pairing against stubbed API responses: the banner must stay silent
 * while the incident behind the publication is still live (the `?stale=true`
 * slice is empty), and also when an older backend ignores the filter and
 * answers the unfiltered listing, where the client re-applies the rule.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { PropsWithChildren } from "react";
import "@/i18n";

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock("./client", () => ({
  apiFetch: mocks.apiFetch,
  getToken: () => "test-token",
}));

const { useStalePublications } = await import("./hooks");
const { StalePublicationsBanner } =
  await import("@/components/shared/stale-publications-banner");

const PAGE_UID = "page-1";

function publication(overrides: Record<string, unknown> = {}) {
  return {
    uid: "pub-1",
    statusPageUid: PAGE_UID,
    incidentUid: "inc-1",
    title: "Some services are experiencing issues",
    state: "identified",
    autoCreated: false,
    humanTouched: false,
    publishedAt: "2026-08-23T12:17:51Z",
    createdAt: "2026-08-23T12:17:51Z",
    updatedAt: "2026-08-23T12:18:21Z",
    stale: true,
    ...overrides,
  };
}

/** Answers the two endpoints the banner reads; `incidents` is the page's slice. */
function stubApi(incidents: unknown[]) {
  mocks.apiFetch.mockImplementation(async (url: string) => {
    if (url === "/api/v1/orgs/acme/status-pages") {
      return { data: [{ uid: PAGE_UID, name: "Public status" }] };
    }
    if (
      url.startsWith(`/api/v1/orgs/acme/status-pages/${PAGE_UID}/incidents?`)
    ) {
      return { data: incidents };
    }
    throw new Error(`unexpected request ${url}`);
  });
}

let client: QueryClient;

function wrapper({ children }: PropsWithChildren) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function ChecksListBanner() {
  const stale = useStalePublications("acme");
  return (
    <div data-testid="host">
      <StalePublicationsBanner org="acme" stale={stale} />
    </div>
  );
}

/**
 * Waits until the page's publications slice is in the cache, so a "nothing
 * rendered" assertion runs after the data arrived rather than before.
 */
async function waitForPublicationsFetch() {
  await waitFor(() =>
    expect(
      client.getQueryState([
        "incidentPublications",
        "acme",
        PAGE_UID,
        true,
        true,
      ])?.status,
    ).toBe("success"),
  );
}

describe("stale-publication banner on the checks list", () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset();
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  });

  afterEach(() => {
    cleanup();
  });

  it("reports a publication the server called stale (positive control)", async () => {
    stubApi([publication()]);
    const { result } = renderHook(() => useStalePublications("acme"), {
      wrapper,
    });
    await waitFor(() => expect(result.current).toHaveLength(1));
    expect(result.current[0].page.uid).toBe(PAGE_UID);
    expect(result.current[0].publication.uid).toBe("pub-1");
  });

  it("stays silent while the linked incident is still live", async () => {
    // The `?stale=true` slice is empty: the publication is open because the
    // incident behind it is still ongoing.
    stubApi([]);
    const { getByTestId } = render(<ChecksListBanner />, { wrapper });
    await waitForPublicationsFetch();
    expect(getByTestId("host").childElementCount).toBe(0);
  });

  it("stays silent when the server ignores the stale filter", async () => {
    // An older backend answers the unfiltered listing, with no `stale` key.
    stubApi([publication({ stale: undefined })]);
    const { result } = renderHook(() => useStalePublications("acme"), {
      wrapper,
    });
    await waitForPublicationsFetch();
    expect(result.current).toEqual([]);

    cleanup();
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { getByTestId } = render(<ChecksListBanner />, { wrapper });
    await waitForPublicationsFetch();
    expect(getByTestId("host").childElementCount).toBe(0);
  });
});
