// Classifies a passkey (WebAuthn) ceremony failure into a small, i18n-free set
// of kinds so the login page can pick the right message. Co-located with the
// other login helpers (see lib/last-auth-method.ts).
//
// The classification mirrors how @simplewebauthn/browser surfaces failures from
// navigator.credentials.get():
//   - User cancel / timeout → a plain Error with name "NotAllowedError".
//   - RP-ID / domain mismatch (e.g. opening the dashboard on a host that isn't
//     valid for the backend's WebAuthn RP ID) → a WebAuthnError with code
//     ERROR_INVALID_RP_ID or ERROR_INVALID_DOMAIN.
//   - Any other WebAuthn failure → a WebAuthnError with some other code.
//   - Everything else (network errors, ApiError from begin/finish) → "other",
//     which the caller routes to its generic error reporter.
//
// Honest limitation: some browsers report an RP-ID mismatch as a plain
// NotAllowedError, which is indistinguishable from a real user-cancel and is
// therefore classified as "cancelled".

import { WebAuthnError } from "@simplewebauthn/browser";

export type PasskeyErrorKind = "cancelled" | "domainMismatch" | "failed" | "other";

export function classifyPasskeyError(err: unknown): PasskeyErrorKind {
  if (err instanceof Error && err.name === "NotAllowedError") return "cancelled";
  if (err instanceof WebAuthnError) {
    if (err.code === "ERROR_INVALID_RP_ID" || err.code === "ERROR_INVALID_DOMAIN") {
      return "domainMismatch";
    }
    return "failed";
  }
  return "other";
}
