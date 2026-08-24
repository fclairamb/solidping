import { describe, expect, it } from "vitest";

import {
  deviceVerificationReturnTo,
  isDeviceVerificationReturnTo,
  isOAuthAuthorizeReturnTo,
  isSafeReturnTo,
  resolveDestination,
  returnToOrg,
  stripOAuthErrorParams,
} from "./login-destination";

const BASE = "/dash0";

describe("isOAuthAuthorizeReturnTo", () => {
  it("accepts the bare authorize path and the path with a query string", () => {
    expect(isOAuthAuthorizeReturnTo("/api/v1/oauth/authorize")).toBe(true);
    expect(
      isOAuthAuthorizeReturnTo(
        "/api/v1/oauth/authorize?client_id=abc&code_challenge=xyz",
      ),
    ).toBe(true);
  });

  it("rejects absolute and protocol-relative forms (open-redirect guard)", () => {
    expect(
      isOAuthAuthorizeReturnTo("https://evil.com/api/v1/oauth/authorize"),
    ).toBe(false);
    expect(
      isOAuthAuthorizeReturnTo("//evil.com/api/v1/oauth/authorize"),
    ).toBe(false);
  });

  it("rejects lookalike paths and subpaths", () => {
    expect(isOAuthAuthorizeReturnTo("/api/v1/oauth/authorizeX")).toBe(false);
    expect(isOAuthAuthorizeReturnTo("/api/v1/oauth/authorize/extra")).toBe(
      false,
    );
    expect(isOAuthAuthorizeReturnTo("/api/v1/oauth/token")).toBe(false);
  });

  it("rejects empty / missing values", () => {
    expect(isOAuthAuthorizeReturnTo(undefined)).toBe(false);
    expect(isOAuthAuthorizeReturnTo(null)).toBe(false);
    expect(isOAuthAuthorizeReturnTo("")).toBe(false);
  });
});

describe("isDeviceVerificationReturnTo", () => {
  it("accepts the bare device path and the path with a user_code", () => {
    expect(isDeviceVerificationReturnTo("/dash0/device", BASE)).toBe(true);
    expect(
      isDeviceVerificationReturnTo("/dash0/device?user_code=WDJP-4KXR", BASE),
    ).toBe(true);
  });

  it("respects the app base path", () => {
    expect(isDeviceVerificationReturnTo("/device", "")).toBe(true);
    expect(isDeviceVerificationReturnTo("/device", BASE)).toBe(false);
  });

  it("rejects absolute and protocol-relative forms (open-redirect guard)", () => {
    expect(isDeviceVerificationReturnTo("https://evil.com/dash0/device", BASE)).toBe(
      false,
    );
    expect(isDeviceVerificationReturnTo("//evil.com/dash0/device", BASE)).toBe(
      false,
    );
  });

  it("rejects lookalike paths and subpaths", () => {
    expect(isDeviceVerificationReturnTo("/dash0/deviceX", BASE)).toBe(false);
    expect(isDeviceVerificationReturnTo("/dash0/device/extra", BASE)).toBe(false);
  });

  it("rejects empty / missing values", () => {
    expect(isDeviceVerificationReturnTo(undefined, BASE)).toBe(false);
    expect(isDeviceVerificationReturnTo(null, BASE)).toBe(false);
    expect(isDeviceVerificationReturnTo("", BASE)).toBe(false);
  });
});

describe("deviceVerificationReturnTo", () => {
  it("carries the one-time code, url-encoded", () => {
    expect(deviceVerificationReturnTo(BASE, "WDJP-4KXR")).toBe(
      "/dash0/device?user_code=WDJP-4KXR",
    );
    expect(deviceVerificationReturnTo(BASE, "A B")).toBe(
      "/dash0/device?user_code=A%20B",
    );
  });

  it("omits the query string when there is no code", () => {
    expect(deviceVerificationReturnTo(BASE, undefined)).toBe("/dash0/device");
    expect(deviceVerificationReturnTo(BASE, "")).toBe("/dash0/device");
  });

  it("round-trips through the guard it is built for", () => {
    const returnTo = deviceVerificationReturnTo(BASE, "WDJP-4KXR");
    expect(isDeviceVerificationReturnTo(returnTo, BASE)).toBe(true);
  });
});

