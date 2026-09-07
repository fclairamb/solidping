---
model: sonnet
effort: medium
---

# The French dashboard still shows English on the check form and the incident page, and every demo refusal is a raw, contradictory server string

## Problem

Five screenshots taken on 2026-09-07 in the shared live demo on `solidping.io`,
browser locale `fr`. They show two distinct defects.

### 1. Four components never went through i18n (or only half did)

The dashboard ships four locales (`en`, `fr`, `de`, `es`, wired in
[`web/dash0/src/i18n.ts`](../../web/dash0/src/i18n.ts) with `fallbackLng: "en"`),
yet these render English inside otherwise-French pages:

| Component | i18n state | English the visitor sees |
|---|---|---|
| [`components/shared/check-form.tsx`](../../web/dash0/src/components/shared/check-form.tsx) | partial — 33 `t()` calls (the flap/recovery block is French: "Fenêtre d'instabilité", "Facteur de progression") next to ~25 hard-coded labels | `Edit Check` / `New Check`, `Update the monitoring check parameters`, `Save Changes` / `Create Check` (949-951); `Identity & target`, `What to monitor, and how to find it later` (1078-1079); `Enabled` (1085); `Type` (1097); the collapsible `title="Advanced"` (1683) and its summary `timeout 15s (default)` (1036); `Cancel` (1799); the `Notifications` card title (1482); placeholders `120 (default)`, `5 (default)`, `21600 (default)`, `2 (default)`, `8 (default)`, `15 seconds (default)` (1607-1697); inline validation `Timeout must be between 1 and 30 seconds`, `Minimum/Maximum check interval for … is …` (852, 868, 872); `Failed to create check` / `Failed to update check` (941-943) |
| [`routes/orgs/$org/incidents.$incidentUid.tsx`](../../web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx) | mostly translated (113 `t()` calls) — the event log is French — but two cards were skipped | **Status updates** card: title, `Add update` button, `Narrative updates published to your status page for this incident.` (489-504), dialog footer `Saving…` / `Save changes` / `Add update` (443-446), `Select a status page` (361). **Notifications** card: title, `Who was notified and the delivery status.`, `No notifications recorded for this incident yet.`, table heads `Time` `Event` `Status` `Target` `Source` `Channel` `Error` (1794-1822) |
| [`components/incidents/incident-publications-panel.tsx`](../../web/dash0/src/components/incidents/incident-publications-panel.tsx) | none — zero `useTranslation` | `Published on` (87), `Status pages where customers can see this incident. Publishing writes a customer-readable title — never the internal one.`, `Not published on any status page.` (99), `Publish on`, `Select a status page` (156), `Severity`, `No badge` (178), `Publish` |
| [`components/shared/status-update-form.tsx`](../../web/dash0/src/components/shared/status-update-form.tsx) | none — 16 hard-coded labels, `Select a status page` (172) | the whole status-update dialog |
| [`routes/orgs/$org/status-pages.$statusPageUid.incidents.$uid.tsx`](../../web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.incidents.$uid.tsx) | one leftover | `No badge` (183) |

