---
model: sonnet
effort: medium
---

# Port fields: real default, common-port chips, no spinner, `host:port` paste, no error before input

GitHub Issue: https://github.com/fclairamb/solidping/issues/495

## Source

From PostHog, the owner saw a new user creating their first TCP check (SSH, port 22) click
into the port field and step the browser spinner up one unit at a time: 28 `change` events in
about 7 seconds and 2 rage clicks, with the field in its error state the whole time. A second
TCP check four minutes later drew another rage click on the same field. The session has about
200 clicks on `/checks/new` for 3 simple checks.

The report is right: the field is empty, its placeholder (`443`) reads like a value, and the
only visible affordance besides typing is a spinner that counts up from 1.

## Problem

- TCP/UDP port input: `web/dash0/src/components/checks/form/types/network.tsx:175-187`,
  `type="number"`, `w-24`, `placeholder={type === "udp" ? "53" : "443"}`, empty value.
  `tcpModule.fromConfig` (`:79`) reads `port` from config, which is empty for a new check,
  and `toConfig` (`:100`) omits it when empty.
- The same pattern (number input, placeholder only) repeats for SSH (`network.tsx:366`),
  SFTP (`:524`), FTP (`:647`) and the port field in `misc.tsx:89-99`.
- The error border while still empty comes from live server validation:
  `useCheckValidationResult` (`web/dash0/src/hooks/use-check-validation.ts:77,95-144`) posts
  the draft to `/checks/validate` after a 1 s debounce, and its field errors are rendered
  immediately (`network.tsx:184,194`). An untouched required field is flagged as soon as the
  form is opened.
- Existing e2e tests fill the port by test id (`e2e/checks.spec.ts:683,788`,
  `e2e/check-vnc.spec.ts:47`, `e2e/check-ip-version.spec.ts:145`), so keeping
  `data-testid="check-port-input"` keeps them working.

## Why a spec

The full fix touches six port fields in two files, adds a new chip component (to the design
reference page, per AGENTS.md), adds paste parsing on the host field, and changes when
validation errors are shown, which affects every field of every check type (cross-cutting).
That is beyond 4 files / 150 lines, and the error-timing change needs a scope decision.

## Proposal

1. **Shared `PortInput` component** (`web/dash0/src/components/checks/form/port-input.tsx`):
   `type="text" inputMode="numeric" pattern="[0-9]*"`, width wide enough for 5 digits, client
   range check 1–65535, `data-testid="check-port-input"`. Replace the six inputs with it.
2. **Real default per type**, written into state for a **new** check only (never overwrite a
   stored port): tcp 443, udp 53, ssh 22, sftp 22, ftp 21, and the `misc.tsx` type's own
   default. Put the defaults in one map next to the modules.
3. **Common-port chips** under the field, one click sets the port: TCP: 22 SSH, 80 HTTP,
   443 HTTPS, 3306 MySQL, 5432 PostgreSQL, 6379 Redis, 25 SMTP, 587 Submission. UDP: 53 DNS,
   123 NTP, 161 SNMP. Not shown for SSH/SFTP/FTP (a single obvious default). Reuse the
   quick-start chip primitive from the empty dashboard if it fits. Otherwise add a `Chip`
   primitive to the design reference page in the same change.
4. **`host:port` paste.** In host fields that sit next to a `PortInput`, when the value
   matches `^[^\s:/]+:(\d{1,5})$` or `^\[[0-9a-f:]+\]:(\d{1,5})$` (bracketed IPv6), split it
   into host and port on change. A bare IPv6 address without brackets is left alone.
5. **No error before input.** Keep server validation as is, but show a field's error only once
   that field was touched (blurred or edited) or after a submit attempt. Implement it in the
   shared validation rendering, not per field (see Open questions on scope).

### Tests

- Playwright (`e2e/checks.spec.ts`): new TCP check shows `443` in the port, no
  `input[type=number]` exists for the port, clicking the `22 SSH` chip sets `22`, saving
  creates a check with port 22. Pasting `example.com:22` into host yields host
  `example.com`, port `22`. An untouched empty host shows no error border before submit.
- Existing e2e that fill `check-port-input` stay green.
- Vitest unit test for the `host:port` split, including bracketed IPv6 and a bare IPv6 that
  must not split.

## Open questions

1. **Scope of "no error before input".** Recommended: apply touched-or-submitted gating to
   **all** check form fields through the shared error rendering, since the same flash-of-red
   happens on every required field. Trade-off: a larger diff touching every check type's
   error display, vs. fixing only the port field and leaving the inconsistency elsewhere.

## Closing the issue

This spec closes #495. The implementing PR body **must** carry one `Closes #495` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 495 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
