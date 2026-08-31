import { describe, expect, it } from "vitest";

import authDe from "@/locales/de/auth.json";
import authEn from "@/locales/en/auth.json";
import authEs from "@/locales/es/auth.json";
import authFr from "@/locales/fr/auth.json";

// The invite page (routes/invite.$token.tsx) renders a distinct, retryable
// "temporary error" card for a 429/5xx/network failure from useInviteInfo,
// separate from the "invalid or expired" card — see lib/invite-error.ts. The
// two new copy keys it uses must exist, non-empty, as real translations in
// all four locales, or a non-English viewer would see the raw dotted key
// path on the one screen a brand-new (often unauthenticated, first-contact)
// user is looking at.

const AUTH_LOCALES = [
  ["en", authEn],
  ["fr", authFr],
  ["de", authDe],
  ["es", authEs],
] as const;

const INVITE_ERROR_KEYS = [
  "invite.invalid",
  "invite.invalidDescription",
  "invite.temporaryError",
  "invite.temporaryErrorDescription",
];

function lookup(bundle: unknown, path: string): unknown {
  return path
    .split(".")
    .reduce<unknown>(
      (node, segment) =>
        node && typeof node === "object"
          ? (node as Record<string, unknown>)[segment]
          : undefined,
      bundle,
    );
}

describe("invite page error copy locale parity", () => {
  it.each(AUTH_LOCALES)("%s carries every invite error key", (_locale, bundle) => {
    for (const key of INVITE_ERROR_KEYS) {
      const value = lookup(bundle, key);
      expect(value, `${key} is missing`).toBeTypeOf("string");
      expect(String(value).trim(), `${key} is empty`).not.toBe("");
    }
  });

  it("does not reuse the invalid-invite copy for the temporary error", () => {
    for (const [, bundle] of AUTH_LOCALES) {
      const invalid = lookup(bundle, "invite.invalidDescription");
      const temporary = lookup(bundle, "invite.temporaryErrorDescription");
      expect(temporary).not.toBe(invalid);
    }
  });
});
