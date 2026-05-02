# Auth provider enable/disable — frontend admin UI

## Context

The server admin auth page at `web/dash0/src/routes/orgs/$org/server.auth.tsx` lets an admin enter OAuth credentials (Client ID, Client Secret, plus Slack-specific App ID / Signing Secret) for six providers: Google, GitHub, GitLab, Microsoft, Slack — and Discord is currently missing entirely. Whether a provider is shown on the public login page is implicitly tied to whether credentials are filled in (`server/internal/handlers/auth/providers_available.go`).

A sibling spec (`2026-05-02-auth-provider-enable-toggle-backend.md`) adds an explicit `auth.<provider>.enabled` system parameter (default `true`) and gates `/api/v1/auth/providers` and OAuth route registration on that flag. **This spec depends on that one shipping first** — the backend must accept and honor the new param keys before the UI can persist them meaningfully.

This spec adds the user-facing toggle: a Switch on each provider card, persisted through the existing `/api/v1/system/parameters` endpoint, with appropriate UX for the configured-vs-enabled distinction.

## Scope

Just `web/dash0/src/routes/orgs/$org/server.auth.tsx` and locale files.

This spec also **adds Discord** to the admin UI. The backend has Discord OAuth (see `discord_oauth.go`) but the admin form omits it; the sibling backend spec adds the Discord credentials to `getKnownParameters()`, so wiring up the admin form is the natural counterpart.

Out of scope:

- The login page (`web/dash0/src/routes/orgs/$org/login.tsx`) needs **no changes**: it already iterates over whatever `useProviders()` returns, so a backend-filtered list naturally hides disabled providers. Discord just needs an icon in the existing `PROVIDER_ICONS` map — verify it's already there; if not, add it (lucide-react has no Discord icon, use a simple SVG or skip the icon, leaving the button text-only).
- The 4 non-dash0 locales already updated by `4517f3ea feat(i18n): translate admin pages and remaining dash0 surfaces` — we add the new keys to all 4 (`en`, `fr`, `de`, `es`).

## UX design

Each provider card gets:

1. **Header row**: `<CardTitle>` + an inline Switch on the right with label "Enabled".
2. **Body**: existing credential fields, unchanged. Discord adds a fifth card with fields: `client_id`, `client_secret` (secret), `bot_token` (secret), `redirect_url`.
3. **State logic**:
   - When credentials are not yet stored (no `client_id` value), the toggle is **disabled** and visually muted with a tooltip "Configure credentials first" — enabling without credentials would just have the backend filter the provider out anyway, and surfacing the dependency upfront avoids confusion.
   - When credentials are stored and `enabled` param is unset → toggle defaults to **on** (matching backend default).
   - Toggling **does not persist immediately** — it sets local state and marks the card as "dirty". The existing Save button on each card persists everything (credentials + `enabled`) in one batch, so the toggle and the credential text fields share the same save flow.
4. **Unsaved-changes notification**:
   - When the toggle differs from the persisted value, render a small badge next to the Save button: *"Unsaved changes"* (muted yellow / warning color).
   - The Save button changes from secondary to primary variant while there are unsaved changes, to draw the eye.
   - On successful save, the badge disappears; the existing top-of-page success alert (`{t("server:saved")}`) stays as-is.
   - If the user toggles back to the original value, the dirty marker clears.

This avoids the surprise of "I clicked the switch and nothing happened" while also avoiding the surprise of "I clicked the switch and it persisted before I confirmed". The badge is the bridge between the two.

A single static help text at the top of the page documents the restart caveat for credential changes (the Save button persists credentials immediately, but the OAuth route only registers/unregisters at startup):

> *"Changes to provider credentials take effect after a server restart. The Enabled toggle takes effect immediately for the login page once saved."*

## File changes

### 1. `web/dash0/src/routes/orgs/$org/server.auth.tsx`

**Imports**: add `Switch` from `@/components/ui/switch` (the component already exists at `web/dash0/src/components/ui/switch.tsx`).

**`ProviderConfig` type** (line 29): add an `enabledKey: string` field — the system-parameter key for the toggle.

