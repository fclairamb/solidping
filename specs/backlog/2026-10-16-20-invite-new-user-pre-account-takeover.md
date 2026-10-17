---
model: opus
effort: high
---

# The invite token holder chooses a new invitee's first password (pre-account takeover)

**Status:** backlog. Split out of `specs/done/2026/10/2026-10-16-06-accept-invite-existing-account.md` (resolved open question 2), to be designed together with N6 (security scan 2026-10-12, pending-registration overwrite).

## Problem
Accepting an invitation for an email with no account creates that account with a password chosen by whoever holds the invite token, and marks it verified. The inviter holds the token too. So an org admin can invite `alice@acme.com`, accept the invite themselves, and own an account under alice's address before alice ever signs up; alice's later federated sign-in may then link to it. N6 (pending-registration overwrite) is the same pre-account-takeover family.

## Proposal (to refine)
- Require proof of email ownership before the account becomes usable: accepting creates the account unverified and sends a verification email, or the invite link itself is only delivered by email and accepting requires a one-time code sent to the invitee.
- Do not mark the account verified from the invite alone.
- Never auto-link a federated identity to an account whose email was not proven.
- Cover N6 in the same design.

## Tests
- The inviter accepting their own invitation for a new email does not yield a usable verified account.
- The invitee completing email verification gets the account; a federated sign-in for that email before verification does not link to it.
