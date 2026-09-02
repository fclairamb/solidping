---
model: sonnet
effort: medium
---

# The embedded TCP/UDP heartbeat panel is always fully expanded, and it carries a whole Arduino sketch that belongs in the docs

## Problem

The heartbeat check detail page renders an "Embedded devices (TCP/UDP)" block
under the HTTPS endpoint whenever the server reports a TCP or UDP listener
enabled (`HeartbeatPushEndpoint`,
[`checks.$checkUid.index.tsx:534`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)).
Everything in it is open all the time:

- the section title + description paragraph,
- the TCP `nc` one-liner and its label,
- the UDP `nc` one-liner and its label,
- the annotation hint,
- the "Arduino / ESP sketch (SP2, signed)" `CollapsibleCode` (the only piece
  that already collapses) plus its hint,
- the "Require signed beats (HMAC)" switch + hint,
- the rotate-token nudge alert (only when HMAC is required).

For the vast majority of heartbeat checks — cron jobs hitting the HTTPS URL —
none of this is relevant, yet it doubles the height of the endpoint card and
pushes the response-time chart and result list down the page. Two issues:

1. **Nothing is collapsed by default.** The TCP and UDP transports are an
   opt-in, deployment-level feature; they should be discoverable, not
   dominant. The user's ask: *"everything should be collapsible and
   collapsed by default"*.

2. **The Arduino / ESP sketch lives in the setup page.** `arduinoSketch()`
   ([`checks.$checkUid.index.tsx:468`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx))
   builds ~40 lines of C++ (mbedTLS HMAC, `WiFiUDP`, counter handling) and
   the panel renders it inline. The very same sketch is already published in
   the docs at
   [`embedded-push.md:101`](../../web/docs/docs/features/embedded-push.md)
   under "A minimal Arduino / ESP sketch", next to the counter recipe and the
   security discussion the sketch depends on. Keeping two copies means they
   drift, and the setup page is the wrong place for a code walkthrough — it
   should show the check's own token/target/port and point at the docs for
   the code.

## Proposal

### 1. Collapse the whole embedded block, collapsed by default

Wrap the "Embedded devices (TCP/UDP)" content in the existing
`CollapsibleSection` primitive
([`components/ui/collapsible-section.tsx`](../../web/dash0/src/components/ui/collapsible-section.tsx),
catalogued in the design reference as "Collapsible section") with
`defaultOpen={false}`:

- **Header** = the current title (`endpoints.heartbeat.push.title`). Use the
  `summary` slot to keep the collapsed row honest, e.g. `TCP 4002 · UDP 4002`
  or `TCP 4002 · UDP 4002 · signed only` when `require_hmac` is on — so
  the reader sees what is enabled without expanding.
- **Body** = the description paragraph, the TCP and UDP one-liners, the
  annotation hint, the require-HMAC switch and its hint.
- Inside the body, the **TCP and UDP one-liners each become a collapsed
  `CollapsibleCode`** (`defaultOpen={false}`) labelled with the existing
  `tcpLabel` / `udpLabel` strings, replacing the hand-rolled
  `bg-muted … <Copy/>` boxes. `CollapsibleCode` already ships a copy button,
  so the local `copyToClipboard` helper for these two values goes away.
- **The rotate-token nudge stays visible when the section is collapsed.**
  It is a security warning ("your token may have been seen on the wire —
  regenerate it"); hiding it behind a chevron defeats its purpose. Render
  it *outside* the `CollapsibleSection`, directly below it, still gated on
  `require_hmac`.
- Keep the `data-testid`s (`heartbeat-push`, `heartbeat-push-tcp`,
  `heartbeat-push-udp`, `heartbeat-require-hmac`, `heartbeat-rotate-nudge`)
  so the existing E2E ids survive; add a `data-testid` on the section trigger
  (e.g. `heartbeat-push-toggle`) for the tests to expand it.

Whether the section should auto-expand when `require_hmac` is already on is
left to the implementer — the `summary` line covers it, so the default answer
is **no, stay collapsed**.

### 2. Move the sketch out of the setup page, link to the docs

- Delete `arduinoSketch()` and the `heartbeat-push-sketch` block from
  `HeartbeatPushEndpoint`. Drop the `sketchLabel` / `sketchHint` keys from
  all four locales (`en`, `fr`, `de`, `es` in `web/dash0/src/locales/*/checks.json`).
- In its place, a one-line pointer using the shared `DocsLink` component
  ([`components/shared/docs-link.tsx`](../../web/dash0/src/components/shared/docs-link.tsx))
  to `/docs/features/embedded-push#a-minimal-arduino--esp-sketch` (verify the
  Docusaurus-generated anchor slug), with copy along the lines of
  *"For a ready-to-flash signed (SP2) sender, see the Arduino / ESP sketch
  in the Embedded devices docs."* — one new locale key, four locales.
- Make sure the docs sketch is self-sufficient without the page's
  substitution: today the page fills in the check's real `org/identifier`,
  token, host and port; the docs version uses placeholders. Add a short
  sentence above the docs sketch telling the reader which four values to
  replace and where to find them (the check's endpoint card). No new docs
  page — `embedded-push.md` already has the section.

### 3. Tests

- Update [`e2e/check-heartbeat-push.spec.ts`](../../web/dash0/e2e/check-heartbeat-push.spec.ts):
  - assert the section is **collapsed on load** (one-liners not visible,
    summary text visible), then expand via the trigger and assert the TCP /
    UDP commands as today (lines ~101–116 currently assert them directly).
  - replace the sketch assertions (lines ~117–126: "Arduino",
    `SP2 %s/%s 0 %llu`, `mbedtls_md_hmac_starts`) with an assertion that
    the docs link is present and points at the embedded-push page.
  - keep the require-HMAC toggle + rotate-nudge tests, and add one asserting
    the nudge is visible **while the section is collapsed**.
- Remove and add locale keys in all four locales in the same change — a key
  that survives in one locale only, or that is still referenced by a `t()`
  call after removal, is a bug. `bun run test:unit` and `bun run lint` in
  `web/dash0` must stay green (no *new* eslint errors; the base has known
  react-hooks debt).

### Out of scope

- The HTTPS endpoint block above (`HeartbeatEndpoint`) stays as is — it is
  the primary path and must remain one click away.
- No server-side change; `features.heartbeatPush` is untouched.
