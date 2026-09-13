---
model: opus
effort: high
---

# The browser check captures JPEG, calls it PNG, and the attachment store rejects it

## Problem

`server/internal/checkers/checkbrowser/checker.go:33-36` declares:

```go
// screenshotQuality is chromedp.FullScreenshot's quality argument. It is
// ignored for PNG (lossless) and only matters if this ever switches to JPEG;
// 90 is the conventional value to pass.
const screenshotQuality = 90
```

That comment is **false**. `chromedp.FullScreenshot(res, quality)` (chromedp
v0.16.0, `screenshot.go:139`) selects `page.CaptureScreenshotFormatPng` **only
when `quality == 100`**, and JPEG for every other value. Verified empirically
against a real headless-shell: the captured bytes begin `ff d8 ff e0` (JPEG
SOI), not `89 50 4e 47`.

So `server/internal/checkers/checkbrowser/session.go:469` and the browser
checker's capture path have always produced **JPEG bytes**, stored in a field
literally named `PNG`.

### This is not only a naming problem — the capture almost certainly never lands

Every write into the attachment store funnels through
`attachments.Service.put` → `sniffMime`
(`server/internal/handlers/attachments/service.go:230`, `:259-272`), and for
`KindScreenshot` that function accepts **PNG magic bytes only**:

```go
case KindScreenshot:
    if len(body) >= len(pngMagic) && bytes.Equal(body[:len(pngMagic)], pngMagic) {
        return mimePNG, nil
    }
    return "", fmt.Errorf("%w: %s attachments must be %s", ErrUnsupportedMediaType, KindScreenshot, mimePNG)
```

`pngMagic` is `89 50 4E 47 0D 0A 1A 0A` (`service.go:47`). JPEG bytes fail that
comparison, so **both** write paths should be failing closed today:

- **In-process worker** — `incidents.Service.persistScreenshot`
  (`server/internal/handlers/incidents/service.go:487`) calls
  `PutIncidentScreenshot` (`attachments/service.go:139-151`), which goes through
  the same `put` → `sniffMime`. Rejected.
- **Deported agent** — `POST /api/v1/agent/attachments`, body uploaded by
  `checkworker/backend/ws_capture.go` with a hardcoded
  `mimePNG = "image/png"` (`ws_capture.go:39`) Content-Type. The server ignores
  the declared type and sniffs; sniffing rejects it too.

The unit tests never caught it because their fixtures are hand-written PNG magic
(`handlers/agentws/screenshot_upload_test.go:237` and friends) — a real capture
is never fed through the store.

**First task of this spec is to confirm that failure mode end to end**, not to
assume it. If screenshots *are* landing for browser checks today, something in
the above reading is wrong and the fix changes shape.

### And even if it landed, it would not render

`server/internal/handlers/files/handler.go:215` sets
`X-Content-Type-Options: nosniff` on every served file, and
`safeInlineMIME` (`handler.go:163`) serves it inline with the **stored** type.
JPEG bytes stored as `image/png` + `nosniff` = a browser that refuses to decode
it. The incident card at
`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1459` is a plain
`<img src={shot.downloadUrl}>`, so the user sees a broken-image icon.

### The name "PNG" is load-bearing across the whole chain

- `checkerdef.Screenshot.PNG` (`server/internal/checkers/checkerdef/diagnostics.go:69`),
  plus a type doc that says "PNG" five times (`:50-67`).
- `agents/capturecache` — `entry.png`, `Cache.Put(png []byte)`, package doc
  (`cache.go:2,5,50,126-146,172,222`).
- `checkworker/backend/ws_capture.go` — `mimePNG`, `stashCapture` doc.
- `handlers/attachments/service.go` — `mimePNG`, `pngMagic`, `PutIncidentScreenshot`,
  and the stored filename `incident-<uid>-screenshot.png` (`:148`).
- `handlers/incidents/service.go` — `persistScreenshot`, `shot.PNG`, docs.
- `checkers/checkjs/browser.go:425` — the JS check's `page.screenshot()` bridge.
- `server/internal/app/openapi/openapi.yaml:11161,11213` — "the PNG a failing
  browser check took".
- `wiki/features/browser-monitoring.md` and the JS-check docs.

### The live test knows, and says so

`server/internal/checkers/checkbrowser/session_live_test.go:197-205` deliberately
asserts "a real image" rather than "a PNG", with a comment explaining the
mismatch it found and chose not to encode. That comment is the regression guard
we now want to replace with a real assertion.

