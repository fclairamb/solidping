/**
 * Client-side checks of the forced first-login screen (spec 2026-08-23-04,
 * email part spec 2026-10-13-02). The server repeats every one of them; these
 * only save a round trip and keep the error next to the form.
 */

/** The seeded admin address, published in the repository (server `defaults.Email`). */
export const DEFAULT_ADMIN_EMAIL = "admin@solidping.io";

export const MIN_PASSWORD_LENGTH = 8;

export type ForcedChangeError =
  | "emailMismatch"
  | "emailDefault"
  | "passwordTooShort"
  | "passwordMismatch";

export interface ForcedChangeInput {
  askEmail: boolean;
  email: string;
  emailConfirm: string;
  next: string;
  confirm: string;
}

export interface ForcedChangeResult {
  error: ForcedChangeError | null;
  /** The email to send, or undefined when none should be sent. */
  email?: string;
}

export function validateForcedChange(input: ForcedChangeInput): ForcedChangeResult {
  let email: string | undefined;

  if (input.askEmail) {
    const value = input.email.trim();
    const confirm = input.emailConfirm.trim();
    if (value.toLowerCase() !== confirm.toLowerCase()) {
      return { error: "emailMismatch" };
    }
    if (value.toLowerCase() === DEFAULT_ADMIN_EMAIL) {
      return { error: "emailDefault" };
    }
    // Both empty: the email stays optional and nothing is sent.
    email = value === "" ? undefined : value;
  }

  if (input.next.length < MIN_PASSWORD_LENGTH) {
    return { error: "passwordTooShort" };
  }
  if (input.next !== input.confirm) {
    return { error: "passwordMismatch" };
  }

  return { error: null, email };
}
