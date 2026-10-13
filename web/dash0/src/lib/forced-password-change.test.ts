import { describe, expect, it } from "vitest";
import { DEFAULT_ADMIN_EMAIL, validateForcedChange } from "./forced-password-change";

const base = { askEmail: true, email: "", emailConfirm: "", next: "brandnewpassword", confirm: "brandnewpassword" };

describe("validateForcedChange", () => {
  it("sends no email when both email fields are empty", () => {
    expect(validateForcedChange(base)).toEqual({ error: null, email: undefined });
  });

  it("sends the trimmed email when both fields match", () => {
    expect(
      validateForcedChange({ ...base, email: " ops@acme.com ", emailConfirm: "OPS@acme.com" })
    ).toEqual({ error: null, email: "ops@acme.com" });
  });

  it("refuses mismatched emails", () => {
    expect(validateForcedChange({ ...base, email: "ops@acme.com", emailConfirm: "" }).error).toBe(
      "emailMismatch"
    );
  });

  it("refuses the default address in any case", () => {
    const upper = DEFAULT_ADMIN_EMAIL.toUpperCase();
    expect(validateForcedChange({ ...base, email: upper, emailConfirm: upper }).error).toBe(
      "emailDefault"
    );
  });

  it("ignores the email fields when the screen does not ask for one", () => {
    expect(
      validateForcedChange({ ...base, askEmail: false, email: "a@acme.com", emailConfirm: "b@acme.com" })
    ).toEqual({ error: null, email: undefined });
  });

  it("keeps the password rules", () => {
    expect(validateForcedChange({ ...base, next: "short", confirm: "short" }).error).toBe(
      "passwordTooShort"
    );
    expect(validateForcedChange({ ...base, confirm: "differentpassword" }).error).toBe(
      "passwordMismatch"
    );
  });
});
