---
model: opus
effort: high
pr: own
---

# Keep the browser-generated region key, and every shown-once secret, out of session replay and autocapture

## Problem

From security advisory GHSA-c9qq-4m99-c7f6.

- **Severity:** High.
- **Who can trigger it:** nobody has to. The key leaks whenever an org admin generates or rotates a shared region
  key in dash0 while PostHog session replay is recording. That is the case on SolidPing SaaS, and on any
  self-hosted instance with `SP_POSTHOG_PROJECT_API_KEY` set. Whoever reads the PostHog project receives the key.
- **Affected:** v0.47.0, the first release with browser-sealed secrets (#834).

Browser sealing promises that the server and its operator never see a check's secrets
(`web/docs/docs/features/browser-sealed-secrets.md`). With a shared region key, the browser generates the age
identity (`web/dash0/src/lib/sealing.ts:183`) and shows it once in the region key dialog
(`web/dash0/src/components/shared/region-key-cell.tsx:203-222`):

- the `AGE-SECRET-KEY-1…` identity in a `<code data-testid="region-key-secret">`;
- the `SP_AGENT_REGION_KEYS=…` line in a `<pre>`.

dash0 records sessions with no text or input masking, on purpose (`web/dash0/src/lib/analytics.ts:14-26`, `:335-343`,
`disable_session_recording: false`). The credential hooks of `web/dash0/src/lib/analytics-redaction.ts` filter URLs
and network captures only, and say so: "credentials rendered as page text are NOT masked". No element of dash0
carries PostHog's `ph-no-capture` class. So:

- the identity text goes into the replay DOM snapshot, through the `/ingest` proxy, to the PostHog project;
- autocapture can also record it as `$el_text` when the admin clicks the `<code>`.

The server stores every blob sealed to that key (`checks.config_sealed`, `check_jobs.config_sealed`). Whoever holds
both, the SaaS operator first of all, can open every browser-sealed secret of the region. That is exactly what
the feature is meant to prevent.

Checked against the code: password-type inputs (most secret fields, for example `http.tsx` secret header values)
are masked by rrweb by default, and keys generated offline (`solidping agent region-key generate` + *Paste a
public key*) never reach the browser. Neither is in scope beyond the hardening in step 3. Text-type secret inputs
(for example the basic-auth username, `http.tsx:490`) are not masked, and step 3 covers them.

## Proposal

1. **A `SecretText` primitive.** Add `web/dash0/src/components/ui/secret-text.tsx`: a `<code>`/`<pre>` wrapper
   that always carries `className="ph-no-capture"` plus a `data-secret-text` attribute. Add it to the design
   reference page (`routes/orgs/$org/design-reference.tsx`) with its import line and a note: "every secret shown
   to the user as text goes through this".
2. **Use it in the region key dialog** (`region-key-cell.tsx:207-222`) for the identity and the env line. Put
   `ph-no-capture` on the whole `DialogContent` (`data-testid="region-key-dialog"`) too, so the copy and download
   buttons don't record anything either.
3. **Same for the other shown-once secrets rendered as text**, which share the gap:
   - PAT creation (`routes/orgs/$org/account.tokens.tsx`);
   - TOTP setup secret and recovery codes (`components/security/TOTPSetupDialog.tsx`);
   - agent enrollment tokens (`organization.private-locations.register.tsx`, `organization.private-locations.index.tsx`);
   - webhook / integration signing secrets (`components/integrations/integration-form.tsx`,
     `components/shared/status-page-subscribers.tsx`);
   - any other screen found by grepping for "shown once" copy.

   Also add `ph-no-capture` to text-type inputs marked `data-secret-input` (the check form), so a browser-sealed
   check's plaintext never reaches the replay, whatever the input type.
4. **Pause recording while the region key dialog is open.** Add `pauseSessionRecording()` / `resumeSessionRecording()`
   to `lib/analytics.ts`. They are no-ops when PostHog is off. Resume only if it was recording before. Call them
   from the dialog's open state. This is defence in depth, in case a later edit moves the secret outside the
   blocked element.
5. **An ESLint guard** (in `web/dash0/eslint-rules/`): flag a JSX text child or `{…}` expression named
   `identity`, `secret`, `token`, `recoveryCodes` or similar, rendered inside `<code>`/`<pre>` without `SecretText`.
   If that is too noisy, a unit test that renders each listed screen and asserts the `ph-no-capture` ancestor is
   enough.
6. **Docs and release note.** In `browser-sealed-secrets.md`, say that admins who generated a region key in dash0
   on v0.47.0 must rotate it: generate a new key, give it to the agents, re-seal, remove the old one. Add a
   one-time notice on the private locations page for a region whose key was created before this fix (the key's
   `createdAt` is before the fix release). Add a CHANGELOG entry under Security.

## Tests

- Vitest, `region-key-cell.test.tsx`: after generating a key, the element containing the identity, and the `<pre>`
  env line, both have a `ph-no-capture` ancestor. Fails on main today (no such class anywhere).
- Vitest: the `SecretText` primitive renders `ph-no-capture` and `data-secret-text`.
- Vitest: each screen of step 3 renders its secret inside a `ph-no-capture` element.
- Vitest, `analytics.test.ts`: `pauseSessionRecording` calls `stopSessionRecording` only when recording was on, and
  `resumeSessionRecording` restarts it only then. Both are no-ops with PostHog off.
- Playwright (`browser-sealed-secrets.spec.ts`, with a stubbed posthog-js recorder that collects rrweb snapshots):
  generating a region key → the identity string never appears in any snapshot or captured event. Positive control:
  a non-secret text of the same dialog (the title) does appear, so the stub really records.