## Proposal

**Support a real, explicit set of formats — PNG, JPEG and WebP — end to end,
instead of picking one and lying about it.** The format becomes a value that
travels with the bytes rather than a constant baked into a field name.

Nothing here changes *when* a screenshot is taken, what triggers it, or the
4 MiB `MaxScreenshotBytes` / `MaxAttachmentBytes` ceiling.

### 1. Confirm the live failure first (positive control)

Before touching anything, prove the current state with a test that would pass on
a fixed build and fail on today's:

```bash
docker run -d --rm -p 9222:9222 --shm-size=1g chromedp/headless-shell:152.0.7939.3 \
  --remote-allow-origins='*' --disable-gpu --no-sandbox
SP_CHECKERS_BROWSER_CDP_URL=ws://127.0.0.1:9222 \
  go test ./internal/checkers/checkbrowser/ -run TestSessionDrivesARealPage
```

Then feed a *real* capture (not a fixture) through
`attachments.Service.PutIncidentScreenshot` and assert what happens today. Write
down the observed behaviour in the spec's eventual commit message — "screenshots
for browser checks are silently dropped at the store" is a shipped-bug claim and
needs evidence, not inference.

### 2. Capture: a format, not a magic quality number

In `checkbrowser`:

- Replace `screenshotQuality = 90` with an explicit format + quality pair. Drive
  the capture through `page.CaptureScreenshot` with
  `WithFormat(...)`/`WithQuality(...)` and `WithCaptureBeyondViewport(true)`
  (which is what `chromedp.FullScreenshot` does internally) so **WebP is
  reachable at all** — `chromedp.FullScreenshot` can only ever emit PNG or JPEG.
  `page.CaptureScreenshotFormatWebp` exists in the pinned cdproto
  (`page/types.go:1695`).
- Default: **WebP, quality ~85.** It is 25-35% smaller than JPEG at equal
  quality, is decoded by every browser that can open the dashboard, and keeps
  the 4 MiB cap comfortable on a busy full-page capture. PNG at quality 100 is
  the alternative the original comment imagined, but a full-page PNG of a real
  site is several times larger and would start hitting `MaxScreenshotBytes` on
  exactly the pathological pages the cap exists for. **Record the chosen default
  and the reasoning in the constant's doc comment** — the whole point of this
  spec is that the format stops being folklore.
