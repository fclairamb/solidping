---
model: opus
effort: high
---

# Reject `exec` / `auth-provider` kubeconfigs and restrict `inCluster` for Kubernetes connections

## Problem
Found by the security scan of 2026-10-11 (finding C1). **Critical (RCE)**, triggerable by any role ≥ `user` — on a SaaS instance with open signup, anyone who registers (any authenticated user can create their own org, `server/internal/app/server.go:915-918`).

A Kubernetes cluster connection stores a user-pasted kubeconfig, and the server turns it into a client with `clientcmd.RESTConfigFromKubeConfig([]byte(priv.Kubeconfig))` (`server/internal/integrations/kubernetes/client.go:197-205`, inside `BuildRestConfig`). Nothing strips `users[].user.exec` or `auth-provider`. In client-go v0.37.1 (`server/go.mod:337`) `RESTConfigFromKubeConfig` carries the exec/auth-provider config straight onto the rest.Config (`tools/clientcmd/client_config.go:306-311`), and `PluginPolicy` is left unset (= `AllowAll`, `tools/clientcmd/api/types.go:287-334`), so the configured `exec.command` is run with `os.exec` on the first API call. A member who creates a `kubernetes` integration whose kubeconfig has `exec: {command: ..., args: [...]}` and triggers it gets command execution **on the process that builds the client** — the egress guard does not help because the exec runs before any network dial.

**Every path flows through the one unguarded builder** `BuildRestConfig` (`client.go:172-208`) via `ResolveClientset` (`client.go:59-77`):
- **Connection test**, on the API process: `POST /orgs/:org/integrations/:uid/test` → `testKubernetesConnection` → `ResolveClientset` (`handlers/integrations/service.go:1689-1691`, `:1771`).
- **The `kubernetes` check**, on a worker: the resolver wired at `server.go:447-449` calls `ResolveClientsetByUID` (`client.go:86-105`).
- **Discovery probe / scan**: `discovery/scantypes/kubernetes.go:78` and `jobs/jobtypes/job_kubernetes_discovery.go:113` both call `ResolveClientsetByUID`.

**`inCluster` is unrestricted** too (`client.go:188-194` → `inClusterConfig()` = `rest.InClusterConfig`, `client.go:261`): any member can set `settings.inCluster=true` (`db/models/integration.go:661-664`; UI mode at `web/dash0/src/components/integrations/integration-form.tsx:734-735`) and make the server authenticate to *its own* cluster with the pod's mounted service-account token — serious for the k8s-hosted prod.

There is no validation of kubeconfig contents or of `inCluster` at save time: `validateConnectionType` (`handlers/integrations/service.go:581-594`) only special-cases Twilio, and `checkCreateTypeConstraints` (`:601-639`) has no `kubernetes` branch. `CreateIntegration` (`:477-525`) and the PATCH merge `applyUpdateSettings` (`:1067-1127`) both go straight to `applySettingsEncryption` for kubernetes.

## Proposal
Reject exec/auth-provider kubeconfigs and gate `inCluster`, at save time **and** at use time (defense in depth: a row written before the fix, or by a future direct DB path, must still be refused when a client is built).

1. **A single parse-and-reject helper in `server/internal/integrations/kubernetes`.** Add e.g. `validateKubeconfigSafe(raw string) error`: parse with `clientcmd.Load([]byte(raw))` (not `RESTConfigFromKubeConfig`), then reject if any `AuthInfo` in the parsed `api.Config` has `Exec != nil` or `AuthProvider != nil`. Return a coded, non-leaky error. Export it so the handler can call it at save time.
2. **Enforce at use time inside `BuildRestConfig`** (`client.go:172-208`): in the `priv.Kubeconfig != ""` branch (`:197-204`), run `validateKubeconfigSafe` before `RESTConfigFromKubeConfig` and return its error. Belt-and-braces, also set `restCfg`'s plugin policy to deny if client-go exposes it on the built config, but the parse-time reject is the primary guard since it covers both exec and auth-provider. This one chokepoint covers test, check, discovery and scan, because all four reach `BuildRestConfig` via `ResolveClientset`/`ResolveClientsetByUID`.
3. **Restrict `inCluster` in `BuildRestConfig`** (`client.go:188-194`): resolve it only when a deployment-level opt-in allows it (see Open question for which gate). Add a config flag in `server/internal/config` (e.g. `Deployment`/a new `kubernetes` block, env `SP_K8S_ALLOW_IN_CLUSTER`, default false) and thread it into the kubernetes package (it currently depends only on `db.Service` + `credentials.Service` — pass the flag through `ResolveClientset`/the resolver closure wired at `server.go:447`, or via a package-level setter mirroring `inClusterConfig` at `client.go:261`). When disallowed, return a coded error instead of calling `inClusterConfig()`.
4. **Reject at save time.** Add a `kubernetes` branch to the create/update validation:
   - In `checkCreateTypeConstraints` (`handlers/integrations/service.go:601-639`) and in `applyUpdateSettings` after the merge (`service.go:1085-1118`, alongside the Teams/Twilio/Freebox/sender-URL re-validations), parse the connection's kubeconfig (from the incoming/merged secret settings) with `validateKubeconfigSafe` and reject exec/auth-provider. Do it on the PATCH-merged settings so a two-step create-then-PATCH cannot smuggle a bad kubeconfig past creation (same pattern the Freebox baseUrl re-validation uses, `service.go:1118-1124`).
   - Reject `settings.inCluster=true` at save time unless the opt-in from step 3 is set, so the dashboard shows the error on save rather than only when the connection is first used.
