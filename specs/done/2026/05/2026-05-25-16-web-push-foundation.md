# Web Push Foundation (service worker + VAPID + subscription capture)

> Prerequisite for `2026-05-25-17-web-push-notification-delivery.md`. Does **not** wire
> subscriptions to any notification path — that is intentionally deferred to the next spec.

## Context

Background Web Push (Push API + VAPID + service worker) is the only notification mechanism
that wakes the browser when the dashboard is not open. The foreground-only `Notification`
API fires only while a tab is active, which is near-useless for downtime alerts.

What exists today:

- A static `web/dash0/public/manifest.webmanifest` (`start_url: /dash0/`, `display:
  standalone`, three icons) — linked at `web/dash0/index.html:8`. The app is therefore
  installable but has no offline or push capability.
- No service worker anywhere. No `vite-plugin-pwa`/`workbox` dependency. No VAPID keys.
  No use of `PushManager`, `Notification`, or `navigator.serviceWorker` in app code (the
  only permission-adjacent call is `navigator.clipboard.writeText` in
  `web/dash0/src/components/channels/channel-form.tsx:331-340`).

Serving constraint: dash0 is a Vite SPA with `VITE_BASE_URL=/dash0/` embedded into the
Go binary. Files placed in `web/dash0/public/` are copied by Vite to the dist root and
served at `/dash0/<filename>`. A service worker registered from `/dash0/sw.js` naturally
covers scope `/dash0/`, which is exactly the app's origin-relative path.

### What already exists (build on these, don't reinvent)

- Dual DB backends: every table/query is implemented twice —
  `server/internal/db/sqlite/` and `server/internal/db/postgres/` — plus migrations in
  both `server/internal/db/{sqlite,postgres}/migrations/`. Latest migration is **033**
  (`033_webhook_settings_url_key`); the new one is **034**. Models live in
  `server/internal/db/models/`. Keep both backends in lockstep.
- DB service interface: `server/internal/db/service.go` — add new method signatures here.
- App config: `server/internal/app/config.go` + koanf env binding. Multi-word `SP_*` env
  vars require a manual reader entry (see `project_koanf_env_quirk`).
- App startup: `server/internal/app/app.go` — good place to initialize VAPID keys.

## Goals

1. A stable VAPID keypair: auto-generated at first startup if env vars are absent,
   persisted in a new `app_settings` DB table, survivable across restarts and deployments.
2. A Go `server/internal/webpush/` package with a `Send` helper that encrypts and POSTs
   to a push service endpoint, returning a typed `ErrSubscriptionGone` sentinel for
   `404`/`410` responses (callers must prune dead subscriptions).
3. A service worker at `web/dash0/public/sw.js` that handles `push` →
   `showNotification` and `notificationclick` → opens the incident URL.
4. A reusable `useWebPushSubscription(org)` hook + `WebPushEnableButton` component in
   dash0 that gates on `Notification.permission`, requests permission, and calls
   `PushManager.subscribe`, returning the resulting `PushSubscription` JSON.
5. A `GET /api/v1/orgs/:org/webpush/vapid-public-key` endpoint exposing the server's
   VAPID public key so the frontend can subscribe.
6. Config env vars `SP_WEBPUSH_VAPID_PUBLIC_KEY`, `SP_WEBPUSH_VAPID_PRIVATE_KEY`,
   `SP_WEBPUSH_SUBJECT` for deployments that pre-provision keys.

## Non-goals

- Where subscriptions are stored or how alerts are routed — that is Spec 2.
- iOS Safari push: requires the PWA to be installed to the home screen (iOS 16.4+);
  not blocked, but not explicitly tested in this spec.
- Foreground-only notifications (`new Notification(...)` without a service worker).
- Web Push for `web/status0` — a separate consideration; the infra built here is reusable.

## Data model

New table `app_settings` — a generic key/value store for server-level configuration that
must survive restarts (starting with VAPID keys; extensible).

Model `server/internal/db/models/app_setting.go`:

| Field | bun column | Notes |
|---|---|---|
| `Key` | `key` pk text | Namespaced string, e.g. `webpush.vapid_public_key` |
| `Value` | `value` text notnull | Plaintext for public key; the private key is stored here too in V1 (no separate encryption envelope — it is never returned to clients) |
| `UpdatedAt` | `updated_at` notnull default now | |