describe("resolveDestination", () => {
  it("honors a returnTo whose org matches the resolved org", () => {
    expect(
      resolveDestination("test", "/dash0/orgs/test/checks?q=1", BASE),
    ).toEqual({ href: "/dash0/orgs/test/checks?q=1" });
  });

  it("honors an MCP OAuth authorize returnTo regardless of org (the consent bounce)", () => {
    // The embedded authorization server derives the org from the session
    // claims, so the org-match rule does not apply to this shape — any
    // logged-in org may resume the consent flow.
    const authorize =
      "/api/v1/oauth/authorize?client_id=abc&redirect_uri=x&code_challenge=y";
    expect(resolveDestination("test", authorize, BASE)).toEqual({
      href: authorize,
    });
    expect(resolveDestination("other-org", authorize, BASE)).toEqual({
      href: authorize,
    });
  });

  it("honors the device verification returnTo for ANY org (the code must survive login)", () => {
    // The org-less /device route resolves the org from the session after
    // login, so the org-match rule must not apply — otherwise every user whose
    // org is not the slug the logged-out visitor was bounced through loses the
    // pre-filled one-time code (spec 2026-08-08-02).
    const device = "/dash0/device?user_code=WDJP-4KXR";
    expect(resolveDestination("test", device, BASE)).toEqual({ href: device });
    expect(resolveDestination("some-other-org", device, BASE)).toEqual({
      href: device,
    });
  });

  it("preserves the query string of the deep link", () => {
    expect(
      resolveDestination(
        "test",
        "/dash0/orgs/test/incidents?state=open&sort=desc",
        BASE,
      ),
    ).toEqual({
      href: "/dash0/orgs/test/incidents?state=open&sort=desc",
    });
  });

  it("falls back to the org root when the returnTo org differs", () => {
    expect(
      resolveDestination("test", "/dash0/orgs/other/checks", BASE),
    ).toEqual({ to: "/orgs/$org", params: { org: "test" } });
  });

  it("falls back to the org root when returnTo is missing", () => {
    expect(resolveDestination("test", undefined, BASE)).toEqual({
      to: "/orgs/$org",
      params: { org: "test" },
    });
    expect(resolveDestination("test", null, BASE)).toEqual({
      to: "/orgs/$org",
      params: { org: "test" },
    });
    expect(resolveDestination("test", "", BASE)).toEqual({
      to: "/orgs/$org",
      params: { org: "test" },
    });
  });

  it("rejects an absolute http(s) URL even if it targets the right org", () => {
    expect(
      resolveDestination("test", "https://evil.com/dash0/orgs/test/checks", BASE),
    ).toEqual({ to: "/orgs/$org", params: { org: "test" } });
    expect(
      resolveDestination("test", "http://evil.com/dash0/orgs/test", BASE),
    ).toEqual({ to: "/orgs/$org", params: { org: "test" } });
  });

  it("rejects a protocol-relative //host URL", () => {
    expect(
      resolveDestination("test", "//evil.com/dash0/orgs/test/checks", BASE),
    ).toEqual({ to: "/orgs/$org", params: { org: "test" } });
  });

  it("rejects a backslash-obfuscated URL", () => {
    expect(
      resolveDestination("test", "/\\evil.com/dash0/orgs/test", BASE),
    ).toEqual({ to: "/orgs/$org", params: { org: "test" } });
  });

  it("rejects a path outside the /orgs/ subtree", () => {
    expect(
      resolveDestination("test", "/dash0/settings", BASE),
    ).toEqual({ to: "/orgs/$org", params: { org: "test" } });
  });

  it("works with an empty base path", () => {
    expect(resolveDestination("test", "/orgs/test/checks", "")).toEqual({
      href: "/orgs/test/checks",
    });
    expect(resolveDestination("test", "//evil.com/orgs/test", "")).toEqual({
      to: "/orgs/$org",
      params: { org: "test" },
    });
  });
});