5. **Keep the test-only seam.** `inClusterConfig` is already indirected for tests (`client.go:256-261`); keep that. Add a sibling seam if needed so the exec-rejection path can be unit-tested without a real plugin.
6. **Docs.** Note the exec/auth-provider restriction and the `inCluster` opt-in in `web/docs/docs/features/check-types.md:1365-1372` (the "Cluster connection" section) and the kubernetes connection model doc (`db/models/integration.go:643-664` comment).

## Tests
- `server/internal/integrations/kubernetes/client_test.go` (table-driven on `BuildRestConfig` / the new helper): **regression** — a kubeconfig with an `exec` block is rejected (today `TestBuildRestConfigKubeconfig` at `:211-242` builds a config from a benign kubeconfig; add the malicious-kubeconfig case and assert a coded error, not a `*rest.Config`); the same for an `auth-provider` block. **Positive control** — the existing benign-kubeconfig case (token/cert auth) still builds a working `*rest.Config` with the expected `BearerToken`.
- `server/internal/integrations/kubernetes/client_test.go`: **regression** — with the in-cluster opt-in off, `BuildRestConfig` with `settings.InCluster=true` returns a coded error and never calls the `inClusterConfig` seam; **positive control** — with the opt-in on, it uses the (stubbed) seam.
- `server/internal/handlers/integrations/kubernetes_test.go`: **regression** — `CreateIntegration` and `UpdateIntegration` (PATCH) for a `kubernetes` connection whose kubeconfig carries `exec`/`auth-provider` are rejected with `VALIDATION_ERROR` before any row is written; a create-clean-then-PATCH-malicious sequence is also rejected. **Positive control** — a token-auth or clean-kubeconfig connection is created and updated normally.
- `server/internal/handlers/integrations/kubernetes_test.go`: `POST /test` on a stored connection whose kubeconfig has `exec` returns a failed `IntegrationTestResult` with the coded reason and never execs (assert via a seam / that the error is the rejection, not an exec/stat error).
- The check and discovery paths need no separate RCE test beyond the `BuildRestConfig` unit tests, since both reach it through `ResolveClientsetByUID`; add one thin test asserting `ResolveClientsetByUID` surfaces the rejection error for a stored exec-kubeconfig connection, to pin that the use-time guard covers the worker/discovery path.

## To verify
- That `clientcmd.Load` exposes `AuthInfo.Exec` / `AuthInfo.AuthProvider` on the parsed `api.Config` in v0.37.1 (confirmed indirectly: `client_config.go:306-311` reads `configAuthInfo.AuthProvider` / `configAuthInfo.Exec`; the `api.AuthInfo` struct carries both). Confirm the exact field names when implementing.
- Whether client-go v0.37.1 lets us set `PluginPolicy`/`ExecProvider=nil` on the built `*rest.Config` as an extra belt (nice-to-have; the parse-time reject is sufficient).

## Open questions
1. **How to gate `inCluster`.** Options: (a) super-admin-only (resolve the caller's `user.SuperAdmin`, `middleware/auth.go:775-798`), or (b) a self-hosted deployment opt-in flag, default off. Recommended: **(b) a deployment opt-in** (`SP_K8S_ALLOW_IN_CLUSTER`, default false), because the resolve-time chokepoint (`BuildRestConfig`) has no caller identity — it runs on workers and discovery jobs with no `user` in scope — so a role check cannot be enforced there, only at the save-time handler. A deployment flag is enforceable at both layers uniformly, and in-cluster is inherently a self-hosted (SolidPing-runs-in-the-target-cluster) feature, never a SaaS one. If the operator wants it, they set one env var; SaaS leaves it off and the pod's service-account token is never reachable by a tenant.

## Resolved open questions
Decided 2026-10-16 (all recommendations accepted):
1. Gate `inCluster` with a deployment opt-in, `SP_K8S_ALLOW_IN_CLUSTER`, default false, enforced both at save time and in `BuildRestConfig`.
