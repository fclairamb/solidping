The `state_entries` table shall have the following conventions:

For an incident that is posted on a connection channel we shall store around the incident the channel_id, message_id, thread_ts using:
- organization_uid = $organization_uid
- key = incidents/$incident_uid/slack/thread
- value = {"channel_id": $channel_id, "message_id": $message_id, "thread_ts": $thread_ts}

## Single-use tokens are never stored in plaintext

A token that grants something when presented (an invite link, a password reset
link, a registration confirmation) is stored as `hashPendingToken(token)`
(lowercase hex SHA-256), never as the token itself, in the key or in the value.
Anyone who can read `state_entries` must not be able to rebuild a working link.

| Flow | Key | Where the hash lives |
|---|---|---|
| Password reset | `password_reset:<hash>` (global) | key suffix |
| Invitation | `invite:<hash>` (org-scoped) | key suffix (spec 2026-09-26-02) |
| Email registration | keyed by email | value field `tokenHash` (spec 2026-09-25-30) |

Lookups hash the presented token and look the key up; the hash itself must never
work as a token. Invites created before spec 2026-09-26-02 are still keyed
`invite:<plaintext>` with `token` in the value; `findInvite` accepts them only
when that stored `token` equals the presented one, and the fallback is marked
`TODO(remove after 2026-10-10)`. Tests that need a token the API response does
not return recover it from the queued email via `GET /api/v1/test/jobs?type=email`.
