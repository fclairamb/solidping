# Microsoft OAuth Tenant ID — expose in Server Settings → Authentication

## Context

Microsoft (Entra ID / Azure AD) OAuth embeds the **tenant** in its endpoints:

```
https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize
https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token
```

The backend **already supports this end-to-end**:

- `config.MicrosoftOAuthConfig` has `TenantID string koanf:"tenant_id"` — *"Defaults
  to common for multi-tenant"* (`server/internal/config/microsoft_oauth.go:8`).
- The authorize URL builder reads it and falls back to `common` when empty
  (`server/internal/handlers/auth/microsoft.go:100-112`).
- The token URL builder does the same (`server/internal/handlers/auth/microsoft_service.go:200-207`).
- It's covered by tests — *"with configured tenant"* / *"defaults to common tenant"*
  (`server/internal/handlers/auth/microsoft_service_test.go:348-360`).

**The gap is purely configuration plumbing + UI.** Unlike `client_id` /
`client_secret`, the tenant is **not registered in `systemconfig`**, so it can only
be set via YAML (`config.yml` / `config.local.yml`) — there is **no
`SP_MICROSOFT_TENANT_ID` env var and no `auth.microsoft.tenant_id` system
parameter**, and therefore **no field on Server Settings → Authentication**
(`/orgs/$org/server/auth`). An operator using the UI can set the Microsoft client
ID and secret but is silently stuck on the `common` tenant — wrong for any
single-tenant Entra app registration (which rejects `common` / cross-tenant
sign-ins).

The Server Settings → Authentication page is **declarative**: a `providers[]`
catalog of `{name, enabledKey, fields[]}` drives rendering, dirty-tracking, secret
masking, and the save handler generically (`server/../web/dash0/src/routes/orgs/$org/server.auth.tsx:40-93`,
render map `:280-354`, save `:156-202`). Adding a field is a one-line catalog entry
plus a label — the existing machinery does the rest.

This spec wires `auth.microsoft.tenant_id` into `systemconfig` (env + DB system
parameter, **non-secret**, mirroring `auth.microsoft.client_id`) and adds the
**Tenant ID** field to the Microsoft card in the Authentication settings page, with
help text explaining the accepted values and the `common` default.

## The key questions

### Q1 — Is the backend OAuth flow changing? **No.**

The authorize/token URL builders already consume `cfg.Microsoft.TenantID` with the
`common` fallback. We only need to make that field *settable from env / DB / UI*,
the same way `client_id` and `client_secret` already are. No change to
`microsoft.go` / `microsoft_service.go`.

### Q2 — Is the tenant a secret? **No — `Secret: false`.**

The tenant ID is part of the public authorize URL the browser is redirected to;
it is an identifier, not a credential. Register it exactly like
`auth.microsoft.client_id` (`Secret: false`), not like `client_secret`. This keeps
the UI field a plain text input (no mask/edit/visibility-toggle dance) and keeps the
value readable back from `GET /system/parameters`.

### Q3 — What does an empty value mean? **`common` (unchanged).**

Empty stays the multi-tenant default: the URL builders already substitute `common`
when `TenantID == ""`. Clearing the field (the UI writes `value: ""`) reverts to
multi-tenant. No sentinel, no special-casing — just don't break the existing
fallback. Normalize by trimming surrounding whitespace on write so a stray space in
a pasted GUID can't silently corrupt the URL.

### Q4 — When does a change take effect? **After a server restart.**

`cfg.Microsoft.TenantID` is populated by the `systemconfig` overlay at startup
(`Service.Initialize`), and the URL builders read `cfg` per-request — but `cfg` is
not re-overlaid live. So a tenant change behaves like every other credential here.
The page's existing help text already states this verbatim — *"Credential changes
take effect after a server restart. The Enabled toggle takes effect immediately…"*
(`en/server.json` `auth.helpText`) — so **no new timing copy is needed.**

### Q5 — Should we validate the tenant format? **Light normalization, no hard rejection.**

