import { describe, expect, it } from "vitest";
import {
  CREDENTIAL_QUERY_PARAMS,
  REDACTED,
  redactCapturedNetworkRequest,
  redactCredentialsBeforeSend,
  redactCredentialsInText,
  redactUrl,
} from "./analytics-redaction";

// Realistic shapes: the access token is a JWT (three base64url parts), the
// refresh token an opaque random string, exactly what setSession() stores
// under `solidping_session_token` / `solidping_refresh_token`.
const JWT =
  "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
  "eyJzdWIiOiJ1c2VyLXVpZCIsIm9yZyI6ImFjbWUiLCJleHAiOjE3OTAwMDAwMDB9." +
  "Zm9vYmFyLXNpZ25hdHVyZS1ub3QtcmVhbA";
const REFRESH = "rt_9f3c1a7e5b2d4c6f8a0e1b3d5f7a9c2e4b6d8f0a1c3e5b7d9f2a4c6e8b0d1f3a";

// A shorter stand-in credential for the exception/free-text redaction tests
// below — a real JWT would work identically, this is just easier to read
// inline in an error message.
const JWT_PLACEHOLDER = "eyJhbGciOiJIUzI1NiJ9.payload.sig";

// The URL a federated login produced before spec 2026-09-25-12 (and still does
// when an old pod answers the callback during a rolling deploy), and its
// org-less variant.
const ORG_REDIRECT = `https://solidping.example/d/orgs/acme?access_token=${JWT}&expires_in=900&org=acme&refresh_token=${REFRESH}`;
const NO_ORG_REDIRECT = `https://solidping.example/d/no-org?access_token=${JWT}&expires_in=900&membershipPending=acme`;