```ts
interface ProviderConfig {
  name: string;
  enabledKey: string;
  fields: { key: string; labelKey: FieldKind; secret: boolean }[];
}
```

**`FieldKind` union** (line 27): extend with the two new Discord-specific labels:

```ts
type FieldKind = "clientId" | "clientSecret" | "appId" | "signingSecret" | "botToken" | "redirectUrl";
```

**`providers` array** (line 38): add `enabledKey` to each entry, and append a Discord entry:

```ts
{ name: "Google",    enabledKey: "auth.google.enabled",    fields: [ … ] },
{ name: "GitHub",    enabledKey: "auth.github.enabled",    fields: [ … ] },
{ name: "GitLab",    enabledKey: "auth.gitlab.enabled",    fields: [ … ] },
{ name: "Microsoft", enabledKey: "auth.microsoft.enabled", fields: [ … ] },
{ name: "Slack",     enabledKey: "auth.slack.enabled",     fields: [ … ] },
{
  name: "Discord",
  enabledKey: "auth.discord.enabled",
  fields: [
    { key: "auth.discord.client_id",     labelKey: "clientId",     secret: false },
    { key: "auth.discord.client_secret", labelKey: "clientSecret", secret: true  },
    { key: "auth.discord.bot_token",     labelKey: "botToken",     secret: true  },
    { key: "auth.discord.redirect_url",  labelKey: "redirectUrl",  secret: false },
  ],
},
```

**State**: extend the params loading effect (line 89) to also pre-fill an `enabled` map:

```ts
const [enabled, setEnabled] = useState<Record<string, boolean>>({});

useEffect(() => {
  if (params) {
    const newValues: Record<string, string> = {};
    const newEnabled: Record<string, boolean> = {};
    for (const provider of providers) {
      for (const field of provider.fields) {
        const param = params.find((p: SystemParameter) => p.key === field.key);
        newValues[field.key] = (param?.value as string) ?? "";
      }
      const enabledParam = params.find((p: SystemParameter) => p.key === provider.enabledKey);
      // Default to true when the param is absent — matches backend default.
      newEnabled[provider.enabledKey] = enabledParam?.value === undefined ? true : Boolean(enabledParam.value);
    }
    setValues(newValues);
    setEnabled(newEnabled);
  }
}, [params]);
```

**Helper — credentials configured?**

```ts
const isConfigured = (provider: ProviderConfig) => {
  const clientIdField = provider.fields.find((f) => f.labelKey === "clientId");
  return clientIdField ? Boolean((values[clientIdField.key] || "").trim()) : false;
};
```

**Helper — persisted enabled value, used to detect dirty state**:

```ts
const persistedEnabled = (provider: ProviderConfig): boolean => {
  const param = params?.find((p: SystemParameter) => p.key === provider.enabledKey);
  return param?.value === undefined ? true : Boolean(param.value);
};

const isEnabledDirty = (provider: ProviderConfig): boolean =>
  (enabled[provider.enabledKey] ?? true) !== persistedEnabled(provider);

// Used by the Save button to also know if any credential field is dirty.
const isCredentialDirty = (provider: ProviderConfig): boolean =>
  provider.fields.some((field) => {
    const original = (params?.find((p: SystemParameter) => p.key === field.key)?.value as string) ?? "";
    // Editing a stored secret means we have an explicit edit pending.
    if (field.secret && editingSecrets.has(field.key)) return values[field.key] !== original;
    if (field.secret) return false; // not currently editing a stored secret
    return (values[field.key] ?? "") !== original;
  });

const isDirty = (provider: ProviderConfig): boolean =>
  isEnabledDirty(provider) || isCredentialDirty(provider);
```

**Handler — toggle (local state only)**:

```ts
const handleToggleEnabled = (provider: ProviderConfig, next: boolean) => {
  setEnabled((prev) => ({ ...prev, [provider.enabledKey]: next }));
};
```

**Handler — save (extend existing `handleSave` to also persist `enabled`)**:

The existing `handleSave` (line 105) saves credential fields with `Promise.all`. Add a parallel `setParam.mutateAsync` for the `enabled` key, but only when it changed (avoid unnecessary writes):

