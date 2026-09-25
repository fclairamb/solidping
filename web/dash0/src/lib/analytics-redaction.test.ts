import { describe, expect, it } from "vitest";
import {
  CREDENTIAL_QUERY_PARAMS,
  REDACTED,
  redactBody,
  redactCapturedNetworkRequest,
  redactCredentialsBeforeSend,
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

// The URL the backend's buildSuccessRedirect produces for a federated login,
// and the org-less variant from pendingMembershipRedirect.
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
    // Spec 2026-09-25-12's one-time handoff code.
    expect(redactUrl("/d/auth/complete?code=hc_abc123")).toBe(`/d/auth/complete?code=${REDACTED}`);
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

  it("redacts credentials in captured bodies", () => {
    const out = redactCapturedNetworkRequest({
      name: "/api/v1/auth/refresh",
      requestBody: JSON.stringify({ refreshToken: REFRESH }),
      responseBody: JSON.stringify({ accessToken: JWT, refreshToken: REFRESH, expiresIn: 900 }),
    });
    expect(JSON.stringify(out)).not.toContain(JWT);
    expect(JSON.stringify(out)).not.toContain(REFRESH);
    expect(JSON.parse(out.responseBody!)).toEqual({
      accessToken: REDACTED,
      refreshToken: REDACTED,
      expiresIn: 900,
    });
  });
});

describe("redactBody", () => {
  it("redacts credential keys at any depth, keeps the API error `code`", () => {
    const body = JSON.stringify({
      data: [{ uid: "t1", token: "pat_abc" }],
      login: { email: "alice@acme.com", password: "hunter2", tempToken: "tt" },
      code: "VALIDATION_ERROR",
    });
    expect(JSON.parse(redactBody(body)!)).toEqual({
      data: [{ uid: "t1", token: REDACTED }],
      login: { email: "alice@acme.com", password: REDACTED, tempToken: REDACTED },
      code: "VALIDATION_ERROR",
    });
  });

  it("redacts form-encoded credential params", () => {
    expect(redactBody(`grant_type=refresh_token&refresh_token=${REFRESH}`)).toBe(
      `grant_type=refresh_token&refresh_token=${REDACTED}`,
    );
  });

  it("returns harmless bodies byte for byte", () => {
    const pretty = '{\n  "name": "My API",\n  "type": "http"\n}';
    expect(redactBody(pretty)).toBe(pretty);
    expect(redactBody("plain text, token=nothing here")).toBe("plain text, token=nothing here");
    expect(redactBody("{not json")).toBe("{not json");
    expect(redactBody(null)).toBeNull();
    expect(redactBody(undefined)).toBeUndefined();
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
