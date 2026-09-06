---
model: sonnet
effort: medium
---

# The login page footer shows a bare version number with nowhere to go

## Problem

The bottom of the login card renders only the server version — `v0.24.0`
today — as plain text. Nothing there says *what* is at version 0.24.0, and
neither the product name nor the version leads anywhere: a visitor who wants
to know what SolidPing is, or what changed in this release, has to leave the
page and search.

The footer is built in
[web/dash0/src/routes/orgs/$org/login.tsx:892](web/dash0/src/routes/orgs/$org/login.tsx:892):

```tsx
{versionData && (
  <div className="mt-6 pt-4 border-t text-center text-xs text-muted-foreground">
    <span data-testid="login-version">
      v{versionData.version || "unknown"}
    </span>
    {(versionData.runMode === "demo" || versionData.runMode === "test") && (
      <span … data-testid="login-runmode">{versionData.runMode}</span>
    )}
  </div>
)}
```

`versionData` comes from `useVersion()`
([web/dash0/src/api/hooks.ts:3974](web/dash0/src/api/hooks.ts:3974)), which
polls `GET /api/mgmt/version`. That endpoint
([server/internal/app/server.go:2452](server/internal/app/server.go:2452))
serialises `version.Info`
([server/internal/version/version.go:30](server/internal/version/version.go:30))
— `version`, `commit`, `gitTime`, `runMode` — and nothing else. In particular
the **deployment mode** (`SP_DEPLOYMENT_MODE`, `saas` vs `self-hosted`, see
[server/internal/config/config.go:72](server/internal/config/config.go:72))
is not exposed to the browser anywhere: dash0 has no way today to tell whether
it is running on the hosted service or on someone's own install. That matters
here because the marketing link should carry UTM parameters that say which
flavour of the product sent the visitor.

There is no existing UTM convention in the repo (no `utm_` string anywhere in
`web/` or `server/`), and the three hard-coded `https://www.solidping.io/…`
links dash0 already has (`auth.slack.complete.tsx`,
`account.notifications.tsx`) carry none. Nothing in the E2E suite asserts on
the footer either (`login-version` / `login-runmode` are referenced only by the
page itself), so it can be restructured freely as long as new coverage lands
with it.

## Proposal

Render the footer as **`SolidPing v0.24.0`**, where the two halves are two
independent links, and keep the existing run-mode badge after them:

- **`SolidPing`** → `https://www.solidping.io/` with UTM parameters that
  differ between the SaaS and the self-hosted build.
- **`v0.24.0`** → `https://solidping.io/docs/changelog`.

### 1. Expose the deployment mode on `/api/mgmt/version`

Add `DeploymentMode string \`json:"deploymentMode,omitempty"\`` to
`version.Info` and set it in `getVersion` from `s.config.Deployment.Mode`,
exactly the way `RunMode` is set today. Values are the existing constants
`saas` / `self-hosted`; the default config already resolves an unset mode to
`self-hosted` ([config.go:2765](server/internal/config/config.go:2765)), so
the field is always populated in practice.

- Update `VersionResponse` in
  [server/internal/app/openapi/openapi.yaml:8711](server/internal/app/openapi/openapi.yaml:8711)
  (`deploymentMode: {type: string, enum: [saas, self-hosted]}`) and regenerate
  the Go client (`go generate ./pkg/client/...` — `client_generated.go` carries
  `VersionResponse`).
- Extend the `useVersion` response type in `hooks.ts` with
  `deploymentMode?: "saas" | "self-hosted"`.
- The endpoint is unauthenticated and the value is not sensitive (the hosted
  service is public; self-hosted is the default), so no gating is needed.

Reusing `/api/mgmt/version` means the login page needs **no extra request** —
it already holds `versionData`. Do not infer SaaS from the hostname.

### 2. One helper owns the marketing URL

Add `web/dash0/src/lib/marketing-url.ts` exporting something like
`marketingSiteUrl(deploymentMode: "saas" | "self-hosted" | undefined): string`
that returns `https://www.solidping.io/` with:

| param | value |
|---|---|
| `utm_source` | `solidping-dashboard` |
| `utm_medium` | `app` |
| `utm_campaign` | `saas` or `self-hosted` (falls back to `self-hosted` when the mode is unknown) |
| `utm_content` | `login-footer` |

Build it with `URL` / `URLSearchParams`, not string concatenation. The exact
values are a convention we are setting here for the first time — the point
of the helper is that they live in one place and every future outbound link
from the dashboard can reuse it (pass a different `utm_content` for other
placements). Keep the changelog URL as a plain constant next to it; the
request is for an unadorned `https://solidping.io/docs/changelog`.

### 3. Rework the footer in `login.tsx`

- Keep the `versionData &&` gate as it is: the footer appears once the version
  is known, so the UTM campaign is always accurate and loading behaviour does
  not change.
- `<a href={marketingSiteUrl(versionData.deploymentMode)} target="_blank"
  rel="noopener noreferrer" data-testid="login-brand-link">SolidPing</a>`,
  a space, then `<a href={CHANGELOG_URL} target="_blank"
  rel="noopener noreferrer" data-testid="login-version">v{version}</a>`.
  Keep the `login-version` test id on the version link and keep the
  `login-runmode` badge untouched.
- Styling: the footer must stay quiet. Links keep the `text-muted-foreground`
  colour and gain `underline-offset-4 hover:underline` (the same affordance the
  "create one" link uses a few lines above, minus `text-primary`). External
  links in dash0 use `target="_blank" rel="noopener noreferrer"` — see the
  figure pattern in
  [design-reference.tsx:3345](web/dash0/src/routes/orgs/$org/design-reference.tsx:3345).
  If the design reference has no "muted inline external link" entry, add one
  as part of this change (the reference page is the canonical catalog; a new
  pattern goes there first).
- When `version` is empty, render the version link text as `vunknown` exactly
  as today — do not hide the brand link because the version is missing.
- No new locale strings: "SolidPing" is a brand name and the version is data.
  If an `aria-label` / `title` is added to either link, add its key to **all
  four** `locales/{en,de,fr,es}/auth.json` files — `bun run test:unit` checks
  locale parity and a single missing key fails it.

### 4. Tests

- **Backend** (`server/internal/version` or an app-level handler test): a
  table-driven test that `GET /api/mgmt/version` returns
  `deploymentMode: "self-hosted"` with the default config and `"saas"` when
  `Deployment.Mode` is `saas`. The two existing tests that hit the endpoint
  (`custom_domain_routing_test.go`, `ratelimit_test.go`) only check status
  codes and must keep passing.
- **Frontend unit** (`web/dash0/src/lib/marketing-url.test.ts`, vitest, next
  to the other `lib/*.test.ts` files): assert the **exact** URL for `saas`,
  `self-hosted` and `undefined`, so a drift in any UTM value fails loudly.
- **E2E** (`web/dash0/e2e/login.spec.ts`): on the login page, the footer
  text matches `/^SolidPing v\d+\.\d+\.\d+/`; `login-brand-link` has
  `target="_blank"`, an href whose origin is `https://www.solidping.io` and
  whose query carries `utm_campaign=self-hosted` (the test server runs
  self-hosted); `login-version` has href
  `https://solidping.io/docs/changelog` and `target="_blank"`; the
  `login-runmode` badge still reads `test`.

### Out of scope

- Deep-linking the version to its own changelog entry (`#v0-24-0` or
  similar). The Docusaurus page generates heading ids from the heading text
  and they are not stable enough to depend on; leave the link at the page top.
- The sidebar version indicator
  (`components/layout/server-version-indicator.tsx`) and the other two
  `www.solidping.io` links in dash0. Once the helper exists they are a
  one-line follow-up each, but this spec is the login footer only.
- Making the marketing origin configurable. Hard-code it in the helper like
  the existing links do.