```ts
const handleSave = async (providerName: string) => {
  setError(null);
  setSaved(false);

  const provider = providers.find((p) => p.name === providerName);
  if (!provider) return;

  try {
    const writes = provider.fields
      .filter((field) => !field.secret || editingSecrets.has(field.key) || !isSecretStored(field.key))
      .map((field) =>
        setParam.mutateAsync({
          key: field.key,
          value: values[field.key] || "",
          secret: field.secret || undefined,
        })
      );

    if (isEnabledDirty(provider)) {
      writes.push(
        setParam.mutateAsync({
          key: provider.enabledKey,
          value: enabled[provider.enabledKey] ?? true,
        })
      );
    }

    await Promise.all(writes);
    // … rest of existing logic (clear editingSecrets, setSaved, etc.)
  } catch (err) {
    // … existing error handling
  }
};
```

**Card header** (line 195): add the Switch on the right:

```tsx
<CardHeader className="flex flex-row items-center justify-between">
  <div className="space-y-1.5">
    <CardTitle className="text-lg">{provider.name}</CardTitle>
    <CardDescription>
      {t("server:auth.providerDescription", { provider: provider.name })}
    </CardDescription>
  </div>
  <div className="flex items-center gap-2">
    <Label htmlFor={`${provider.enabledKey}-switch`} className="text-sm font-normal">
      {t("server:auth.enabled")}
    </Label>
    <Switch
      id={`${provider.enabledKey}-switch`}
      checked={enabled[provider.enabledKey] ?? true}
      disabled={!isConfigured(provider) || setParam.isPending}
      onCheckedChange={(next) => handleToggleEnabled(provider, next)}
      data-testid={`provider-enabled-${provider.name.toLowerCase()}`}
    />
  </div>
</CardHeader>
```

**Save button row** (line 279): add the unsaved-changes badge inline, and switch the button variant when dirty:

```tsx
<div className="flex items-center gap-3">
  <Button
    onClick={() => handleSave(provider.name)}
    disabled={setParam.isPending || !isDirty(provider)}
    variant={isDirty(provider) ? "default" : "secondary"}
  >
    {setParam.isPending ? (
      <>
        <Loader2 className="mr-2 h-4 w-4 animate-spin" />
        {t("common:saving")}
      </>
    ) : (
      t("common:save")
    )}
  </Button>
  {isDirty(provider) && (
    <span
      className="text-xs text-yellow-600 dark:text-yellow-500"
      data-testid={`provider-dirty-${provider.name.toLowerCase()}`}
    >
      {t("server:auth.unsavedChanges")}
    </span>
  )}
</div>
```

**Help text** (above the cards loop, line 194): one line stating that credential changes take effect after restart while the Enabled toggle is immediate. See *UX design — conservative approach* above.

```tsx
<p className="text-sm text-muted-foreground">
  {t("server:auth.helpText")}
</p>
```

### 2. Locale files — new keys

Four files: `web/dash0/src/locales/{en,fr,de,es}/server.json`. Inside the existing `auth` block (line 60 in `en/server.json`), add the toggle / dirty / help keys, plus the two new Discord-specific field labels under `auth.fields`:

| Key | EN | FR | DE | ES |
|---|---|---|---|---|
| `auth.enabled` | `"Enabled"` | `"Activé"` | `"Aktiviert"` | `"Activado"` |
| `auth.unsavedChanges` | `"Unsaved changes"` | `"Modifications non enregistrées"` | `"Nicht gespeicherte Änderungen"` | `"Cambios sin guardar"` |
| `auth.helpText` | `"Credential changes take effect after a server restart. The Enabled toggle takes effect immediately for the login page once saved."` | `"Les modifications d'identifiants prennent effet après le redémarrage du serveur. L'interrupteur Activé prend effet immédiatement pour la page de connexion une fois enregistré."` | `"Änderungen an den Anmeldedaten werden nach einem Server-Neustart wirksam. Der Schalter \"Aktiviert\" wirkt nach dem Speichern sofort auf die Anmeldeseite."` | `"Los cambios de credenciales surten efecto tras reiniciar el servidor. El interruptor Activado se aplica inmediatamente a la página de inicio de sesión una vez guardado."` |
| `auth.fields.botToken` | `"Bot Token"` | `"Jeton du bot"` | `"Bot-Token"` | `"Token del bot"` |
| `auth.fields.redirectUrl` | `"Redirect URL"` | `"URL de redirection"` | `"Weiterleitungs-URL"` | `"URL de redirección"` |