A valid Microsoft tenant can be any of: `common`, `organizations`, `consumers`, a
**tenant GUID**, or a **verified domain** (`contoso.onmicrosoft.com`, `contoso.com`).
That space is too varied to safely hard-reject, and a wrong value fails loudly at
Microsoft's end with a clear error. So: **trim whitespace, accept empty, otherwise
accept any non-empty string.** Document the accepted forms in the field's help text
rather than enforcing a regex. (This matches the project's current `SetParameter`,
which does no per-key validation — and avoids the over-validation trap of blocking
legitimate domain tenants.)

### Q6 — How does the operator know what to type? **Per-field help text.**

`tenant_id` semantics are non-obvious, so "handling it properly" means more than a
bare input. Extend the declarative field config with an **optional** `helpKey` and
render a muted hint under the input when present (used only by tenant_id for now).
Hint: *"Your Entra directory (tenant) ID, or a verified domain. Leave blank for
`common` (multi-tenant). Use `organizations` or `consumers` to scope account types."*
Also set the input placeholder to `common` so the default is visible at a glance.

## Goal

A super-admin opens **Server Settings → Authentication**, and the **Microsoft** card
shows a **Tenant ID** field (alongside Client ID / Client Secret) with help text and
a `common` placeholder. They paste their Entra directory (tenant) ID (or a domain, or
leave it blank), Save, and after a server restart the Microsoft authorize/token URLs
target that tenant. The value is also settable via `SP_MICROSOFT_TENANT_ID` and
persists as the `auth.microsoft.tenant_id` system parameter — consistent with
`client_id`. Empty continues to mean `common`.

## Non-goals

- **Per-organization tenant.** One process-wide tenant, like the rest of
  `cfg.Microsoft.*`. Multi-tenant-by-org is a separate, larger effort.
- **Live (no-restart) apply.** Matches the restart-to-apply convention of every
  other credential field on this page.
- **Strict tenant-format validation / GUID regex.** Deliberately lenient (Q5).
- **New OAuth flow behavior, scopes, or `prompt`/`domain_hint` params.** Only the
  tenant segment of the existing URLs is affected, and that code is unchanged.
- **Reworking the secret-field UI.** Tenant ID is non-secret (Q2); it uses the plain
  text-input branch already in the renderer.

## Design

### Backend (`server/`)

The only code path that needs `tenant_id` is the `systemconfig` registry — the config
struct field and the URL builders already exist.

**1. `systemconfig/systemconfig.go`** — add the key + definition, mirroring
`KeyMicrosoftClientID` exactly:

```go
// alongside KeyMicrosoftClientID / KeyMicrosoftClientSecret (~:49-50)
KeyMicrosoftTenantID ParameterKey = "auth.microsoft.tenant_id"
```

```go
// in getKnownParameters(), next to the other Microsoft entries (~:338-357)
{
    Key:    KeyMicrosoftTenantID,
    EnvVar: "SP_MICROSOFT_TENANT_ID",
    Secret: false,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok {
            cfg.Microsoft.TenantID = strings.TrimSpace(v)
        }
    },
},
```

- **Env precedence** is handled by the same `EnvVar` mechanism every other `auth.*`
  key uses; `client_id` (also a multi-word koanf key) proves the path, so the koanf
  multi-word-env quirk does **not** apply here — `systemconfig` reads `EnvVar`
  directly and applies via `ApplyFunc`, bypassing koanf's env provider.
- The `strings.TrimSpace` is the Q3/Q5 normalization. (If `strings` isn't already
  imported in this file, add it.)

**No changes** to `config/microsoft_oauth.go`, `handlers/auth/microsoft.go`, or
`handlers/auth/microsoft_service.go` — `TenantID` and the `common` fallback are
already there.

### Frontend (`web/dash0/`)

All three edits are in `server.auth.tsx` + the four locale files; the generic
renderer/save/dirty-tracking needs **no** changes.

**1. `src/routes/orgs/$org/server.auth.tsx`:**

