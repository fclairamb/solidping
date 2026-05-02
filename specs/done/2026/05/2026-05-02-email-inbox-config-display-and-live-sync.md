# Email-inbox admin page — display saved config, make push the primary sync path

## Context

The admin page at `/dash0/orgs/<org>/server/email-inbox`
(`web/dash0/src/routes/orgs/$org/server.email-inbox.tsx`) has two visible defects
when reopened after the inbox has been configured:

1. **All form fields are blank** — Session URL, address domain, username,
   mailbox names, retention values, etc. The "État" (status) card next to it
   still correctly shows `Connecté`, the right `addressDomain`, and a recent
   `lastSyncedAt`, so the JMAP supervisor *is* connected with the saved
   credentials. Only the form lost the values. That makes the page look broken
   ("did my settings get wiped?") and forces the operator to retype everything
   any time they want to tweak one field.

2. **Sync cadence is treated as polling-only.** The supervisor already supports
   JMAP's EventSource (RFC 8620 §7.3) push channel and uses it whenever the
   server publishes `eventSourceUrl`, but the implementation is *push-or-poll,
   never both*: `runEventSource` reacts to push events only and has no
   time-based safety net, while `runPolling` ticks every `pollIntervalSeconds`
   (default 60). If the SSE connection dies silently — proxy timeouts, NAT
   rebinding, server bug — the inbox can stop syncing until the next reconnect.
   The default of 60 s also makes the field read like "how often we hit the
   server", which is misleading when push is active.

The user wants both fixed: the form should reflect the stored configuration,
and live-sync should be the primary mechanism with a *fallback* poll running
much less frequently (15 minutes by default) just to catch silent push
failures.

## Root causes

### 1. The list endpoint returns `"******"` for secret parameters

`Service.toResponse` in
`server/internal/handlers/system/service.go:132` does:

```go
isSecret := param.Secret != nil && *param.Secret
value := s.extractValue(param.Value)

if isSecret {
    value = "******"
}
```

`email_inbox` is stored with `secret: true` (correctly — the password is in
there), so `GET /api/v1/system/parameters` returns:

```json
{ "key": "email_inbox", "value": "******", "secret": true, "updatedAt": "…" }
```

The dashboard form does:

```ts
const cfg = (param.value ?? {}) as Partial<EmailInboxConfig>;
setSessionUrl(cfg.sessionUrl ?? "");
…
```

Reading `.sessionUrl` off the string `"******"` returns `undefined`, every
field falls back to its empty/default initializer, and the form looks
unconfigured. The "Modifier" / edit-password affordance keeps working only
because it keys on `param.secret === true`, which survives the masking.

The current behavior is correct for the *generic* list endpoint — a future
parameter might be wholly secret (e.g. a webhook signing key) where exposing
any structure would leak. But for `email_inbox` we know the shape and only one
field (`password`) is sensitive; everything else is operational config the
admin needs to see.

### 2. EventSource and polling are mutually exclusive in the supervisor

`server/internal/jmap/manager.go:181` (`Run`):

```go
if client.EventSourceURL() != "" {
    m.runEventSource(ctx, client, mboxes, cfg, logger)
} else {
    m.runPolling(ctx, client, mboxes, cfg, logger)
}
```

`runEventSource` (`manager.go:264`) only `select`s on the SSE-driven
`syncTrigger` and an hourly `cleanupTicker`. There is **no time-based sync
ticker**, so a silently-dropped SSE stream leaves the inbox unread until
`ListenEventSourceWithReconnect`'s exponential backoff manages a fresh
connection and re-issues `syncEmails`. The `runPolling` path *does* tick on
`pollIntervalSeconds`, but it's only entered when the server doesn't advertise
EventSource at all.

`DefaultPollIntervalSeconds = 60` (`jmap/types.go:41`) is also too aggressive
for a *fallback* role — 60 s of stale data is fine-as-fallback, but if push is
working it's a wasteful "just in case" hammer on the JMAP server.

## Scope

Two concrete fixes:

1. **Backend: expose the stored email_inbox config (without password) so the
   admin form can prefill itself.** Add a dedicated handler — keep the generic
   list endpoint's blanket masking unchanged.

2. **Backend: run the fallback poll alongside EventSource, not instead of it.
   Default to 900 s (15 min).** Tweak the config field's documented semantics
   and the status surface so push-vs-poll mode is visible.

3. **Frontend: prefill the form from the new endpoint, update help text and
   the status card to communicate the new model.**

Out of scope:
- Per-key redaction in the generic list endpoint. We considered structural
  redaction (mask `value.password`, return the rest) but it requires the list
  endpoint to know about each parameter's shape. Cleaner to keep the generic
  endpoint dumb and add typed projections per parameter, as we already do for
  `EmailInboxPublic`.
