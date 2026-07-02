import { describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  HINT_KIND_QUERY_ROOTS,
  LIVE_LAZY_POLL_MS,
  invalidateAllForOrg,
  invalidateForKinds,
  stretchWhileLive,
} from "./LiveEventsContext";

const ORG = "acme";

function seedQueries(client: QueryClient): void {
  const keys: unknown[][] = [
    ["checks", ORG, { limit: 1000 }],
    ["checks", "infinite", ORG, {}],
    ["check", ORG, "uid-1", {}],
    ["results", ORG, {}],
    ["allResults", ORG, {}],
    ["checkAvailability", ORG, "uid-1", ["24h"], undefined],
    ["incidents", ORG, {}],
    ["events", ORG, {}],
    ["jobsStats", ORG, { allOrgs: false }],
    ["backgroundJobs", ORG, {}],
    // Other org + non-org keys must never be touched.
    ["checks", "other-org", {}],
    ["features"],
  ];
  for (const key of keys) {
    client.setQueryData(key, { seeded: true });
  }
}

function staleKeys(client: QueryClient): string[] {
  return client
    .getQueryCache()
    .getAll()
    .filter((q) => q.state.isInvalidated)
    .map((q) => JSON.stringify(q.queryKey));
}

describe("invalidateForKinds", () => {
  it("invalidates exactly the mapped org-scoped keys", () => {
    const client = new QueryClient();
    seedQueries(client);

    invalidateForKinds(client, ORG, ["checks"]);

    const stale = staleKeys(client);
    expect(stale).toContain(JSON.stringify(["checks", ORG, { limit: 1000 }]));
    expect(stale).toContain(JSON.stringify(["checks", "infinite", ORG, {}]));
    expect(stale).toContain(JSON.stringify(["check", ORG, "uid-1", {}]));
    expect(stale).not.toContain(JSON.stringify(["results", ORG, {}]));
    expect(stale).not.toContain(JSON.stringify(["checks", "other-org", {}]));
    expect(stale).not.toContain(JSON.stringify(["features"]));
  });

  it("maps results to every result-shaped cache", () => {
    const client = new QueryClient();
    seedQueries(client);

    invalidateForKinds(client, ORG, ["results"]);

    const stale = staleKeys(client);
    expect(stale).toContain(JSON.stringify(["results", ORG, {}]));
    expect(stale).toContain(JSON.stringify(["allResults", ORG, {}]));
    expect(stale).toContain(
      JSON.stringify(["checkAvailability", ORG, "uid-1", ["24h"], undefined]),
    );
    expect(stale).not.toContain(JSON.stringify(["incidents", ORG, {}]));
  });

  it("ignores unknown kinds", () => {
    const client = new QueryClient();
    seedQueries(client);

    invalidateForKinds(client, ORG, ["mystery"]);

    expect(staleKeys(client)).toEqual([]);
  });

  it("covers every advertised kind with at least one root", () => {
    for (const kind of ["checks", "results", "incidents", "events", "jobs"]) {
      expect(HINT_KIND_QUERY_ROOTS[kind]?.length ?? 0).toBeGreaterThan(0);
    }
  });
});

describe("invalidateAllForOrg", () => {
  it("invalidates every org key and nothing else", () => {
    const client = new QueryClient();
    seedQueries(client);

    invalidateAllForOrg(client, ORG);

    const stale = staleKeys(client);
    expect(stale).toContain(JSON.stringify(["checks", ORG, { limit: 1000 }]));
    expect(stale).toContain(JSON.stringify(["jobsStats", ORG, { allOrgs: false }]));
    expect(stale).toContain(JSON.stringify(["events", ORG, {}]));
    expect(stale).not.toContain(JSON.stringify(["checks", "other-org", {}]));
    expect(stale).not.toContain(JSON.stringify(["features"]));
  });
});

describe("stretchWhileLive", () => {
  it("keeps the base interval when not live", () => {
    expect(stretchWhileLive(30_000, false)).toBe(30_000);
  });

  it("stretches to the lazy safety net when live", () => {
    expect(stretchWhileLive(30_000, true)).toBe(LIVE_LAZY_POLL_MS);
  });

  it("never shrinks an interval already slower than the safety net", () => {
    expect(stretchWhileLive(LIVE_LAZY_POLL_MS * 2, true)).toBe(
      LIVE_LAZY_POLL_MS * 2,
    );
  });
});