describe("redactUrl", () => {
  it("redacts every listed credential param", () => {
    for (const param of CREDENTIAL_QUERY_PARAMS) {
      const out = redactUrl(`/d/x?${param}=s3cret&keep=1`);
      expect(out).toBe(`/d/x?${param}=${REDACTED}&keep=1`);
    }
  });

  it("strips the OAuth handoff tokens but keeps the rest of the URL intact", () => {
    const out = redactUrl(ORG_REDIRECT);
    expect(out).toBe(
      `https://solidping.example/d/orgs/acme?access_token=${REDACTED}&expires_in=900&org=acme&refresh_token=${REDACTED}`,
    );
    expect(out).not.toContain(JWT);
    expect(out).not.toContain(REFRESH);

    const noOrg = redactUrl(NO_ORG_REDIRECT);
    expect(noOrg).not.toContain(JWT);
    expect(noOrg).toContain("membershipPending=acme");
    expect(noOrg).toContain("expires_in=900");
  });

  it("matches keys case-insensitively and through percent-encoding", () => {
    expect(redactUrl("/x?TempToken=abc")).toBe(`/x?TempToken=${REDACTED}`);
    expect(redactUrl("/x?temptoken=abc")).toBe(`/x?temptoken=${REDACTED}`);
    expect(redactUrl("/x?access%5Ftoken=abc")).toBe(`/x?access%5Ftoken=${REDACTED}`);
  });

  it("redacts repeated params", () => {
    expect(redactUrl("/x?token=a&token=b&org=acme&token=c")).toBe(
      `/x?token=${REDACTED}&token=${REDACTED}&org=acme&token=${REDACTED}`,
    );
  });

  it("handles relative URLs, bare paths and URLs with no query", () => {
    expect(redactUrl("/d/orgs/acme")).toBe("/d/orgs/acme");
    expect(redactUrl("orgs/acme?code=xyz")).toBe(`orgs/acme?code=${REDACTED}`);
    expect(redactUrl("?state=xyz")).toBe(`?state=${REDACTED}`);
    expect(redactUrl("https://solidping.example/d/orgs/acme/checks")).toBe(
      "https://solidping.example/d/orgs/acme/checks",
    );
    expect(redactUrl("")).toBe("");
  });

  it("leaves a plain fragment untouched", () => {
    expect(redactUrl("/d/docs?org=acme#section-2")).toBe("/d/docs?org=acme#section-2");
    expect(redactUrl("/d/#/orgs/acme")).toBe("/d/#/orgs/acme");
    // A credential-looking word in a plain fragment is not a param.
    expect(redactUrl("/d/x#token")).toBe("/d/x#token");
  });

  it("redacts a token-bearing fragment (implicit-flow shape and hash routes)", () => {
    const out = redactUrl(
      `https://solidping.example/d/cb#access_token=${JWT}&refresh_token=${REFRESH}&token_type=bearer`,
    );
    expect(out).toBe(
      `https://solidping.example/d/cb#access_token=${REDACTED}&refresh_token=${REDACTED}&token_type=bearer`,
    );
    expect(redactUrl("/d/#/login?token=abc&x=1")).toBe(`/d/#/login?token=${REDACTED}&x=1`);
    // Query and fragment both carry one.
    expect(redactUrl("/x?code=a#id_token=b")).toBe(`/x?code=${REDACTED}#id_token=${REDACTED}`);
  });

  it("redacts one-time codes and CSRF state from provider callbacks", () => {
    expect(redactUrl("/d/auth/slack/complete?code=4f8a.b9c1&state=nonce123")).toBe(
      `/d/auth/slack/complete?code=${REDACTED}&state=${REDACTED}`,
    );
    // Spec 2026-09-25-12's one-time handoff code, in the exact shapes the
    // provider callbacks redirect to (join_policy.go handoffRedirect).
    expect(redactUrl("/d/auth/complete?code=hc_abc123")).toBe(`/d/auth/complete?code=${REDACTED}`);
    const handoff = "https://solidping.example/d/auth/complete?code=Zm9vYmFyYmF6cXV4LWJhc2U2NHVybC1jb2RlLTQzY2hhcnM&membershipPending=acme";
    expect(redactUrl(handoff)).toBe(
      `https://solidping.example/d/auth/complete?code=${REDACTED}&membershipPending=acme`,
    );
  });

  it("redacts a token-bearing URL nested, percent-encoded, in a harmless param", () => {
    const nested = encodeURIComponent(`/d/orgs/acme?access_token=${JWT}&org=acme`);
    const out = redactUrl(`/d/login?returnTo=${nested}`);
    expect(out).not.toContain(JWT);
    expect(decodeURIComponent(out)).toContain(`access_token=${REDACTED}`);
    expect(decodeURIComponent(out)).toContain("org=acme");
  });

  it("redacts single-use tokens carried in the path", () => {
    expect(redactUrl("https://solidping.example/d/reset-password/rp_abc123")).toBe(
      `https://solidping.example/d/reset-password/${REDACTED}`,
    );
    expect(redactUrl("/d/invite/inv_abc123?x=1")).toBe(`/d/invite/${REDACTED}?x=1`);
    expect(redactUrl("/api/v1/auth/invite/inv_abc123/accept")).toBe(
      `/api/v1/auth/invite/${REDACTED}/accept`,
    );
    expect(redactUrl("/d/confirm-registration/cr_abc123")).toBe(
      `/d/confirm-registration/${REDACTED}`,
    );
  });

  // Negative controls: nothing that is not a credential may change, byte for
  // byte, or the analytics stop being readable.
  it("does not touch harmless URLs", () => {
    for (const url of [
      "https://solidping.example/d/orgs/acme/checks?q=api&limit=50&checkUid=a,b",
      "/d/orgs/acme/incidents?status=open&page=2",
      "/d/orgs/acme/organization/invitations/9b1c",
      "/d/orgs/acme/checks/new?type=http&name=My%20API",
      "/d/orgs/acme?org=acme&expires_in=900&returnTo=%2Fd%2Forgs%2Facme%2Fchecks",
      "/d/forgot-password",
      "/d/reset-password",
      "https://solidping.example/d/orgs/acme?tokenize=1&codec=h264&statement=x",
    ]) {
      expect(redactUrl(url)).toBe(url);
    }
  });
});