Screenshot 3 (incident page, Notifications card above "Journal des
événements") and screenshot 4 (Status updates + Published on) are the incident
page; screenshots 1 and 5 are the check edit form.

Not a defect, for the record: the Notifications table showing an "Incident
escaladé" badge in its *Event* column is by design — Status, Target, Source and
Channel are the columns to its right, off-screen on a phone.

### 2. The demo refusal is untranslated, wears three different costumes, and contradicts itself

Every write a demo session is not allowed answers `403 DEMO_READ_ONLY` with the
same English title, `auth.DemoWriteMessage`
([`server/internal/handlers/auth/demo_guard.go:12-14`](../../server/internal/handlers/auth/demo_guard.go)):

> This is the shared read-only live demo. Creating and editing your own checks
> is allowed; everything else is not. Sign up for a free account to make changes.

Three problems with how it reaches the visitor:

- **It is never translated.** The API client surfaces `error.title` verbatim
  ([`api/client.ts:403-405`](../../web/dash0/src/api/client.ts)) and makes it
  `ApiError.message`, so every consumer that prints `err.message` prints
  English in a French UI.
- **Three surfaces for one refusal, sometimes two at once.** The client fires a
  blue *info* toast (`toast.info`, `announceDemoReadOnly`, client.ts:275-277)
  for every `DEMO_READ_ONLY`. On top of that the check form *also* does
  `setError(err.message)` and renders a red `destructive` Alert
  ([`check-form.tsx:938`](../../web/dash0/src/components/shared/check-form.tsx),
  1069) — screenshot 5 is that banner, screenshot 1 is the toast on the same
  page. The status-page section dialog instead does `toast.error(err.message)`
  ([`status-pages.$statusPageUid.index.tsx:215`](../../web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx))
  — screenshot 2, red "!" toast. Same refusal: blue info, red error, red banner,
  depending on the page.
- **The wording contradicts the situation.** Screenshot 5: a visitor on the
  *Edit Check* page of the seeded `solidping.io A record` check reads "creating
  and editing your own checks is allowed". The server actually refused for
  *ownership* (`checks.ErrDemoReadOnly`, "the demo can only change checks it
  created", [`checks/demo.go:28`](../../server/internal/handlers/checks/demo.go))
  but `writeDemoReadOnlyError` ([`checks/handler.go:951-956`](../../server/internal/handlers/checks/handler.go))
  puts that reason in `detail`, which no UI surface shows, under the generic
  title.

The root cause of the contradiction is upstream of the message: the **edit
route has no demo gating**. The detail page already computes ownership with
`canDemoEditCheck` ([`lib/demo.ts:40-49`](../../web/dash0/src/lib/demo.ts)),
hides the edit button for a seeded check and explains why with
`org:demo.seededCheck` ("…Clone it to get a copy you can edit",
[`checks.$checkUid.index.tsx:746-750`, `1047-1056`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)).
But [`checks.$checkUid.edit.tsx`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx)
renders a fully enabled `CheckForm` for anyone (zero `isDemo` references), and a
demo visitor reaches it through the checks-list row menu
([`checks.index.tsx:577`](../../web/dash0/src/routes/orgs/$org/checks.index.tsx))
or the breadcrumb. They fill the form and only learn on Save.

Same gap on the status page detail:
[`status-pages.$statusPageUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx)
still shows "add section" / "add resource" to demo sessions (screenshot 2, the
`+` under "Aucune section pour le moment"), whereas the status-pages list and
the integrations page already swap the affordance for `DemoReadOnlyNote`
(spec 2026-09-06-02 §8 "hide what cannot be done";
[`status-pages.index.tsx:315`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx),
[`integrations.index.tsx:195`](../../web/dash0/src/routes/orgs/$org/integrations.index.tsx)).

## Proposal

### A. Translate the four components

Move every string in the table above into the locale files — `checks.json` for
the check form, `incidents.json` for the incident page cards and the
publications panel, `statusUpdates.json` (already exists) for the status-update
form — in **all four** locales `en`, `fr`, `de`, `es`. Include the placeholders
(`120 (default)` → `t("form.defaultPlaceholder", { value: 120 })` or similar),
the inline validation messages and the two `Failed to … check` fallbacks; a
form that validates in English is still an untranslated form.

Keep the existing key style (`form.*`, `detail.*`); do not rename keys the
components already use.

### B. One localized surface per demo refusal

1. **Translate by code, not by title.** In `api/client.ts`, when the response is
   `403 DEMO_READ_ONLY`, build the `ApiError` with a *localized* message
   (`i18n.t("org:demo.writeRefused")`, i18next is usable outside React) instead
   of `error.title`. Every `err.message` consumer — the check-form banner, the
   section toast, anything added later — becomes localized without touching it.
   Keep the English `title` in the JSON body for curl/CLI users; that is what
   `DemoWriteMessage` is for.
2. **One surface, one variant.** Keep the deduplicated toast as the single
   announcement (pick one variant and use it everywhere — the refusal is not an
   error the visitor caused, so `info`/`warning` fits better than `error`). Make
   the check form and the section dialog *not* render a second copy for this
   code: `check-form.tsx` should skip `setError` for `DEMO_READ_ONLY` the way
   [`checks.new.tsx:222`](../../web/dash0/src/routes/orgs/$org/checks.new.tsx)
   already swallows it, and the section dialog should not `toast.error` on top
   of the client's toast.
3. **Reword so it is true in both cases.** The route-level refusal and the
   ownership refusal share one message; make it accurate for both, e.g.
   *"This is the shared live demo. Only checks you create here can be changed —
   sign up free to change anything else."* Update `DemoWriteMessage` and the new
   locale key together; the `detail` from `ErrDemoReadOnly` stays as is.
4. **Gate the edit route.** In `checks.$checkUid.edit.tsx`, when
   `useIsDemoSession()` and `!canDemoEditCheck(…, check.createdBy)`, render the
   existing `org:demo.seededCheck` alert plus a **Clone** button (the detail
   page's `useCloneCheck` flow) instead of the form. A visitor should never be
   able to fill a form the server will refuse.
5. **Hide what cannot be done on the status page detail.** Replace the
   add-section / add-resource affordances with `<DemoReadOnlyNote />` for demo
   sessions, mirroring `status-pages.index.tsx:315`.

### C. Tests

- `api/client.test.ts`: `DEMO_READ_ONLY` yields a localized `ApiError.message`
  (assert against the `fr` bundle, not the English title) and exactly one toast.
- A vitest that loads every namespace for `en` and asserts the same key set
  exists in `fr`, `de`, `es` — there is no generic parity test today (only
  `lib/operator-notifications-locales.test.ts` for one feature), which is how
  these components slipped through. Scope it to the whole `locales/` tree so the
  next skipped component fails CI.
- Playwright (demo E2E suite): a demo session opening `/checks/<seeded>/edit`
  sees the seeded-check note and a Clone button, not a form; the status page
  detail shows the read-only note and no add-section button.
- Backend: the `DemoWriteMessage` reword updates
  `demo_guard_route_table_test.go` / `demo_rules_test.go` expectations.

### Open question

Whether to distinguish the ownership refusal with its own error code (so the
client can say "clone it" rather than the generic sentence). Not needed if B.4
lands — the ownership path then only fires for direct API callers, who get
`detail`. Default: no new code.