- Extend the field-kind union (`:28`) and the field config type with an optional help
  key:

  ```ts
  type FieldKind =
    | "clientId" | "clientSecret" | "appId"
    | "signingSecret" | "botToken" | "redirectUrl"
    | "tenantId";
  ```

  Add `helpKey?: string` to the field interface (the `ProviderConfig.fields[]` element
  type near `:30-37`).

- Add the field to the Microsoft provider (`:66-72`), **after** the client secret:

  ```ts
  {
    name: "Microsoft",
    enabledKey: "auth.microsoft.enabled",
    fields: [
      { key: "auth.microsoft.client_id", labelKey: "clientId", secret: false },
      { key: "auth.microsoft.client_secret", labelKey: "clientSecret", secret: true },
      { key: "auth.microsoft.tenant_id", labelKey: "tenantId", secret: false,
        helpKey: "tenantId" },
    ],
  }
  ```

- In the non-secret input branch of the field render map (`:280-354`), set a sensible
  placeholder and render the optional help line below the input:

  ```tsx
  // placeholder: prefer "common" for tenant, else the label (existing behavior)
  placeholder={field.labelKey === "tenantId" ? "common" : label}
  // …after the <Input>/wrapper, when field.helpKey is set:
  {field.helpKey && (
    <p className="text-xs text-muted-foreground">
      {t(`server:auth.fieldHelp.${field.helpKey}`)}
    </p>
  )}
  ```

  Keep this minimal and reuse design-reference primitives (`Label`, `Input`,
  muted-`<p>`); no raw Radix.

**2. i18n — `src/locales/{en,fr,de,es}/server.json`:**

- Add `tenantId` to the existing `auth.fields` object (label):

  ```json
  "fields": {
    "clientId": "Client ID",
    "clientSecret": "Client Secret",
    "appId": "App ID",
    "signingSecret": "Signing Secret",
    "botToken": "Bot Token",
    "redirectUrl": "Redirect URL",
    "tenantId": "Tenant ID"
  }
  ```

- Add a new `auth.fieldHelp` object with the tenant hint (en shown; translate for
  fr/de/es):

  ```json
  "fieldHelp": {
    "tenantId": "Your Entra directory (tenant) ID, or a verified domain. Leave blank for \"common\" (multi-tenant). Use \"organizations\" or \"consumers\" to restrict account types."
  }
  ```

### Docs

- `web/docs/docs/configuration/authentication.md` (`:124-125`) — add
  `SP_MICROSOFT_TENANT_ID=common` to the Microsoft block, with a one-line note on the
  `common` default and accepted values (GUID / domain / `organizations` / `consumers`).
- `web/docs/docs/configuration/index.md` (`:75`) and `README.md` (`:186`) — append
  `SP_MICROSOFT_TENANT_ID` to the Microsoft env-var entry.

## Files to create / modify

**Modified (backend):**
- `server/internal/systemconfig/systemconfig.go` — `KeyMicrosoftTenantID` const +
  `ParameterDefinition` (`EnvVar: SP_MICROSOFT_TENANT_ID`, `Secret: false`, trimming
  `ApplyFunc`); ensure `strings` import.
- `server/internal/systemconfig/systemconfig_test.go` (or equivalent) — overlay /
  precedence test for the new key.

**Modified (frontend):**
- `web/dash0/src/routes/orgs/$org/server.auth.tsx` — `FieldKind` + optional `helpKey`,
  Microsoft `tenant_id` field, placeholder + help-line render.
- `web/dash0/src/locales/en/server.json` — `auth.fields.tenantId` + `auth.fieldHelp.tenantId`.
- `web/dash0/src/locales/fr/server.json`, `de/server.json`, `es/server.json` — same keys, translated.

**Modified (docs):**
- `web/docs/docs/configuration/authentication.md`, `web/docs/docs/configuration/index.md`, `README.md`.

**New:** none.

## Verification

Backend tests use `testify/require` + `t.Parallel()` (`server/CLAUDE.md`).