describe("redactCapturedNetworkRequest", () => {
  // The exact entry that leaked: the replay network plugin records the
  // document's navigation entry with its ORIGINAL (pre-replaceState) URL.
  const navigationEntry = {
    name: ORG_REDIRECT,
    entryType: "navigation",
    initiatorType: "navigation" as const,
    startTime: 0,
    duration: 120,
    isInitial: true,
  };

  it("redacts the navigation entry URL", () => {
    const out = redactCapturedNetworkRequest(navigationEntry);
    expect(out.name).not.toContain(JWT);
    expect(out.name).not.toContain(REFRESH);
    expect(out.name).toContain("org=acme");
    // Everything else about the entry is kept.
    expect(out).toMatchObject({ entryType: "navigation", duration: 120, isInitial: true });
    // The input is not mutated.
    expect(navigationEntry.name).toBe(ORG_REDIRECT);
  });

  it("handles the partial `{ name }` posthog-js passes for replay Meta hrefs", () => {
    expect(redactCapturedNetworkRequest({ name: NO_ORG_REDIRECT }).name).not.toContain(JWT);
    expect(redactCapturedNetworkRequest({ url: ORG_REDIRECT }).url).not.toContain(REFRESH);
  });

  it("never drops a request (returning nullish would remove it from the replay)", () => {
    const out = redactCapturedNetworkRequest({ name: "/api/v1/orgs/acme/checks" });
    expect(out).toEqual({ name: "/api/v1/orgs/acme/checks" });
  });

  it("drops credential headers and keeps the others", () => {
    const out = redactCapturedNetworkRequest({
      name: "/api/v1/orgs/acme/checks",
      requestHeaders: {
        Authorization: `Bearer ${JWT}`,
        Cookie: `access_token=${JWT}`,
        "Content-Type": "application/json",
      },
      responseHeaders: { "set-cookie": `access_token=${JWT}`, "x-request-id": "r1" },
    });
    expect(out.requestHeaders).toEqual({ "Content-Type": "application/json" });
    expect(out.responseHeaders).toEqual({ "x-request-id": "r1" });
    expect(JSON.stringify(out)).not.toContain(JWT);
  });

  // Supplying maskCapturedNetworkRequestFn disables posthog-js's own body
  // scrubber, and a key-name filter would miss `recoveryCodes`, `signingSecret`
  // or a heartbeat URL inside a value. So bodies fail closed: any body that
  // still reaches the hook is dropped, harmless or not.
  it("drops captured bodies outright", () => {
    const out = redactCapturedNetworkRequest({
      name: "/api/v1/auth/refresh",
      requestBody: JSON.stringify({ refreshToken: REFRESH }),
      responseBody: JSON.stringify({
        accessToken: JWT,
        recoveryCodes: ["a1b2-c3d4"],
        signingSecret: "whsec_x",
        heartbeatUrl: "https://solidping.example/api/v1/heartbeat/acme/h?token=hb_x",
      }),
    });
    expect(out.requestBody).toBeUndefined();
    expect(out.responseBody).toBeUndefined();
    expect(out.name).toBe("/api/v1/auth/refresh");

    const harmless = redactCapturedNetworkRequest({
      name: "/api/v1/orgs/acme/checks",
      responseBody: '{"data":[]}',
    });
    expect(harmless.responseBody).toBeUndefined();
  });
});

describe("redactCredentialsBeforeSend", () => {
  it("redacts URL properties in properties, $set and $set_once", () => {
    const event = {
      event: "$pageview",
      properties: {
        $current_url: ORG_REDIRECT,
        $referrer: "https://accounts.google.com/",
        $pathname: "/d/orgs/acme",
        $session_entry_url: ORG_REDIRECT,
        checkType: "http",
      },
      $set: { $current_url: ORG_REDIRECT },
      $set_once: { $initial_current_url: ORG_REDIRECT, $initial_referrer: "$direct" },
    };

    const out = redactCredentialsBeforeSend(event)!;
    const serialized = JSON.stringify(out);
    expect(serialized).not.toContain(JWT);
    expect(serialized).not.toContain(REFRESH);
    expect(out.properties?.$current_url).toContain("org=acme");
    expect(out.properties?.$referrer).toBe("https://accounts.google.com/");
    expect(out.properties?.checkType).toBe("http");
    expect(out.$set_once?.$initial_referrer).toBe("$direct");
    // The input event is not mutated.
    expect(event.properties.$current_url).toBe(ORG_REDIRECT);
  });

  it("never drops an event, and passes null through", () => {
    const event = { event: "check_created", properties: { $current_url: "/d/orgs/acme" } };
    expect(redactCredentialsBeforeSend(event)).toEqual(event);
    expect(redactCredentialsBeforeSend(null)).toBeNull();
    expect(redactCredentialsBeforeSend({ event: "x" })).toEqual({ event: "x" });
  });
});

