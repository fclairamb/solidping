/**
 * @vitest-environment jsdom
 *
 * GHSA-c9qq-4m99-c7f6 (spec 2026-11-11-05): the browser-generated region key
 * must never reach PostHog. posthog-js keeps any element under a
 * `ph-no-capture` ancestor out of session replay and autocapture, and the
 * dialog pauses the recording while it is open.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "@/i18n";

const IDENTITY = "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ";

vi.mock("@/lib/sealing", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/sealing")>()),
  canSealInBrowser: () => true,
  generateRegionKey: async () => ({ identity: IDENTITY, recipient: "age1recipient", fingerprint: "fp" }),
}));

const recording = vi.hoisted(() => ({ calls: [] as string[] }));
vi.mock("@/lib/analytics", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/analytics")>()),
  pauseSessionRecording: () => recording.calls.push("pause"),
  resumeSessionRecording: () => recording.calls.push("resume"),
}));

import { RegionKeyCell } from "./region-key-cell";
import type { PrivateRegion } from "@/api/hooks";

afterEach(() => {
  cleanup();
  recording.calls.length = 0;
});

const region = { region: "@paris", slug: "paris", keyMode: "perAgent" } as unknown as PrivateRegion;

function renderCell() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <RegionKeyCell org="acme" region={region} />
    </QueryClientProvider>,
  );
}

function hasNoCaptureAncestor(element: Element): boolean {
  return element.closest(".ph-no-capture") !== null;
}

describe("RegionKeyCell region key dialog", () => {
  it("keeps the identity and the env line under a ph-no-capture ancestor", async () => {
    renderCell();
    fireEvent.click(screen.getByTestId("region-key-generate-button"));

    const secret = await screen.findByTestId("region-key-secret");
    expect(secret.textContent).toBe(IDENTITY);
    expect(hasNoCaptureAncestor(secret)).toBe(true);
    expect(secret.hasAttribute("data-secret-text")).toBe(true);

    const envLine = screen.getByTestId("region-key-env-line");
    expect(envLine.tagName).toBe("PRE");
    expect(envLine.textContent).toContain(IDENTITY);
    expect(hasNoCaptureAncestor(envLine)).toBe(true);

    // The whole dialog, buttons included, is blocked.
    expect(screen.getByTestId("region-key-dialog").classList.contains("ph-no-capture")).toBe(true);
    expect(hasNoCaptureAncestor(screen.getByTestId("region-key-copy"))).toBe(true);

    // No text node carrying the identity sits outside a blocked element.
    for (const el of document.querySelectorAll("body *")) {
      const own = [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent?.includes(IDENTITY));
      if (own) expect(hasNoCaptureAncestor(el)).toBe(true);
    }
  });

  it("pauses session replay while the dialog is open", async () => {
    renderCell();
    expect(recording.calls).toEqual([]);
    fireEvent.click(screen.getByTestId("region-key-generate-button"));
    await screen.findByTestId("region-key-secret");
    expect(recording.calls).toEqual(["pause"]);

    fireEvent.click(screen.getByTestId("region-key-cancel"));
    await waitFor(() => expect(recording.calls).toEqual(["pause", "resume"]));
  });
});

describe("RegionKeyCell advisory notice (GHSA-c9qq-4m99-c7f6)", () => {
  const sharedRegion = (createdAt: string) =>
    ({
      region: "@paris",
      slug: "paris",
      keyMode: "shared",
      agentsWithRegionKey: 1,
      regionKey: { recipient: "age1x", fingerprint: `fp-${createdAt}`, createdAt },
    }) as unknown as PrivateRegion;

  function renderShared(createdAt: string) {
    return render(
      <QueryClientProvider client={new QueryClient()}>
        <RegionKeyCell org="acme" region={sharedRegion(createdAt)} />
      </QueryClientProvider>,
    );
  }

  afterEach(() => window.localStorage.clear());

  it("asks to rotate a key created before the fix, once per key", () => {
    renderShared("2026-11-10T12:00:00Z");
    const notice = screen.getByTestId("region-key-advisory-paris");
    expect(notice.textContent).toContain("GHSA-c9qq-4m99-c7f6");

    fireEvent.click(screen.getByTestId("region-key-advisory-dismiss-paris"));
    expect(screen.queryByTestId("region-key-advisory-paris")).toBeNull();

    cleanup();
    renderShared("2026-11-10T12:00:00Z");
    expect(screen.queryByTestId("region-key-advisory-paris")).toBeNull();
  });

  it("says nothing for a key created after the fix", () => {
    renderShared("2026-12-01T00:00:00Z");
    expect(screen.queryByTestId("region-key-advisory-paris")).toBeNull();
  });
});
