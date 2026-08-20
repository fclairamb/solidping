import { describe, expect, it } from "vitest";

import {
  CHECK_TYPE_IDENTITY,
  getCheckTypeIdentity,
  iconToneClassName,
} from "@/components/shared/check-type-identity";
import { checkTypeDocsAnchors } from "@/components/shared/check-type-docs-anchors";
import type { CheckType } from "@/components/checks/form/types/common";

// ALL_CHECK_TYPES is the drift guard for the `CheckType` union itself: a
// `Record<CheckType, true>` object literal requires EVERY member of the
// union as a key and rejects any key that isn't one — so if common.ts gains
// (or renames) a check type and this literal isn't updated to match, `tsc`
// fails to compile (caught by `make build-dash0`, per the spec's "a new
// check type fails the build until it gets an identity"). Object.keys(...)
// then gives the real, exhaustive member list to iterate at test runtime.
const ALL_CHECK_TYPES: Record<CheckType, true> = {
  http: true,
  tcp: true,
  icmp: true,
  dns: true,
  ssl: true,
  heartbeat: true,
  email: true,
  domain: true,
  smtp: true,
  udp: true,
  ssh: true,
  pop3: true,
  imap: true,
  websocket: true,
  postgresql: true,
  mysql: true,
  redis: true,
  mongodb: true,
  ftp: true,
  sftp: true,
  js: true,
  mssql: true,
  oracle: true,
  clickhouse: true,
  grpc: true,
  kafka: true,
  mqtt: true,
  a2s: true,
  minecraft: true,
  rabbitmq: true,
  snmp: true,
  prometheus: true,
  docker: true,
  browser: true,
  freebox_line: true,
  dnsbl: true,
  sip: true,
  ntp: true,
  rdp: true,
  sleep: true,
};
const CHECK_TYPES = Object.keys(ALL_CHECK_TYPES) as CheckType[];

// TONE_SHAPE matches the class shape every registry entry's tone must have:
// `bg-{hue}-500/10 text-{hue}-700 dark:text-{hue}-400 border-{hue}-500/25`,
// same hue throughout.
const TONE_SHAPE = /^bg-([a-z]+)-500\/10 text-\1-700 dark:text-\1-400 border-\1-500\/25$/;

describe("positive controls", () => {
  it("actually enumerated check types from the union", () => {
    expect(CHECK_TYPES.length).toBeGreaterThan(30);
  });

  it("actually found docs-anchor keys", () => {
    expect(Object.keys(checkTypeDocsAnchors).length).toBeGreaterThan(30);
  });

  it("the tone shape regex actually matches a real tone", () => {
    expect(TONE_SHAPE.test("bg-blue-500/10 text-blue-700 dark:text-blue-400 border-blue-500/25")).toBe(
      true,
    );
  });

  it("the tone shape regex actually rejects a mismatched-hue tone", () => {
    expect(
      TONE_SHAPE.test("bg-blue-500/10 text-red-700 dark:text-blue-400 border-blue-500/25"),
    ).toBe(false);
  });
});

describe("every CheckType union member has a registry entry", () => {
  it.each(CHECK_TYPES)("%s resolves to a registered identity, not the fallback", (type) => {
    expect(
      CHECK_TYPE_IDENTITY[type],
      `${type} is a member of the CheckType union but has no CHECK_TYPE_IDENTITY entry`,
    ).toBeDefined();
  });
});

describe("every checkTypeDocsAnchors key has a registry entry", () => {
  const anchorTypes = Object.keys(checkTypeDocsAnchors);

  it.each(anchorTypes)("%s resolves to a registered identity, not the fallback", (type) => {
    expect(
      CHECK_TYPE_IDENTITY[type],
      `${type} is a key of checkTypeDocsAnchors but has no CHECK_TYPE_IDENTITY entry`,
    ).toBeDefined();
  });
});

describe("every registered tone matches the expected class shape", () => {
  const entries = Object.entries(CHECK_TYPE_IDENTITY) as [string, { tone: string }][];

  it.each(entries)("%s's tone matches bg/text/dark:text/border on one hue", (_type, identity) => {
    expect(identity.tone).toMatch(TONE_SHAPE);
  });
});

describe("the five shipped tones never change", () => {
  // Pinned literally (not derived from a shared constant) so a future edit
  // to check-type-identity.tsx that accidentally repoints one of these five
  // still fails this test — these are in users' muscle memory.
  const SHIPPED: [string, string][] = [
    ["http", "bg-blue-500/10 text-blue-700 dark:text-blue-400 border-blue-500/25"],
    ["tcp", "bg-cyan-500/10 text-cyan-700 dark:text-cyan-400 border-cyan-500/25"],
    ["dns", "bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/25"],
    ["icmp", "bg-purple-500/10 text-purple-700 dark:text-purple-400 border-purple-500/25"],
    ["ssl", "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400 border-emerald-500/25"],
  ];

  it.each(SHIPPED)("%s keeps its shipped tone", (type, tone) => {
    expect(getCheckTypeIdentity(type).tone).toBe(tone);
  });
});

describe("legacy aliases resolve to their canonical type's identity", () => {
  it("https matches http", () => {
    expect(getCheckTypeIdentity("https")).toEqual(getCheckTypeIdentity("http"));
  });

  it("ping matches icmp", () => {
    expect(getCheckTypeIdentity("ping")).toEqual(getCheckTypeIdentity("icmp"));
  });

  it("tls matches ssl", () => {
    expect(getCheckTypeIdentity("tls")).toEqual(getCheckTypeIdentity("ssl"));
  });
});

describe("getCheckTypeIdentity never returns undefined", () => {
  it("degrades gracefully for an unmapped type: plain tone, raw name, neutral icon", () => {
    const identity = getCheckTypeIdentity("some_future_type");
    expect(identity).toBeDefined();
    expect(identity.tone).toBe("");
    expect(identity.label).toBe("some_future_type");
    expect(identity.icon).toBeDefined();
  });

  it("degrades gracefully for undefined", () => {
    const identity = getCheckTypeIdentity(undefined);
    expect(identity).toBeDefined();
    expect(identity.tone).toBe("");
    expect(identity.icon).toBeDefined();
  });

  it("is case-insensitive on lookup", () => {
    expect(getCheckTypeIdentity("HTTP")).toEqual(getCheckTypeIdentity("http"));
  });
});

describe("iconToneClassName", () => {
  it("keeps only the text-color classes from a tone string", () => {
    expect(
      iconToneClassName("bg-blue-500/10 text-blue-700 dark:text-blue-400 border-blue-500/25"),
    ).toBe("text-blue-700 dark:text-blue-400");
  });

  it("returns empty for the fallback's empty tone", () => {
    expect(iconToneClassName("")).toBe("");
  });
});