- **systemconfig unit:** a DB value for `auth.microsoft.tenant_id` is applied to
  `cfg.Microsoft.TenantID`; `SP_MICROSOFT_TENANT_ID` overrides the DB value
  (env > DB > default), mirroring the `client_id` precedence test; a value with
  surrounding whitespace is trimmed; absent key leaves `TenantID == ""`.
- **URL flow (already covered, re-assert):** with `cfg.Microsoft.TenantID` set to a
  GUID, `getTokenURL()` / `buildMicrosoftAuthURL()` embed that tenant; empty → `common`
  (`microsoft_service_test.go:348-360` already proves this — no new code, just confirm
  the overlaid value reaches the builder).
- **Integration (`make dev-test`):** `PUT /api/v1/system/parameters/auth.microsoft.tenant_id`
  with a GUID → restart → hitting `/api/v1/auth/microsoft/login` issues a redirect to
  `https://login.microsoftonline.com/<guid>/oauth2/v2.0/authorize?...`; clearing it
  (empty) → redirect uses `.../common/...`.
- **E2E (Playwright, `web/dash0/e2e/`):** super-admin opens Authentication, the
  Microsoft card shows a **Tenant ID** text field with the help line and `common`
  placeholder; entering a value marks the card dirty and Save persists it
  (reload shows the value, since it's non-secret); a non-super-admin is redirected
  off `/orgs/$org/server/auth` (existing gate, `server.tsx`).
- `make build && make lint && make test`; dash0 `lint` + Playwright; `make fmt`.

## Risk log

| Risk | Mitigation |
|---|---|
| Operator pastes an invalid tenant (typo'd GUID) and sign-in breaks | Lenient by design (Q5); Microsoft returns a clear tenant error; help text documents valid forms; whitespace trimmed so copy-paste artifacts don't silently corrupt the URL |
| User expects the change to apply instantly | Restart-to-apply, identical to other credentials; existing `auth.helpText` already states this — no behavior surprise |
| Stray double-handling of the env var via koanf's multi-word quirk | `systemconfig` reads `EnvVar` directly (not koanf's env provider); `auth.microsoft.client_id` already proves this exact path works for a multi-word key |
| Empty value regressing to a broken URL | Empty → `common` is the *existing, tested* fallback; we only avoid breaking it |
| Tenant ID mistakenly treated as a secret (masked, unreadable) | `Secret: false` (Q2) — plain text field, value round-trips through `GET /system/parameters` |
| Help-text plumbing touches the shared renderer | `helpKey` is optional and additive; only the tenant field sets it; all other providers render unchanged |
| Locale drift (en updated, fr/de/es not) | Add `fields.tenantId` + `fieldHelp.tenantId` to all four files in the same change |

**Status**: Todo | **Created**: 2026-06-30 | **Depends on**: none (backend tenant support already shipped — `specs/done/2026/02/2026-02-10-third-party-auth.md`)

## Implementation Plan

1. **Backend:** add `KeyMicrosoftTenantID` + its `ParameterDefinition`
   (`SP_MICROSOFT_TENANT_ID`, `Secret:false`, `strings.TrimSpace` apply) in
   `systemconfig.go`; add the overlay/precedence/trim unit test. `make test`.
2. **Frontend:** extend `FieldKind` + add optional `helpKey`; add the Microsoft
   `tenant_id` field; render placeholder `common` + optional help line in
   `server.auth.tsx`. Add `fields.tenantId` + `fieldHelp.tenantId` to en/fr/de/es
   `server.json`. dash0 `lint`.
3. **Verify:** integration check that a DB-set tenant flows into the authorize/token
   redirect after restart, and empty → `common`; Playwright for the field + save +
   persistence + super-admin gate.
4. **Docs:** `authentication.md`, `configuration/index.md`, `README.md` —
   `SP_MICROSOFT_TENANT_ID` + the `common` default note.
5. `make build && make lint && make test`; dash0 lint + Playwright; `make fmt`.