Migration **`034_app_settings`** in **both** `db/sqlite/migrations/` and
`db/postgres/migrations/`:

```sql
-- up
CREATE TABLE app_settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))  -- sqlite form
);
-- postgres: TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
```

Down file drops the table.

DB methods in `db/sqlite/app_settings.go` and `db/postgres/app_settings.go`:
`GetAppSetting(ctx, key string) (string, error)`,
`SetAppSetting(ctx, key, value string) error` (upsert).
Add signatures to `db/service.go`.

## Backend

### `server/internal/webpush/` package

New package; depends on `github.com/SherClockHolmes/webpush-go` (de-facto standard Go
Web Push implementation).

**`sender.go`**

```go
// ErrSubscriptionGone is returned when the push service reports the
// subscription is no longer valid (HTTP 404 or 410). The caller must
// soft-delete the corresponding UserContact or prune it from channel Settings.
var ErrSubscriptionGone = errors.New("webpush: subscription gone")

type Message struct {
    Title string `json:"title"`
    Body  string `json:"body"`
    URL   string `json:"url"` // incident or check URL to open on click
}

// Send encrypts msg and POSTs it to the push service endpoint encoded in
// subscriptionJSON (a JSON-serialised PushSubscription from the browser).
// Returns ErrSubscriptionGone for 404/410 responses.
func Send(ctx context.Context, opts Options, subscriptionJSON string, msg Message) error
```

`Options` holds `VAPIDPublicKey`, `VAPIDPrivateKey`, `Subject` (the `mailto:` or URL
required by the VAPID spec). These are populated from the app config.

**`vapid.go`**

```go
// GetOrCreateVAPIDKeys returns the server's VAPID keypair. It reads
// SP_WEBPUSH_VAPID_PUBLIC_KEY / SP_WEBPUSH_VAPID_PRIVATE_KEY from config
// first; if absent, reads from the app_settings table; if still absent,
// generates a new pair, persists both to app_settings, and returns them.
// Must be called once during app startup and the result stored in config.
func GetOrCreateVAPIDKeys(ctx context.Context, cfg WebPushConfig, db db.Service) (pub, priv string, err error)
```

**Config block** in `server/internal/app/config.go`:

```go
WebPush struct {
    VAPIDPublicKey  string `koanf:"vapid_public_key"`
    VAPIDPrivateKey string `koanf:"vapid_private_key"`
    Subject         string `koanf:"subject"` // e.g. "mailto:admin@example.com"
    Enabled         bool   `koanf:"enabled"`
} `koanf:"webpush"`
```

Wire `SP_WEBPUSH_*` env vars via the manual reader pattern already used for other
multi-word koanf keys (see `project_koanf_env_quirk`).

**App startup** in `server/internal/app/app.go` (or equivalent init phase): call
`webpush.GetOrCreateVAPIDKeys(...)` and store the resolved keys back into
`cfg.WebPush.VAPIDPublicKey` / `VAPIDPrivateKey`. Log a warning if Web Push is
effectively disabled (no keys and DB persistence fails).

### API handler

New handler `server/internal/handlers/webpush/handler.go` (or inline in an existing
lightweight handler):

`GET /api/v1/orgs/:org/webpush/vapid-public-key`
- Requires auth (`RequireAuth` middleware — same as all other `/orgs/:org/` routes).
- Returns `{ "data": { "publicKey": "<base64url VAPID public key>" } }` (camelCase,
  `data` envelope per `CLAUDE.md`).
- Returns `404` with `{ "title": "Web Push not configured", "code": "NOT_FOUND" }` if
  `cfg.WebPush.VAPIDPublicKey` is empty.

Register in `server/internal/app/server.go` alongside the channel routes block (~L771):
```
GET /api/v1/orgs/:org/webpush/vapid-public-key
```

## Frontend (web/dash0)

### Service worker `web/dash0/public/sw.js`

Placed in `public/` so it is served at `/dash0/sw.js` (correct scope for the app).
Must **not** intercept fetch or cache the app shell — keep it push-only to avoid caching
surprises with the embedded Go binary server:

```js
self.addEventListener('push', (event) => {
    const data = event.data?.json() ?? {};
    event.waitUntil(
        self.registration.showNotification(data.title ?? 'SolidPing alert', {
            body:  data.body  ?? '',
            icon:  '/dash0/icon-192.png',
            data:  { url: data.url ?? '/dash0/' },
            tag:   data.url ?? 'solidping',
            renotify: true,
        })
    );
});

self.addEventListener('notificationclick', (event) => {
    event.notification.close();
    event.waitUntil(
        clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
            for (const c of list) {
                if (c.url === event.notification.data.url) return c.focus();
            }
            return clients.openWindow(event.notification.data.url);
        })
    );
});
```

### Registration in `web/dash0/src/main.tsx`

After the React root is mounted:

```ts
if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/dash0/sw.js').catch((err) => {
        console.warn('[solidping] SW registration failed', err);
    });
}
```

### API hook `useVapidPublicKey(org)` — `web/dash0/src/api/hooks.ts`

```ts
export function useVapidPublicKey(org: string) {
    return useQuery({
        queryKey: ['vapidPublicKey', org],
        queryFn:  () => apiFetch<{ publicKey: string }>(`/orgs/${org}/webpush/vapid-public-key`),
        staleTime: Infinity, // key is stable; refetch only on mount
    });
}
```

### Hook `web/dash0/src/hooks/useWebPushSubscription.ts`

```ts
export function useWebPushSubscription(org: string) {
    const { data: keyData } = useVapidPublicKey(org);
    const vapidPublicKey = keyData?.publicKey;

    const isSupported =
        typeof window !== 'undefined' &&
        'serviceWorker' in navigator &&
        'PushManager' in window;

    const subscribe = useCallback(async (): Promise<string | null> => {
        if (!isSupported || !vapidPublicKey) return null;
        const permission = await Notification.requestPermission();
        if (permission !== 'granted') return null;
        const reg = await navigator.serviceWorker.ready;
        const sub = await reg.pushManager.subscribe({
            userVisibleOnly:      true,
            applicationServerKey: urlBase64ToUint8Array(vapidPublicKey),
        });
        return JSON.stringify(sub);
    }, [isSupported, vapidPublicKey]);

    return { isSupported, permission: Notification.permission, subscribe };
}
```

`urlBase64ToUint8Array` is a standard base64url → `Uint8Array` helper (inline, no dep).

### Component `web/dash0/src/components/notifications/WebPushEnableButton.tsx`

