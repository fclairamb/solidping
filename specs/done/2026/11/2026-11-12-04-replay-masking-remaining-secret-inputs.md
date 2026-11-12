---
model: sonnet
effort: medium
---

# Some credential inputs still reach the unmasked dash0 session replay

## Problem

Found by the security scan of 2026-11-12 (finding AK4).

**Severity: Medium.** The replay records these values whenever PostHog is enabled (SaaS, or a self-hosted instance with a PostHog key) and an admin types or pastes a credential into one of the inputs below while recorded. Anyone who can read the PostHog project can then read the credential.

dash0 records sessions with typed values unmasked on purpose (`web/dash0/src/lib/analytics.ts:343-351`). #839 (spec `2026-11-11-05`) made `ph-no-capture` the only protection: inputs marked `data-secret-input` get it (`web/dash0/src/lib/no-capture.ts`, `components/ui/input.tsx`, `textarea.tsx:13`), and `PasswordInput` always adds it. rrweb masks native `type="password"` inputs by default, and nothing masks text inputs or textareas. Three inputs are left out:

1. **Kubernetes `kubeconfig`.**
   - The multiline `KubeSecretField` renders a plain `<Textarea>` with no `data-secret-input` (`web/dash0/src/components/integrations/integration-form.tsx:951-958`).
   - A kubeconfig holds bearer tokens and client private keys. It is a declared connection secret (`server/internal/crypto/credentials/conn_secrets.go:57`).
2. **Token-bearing webhook URLs.**
   - `UrlPanel` renders `webhook_url` / `server_url` in a plain `type="url"` input (`integration-form.tsx:2707-2715`), for Discord, Google Chat, MS Teams, Mattermost and Slack webhooks.
   - For those services the URL itself is the credential (a path token or a `key`/`token` query).
3. **Revealed provider secrets on the server sign-in settings page.**
   - `server.auth.tsx` toggles a raw `<Input>` between `password` and `text` (`web/dash0/src/routes/orgs/$org/server.auth.tsx:572-578`).
   - Once revealed, the OAuth client secrets (Google, GitHub, GitLab, Microsoft…) are plain text inputs without `ph-no-capture`. This page is for super admins only.

The secret-header value input of the HTTP check form (`components/checks/form/types/http.tsx:569`) is `type="password"`, so rrweb masks it. It has no `data-secret-input` though, unlike its key input (`:565`). Add one for consistency.

The ESLint rule `require-secret-text` only inspects `<code>` / `<pre>` text, so it cannot catch inputs.

## Proposal

1. Add `data-secret-input` to the `KubeSecretField` `<Textarea>` (`integration-form.tsx:951`) and to the CA cert textarea next to it if it is in the secret list (To verify: `k8s-ca-cert`, `:888`).
2. Give `UrlPanel`'s input `data-secret-input` (`integration-form.tsx:2709`) for the types whose URL is a credential, or for every webhook URL type (simpler).
3. In `server.auth.tsx`, replace the hand-rolled reveal with the `PasswordInput` primitive, or add `data-secret-input` when `field.secret` is set. Do the same for any multiline secret field.
4. Add `data-secret-input` to the HTTP secret-header value input (`http.tsx:569`).
5. Extend the ESLint rule, or add a unit test, so that an `<Input>` / `<Textarea>` whose `id`, `name` or test id says `secret`, `password`, `token`, `kubeconfig`, `webhook` or `private` must carry `data-secret-input` or be a `PasswordInput`.

## Tests

- `web/dash0/src/components/security/shown-once-secrets.test.tsx`: render the Kubernetes integration form, a Discord webhook integration form and the server sign-in settings page with a revealed secret. Assert that each credential input has the `ph-no-capture` class. This fails on main today for the kubeconfig textarea, the webhook URL input and the revealed secret.
- Positive control: a non-secret input of the same forms (the integration name) has no `ph-no-capture` class, so the replay keeps recording ordinary fields.
- If step 5 is a lint rule: its test file gets a failing case (a `<Textarea id="kubeconfig">` without the marker) and a passing case.