- If the default is anything other than PNG, the `MaxScreenshotBytes` doc
  comment at `checker.go:18-25` ("caps a single captured PNG", "a full-page PNG
  of a real site is tens to a few hundred KB") has to be rewritten to match.

Whether the format is **configurable per check** is an open question — see
Open questions. The minimum bar is that the code path supports all three and the
stored artifact is correctly typed.

### 3. Carry the format with the bytes

- Rename `checkerdef.Screenshot.PNG` → `Image []byte` (still `json:"-"`), and
  add a sibling that names the format — either `Format string` (`"png"` /
  `"jpeg"` / `"webp"`) or the MIME type directly. Whichever, it must be
  serialized: the deported-agent marker frame has to tell the server what is
  coming so the server is not left sniffing blind.
- Rewrite the `Screenshot` type doc (`diagnostics.go:50-72`) — it currently
  explains the `json:"-"` decision in terms of "a megabyte PNG". The decision
  is right; the noun is wrong.
- `agents/capturecache` — rename `png` → `image`/`blob` and store the format
  alongside it so the upload can declare the truth.
- `checkworker/backend/ws_capture.go` — the hardcoded `mimePNG` becomes the
  capture's actual type on the `Content-Type` header.

### 4. Fix the attachment store's sniffer

`sniffMime` is the right design — sniffing rather than believing the caller is
what makes the serving rules safe — it just knows one format. Widen it to a
small magic-byte table for the screenshot kind:

| Format | Magic |
|---|---|
| PNG  | `89 50 4E 47 0D 0A 1A 0A` |
| JPEG | `FF D8 FF` |
| WebP | `52 49 46 46 ?? ?? ?? ?? 57 45 42 50` (`RIFF....WEBP`) |

Anything else stays refused — fail closed, per the existing doc. Every accepted
type must also be on `safeInlineMIME`'s allowlist in
`handlers/files/handler.go:163` (PNG, JPEG and WebP already are — confirm, do
not assume).

The stored filename `incident-<uid>-screenshot.png`
(`attachments/service.go:148`) must take the extension matching the sniffed
type, or a user who downloads the attachment gets a `.png` their OS cannot open.

### 5. Existing rows

Any screenshot attachment already stored is either absent (if §1 confirms the
drop) or mislabelled. Decide and state which:

- If nothing ever landed, there is nothing to migrate — say so explicitly in the
  commit rather than leaving it unanswered.
- If rows exist with `image/png` and JPEG bytes, they need either a one-shot
  re-sniff migration or a documented "these render broken until the incident
  ages out" note. Screenshots are short-lived evidence, so "let them age out" is
  a legitimate answer — but it has to be a decision, not an omission.

### 6. Tests

- **Tighten the live test.** `session_live_test.go:197-205`: delete the
  apologetic comment and assert the *actual* format's magic bytes. It must fail
  if the format silently changes again.
- **A store test with real bytes.** Feed a genuine capture (or at minimum
  correct magic-byte prefixes for all three formats plus a rejected one) through
  `attachments.Service` and assert the stored `MimeType` and filename extension.
  This is the test whose absence let the bug ship — the existing fixtures are
  hand-written PNG headers that can never disagree with the sniffer.
- **A negative control.** A body with no recognised magic must still be refused
  for `KindScreenshot`.
- **An end-to-end render check.** The incident card
  (`incidents.$incidentUid.tsx:1459`) serves the attachment with `nosniff`; a
  test that the served `Content-Type` matches the bytes is what proves the
  broken-image icon is gone.
- `checkjs`'s `page.screenshot()` path (`checkjs/browser.go:425`) goes through
  the same capture — cover it too, and update
  `wiki/features/browser-monitoring.md`, the JS-check docs and
  `openapi.yaml:11161,11213`.

## Open questions

1. **Per-check configurability, or one server-wide format?** A per-check
   `screenshotFormat` field is more rope than most users want, and every new
   check-config field is a migration plus a form field plus an import/export
   round-trip. A server-wide system parameter, or simply a well-chosen constant,
   may be the right scope. Recommendation: ship the constant, keep the plumbing
   format-aware so a parameter is a one-line follow-up.
2. **Default format.** WebP is the recommendation above; JPEG is the
   conservative choice if the capture is ever destined for a surface less modern
   than the dashboard. Grep confirms today's only consumer is the incident
   detail API → dash0 `<img>` plus the signed download URL, which makes WebP
   safe — re-confirm before committing to it.
3. **Does the JS check's `page.screenshot()` want to expose the format to the
   script?** Out of scope unless it falls out for free.

## Resolved open questions

> 1. **Per-check configurability, or one server-wide format?**

**Decision: ship a single constant, no per-check field and no system parameter.**
Keep the plumbing format-aware (the format travels with the bytes, per §3) so a
parameter is a one-line follow-up if it is ever asked for. Do not add a
`screenshotFormat` check-config field: that is a migration plus a form field plus
an import/export round-trip for rope nobody has asked for.

> 2. **Default format.** WebP is the recommendation above; JPEG is the
> conservative choice ... which makes WebP safe — re-confirm before committing to it.

**Decision: WebP.**

The re-confirmation the spec asks for was done, and the spec's premise was only
partly right. Consumers found:

- The incident detail API → dash0 `<img>` + the signed download URL, as the spec
  says. Both are fine with WebP.
- **`captureScreenshotHelp` in `web/dash0/src/locales/{en,de,es,fr}/checks.json`
  (~line 1043) explicitly promises the user a *PNG***, e.g. de: *"Speichert ein
  PNG der Seite…"*. The spec missed this. **Updating that string in all four
  locales is part of this spec's scope** — shipping WebP while the UI still
  promises PNG just relocates the lie. `bun run test:unit` is the gate that
  catches a locale left behind.
- `server/internal/handlers/feedback/service.go` has its own, unrelated
  `Screenshot` field (user-submitted feedback attachments). **Do not touch it** —
  it is a different pipeline and is not part of this defect.

> 3. **Does the JS check's `page.screenshot()` want to expose the format to the script?**

**Decision: out of scope.** Leave `page.screenshot()`'s signature alone. It must
keep working and keep producing a valid attachment through the fixed path — that
is the only requirement on it here. (Note `page.screenshot()` landed in spec
2026-09-12-06, so the `checkjs` browser global is now a second caller of this
capture path; both must end up on the shared, format-aware implementation.)
