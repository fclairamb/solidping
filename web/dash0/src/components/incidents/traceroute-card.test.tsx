/**
 * @vitest-environment jsdom
 *
 * Component test for the incident path-trace card (spec 2026-08-21-10). The
 * assertions that matter most are the HONESTY ones: an operator must never be
 * able to read "the routers went silent" out of a capture whose probe mode
 * could not have heard them in the first place.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "@/i18n";
import { IncidentTracerouteCard } from "./traceroute-card";
import type { IncidentDetail, TracerouteCapture } from "@/api/hooks";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function icmpCapture(): TracerouteCapture {
  return {
    version: 1,
    mode: "icmp",
    hopAddressesVisible: true,
    host: "acme.com",
    address: "192.0.2.10",
    family: "ipv4",
    port: 443,
    region: "eu2",
    trigger: "incident-open",
    rounds: 3,
    maxHops: 30,
    startedAt: "2026-08-21T12:00:00Z",
    durationMs: 4200,
    complete: true,
    hops: [
      {
        ttl: 1,
        address: "10.0.0.1",
        hostname: "gw.acme.com",
        sent: 3,
        received: 3,
        lossPct: 0,
        rttAvgMs: 1.5,
      },
      { ttl: 2, sent: 3, received: 0, lossPct: 100 },
      {
        ttl: 3,
        address: "192.0.2.10",
        sent: 3,
        received: 3,
        lossPct: 0,
        rttAvgMs: 18.2,
        final: true,
      },
    ],
  };
}

function incidentWith(kind: string): IncidentDetail {
  return {
    uid: "incident-1",
    attachments: [
      {
        uid: "file-1",
        kind,
        name: "trace.json",
        mimeType: "application/json",
        size: 512,
        downloadUrl: "/pub/files/file-1?exp=1&sig=abc",
      },
    ],
  };
}

function stubFetch(capture: TracerouteCapture | null) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      capture
        ? ({ ok: true, json: async () => capture } as unknown as Response)
        : ({ ok: false, status: 404 } as unknown as Response),
    ),
  );
}

function renderCard(incident: IncidentDetail) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  return render(
    <QueryClientProvider client={client}>
      <IncidentTracerouteCard incident={incident} />
    </QueryClientProvider>,
  );
}

describe("IncidentTracerouteCard", () => {
  it("renders nothing when the incident has no traceroute attachment", () => {
    stubFetch(icmpCapture());
    renderCard({ uid: "incident-1", attachments: [] });
    expect(screen.queryByTestId("incident-traceroute-card")).toBeNull();
  });

  it("ignores attachments of other kinds", () => {
    stubFetch(icmpCapture());
    renderCard(incidentWith("screenshot"));
    expect(screen.queryByTestId("incident-traceroute-card")).toBeNull();
  });

  it("renders one row per hop, with loss and average RTT", async () => {
    stubFetch(icmpCapture());
    renderCard(incidentWith("traceroute"));

    await waitFor(() =>
      expect(screen.getByTestId("incident-traceroute-table")).toBeTruthy(),
    );

    const first = screen.getByTestId("incident-traceroute-hop-1");
    expect(first.textContent).toContain("gw.acme.com");
    expect(first.textContent).toContain("10.0.0.1");
    expect(first.textContent).toContain("1.5");

    // The counts sit next to the percentage so 100% of one probe cannot be
    // mistaken for 100% of three.
    const silent = screen.getByTestId("incident-traceroute-hop-2");
    expect(silent.textContent).toContain("100");
    expect(silent.textContent).toContain("0/3");

    expect(screen.getByTestId("incident-traceroute-hop-3")).toBeTruthy();

    // Mode and region are labelled, per the spec's surfacing requirement.
    expect(
      screen.getByTestId("incident-traceroute-mode").textContent,
    ).toContain("ICMP");
    expect(
      screen.getByTestId("incident-traceroute-region").textContent,
    ).toContain("eu2");
  });

  // The region badge is driven by the CAPTURE's own field, not by the
  // attachment metadata — which is exactly why the server stamps the region
  // into an agent-uploaded capture (attachments.StampTracerouteRegion). An
  // unstamped capture renders no vantage point at all, and on a
  // private-location incident that is the whole question.
  it("renders no region badge when the capture names no region", async () => {
    const capture = icmpCapture();
    capture.region = undefined;

    stubFetch(capture);
    renderCard(incidentWith("traceroute"));

    await waitFor(() =>
      expect(screen.getByTestId("incident-traceroute-table")).toBeTruthy(),
    );

    expect(screen.queryByTestId("incident-traceroute-region")).toBeNull();

    // Positive control: the same card WITH a region does render the badge, so
    // the absence above is the missing field and not a broken selector.
    cleanup();
    stubFetch(icmpCapture());
    renderCard(incidentWith("traceroute"));

    await waitFor(() =>
      expect(screen.getByTestId("incident-traceroute-region")).toBeTruthy(),
    );
  });

  it("says 'no reply' for a silent hop when the mode CAN see hop addresses", async () => {
    stubFetch(icmpCapture());
    renderCard(incidentWith("traceroute"));

    await waitFor(() =>
      expect(screen.getByTestId("incident-traceroute-hop-2")).toBeTruthy(),
    );

    expect(
      screen.getByTestId("incident-traceroute-hop-2").textContent,
    ).toContain("no reply");
    expect(screen.queryByTestId("incident-traceroute-blind-notice")).toBeNull();
  });

  // THE HONESTY TEST. A TCP-mode capture cannot observe intermediate routers at
  // all, so an empty address column means "not observable", never "no reply" —
  // and the card must say so out loud rather than leaving blanks that read as
  // a broken path.
  it("never claims 'no reply' for a mode that cannot see hop addresses", async () => {
    const capture = icmpCapture();
    capture.mode = "tcp";
    capture.hopAddressesVisible = false;
    capture.hops = [{ ttl: 1, sent: 3, received: 0, lossPct: 100 }];

    stubFetch(capture);
    renderCard(incidentWith("traceroute"));

    await waitFor(() =>
      expect(
        screen.getByTestId("incident-traceroute-blind-notice"),
      ).toBeTruthy(),
    );

    const hop = screen.getByTestId("incident-traceroute-hop-1");
    expect(hop.textContent).toContain("not observable");
    expect(hop.textContent).not.toContain("no reply");

    expect(
      screen.getByTestId("incident-traceroute-blind-notice").textContent,
    ).toContain("not observable");
  });

  it("flags a trace that never reached the target", async () => {
    const capture = icmpCapture();
    capture.complete = false;
    capture.truncated = true;

    stubFetch(capture);
    renderCard(incidentWith("traceroute"));

    await waitFor(() =>
      expect(screen.getByTestId("incident-traceroute-incomplete")).toBeTruthy(),
    );

    expect(
      screen.getByTestId("incident-traceroute-incomplete").textContent,
    ).toContain("time budget");
  });

  it("shows a real error, not a raw i18n key, when the capture cannot be fetched", async () => {
    stubFetch(null);
    renderCard(incidentWith("traceroute"));

    await waitFor(() =>
      expect(screen.getByTestId("incident-traceroute-error")).toBeTruthy(),
    );

    const alert = screen.getByTestId("incident-traceroute-error");
    expect(alert.textContent).toContain("could not be loaded");
    expect(alert.textContent).not.toContain("detail.traceroute");
  });

  it("captions the trace as taken after the failure, never as the failure itself", async () => {
    stubFetch(icmpCapture());
    renderCard(incidentWith("traceroute"));

    await waitFor(() =>
      expect(screen.getByTestId("incident-traceroute-caption")).toBeTruthy(),
    );

    const caption =
      screen.getByTestId("incident-traceroute-caption").textContent ?? "";
    expect(caption).toContain("after the failing probe was already reported");
  });
});
