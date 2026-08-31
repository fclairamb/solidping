// Classifies a failure from `useInviteInfo` (GET /api/v1/auth/invite/:token)
// so the invite page can tell "this invitation genuinely doesn't exist" apart
// from "the server hiccuped." Only the former should render the destructive
// "invalid or expired" card — a 429 (rate limited), a 5xx, or a network
// failure is a dead end for someone with a perfectly valid invitation if it
// renders the same message, since there is nothing for them to do about an
// expired invite except ask for a new one.
//
// The backend maps ErrInvitationNotFound to 404/INVITATION_NOT_FOUND and
// ErrInvitationExpired to 410/INVITATION_EXPIRED (see
// server/internal/handlers/auth/handler.go handleInvitationError) — both are
// genuine "this link is dead" signals. Everything else (network errors,
// unexpected exceptions, any other ApiError status) is retryable.

import { ApiError } from "@/api/client";

const INVALID_INVITE_CODES = new Set(["INVITATION_NOT_FOUND", "INVITATION_EXPIRED"]);
const INVALID_INVITE_STATUSES = new Set([404, 410]);

export function isInviteInvalidError(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  if (err.status !== undefined && INVALID_INVITE_STATUSES.has(err.status)) return true;
  return INVALID_INVITE_CODES.has(err.code);
}
