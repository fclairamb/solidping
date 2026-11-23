/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const captured: [string, Record<string, unknown>][] = [];
vi.mock("@/lib/analytics", () => ({
  captureEventQueued: (event: string, props: Record<string, unknown>) =>
    captured.push([event, props]),
}));

import {
  __resetSessionDiagnosticsForTests,
  forgetSessionCleared,
  isStorageWritable,
  recordSessionCleared,
  recordSessionPersistFailed,
  REFRESH_TOKEN_KEY,
  reportSessionMissingOnLoad,
  SESSION_CLEARED_KEY,
  sessionMissingProperties,
  TOKEN_KEY,
  type SessionMissingEnv,
} from "./session-diagnostics";
import { SESSION_ORG_KEY } from "./session-org";

function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial));
  return {
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
    data,
  };
}

const ACCESS = "eyJhbGciOiJIUzI1NiJ9.acme-access-token";
const REFRESH = "acme-refresh-token-value";

function env(overrides: Partial<SessionMissingEnv> = {}): SessionMissingEnv {
  return {
    storage: memoryStorage(),
    referrer: "",
    hasOpener: false,
    standalone: false,
    now: 1_000_000,
    ...overrides,
  };
}

beforeEach(() => {
  captured.length = 0;
  __resetSessionDiagnosticsForTests();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("sessionMissingProperties", () => {
  it("reports each session key as a boolean, never its value", () => {
    const storage = memoryStorage({
      [TOKEN_KEY]: ACCESS,
      [REFRESH_TOKEN_KEY]: REFRESH,
      [SESSION_ORG_KEY]: "acme",
    });
    const props = sessionMissingProperties("org-login", env({ storage }));
    expect(props).toMatchObject({ where: "org-login", token: true, refresh: true, org: true });
    const serialized = JSON.stringify(props);
    expect(serialized).not.toContain(ACCESS);
    expect(serialized).not.toContain(REFRESH);
    expect(serialized).not.toContain("acme");
    for (const key of ["token", "refresh", "org", "storageAvailable", "storageWritable"]) {
      expect(typeof props[key]).toBe("boolean");
    }
  });

  it("reports an empty storage, the referrer origin only, opener and standalone", () => {
    const props = sessionMissingProperties(
      "root-redirect",
      env({
        referrer: "https://www.acme.test/pricing?ref=secret#x",
        hasOpener: true,
        standalone: true,
      }),
    );
    expect(props).toEqual({
      where: "root-redirect",
      token: false,
      refresh: false,
      org: false,
      storageAvailable: true,
      storageWritable: true,
      lastClearReason: null,
      lastClearAgeSeconds: null,
      referrerOrigin: "https://www.acme.test",
      hasOpener: true,
      standalone: true,
    });
  });

  it("repeats the last clear reason and its age", () => {
    const storage = memoryStorage();
    recordSessionCleared("refresh-rejected", storage, 1_000_000 - 42_000);
    const props = sessionMissingProperties("root-redirect", env({ storage }));
    expect(props.lastClearReason).toBe("refresh-rejected");
    expect(props.lastClearAgeSeconds).toBe(42);
  });

  it("flags an unavailable or read-only storage", () => {
    expect(sessionMissingProperties("org-login", env({ storage: null }))).toMatchObject({
      storageAvailable: false,
      storageWritable: false,
      token: false,
    });
    const throwing = {
      getItem: () => {
        throw new Error("SecurityError");
      },
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
      removeItem: () => {},
    };
    expect(sessionMissingProperties("org-login", env({ storage: throwing }))).toMatchObject({
      storageAvailable: true,
      storageWritable: false,
      token: false,
      lastClearReason: null,
    });
    expect(isStorageWritable(throwing)).toBe(false);
  });

  it("ignores a malformed clear marker and a bad referrer", () => {
    const storage = memoryStorage({ [SESSION_CLEARED_KEY]: "{not json" });
    const props = sessionMissingProperties("org-login", env({ storage, referrer: "not a url" }));
    expect(props.lastClearReason).toBeNull();
    expect(props.referrerOrigin).toBeNull();
  });
});

describe("recordSessionCleared", () => {
  it("captures session_cleared with the reason and keeps a marker", () => {
    const storage = memoryStorage();
    recordSessionCleared("logout", storage, 5);
    expect(captured).toEqual([["session_cleared", { reason: "logout" }]]);
    expect(JSON.parse(storage.data.get(SESSION_CLEARED_KEY)!)).toEqual({ reason: "logout", at: 5 });
    forgetSessionCleared(storage);
    expect(storage.data.has(SESSION_CLEARED_KEY)).toBe(false);
  });

  it("never throws when storage does", () => {
    const throwing = {
      getItem: () => null,
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
      removeItem: () => {
        throw new Error("SecurityError");
      },
    };
    expect(() => recordSessionCleared("api-401", throwing)).not.toThrow();
    expect(() => forgetSessionCleared(throwing)).not.toThrow();
    expect(captured).toEqual([["session_cleared", { reason: "api-401" }]]);
  });
});

describe("recordSessionPersistFailed", () => {
  it("reports the error name only", () => {
    const error = new DOMException(`cannot store ${ACCESS}`, "QuotaExceededError");
    recordSessionPersistFailed(error);
    expect(captured).toEqual([["session_persist_failed", { errorName: "QuotaExceededError" }]]);
  });
});

describe("reportSessionMissingOnLoad", () => {
  it("captures once per page load and place", () => {
    reportSessionMissingOnLoad("org-login", env());
    reportSessionMissingOnLoad("org-login", env());
    reportSessionMissingOnLoad("root-redirect", env());
    expect(captured.map(([event, props]) => [event, props.where])).toEqual([
      ["session_missing_on_load", "org-login"],
      ["session_missing_on_load", "root-redirect"],
    ]);
  });

  it("reads the real environment by default", () => {
    localStorage.setItem(TOKEN_KEY, ACCESS);
    reportSessionMissingOnLoad("org-login");
    localStorage.removeItem(TOKEN_KEY);
    expect(captured[0][1]).toMatchObject({ token: true, refresh: false, hasOpener: false });
    expect(JSON.stringify(captured)).not.toContain(ACCESS);
  });
});