- Surfacing the JMAP `state` strings or detailed SSE reconnect telemetry.
  Visible "live" vs "polling" mode is enough for the admin UX.
- Renaming `pollIntervalSeconds` in the persisted JSON. We change the *meaning*
  (fallback only, applied to both code paths) but keep the field name to avoid
  a migration. The label in the form changes; the stored key does not.

## Backend changes

### B1. New endpoint: `GET /api/v1/system/email-inbox/config`

Returns the stored `email_inbox` parameter with `password` blanked. Lives next
to the existing `email-inbox/{status,test,sync}` action endpoints — same
super-admin guard, same shape conventions.

`server/internal/handlers/system/handler.go` — add:

```go
// EmailInboxConfigResponse mirrors jmap.Config for the admin form, but
// never includes the password. A non-empty `passwordSet` flag tells the
// frontend whether to render the "Modifier" affordance.
type EmailInboxConfigResponse struct {
    Enabled                bool   `json:"enabled"`
    SessionURL             string `json:"sessionUrl"`
    Username               string `json:"username"`
    AddressDomain          string `json:"addressDomain"`
    MailboxName            string `json:"mailboxName"`
    ProcessedMailboxName   string `json:"processedMailboxName"`
    PollIntervalSeconds    int    `json:"pollIntervalSeconds"`
    ProcessedRetentionDays int    `json:"processedRetentionDays"`
    FailedRetentionDays    int    `json:"failedRetentionDays"`
    RewriteBaseURL         string `json:"rewriteBaseUrl"`
    PasswordSet            bool   `json:"passwordSet"`
}

func (h *Handler) EmailInboxConfig(writer http.ResponseWriter, req bunrouter.Request) error {
    cfg, err := h.svc.EmailInboxConfig(req.Context())
    if err != nil {
        return h.handleEmailInboxError(writer, err)
    }
    return h.WriteJSON(writer, http.StatusOK, cfg)
}
```

`server/internal/handlers/system/service.go` — add:

```go
func (s *Service) EmailInboxConfig(ctx context.Context) (*EmailInboxConfigResponse, error) {
    param, err := s.db.GetSystemParameter(ctx, "email_inbox")
    if errors.Is(err, sql.ErrNoRows) {
        return &EmailInboxConfigResponse{}, nil // empty when never configured
    }
    if err != nil {
        return nil, err
    }

    var cfg jmap.Config
    raw, err := json.Marshal(s.extractValue(param.Value))
    if err != nil {
        return nil, err
    }
    if err := json.Unmarshal(raw, &cfg); err != nil {
        return nil, err
    }

    return &EmailInboxConfigResponse{
        Enabled:                cfg.Enabled,
        SessionURL:             cfg.SessionURL,
        Username:               cfg.Username,
        AddressDomain:          cfg.AddressDomain,
        MailboxName:            cfg.MailboxName,
        ProcessedMailboxName:   cfg.ProcessedMailboxName,
        PollIntervalSeconds:    cfg.PollIntervalSeconds,
        ProcessedRetentionDays: cfg.ProcessedRetentionDays,
        FailedRetentionDays:    cfg.FailedRetentionDays,
        RewriteBaseURL:         cfg.RewriteBaseURL,
        PasswordSet:            cfg.Password != "",
    }, nil
}
```

`server/internal/app/server.go:494` — register on the same super-admin group
that already exposes `/email-inbox/{status,test,sync}`:

```go
systemActions.GET("/email-inbox/config", systemHandler.EmailInboxConfig)
```

Tests: extend `service_test.go` with a case that stores a full `email_inbox`
secret parameter and asserts the response contains every field except the
password, plus `passwordSet: true`. Add a "never configured" case asserting
`passwordSet: false` and zero values. Existing tests for the generic
`/system/parameters` list and get endpoints stay untouched and still expect
`"******"` for secrets.

### B2. Polling runs as a fallback even when push is active

`server/internal/jmap/types.go`:
- Bump the default: `DefaultPollIntervalSeconds = 900`.
- Update the doc comment on `Config.PollIntervalSeconds`: "Fallback sync
  cadence. Always runs in addition to EventSource, in case push silently
  drops. Set high enough to avoid hammering the JMAP server when push is
  healthy."

`server/internal/jmap/manager.go` — refactor so the loop body is shared:

```go
// syncLoop runs the steady-state ticker + cleanup + push-trigger select.
// Used by both runEventSource and runPolling.
func (m *Manager) syncLoop(
    ctx context.Context, client *Client, mboxes *Mailboxes, cfg *Config, logger *slog.Logger,
) {
    syncTicker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
    defer syncTicker.Stop()
    cleanupTicker := time.NewTicker(time.Hour)
    defer cleanupTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-syncTicker.C:
            if err := m.syncEmails(ctx, client, mboxes, cfg); err != nil {
                m.recordError(err)
                logger.WarnContext(ctx, "JMAP sync error", "error", err)
            }
        case <-m.syncTrigger:
            if err := m.syncEmails(ctx, client, mboxes, cfg); err != nil {
                m.recordError(err)
                logger.WarnContext(ctx, "JMAP sync error", "error", err)
            }
        case <-cleanupTicker.C:
            if err := m.cleanupOldEmails(ctx, client, mboxes, cfg); err != nil {
                logger.WarnContext(ctx, "JMAP cleanup error", "error", err)
            }
        }
    }
}
```

`runEventSource` keeps the SSE goroutine that writes to `m.syncTrigger` on
state-change, then calls `m.syncLoop(ctx, client, mboxes, cfg, logger)`. The
fallback ticker now runs alongside push.

`runPolling` becomes a thin wrapper: initial sync, then `m.syncLoop(...)`.

The two now differ only in whether the SSE goroutine is started.

### B3. Surface push-vs-poll mode in the status

`server/internal/jmap/manager.go` — add a `Mode` field to `Status`:

```go
type Status struct {
    Enabled       bool       `json:"enabled"`
    Connected     bool       `json:"connected"`
    Mode          string     `json:"mode,omitempty"` // "push" | "poll" | ""
    LastSyncedAt  *time.Time `json:"lastSyncedAt,omitempty"`
    LastError     string     `json:"lastError,omitempty"`
    AddressDomain string     `json:"addressDomain,omitempty"`
    AccountID     string     `json:"accountId,omitempty"`
}
```

Track it on the manager (set to `"push"` from `runEventSource`, `"poll"`
from `runPolling`, cleared on disconnect — write under the existing
`statusMu`). Mirror the field through `EmailInboxStatusResponse` in
`handlers/system/handler.go:124`.

Tests:
- `manager_test.go`: a unit test for the new shared `syncLoop` that injects a
  short `PollIntervalSeconds`, a fake client, and asserts `syncEmails` is
  called both on a push trigger and on the ticker tick. The existing
  EventSource and polling tests continue to pass unchanged because the
  goroutine wiring stays the same.
- `manager_test.go`: assert `GetStatus().Mode == "push"` after the
  EventSource path starts, and `"poll"` when the manager falls back. Both can
  reuse the existing test scaffolding around `Run` with a context cancelled
  shortly after start.

## Frontend changes

### F1. Prefill form from the new endpoint

`web/dash0/src/api/email-inbox.ts` — add a query for the saved config:

```ts
export interface EmailInboxConfigSnapshot {
  enabled: boolean;
  sessionUrl: string;
  username: string;
  addressDomain: string;
  mailboxName: string;
  processedMailboxName: string;
  pollIntervalSeconds: number;
  processedRetentionDays: number;
  failedRetentionDays: number;
  rewriteBaseUrl: string;
  passwordSet: boolean;
}

export function useEmailInboxConfig() {
  return useQuery({
    queryKey: ["email-inbox", "config"],
    queryFn: () =>
      apiFetch<EmailInboxConfigSnapshot>("/api/v1/system/email-inbox/config"),
    staleTime: 30_000,
  });
}
```

Also add `mode?: "push" | "poll"` to the `EmailInboxStatus` type.

`useSaveEmailInboxConfig` already invalidates `["email-inbox"]`, which now
also covers the `["email-inbox", "config"]` key — no extra invalidation work.

`web/dash0/src/routes/orgs/$org/server.email-inbox.tsx` — replace the
`useSystemParameters`-driven `useEffect` with `useEmailInboxConfig`:

```ts
const { data: config, isLoading } = useEmailInboxConfig();

useEffect(() => {
  if (!config) return;
  setEnabled(config.enabled);
  setSessionUrl(config.sessionUrl);
  setUsername(config.username);
  setAddressDomain(config.addressDomain);
  setMailboxName(config.mailboxName || "Inbox");
  setProcessedMailboxName(config.processedMailboxName || "Processed");
  setPollIntervalSeconds(String(config.pollIntervalSeconds || 900));
  setProcessedRetentionDays(String(config.processedRetentionDays || 30));
  setFailedRetentionDays(String(config.failedRetentionDays || 7));
  setRewriteBaseUrl(config.rewriteBaseUrl);
}, [config]);

const isSecretSet = config?.passwordSet ?? false;
```

Drop `useSystemParameters` from this file's imports — it's no longer needed
here. The "Modifier" affordance keys on `isSecretSet` exactly as before.