A `<Button>` (design-reference primitive) with four states: default ("Enable browser
notifications"), pending ("Enabling…"), granted+subscribed ("Subscribed"), blocked
("Notifications blocked"). Calls `subscribe()` from `useWebPushSubscription` and returns
the subscription JSON via an `onSubscription(json: string) => void` prop for the parent
to persist. Used by Spec 2 in `AddContactForm` and the webpush channel panel.

## Files (high level)

| Area | Files |
|---|---|
| Model | `server/internal/db/models/app_setting.go` |
| Migration | `server/internal/db/sqlite/migrations/034_app_settings.{up,down}.sql`, `server/internal/db/postgres/migrations/034_app_settings.{up,down}.sql` |
| DB methods | `server/internal/db/sqlite/app_settings.go`, `server/internal/db/postgres/app_settings.go`, interface in `server/internal/db/service.go` |
| WebPush package | `server/internal/webpush/sender.go`, `vapid.go`, `sender_test.go`, `vapid_test.go` |
| Config | `server/internal/app/config.go` (new `WebPush` block + env-var wiring) |
| App init | `server/internal/app/app.go` (VAPID key init at startup) |
| Handler + route | `server/internal/handlers/webpush/handler.go`, `server/internal/app/server.go` |
| Service worker | `web/dash0/public/sw.js` |
| SW registration | `web/dash0/src/main.tsx` |
| Hook | `web/dash0/src/hooks/useWebPushSubscription.ts` |
| Component | `web/dash0/src/components/notifications/WebPushEnableButton.tsx` |
| API hook | `web/dash0/src/api/hooks.ts` (`useVapidPublicKey`) |

## Tests

### Backend (table-driven, `testify/require`, `t.Parallel()`)

`server/internal/webpush/sender_test.go`:
- `httptest.NewServer` acting as a push service; assert the request carries the correct
  `Authorization: vapid …` header and `Content-Type: application/octet-stream`.
- Mock returns `201` → `Send` returns `nil`.
- Mock returns `410` → `Send` returns `ErrSubscriptionGone`.
- Mock returns `404` → same.
- Mock returns `500` → `Send` returns a generic error (not `ErrSubscriptionGone`).

`server/internal/webpush/vapid_test.go`:
- First call with empty config and a fresh in-memory DB: generates a keypair, persists
  both rows, returns the keys.
- Second call: reads from DB, returns same keys (idempotent).
- Call with `VAPIDPublicKey` + `VAPIDPrivateKey` pre-set in config: skips DB lookup,
  returns config values directly.

`server/internal/db/sqlite/app_settings_test.go`:
- `GetAppSetting` on a missing key returns `sql.ErrNoRows` (or a typed not-found error).
- `SetAppSetting` upserts correctly (run twice, value changes).

Run new DB-method tests against both sqlite and postgres harnesses (testcontainers) as
existing tests do.

### E2E Playwright (`web/dash0/e2e/webpush.spec.ts`)

- `page.context().grantPermissions(['notifications'])` (pattern from
  `e2e/channels-webhook.spec.ts` for clipboard).
- Navigate to Account → Notifications (the page that will wire the component in Spec 2);
  skip the full UX test here (deferred to Spec 2 E2E) but assert:
  - Service worker registers: `page.evaluate(() => navigator.serviceWorker.getRegistration('/dash0/sw.js'))` returns truthy.
  - VAPID public-key endpoint returns a non-empty string.

## Verification

1. `make build`, `make lint`, `make test` — green including new tests on both DB backends.
2. `make migrate` applies `034` cleanly on both sqlite and postgres; `down` reverts.
3. In dev: `SP_WEBPUSH_VAPID_PUBLIC_KEY` / `SP_WEBPUSH_VAPID_PRIVATE_KEY` unset →
   startup logs "Generated VAPID keys, persisted to app_settings"; `GET
   /api/v1/orgs/default/webpush/vapid-public-key` returns a `publicKey` string.
4. Browser: open `/dash0/orgs/default/` → DevTools → Application → Service Workers shows
   `/dash0/sw.js` registered and active.
5. `go test ./server/internal/webpush/...` passes.

## Priority

P1.3 (foundation for Web Push notification delivery)

## Implementation plan

1. **Migration + DB layer**: `034_app_settings` migrations (sqlite + postgres) +
   `app_setting.go` model + `app_settings.go` DB methods (both backends) + `db/service.go`
   interface entries + unit tests. Commit: `feat(db): add app_settings kv table (migration 034)`.

2. **`server/internal/webpush/` package**: add `go.mod` dependency on
   `github.com/SherClockHolmes/webpush-go`; implement `sender.go` + `vapid.go` +
   their tests. Commit: `feat(webpush): add webpush send helper and VAPID lifecycle`.

3. **Config + app init**: add `WebPush` config block to `config.go`, wire
   `SP_WEBPUSH_*` env vars via the manual reader, call `GetOrCreateVAPIDKeys` in app
   startup. Commit: `feat(webpush): wire VAPID config and auto-generate keys at startup`.

4. **API handler + route**: `handlers/webpush/handler.go` + register
   `GET /api/v1/orgs/:org/webpush/vapid-public-key` in `server.go`.
   Commit: `feat(api): expose VAPID public key endpoint`.

5. **Service worker + registration**: `web/dash0/public/sw.js` (push +
   notificationclick) + `main.tsx` registration. Commit:
   `feat(dash0): add service worker for web push notifications`.

6. **Frontend hook + component**: `useVapidPublicKey` in `hooks.ts` +
   `useWebPushSubscription.ts` + `WebPushEnableButton.tsx`.
   Commit: `feat(dash0): add web push subscription hook and enable button`.

7. **E2E** `web/dash0/e2e/webpush.spec.ts`.
   Commit: `test(dash0): add web push foundation e2e spec`.

8. `make build`, `make lint`, `make test`, `make test-dash` — fix any issues.

9. Archive: move spec to `specs/done/2026/05/2026-05-25-16-web-push-foundation.md`.
