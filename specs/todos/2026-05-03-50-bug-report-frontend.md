# In-app bug reports — frontend (desktop + mobile)

**Depends on:** spec #49 (bug-report backend).

## Context

Adds the in-app bug-report UI to `web/dash0` (and, identically, to
`web/status0` if it has authenticated surfaces today — confirm during
implementation; if it doesn't, scope is dash0 only). When the feature is
enabled server-side:

- **Desktop**: a bug icon in the top-right header + keyboard shortcut.
  Clicking captures a screenshot, opens a dialog with annotation tools
  (rectangle / arrow / text) and a comment field.
- **Mobile**: a "shake to report" gesture opens the same dialog with a
  simplified layout (no annotation canvas, just textarea + submit).

Submission posts to `POST /mgmt/report` (spec #49). Visibility of the
icon and shake-listener is gated on `GET /api/v1/features` returning
`bugReport: true`.

## Honest opinion

1. **Combine desktop + mobile in one spec.** They share the same dialog
   component and the same submission code path; splitting them would
   duplicate the description of `FeedbackDialog.tsx` for no benefit.
2. **Use `html-to-image` for screenshot capture, not `html2canvas`.**
   Smaller, faster, returns a `Blob` directly. Skip `<canvas>` elements
   in the filter (charts won't serialize cleanly) — accept that charts
   appear blank in screenshots; the comment text covers the gap.
3. **Annotations are stored as relative coordinates (0..1).** The
   screenshot is rendered at whatever `pixelRatio` we capture; the canvas
   sits over an `<img>` tag. Storing absolute pixels means the rendered
   PNG has annotations at the wrong place after compositing. (Trust me;
   we'd have hit this otherwise.)
4. **Composite annotations into the PNG before upload, do not store
   them separately.** Two reasons: (a) the GitHub issue should show what
   the user actually drew without an extra rendering step; (b) we don't
   want the backend or anyone else to have to reimplement the annotation
   renderer.
5. **Shake detection on iOS Safari requires explicit
   `DeviceMotionEvent.requestPermission()` from a user gesture.** First
   visit on iOS, the listener is dead until the user taps something. We
   handle this by attaching the request to the first user interaction
   (`pointerdown`) of the page and silently degrading if the permission
   prompt is denied — no big banner, the desktop icon still works on
   tablet.
6. **Skip screen-recording on mobile entirely.** `getDisplayMedia` is
   absent or broken on most mobile browsers, and the recording UI eats
   too much vertical space. Desktop only.
7. **Don't show the icon at all when the server feature flag is off.**
   Showing a disabled icon invites questions. If
   `/api/v1/features.bugReport === false`, the icon, the shortcut
   handler, and the shake listener are all not registered.
8. **The keyboard shortcut should be `Ctrl/Cmd+Shift+B`.** `Shift+?` is
   tempting (used elsewhere) but conflicts with command-palette
   shortcuts. `B` is mnemonic ("Bug") and is unbound across the app
   today. We DO NOT bind it inside text inputs.

## Scope

**In**

- Feature-flag fetch + cache via `useFeatures()` hook (TanStack Query).
- New `feedback/` component family in `web/dash0/src/components/feedback/`:
  - `FeedbackButton.tsx` — bug icon for the header.
  - `FeedbackDialog.tsx` — main dialog (responsive desktop / mobile).
  - `AnnotationCanvas.tsx` — overlay for drawing (rect/arrow/text).
  - `AnnotationToolbar.tsx` — tool / colour / undo controls.
  - `useFeedback.ts` — capture + submit hook (keyboard + shake handlers).
  - `errorCollector.ts` — global console-error / window-error capture
    (10-entry ring buffer).
  - `types.ts` — annotation shapes.
- Wire `<FeedbackButton />` and `<FeedbackDialog />` into `OrgLayout`
  header. Render conditionally on `useFeatures().bugReport`.
- i18n keys in a new `feedback.json` per locale (`en`, `fr`, `de`,
  `es`).
- Translation files for the four locales already present.

**Out**

- Annotation editing after the fact (the canvas is one-shot:
  add/undo/clear).
- Voice notes / audio capture.
- Network-tab capture (the `recent_errors` ring buffer is enough).
- Offline submission queue.
- A "your past reports" history page.

## Design

### 1. Feature-flag hook

```ts
// web/dash0/src/api/hooks.ts (extend)
export function useFeatures() {
  return useQuery({
    queryKey: ['features'],
    queryFn: () => apiFetch<{ bugReport: boolean }>('/api/v1/features'),
    staleTime: 5 * 60 * 1000,
  })
}
```

### 2. Layout wiring

In `web/dash0/src/routes/orgs/$org.tsx`, at the right side of the
header (next to `LanguageSwitcher` / `ThemeToggle`):

```tsx
const { data: features } = useFeatures()
// ...
{features?.bugReport && (
  <>
    <FeedbackButton onClick={feedback.open} isCapturing={feedback.isCapturing} isRecording={feedback.isRecording} />
    <FeedbackDialog open={feedback.isOpen} onOpenChange={(o) => !o && feedback.close()}
      screenshot={feedback.screenshot} video={feedback.video}
      isCapturing={feedback.isCapturing} onStartRecording={feedback.startRecording}
      onSubmit={feedback.submit}
    />
  </>
)}
```

`useFeedback()` returns the open/close/submit handlers and registers
the keyboard + shake listeners; if the flag is off, the hook returns
no-ops and registers nothing (so we don't even hold motion permission
on iOS).

### 3. `useFeedback` hook

Responsibilities:

- `open()` — calls `html-to-image.toBlob(document.documentElement, ...)`
  and stores the resulting blob; sets `isOpen` true.
- `submit(comment, annotations)`:
  - composites annotations onto the screenshot canvas → final PNG blob.
  - builds a `FormData` with `screenshot`, `comment`, `url`,
    `org` (slug), `annotations` (JSON), `context` (JSON: viewport, UA,
    `recent_errors`, frontend build version, user email).
  - `POST /mgmt/report` with `Authorization: Bearer ...` if logged in.
  - swallows / surfaces errors via the dialog state.
- `useEffect` for keyboard shortcut: `Ctrl/Cmd+Shift+B` outside inputs.
- `useEffect` for shake (mobile): `devicemotion` listener with a
  threshold + 2-second debounce; calls `open()` on shake. Wraps the
  iOS permission request in a one-shot `pointerdown` handler.
- (Desktop) `startRecording()` / `stopRecording()`: uses
  `navigator.mediaDevices.getDisplayMedia` + `MediaRecorder`. Skipped
  on `window.innerWidth < 768`.

### 4. Error collector

```ts
// errorCollector.ts — imported once at app entry (main.tsx) so the buffer
// fills regardless of whether the user ever opens the dialog.
const recentErrors: string[] = []
const MAX = 10
const orig = console.error
console.error = (...a) => { push(a.map(String).join(' ')); orig(...a) }
window.addEventListener('error', (e) => push(`${e.message} at ${e.filename}:${e.lineno}:${e.colno}`))
window.addEventListener('unhandledrejection', (e) => push(`Unhandled rejection: ${e.reason}`))
function push(s: string) { recentErrors.push(s); if (recentErrors.length > MAX) recentErrors.shift() }
export function getRecentConsoleErrors() { return [...recentErrors] }
```

### 5. Dialog layout

Below `md` breakpoint (< 768 px):

- `max-w-sm w-[calc(100vw-1rem)]`.
- **Hidden**: screenshot preview, annotation canvas, annotation toolbar,
  record-screen button.
- **Shown**: title, comment textarea (auto-focus, `rows={5}`), context
  line (path • browser • viewport • user), Cancel / Submit. Screenshot
  is still captured silently and submitted in the FormData; annotations
  array is empty.

At `md` and up:

- `max-w-4xl w-full`.
- Two-column body: left = preview + annotation canvas, right = toolbar
  (rect / arrow / text + 6 preset colours + custom colour + undo +
  record button). Below body: comment textarea (`rows={3}`).

The Dialog uses the existing shadcn-style `Dialog` primitive at
`web/dash0/src/components/ui/dialog.tsx`.

### 6. Annotation canvas

- Annotations stored as `{type: 'rect' | 'arrow' | 'text', ...}` with
  coordinates in `[0, 1]`.
- `renderAnnotations(ctx, annotations, w, h)` is exported and used both
  during interactive drawing (preview) and during composition into the
  final PNG before upload.
- Pointer events go through a `<canvas>` overlay sized to match the
  preview `<img>`. ResizeObserver keeps the canvas dimensions in sync.
- Text tool spawns an inline `<input>` at the click position; on
  Enter / blur the value becomes a `TextAnnotation`.

### 7. Translations

New `feedback.json` keyspace (one per locale):

```json
{
  "trigger_title": "Report a bug",
  "stop_recording": "Stop recording",
  "dialog_title": "Report a bug",
  "comment_label": "What happened?",
  "comment_placeholder": "Describe what went wrong…",
  "no_screenshot": "No screenshot available",
  "capturing": "Capturing screen…",
  "record": "Record",
  "rerecord": "Re-record",
  "tool_rect": "Box",
  "tool_arrow": "Arrow",
  "tool_text": "Text",
  "undo": "Undo",
  "cancel": "Cancel",
  "submit": "Send",
  "success": "Thanks! Your report was sent.",
  "error": "Something went wrong. Please try again."
}
```

Add to `web/dash0/src/i18n.ts`.

## Files affected

| File                                                                         | Change                                              |
|------------------------------------------------------------------------------|-----------------------------------------------------|
| `web/dash0/src/components/feedback/FeedbackButton.tsx`                       | New                                                 |
| `web/dash0/src/components/feedback/FeedbackDialog.tsx`                       | New                                                 |
| `web/dash0/src/components/feedback/AnnotationCanvas.tsx`                     | New                                                 |
| `web/dash0/src/components/feedback/AnnotationToolbar.tsx`                    | New                                                 |
| `web/dash0/src/components/feedback/useFeedback.ts`                           | New                                                 |
| `web/dash0/src/components/feedback/errorCollector.ts`                        | New                                                 |
| `web/dash0/src/components/feedback/types.ts`                                 | New                                                 |
| `web/dash0/src/main.tsx`                                                     | Import `errorCollector` so the buffer fills early   |
| `web/dash0/src/api/hooks.ts`                                                 | Add `useFeatures()`                                 |
| `web/dash0/src/routes/orgs/$org.tsx`                                         | Mount feedback button + dialog in header            |
| `web/dash0/src/locales/{en,fr,de,es}/feedback.json`                          | New                                                 |
| `web/dash0/src/i18n.ts`                                                      | Register the `feedback` namespace                   |
| `web/dash0/package.json`                                                     | Add `html-to-image` dependency                      |
| `web/dash0/e2e/bug-report.spec.ts`                                           | New Playwright test                                 |

## Tests

### Playwright (`web/dash0/e2e/bug-report.spec.ts`)

1. **Desktop happy path** — viewport 1280×720; intercept
   `/api/v1/features` → `{bugReport: true}`; click the bug icon → the
   dialog opens; type a comment; click Submit → assert request to
   `/mgmt/report` includes `screenshot`, `comment`, `url`, `context`.
2. **Keyboard shortcut** — press `Cmd+Shift+B` (or `Ctrl+Shift+B`) at
   `body` → dialog opens. Press it inside a textarea → dialog stays
   closed.
3. **Mobile layout** — viewport 375×667; intercept features true;
   programmatically `window.dispatchEvent(new Event('feedback:open'))`
   (the hook listens for this in test mode) → dialog opens, preview &
   toolbar are not visible, textarea is auto-focused.
4. **Feature off** — features endpoint returns `{bugReport: false}`,
   the bug icon is not rendered and the shortcut does nothing.

### Unit (Vitest)

- `errorCollector` — pushes / drops oldest beyond MAX, captures
  `error` and `unhandledrejection` events.
- `AnnotationCanvas.renderAnnotations` — pure function tests over rect /
  arrow / text rendering on a `node-canvas` `CanvasRenderingContext2D`.

## Verification

1. `make build` — type checks pass.
2. `make dev-test` with `SP_APP_GITHUB_ISSUES_TOKEN` and
   `SP_APP_GITHUB_REPO` set:
   - Bug icon visible in the top-right of the dash0 layout.
   - `Cmd+Shift+B` opens the dialog.
   - Draw a red rectangle around something, type a comment, Submit.
   - GitHub issue appears with the annotated screenshot inline.
3. Open dash0 in Chrome DevTools mobile emulation (iPhone SE):
   - Bug icon appears (the layout already keeps the header).
   - Use the test hook to open the dialog (shake can't be emulated);
     verify only the textarea is shown.
4. Real iOS device: load the app, tap once anywhere (grants
   `DeviceMotionEvent` permission), shake → dialog opens.
   - If the user denies the permission prompt, no error is shown and
     the desktop icon still works (this only matters on iPad in desktop
     mode).
5. With features off → icon is absent, shortcut does nothing.
