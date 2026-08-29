import { describe, test, expect, beforeEach } from "bun:test";
import { KIOSK_PARAM, resetKioskToken, withKiosk } from "./kiosk";

describe("withKiosk", () => {
  beforeEach(() => resetKioskToken());

  test("leaves the path alone when no token is held", () => {
    expect(withKiosk("/api/v1/status-pages/acme/main", undefined)).toBe(
      "/api/v1/status-pages/acme/main",
    );
    expect(withKiosk("/api/v1/status-pages/acme/main", "")).toBe(
      "/api/v1/status-pages/acme/main",
    );
  });

  test("appends the token as the documented query parameter", () => {
    expect(withKiosk("/api/v1/status-pages/acme/main", "abc")).toBe(
      `/api/v1/status-pages/acme/main?${KIOSK_PARAM}=abc`,
    );
  });

  test("joins onto an existing query string rather than starting a second one", () => {
    expect(withKiosk("/api/v1/status-pages/acme/main?active=true", "abc")).toBe(
      `/api/v1/status-pages/acme/main?active=true&${KIOSK_PARAM}=abc`,
    );
  });

  // A token is base64url, but encoding it costs nothing and a future token
  // format that included `&` or `#` would otherwise truncate silently.
  test("escapes the token", () => {
    expect(withKiosk("/x", "a&b=c")).toBe(`/x?${KIOSK_PARAM}=a%26b%3Dc`);
  });
});
