---
model: sonnet
effort: medium
---

# Fold `/v1/features` and the deployment fields of `/mgmt/version` into `/v1/config`

## Problem
The dashboard learns what an instance can do from three endpoints.

- `GET /api/v1/config` (`server/internal/handlers/publicconfig/handler.go`), public. It lists instance capabilities built from server config: PostHog, WhatsApp, Telegram, SMS, Discord, demo.
- `GET /api/v1/features` (`server/internal/handlers/features/handler.go`, routed at `server/internal/app/server.go:1647`), auth required. It holds two more capabilities of the same kind: `bugReport` (`cfg.App.EnableBugReport`) and `heartbeatPush` (TCP/UDP enabled, host derived from `server.base_url`, ports).
- `GET /api/mgmt/version` (`server.go:2332`, `getVersion` at `:2748`). It holds the build identity plus `runMode` and `deploymentMode` (`server/internal/version/version.go:34-35`).

`/features` is `/config` under another name. Its auth protects nothing: the host is the public base URL, the ports are listeners exposed to the internet by design, and whether a bug-report button exists is not a secret. The cost is a second request, a second cache, a login-page special case (`web/dash0/src/api/hooks.features.test.tsx`, `e2e/login.spec.ts:185`), and a name that reads like per-org entitlements when it is not.

`/mgmt/version` is an ops endpoint (probes, status0, the stale-bundle check). The dashboard reads product behaviour from it:
- `components/integrations/freebox-form.tsx:62`: `deploymentMode === "saas"`
- `routes/orgs/$org/login.tsx:951`: marketing link UTM from `deploymentMode`
- `routes/orgs/$org/login.tsx:968-974`: demo/test badge from `runMode`
- `components/layout/AppSidebar.tsx:170`: `runMode === "test"`

## Proposal
1. **Backend, `publicconfig`.** Add to `Response`:
   - `bugReport: { enabled }` from `cfg.App.EnableBugReport`.
   - `heartbeat: { tcpEnabled, udpEnabled, host, tcpPort, udpPort }`, same values as today's `heartbeatPush`. Move `heartbeatPushFeature`, `listenPort` and `baseURLHost` from `features/handler.go` into `publicconfig`.
   - `runMode` and `deploymentMode` (top-level strings, `deploymentMode` is `"saas" | "self-hosted"`).
   Keep the existing per-capability `{ enabled, ... }` shape.
2. **Backend, remove `/features`.** Delete the route at `server.go:1647` and the `server/internal/handlers/features` package with its tests.
3. **Backend, slim `/mgmt/version`.** Drop `RunMode` and `DeploymentMode` from `version.Info` and stop setting them in `getVersion`. It returns build identity only: `version`, `gitHash`, `gitBranch`, `gitTime` (check the actual JSON names in `version.go`).
4. **Frontend, dash0.**
   - Extend `PublicConfig` in `web/dash0/src/lib/analytics.ts` with the new fields. Add small hooks next to the existing ones in `web/dash0/src/api/public-config.ts` (e.g. `useBugReportEnabled`, `useHeartbeatPush`, `useDeploymentMode`, `useRunMode`), defaulting to off / `undefined` while loading, as the WhatsApp hook does.
   - `routes/orgs/$org.tsx:1026-1238`: bug-report gating from public config. The `enabled: !isLoginPage` guard goes away, `/config` is public.
   - `routes/orgs/$org/checks.$checkUid.index.tsx:514-516`: heartbeat examples from public config.
   - The four call sites listed in Problem switch from `useVersion()` to the public-config hooks.
   - Remove `useFeatures`, `FeaturesResponse` and `HeartbeatPushFeature` from `api/hooks.ts`. Remove `runMode` / `deploymentMode` from `useVersion`'s type.
   - `useVersion` stays for the stale-bundle poll (`use-server-version-status.ts`, `lib/server-version.ts`) and for `serverVersion` in `organization.private-locations.index.tsx:552`.
5. **status0** (`web/status0/src/api/hooks.ts:449-461`) only reads build fields. Nothing to change, just confirm.
6. **Docs.** `server/internal/app/openapi/openapi.yaml` (`/api/mgmt/version`, the `/api/v1/config` schema, remove `/api/v1/features` if present), `wiki/api-specification/management.md:9` and `:87`, `wiki/api-specification/system.md:19`, and the `/api/mgmt/version` + `useVersion()` entries in `routes/orgs/$org/design-reference.tsx:2630, 2788-2792`.

## Tests
- `server/internal/handlers/publicconfig/handler_test.go`: `bugReport.enabled` false on the default config and true when enabled; `heartbeat` off by default, and host/ports correct when both listeners are on (port the cases from `features/handler_test.go`); `runMode` and `deploymentMode` echo the config.
- A server test that `GET /api/v1/features` now answers 404 and `GET /api/mgmt/version` no longer carries `runMode` / `deploymentMode`.
- `web/dash0/e2e/bug-report.spec.ts` and `e2e/check-heartbeat-push.spec.ts`: stub `**/api/v1/config` instead of `**/api/v1/features`. Merge the stub into the full config body so other capabilities stay as the test expects.
- Replace `api/hooks.features.test.tsx` with a unit test for the new public-config hooks (default while loading, value once loaded).
- E2E: the login page shows the demo/test badge and the SaaS marketing link when `/api/v1/config` says so, and `/api/mgmt/version` is not consulted for either (stub it without those fields).
- The Freebox form still takes its SaaS branch with `deploymentMode: "saas"` in public config.

## To verify
- Spec `2026-09-29-02` (bug report off by default) edits `features/handler.go:59` and adds `features` handler tests. Implement this spec after it and carry its tests into `publicconfig`.
- Whether any external consumer (the agent binary, docs site, CLI, monitoring) reads `runMode` / `deploymentMode` from `/api/mgmt/version`. Grep `server/`, `agent/` if any, `web/docs/`, `deploy/`.
- Whether the CSP / login redirect logic special-cases `/api/v1/features` anywhere beyond the test at `e2e/login.spec.ts:185`.

## Open questions
- Keep `runMode` on `/mgmt/version` too, for ops curling a pod? Recommended: no. One source per value, and `/api/v1/config` is just as curlable.

## Resolved open questions
- Do not keep `runMode` on `/mgmt/version`. `/api/v1/config` is the single source for it.