### F2. Update the poll-interval field's framing

The field is no longer "how often we sync" — it's "how often we poll as a
fallback when push fails or isn't supported". Update labels and help text in
`web/dash0/public/locales/{en,fr}/server.json`:

- `emailInbox.pollInterval` → "Fallback poll interval (seconds)" / "Intervalle de relève (fallback, secondes)"
- New `emailInbox.pollIntervalHelp` — render as a `<p className="text-xs
  text-muted-foreground">` below the input:
  - en: "Used as a safety net when JMAP push fails or isn't supported. Default 900 s (15 min)."
  - fr: "Filet de sécurité quand la diffusion JMAP n'est pas disponible. Par défaut 900 s (15 min)."

Bump the default in the form initial state from `"60"` to `"900"`. The
`useEffect` above already reads the server-side default for already-configured
inboxes; this initial state only matters for first-time configuration.

`min` on the `<Input type="number">` for poll interval: bump from `5` to `60`
to discourage configuring something so aggressive it defeats the purpose. Not
a hard constraint — admins with weird setups can still set 60.

### F3. Show "Live" vs "Polling" mode in the status card

In the "État" card next to the existing `Connecté` badge:

```tsx
{status?.connected && status.mode && (
  <span className="inline-flex items-center gap-1 rounded-full bg-blue-500/10 px-2 py-0.5 text-blue-700 dark:text-blue-400">
    {status.mode === "push"
      ? t("server:emailInbox.status.modePush")
      : t("server:emailInbox.status.modePoll")}
  </span>
)}
```

Translation keys (en / fr):
- `emailInbox.status.modePush` → "Live" / "Direct"
- `emailInbox.status.modePoll` → "Polling" / "Relève"

### F4. (Optional cleanup) `useSystemParameters` callers

This page was the only consumer that depended on the parameter being readable
in the clear. Spot-check the other callers:

```bash
rg -n "useSystemParameters" web/dash0/src
```

Don't touch them in this spec — they're either super-admin tooling that's
fine with `"******"` or they read non-secret parameters where masking doesn't
apply. The existing endpoint stays as-is.

## Manual test plan

After `make dev-test`:

1. **Form prefill**
   - Configure email_inbox via the form (Session URL, username, password,
     address domain, etc.), save.
   - Hard-reload `/dash0/orgs/default/server/email-inbox`. Every field must be
     populated except the password input, which must show `******` with the
     "Modifier" button.
   - Check `GET /api/v1/system/email-inbox/config` returns the values; check
     `GET /api/v1/system/parameters` *still* returns `"******"` for
     `email_inbox` (we did not regress the generic endpoint).

2. **Push + fallback together**
   - With a Fastmail or Stalwart account whose session advertises
     `eventSourceUrl`, watch `lastSyncedAt` after sending a fresh test email:
     it should update within a couple of seconds (push). The "État" card must
     show the "Live" / "Direct" badge.
   - Cut the SSE stream (proxy drop or stop Stalwart's HTTP/2 push briefly):
     the next fallback tick must still call `Email/changes` within
     `pollIntervalSeconds`. Reduce `pollIntervalSeconds` to 60 in the form to
     verify the fallback fires without waiting 15 min.
   - Configure a JMAP server *without* `eventSourceUrl` (or temporarily strip
     it via `rewriteBaseUrl` + a mocked session). Status card must show
     "Polling" / "Relève".

3. **Defaults**
   - Wipe `email_inbox` and reconfigure. The poll-interval field must
     initialize to `900`, not `60`.

4. **Tests pass**
   - `make test` — Go side covers the new endpoint and the shared `syncLoop`.
   - `make lint` — golangci-lint clean.
   - Dash0 build (`make build-dash0`) — type-checks pass.

---

## Implementation Plan

1. Add `GET /api/v1/system/email-inbox/config` that returns the saved `email_inbox` parameter with the password elided, plus a `passwordSet: bool`.
2. Refactor `jmap/manager.go` so a shared `syncLoop` runs the periodic ticker for both `runEventSource` (in addition to the SSE-driven trigger) and `runPolling`; bump `DefaultPollIntervalSeconds` from 60 to 900.
3. Track and expose a `Mode` ("push" | "poll" | "") field on `Status` and surface it through `EmailInboxStatusResponse`.
4. Frontend `useEmailInboxConfig` hook + replace `useSystemParameters`-driven effect on the email-inbox admin page so all non-secret fields are prefilled; default form state's pollInterval to 900.
5. Locales (en/fr): rephrase poll-interval label as fallback; add `emailInbox.status.modePush`/`modePoll`; add status badge rendering live vs polling.
6. Run `make build-backend build-dash0 lint test` and fix anything that breaks.
