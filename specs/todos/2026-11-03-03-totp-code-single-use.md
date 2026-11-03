---
model: opus
effort: medium
pr: batch
automerge: false
---

# A TOTP code can be used more than once within its validity window, and turning 2FA off sends no notice

## Problem

Found by the security scan of 2026-11-03 (finding AD3). Severity: low. It helps an attacker who has seen one valid code of the victim (a real-time phishing proxy, shoulder-surfing) and knows the password or holds a session.

- The three TOTP checks call `totp.Validate` and record nothing (`server/internal/handlers/auth/service.go:5081` `Confirm2FA`, `:5158` `Verify2FA`, `:5260` `Disable2FA`). With the library's default skew, a code is accepted for about 90 s, as many times as it is sent. RFC 6238 §5.2 says a verifier must not accept the same code twice.
- Impact:
  - a phishing proxy that relays the victim's login can reuse the same code to open a second session of its own;
  - the same code, already used for a login, also passes `Disable2FA`, which removes the second factor.
- `Disable2FA` sends no email (`service.go:5236-5277`), while turning 2FA on does (`notifyTOTPEnabled`, `factor_reauth.go:236`). The owner is not told that their second factor was removed.

## Proposal

1. Record the time step of the last accepted code per user, for example a `state_entries` key `totp_last_step:<userUid>` with a TTL of a few minutes (no migration needed), or a column.
2. Use `totp.ValidateCustom` to find the step that matched. Refuse a code whose step is at or before the recorded one, with the same error as a wrong code, and record the step when a code is accepted. Do this in `Verify2FA`, `Disable2FA` and `Confirm2FA`. Use an atomic compare-and-set, so two parallel requests with the same code cannot both pass.
3. Send a "two-factor authentication turned off" notice from `Disable2FA`, like `notifyTOTPEnabled`, to the account's current email, in all four locales.

## Tests

- `server/internal/handlers/auth/` (new `totp_replay_test.go`):
  - a code accepted by `Verify2FA` is refused by a second `Verify2FA` with a new temp token, and by `Disable2FA`. These fail before the fix.
  - two concurrent `Verify2FA` calls with the same code: exactly one succeeds.
  - positive control: the next code (the next time step) is accepted after a refused replay.
  - `Disable2FA` sends the notice email to the current address, and the account keeps working.

## Open questions

- Reuse the 2FA attempt budget for refused replays? Recommended: yes, a replay counts as a failed attempt.

## Resolved open questions

- Reuse the 2FA attempt budget for refused replays: yes, a replay counts as a failed attempt. (autopilot: recommended answer)
