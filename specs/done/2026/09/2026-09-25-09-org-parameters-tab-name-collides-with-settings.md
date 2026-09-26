---
model: sonnet
effort: low
---

# The organization "Parameters" and "Settings" tabs are both called "Paramètres" in French

## Problem

The organization layout (`web/dash0/src/routes/orgs/$org/organization.tsx:44-49`) shows two
tabs side by side, separated only by "Audit":

| Key | en | fr | de | es |
|---|---|---|---|---|
| `nav:parameters` (`/organization/parameters`) | Parameters | **Paramètres** | Parameter | Parámetros |
| `nav:settings` (`/organization/settings`) | Settings | **Paramètres** | Einstellungen | Ajustes |

In French the tab bar reads "Paramètres · Audit · Paramètres". Two tabs, same label, no way to
tell them apart without clicking.

The collision is a symptom. Even in English, "Parameters" next to "Settings" is weak: both words
mean "things you configure". It says nothing about what the page actually holds.

What the page holds (`organization.parameters.tsx`, `locales/en/org.json` `parameters.*`):
key/value pairs, optionally write-only secrets, that check configs reference as `${param:KEY}`.
They are substitution variables for checks, not org configuration.

## Proposal

Rename the tab (and the page title) to **Variables**, the term users already know from CI tools
("Secrets and variables" in GitHub Actions, "CI/CD variables" in GitLab).

| Key | en | fr | de | es |
|---|---|---|---|---|
| `nav:parameters` | Variables | Variables | Variablen | Variables |
| `org:parameters.title` | Variables | Variables | Variablen | Variables |

Also update the strings in `org:parameters.*` that say "parameter" as a noun ("Add parameter",
"Rotate parameter", "No parameters yet", "Delete this parameter?", "Parameter deleted", …) to
"variable" in all four locales, so the page does not contradict its own tab.

Keep unchanged (label-only change, no breaking change):

- The reference syntax `${param:KEY}`. The subtitle already explains it, and "a variable you
  reference as `${param:KEY}`" reads fine.
- The route `/orgs/$org/organization/parameters`, the API (`/orgs/:org/parameters`), the MCP/CLI
  names, and the translation key names (`nav:parameters`, `org:parameters.*`).

`nav:settings` stays "Paramètres" in French. Once the other tab is "Variables" there is no
collision left.

### Tests

- `locale-parity.test.ts` / `translated-defaults.test.ts` must stay green.
- Update any E2E assertion on the tab or page title text
  (`web/dash0/e2e/organization-parameters.spec.ts`, `dialog-close-click-through.spec.ts`).
- Add a unit test (or extend `locale-parity.test.ts`) asserting that, in every locale, the labels
  of the organization layout tabs are pairwise distinct, so this class of collision cannot come
  back silently when a new tab or locale lands.

### Docs

Grep `web/docs/docs/` (`features/config-as-code.md`, `features/javascript-checks.md`, `cli.md`)
for prose that points users at the "Parameters" tab and update it to "Variables". Leave the
generated API reference (`web/docs/docs/api/*org-parameter*`) alone.

## Open questions

- If "Variables" is rejected, fallback candidates: "Secrets" (inaccurate, values can be
  non-secret), "Check variables" (clearer but long for a tab on mobile).
- Should the reference syntax later gain a `${var:KEY}` alias to match the new name? Out of scope
  here; would be its own spec touching the resolver.

## Resolved open questions

- **Tab name**: implement "Variables" as the Proposal says. The fallback, only if "Variables"
  is later rejected, is "Check variables" — never "Secrets", because the values are not
  necessarily secret. Nothing to build for the fallback now.
- **`${var:KEY}` alias**: out of scope, not built here. It would be its own spec.