describe("isSafeReturnTo", () => {
  it("accepts an in-app org path under the base path", () => {
    expect(isSafeReturnTo("/dash0/orgs/test/checks", BASE)).toBe(true);
  });

  it("rejects absolute, protocol-relative and scheme'd values", () => {
    expect(isSafeReturnTo("https://evil.com/dash0/orgs/x", BASE)).toBe(false);
    expect(isSafeReturnTo("//evil.com/dash0/orgs/x", BASE)).toBe(false);
    expect(isSafeReturnTo("javascript:alert(1)", BASE)).toBe(false);
    expect(isSafeReturnTo("/\\evil.com", BASE)).toBe(false);
  });

  it("rejects an in-app path outside /orgs/", () => {
    expect(isSafeReturnTo("/dash0/no-org", BASE)).toBe(false);
  });
});

describe("returnToOrg", () => {
  it("reads the slug after /orgs/, stripping base path and query", () => {
    expect(returnToOrg("/dash0/orgs/test/checks?q=1", BASE)).toBe("test");
    expect(returnToOrg("/dash0/orgs/acme", BASE)).toBe("acme");
    expect(returnToOrg("/orgs/test/checks", "")).toBe("test");
  });

  it("returns null when there is no org segment", () => {
    expect(returnToOrg("/dash0/settings", BASE)).toBeNull();
  });
});

describe("stripOAuthErrorParams", () => {
  it("removes the params a failed OAuth callback appended", () => {
    expect(
      stripOAuthErrorParams(
        "/dash0/orgs/default?error=OAUTH_FAILED&error_description=OAuth+failed",
      ),
    ).toBe("/dash0/orgs/default");
  });

  it("drops the '?' when nothing is left, and leaves a param-less path alone", () => {
    expect(stripOAuthErrorParams("/dash0/orgs/default?error=X")).toBe(
      "/dash0/orgs/default",
    );
    expect(stripOAuthErrorParams("/dash0/orgs/default")).toBe(
      "/dash0/orgs/default",
    );
  });

  it("keeps every other query param and the hash", () => {
    expect(
      stripOAuthErrorParams(
        "/dash0/orgs/acme/checks?error=OAUTH_FAILED&tab=down&error_description=nope&q=api#top",
      ),
    ).toBe("/dash0/orgs/acme/checks?tab=down&q=api#top");
  });

  it("is idempotent, so retries cannot compound", () => {
    const once = stripOAuthErrorParams(
      "/dash0/orgs/default?error=OAUTH_FAILED&error_description=sql%3A+no+rows+in+result+set",
    );
    expect(once).toBe("/dash0/orgs/default");
    expect(stripOAuthErrorParams(once)).toBe(once);
  });

  it("does not turn a relative path into an absolute URL", () => {
    // Regression guard: implementing this with `new URL(path)` would need an
    // origin, and would hand redirect_uri an absolute URL.
    expect(stripOAuthErrorParams("/dash0/orgs/default?error=X&a=1")).toBe(
      "/dash0/orgs/default?a=1",
    );
    expect(
      stripOAuthErrorParams("/dash0/orgs/default?error=X").startsWith("/"),
    ).toBe(true);
  });

  it("leaves a nested error inside an unrelated param's value untouched", () => {
    expect(
      stripOAuthErrorParams(
        "/dash0/orgs/default?returnTo=%2Fdash0%2Forgs%2Fdefault%3Ferror%3DOAUTH_FAILED&error=OAUTH_FAILED",
      ),
    ).toBe(
      "/dash0/orgs/default?returnTo=%2Fdash0%2Forgs%2Fdefault%3Ferror%3DOAUTH_FAILED",
    );
  });
});