describe("redactCredentialsInText", () => {
  // Positive control: a token-bearing URL embedded in free text (a failed
  // fetch message, a manual "[auth] token refresh failed: <url>" log) must
  // be redacted even though the whole string is not itself a URL.
  it("redacts a credential param embedded in free text", () => {
    const out = redactCredentialsInText(
      `[auth] token refresh failed: fetch https://solidping.example/d/auth/complete?code=${JWT_PLACEHOLDER}&org=acme failed`,
    );
    expect(out).toBe(
      `[auth] token refresh failed: fetch https://solidping.example/d/auth/complete?code=${REDACTED}&org=acme failed`,
    );
  });

  it("redacts every listed credential param found in text, wherever it sits", () => {
    for (const param of CREDENTIAL_QUERY_PARAMS) {
      expect(redactCredentialsInText(`TypeError: bad response for ${param}=abc123 request`)).toBe(
        `TypeError: bad response for ${param}=${REDACTED} request`,
      );
    }
  });

  it("redacts a single-use path segment embedded in text", () => {
    const out = redactCredentialsInText(
      "Failed to load resource: the server responded with a status of 404 () at /d/reset-password/rp_abc123",
    );
    expect(out).toBe(
      `Failed to load resource: the server responded with a status of 404 () at /d/reset-password/${REDACTED}`,
    );
  });

  // Negative control: harmless text — including a word that merely CONTAINS
  // a param name as a substring — is unchanged, byte for byte.
  it("does not touch harmless text", () => {
    for (const text of [
      "TypeError: Cannot read properties of undefined (reading 'map')",
      "Failed to fetch",
      "[auth] token refresh failed: network error",
      "NetworkError when attempting to fetch resource /api/v1/orgs/acme/checks",
      "tokenize=1 is not a credential param",
      "",
    ]) {
      expect(redactCredentialsInText(text)).toBe(text);
    }
  });

  it("passes non-string input through unchanged", () => {
    // @ts-expect-error exercising the runtime guard for non-string input
    expect(redactCredentialsInText(null)).toBeNull();
    // @ts-expect-error exercising the runtime guard for non-string input
    expect(redactCredentialsInText(undefined)).toBeUndefined();
  });
});

describe("redactCredentialsBeforeSend — $exception events", () => {
  // Positive control: an exception message (as posthog-js's console-error
  // wrapper would build it from an Error with no dedicated `instanceof
  // Error` argument — see the module header) that embeds a credential URL.
  it("redacts a credential URL embedded in $exception_list[].value", () => {
    const event = {
      event: "$exception",
      properties: {
        $exception_list: [
          {
            type: "Error",
            value: `Failed to fetch: /d/auth/complete?code=${JWT_PLACEHOLDER}&org=acme`,
            mechanism: { handled: true, type: "generic", synthetic: false },
          },
        ],
        $exception_level: "error",
      },
    };

    const out = redactCredentialsBeforeSend(event)!;
    const list = out.properties!.$exception_list as Array<{ value: string }>;
    expect(list[0].value).toBe(`Failed to fetch: /d/auth/complete?code=${REDACTED}&org=acme`);
    expect(JSON.stringify(out)).not.toContain(JWT_PLACEHOLDER);
    // The input is not mutated.
    expect((event.properties.$exception_list[0] as { value: string }).value).toContain(
      JWT_PLACEHOLDER,
    );
  });

  it("redacts credential-bearing stack frame filename/abs_path", () => {
    const event = {
      event: "$exception",
      properties: {
        $exception_list: [
          {
            type: "TypeError",
            value: "x is undefined",
            stacktrace: {
              type: "raw",
              frames: [
                {
                  filename: `https://solidping.example/d/auth/complete?code=${JWT_PLACEHOLDER}`,
                  abs_path: `https://solidping.example/d/reset-password/${JWT_PLACEHOLDER}`,
                  lineno: 12,
                  colno: 4,
                },
              ],
            },
          },
        ],
      },
    };

    const out = redactCredentialsBeforeSend(event)!;
    const frame = (
      out.properties!.$exception_list as Array<{
        stacktrace: { frames: Array<{ filename: string; abs_path: string; lineno: number }> };
      }>
    )[0].stacktrace.frames[0];
    expect(frame.filename).toBe("https://solidping.example/d/auth/complete?code=REDACTED");
    expect(frame.abs_path).toBe("https://solidping.example/d/reset-password/REDACTED");
    expect(frame.lineno).toBe(12);
  });

  // Negative control: a normal exception with no credential anywhere passes
  // through unchanged, so the assertions above are not vacuous.
  it("leaves a harmless exception event untouched", () => {
    const event = {
      event: "$exception",
      properties: {
        $exception_list: [
          {
            type: "TypeError",
            value: "x is undefined",
            mechanism: { handled: true, type: "generic", synthetic: false },
            stacktrace: {
              type: "raw",
              frames: [{ filename: "https://solidping.example/assets/index-abc.js", lineno: 3 }],
            },
          },
        ],
        $exception_level: "error",
      },
    };

    expect(redactCredentialsBeforeSend(event)).toEqual(event);
  });

  it("never drops an $exception event", () => {
    const event = { event: "$exception", properties: { $exception_list: [] } };
    expect(redactCredentialsBeforeSend(event)).toEqual(event);
  });
});