(Translations are suggestions — adjust to match the tone of existing strings in each locale, e.g., `de/server.json` already uses formal "Sie" register.)

## Tests

Playwright E2E in `web/dash0/tests/`:

1. **Toggle requires save**: navigate to `/orgs/default/server/auth` as admin (with Google credentials seeded), toggle Google off, assert the "Unsaved changes" badge appears and `/api/v1/auth/providers` still includes Google. Click Save, assert the badge disappears, reload, assert the switch is still off and Google is missing from the providers list.
2. **Toggle dirty marker clears on revert**: toggle off, then back on without saving — the badge should disappear (state matches persisted value again).
3. **Toggle disabled when unconfigured**: provider with empty `client_id` → switch is disabled (HTML `aria-disabled="true"` on the Radix Switch root).
4. **Discord card present**: the page shows a Discord card with all four fields (`client_id`, `client_secret`, `bot_token`, `redirect_url`) and an Enabled switch.

If the codebase has existing Playwright fixtures for auth admin (check `rtk grep -l "server.auth" web/dash0/tests/`), extend them. Otherwise add a small new `server-auth.spec.ts` covering all four cases — the dirty/save flow only needs to be exercised once.

## Verification

1. `bun run lint` (in `web/dash0/`) — passes.
2. `make build-dash0` — typechecks.
3. `make dev-test` and visit `http://localhost:4000/dash0/orgs/default/server/auth`:
   - Each provider card (now including Discord) has a header-row Switch labeled "Enabled".
   - Switches default to on for unconfigured providers but are **disabled**, with a muted appearance.
   - Configure Google credentials → switch becomes interactive.
   - Toggle Google off — the "Unsaved changes" badge appears next to the Save button, and the Save button switches to primary variant. The Google button on the public login page is **still visible** (no change persisted yet).
   - Click Save — badge disappears. Reload the public login page (`/dash0/orgs/default/login`) in another tab and confirm the Google button is now gone.
   - Toggle back on, then off again without saving, then on again — the badge tracks the dirty state and clears when the toggle returns to the persisted value.
4. Network panel: confirm a single `PUT /api/v1/system/parameters/auth.google.enabled` fires on Save (alongside any credential field writes), and **no** writes fire when the toggle is moved without clicking Save.
5. Switch language to `fr`, `de`, `es` via `?lang=…` (or the language picker if available on this page) and confirm the new strings render correctly.

## Final grep check

After the change, this should return six matches — one per provider including Discord:

```bash
rtk grep -n 'auth\.\(google\|github\|gitlab\|microsoft\|slack\|discord\)\.enabled' web/dash0/src/routes/orgs/$org/server.auth.tsx
```

---

## Implementation Plan

1. Update `server.auth.tsx`: extend `FieldKind` to include `botToken` and `redirectUrl`; extend `ProviderConfig` with `enabledKey`; add `enabledKey` to existing five providers and append a Discord provider entry with four fields.
2. Add `enabled` state map in the page; initialize it from the `params` effect (default to `true` when the `enabled` param is absent).
3. Add helpers: `isConfigured` (clientId non-empty), `persistedEnabled`, `isEnabledDirty`, `isCredentialDirty`, `isDirty`.
4. Render a Switch in each card header gated on `isConfigured` and labelled with `server:auth.enabled`; update `handleSave` to also persist `enabledKey` when the toggle is dirty.
5. Show the "Unsaved changes" badge next to Save when `isDirty(provider)`; switch the Save button variant to primary when dirty and disable it when not dirty.
6. Add the help text paragraph above the cards loop using `server:auth.helpText`.
7. Add the new locale keys to `en/fr/de/es` server.json: `auth.enabled`, `auth.unsavedChanges`, `auth.helpText`, `auth.fields.botToken`, `auth.fields.redirectUrl`.
8. Run `make build-dash0 lint-dash` and fix anything that breaks.
