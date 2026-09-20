import { describe, expect, it } from "vitest";
import { WebAuthnError } from "@simplewebauthn/browser";

import { classifyPasskeyError } from "@/lib/passkey-error";

describe("classifyPasskeyError", () => {
  it("maps an invalid RP ID to domain-mismatch copy rather than the fallback", () => {
    const error = new WebAuthnError({
      code: "ERROR_INVALID_RP_ID",
      message: "The RP ID is not valid for this origin",
      cause: new Error("SecurityError"),
    });

    expect(classifyPasskeyError(error)).toBe("domainMismatch");
    expect(classifyPasskeyError(error)).not.toBe("failed");
  });
});
