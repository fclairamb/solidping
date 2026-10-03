---
model: sonnet
effort: medium
---

# JS check samples read secrets they never declare, so the form shows no field to fill

## Problem

Three JS samples read a secret that the sample config does not declare:

| Sample | Script reads | Declared `Secrets` |
|---|---|---|
| `JS: Redis AUTH + PING` | `secrets.REDIS_PASSWORD` | none |
| `JS: Bearer Token Login Chain` | `secrets.PASSWORD` | none |
| `JS: Browser Form Login` | `secrets.PASSWORD` | none |

When a user picks one of these samples, the secrets editor is empty. Nothing tells them the script needs a password. If they save it as is, the script reads `undefined` and the check fails at the auth step.

The fix is **not** to move these values to `env`. `env` is stored in the public `config` column. It comes back in every `GET`, appears in exports, and gets snapshotted in the check version history. `secrets` is stored in the encrypted envelope and never comes back out through the API (`server/internal/checkers/checkjs/checker.go:377`). A password belongs in `secrets`, and the samples are the most-copied examples, so they must show the right habit.

The samples were left without `Secrets` on purpose. The comments at `server/internal/checkers/checkjs/samples.go:150`, `:170` and `:185` point to the placeholder-shape bug. That bug was fixed by spec `2026-09-12-02-secret-placeholder-must-match-the-fields-shape` (now in `specs/done/2026/09/`), so the blocker no longer applies.

A backend-only change isn't enough. `jsModule.fromConfig` (`web/dash0/src/components/checks/form/types/misc.tsx:987-993`) always seeds `secrets: []`. That is correct for an existing check, because the form must never re-send a stored credential. But `applySample` (`web/dash0/src/components/shared/check-form.tsx:1138-1146`) goes through the same `fromConfig(sample.config)`, so it drops any secrets a sample declares.

## Proposal

1. **Backend samples** (`server/internal/checkers/checkjs/samples.go`):
   - Redis sample (~line 185): add `Secrets: map[string]string{"REDIS_PASSWORD": ""}`.
   - Bearer chain sample (~line 150) and Browser login sample (~line 170): add `Secrets: map[string]string{"PASSWORD": ""}`.
   - Replace the three "Deliberately no `Secrets` entry" comments with one short comment. It should say that secrets are declared with empty values so the form shows the key, and that the user must fill the value.
   - Values stay empty. A sample never ships a real or fake credential.
2. **Frontend: let a sample seed secret keys, never values.**
   - Give the module a way to seed from a sample, separate from seeding from an existing check. Recommended: an optional `fromSample?(config)` on `CheckTypeModule` (`web/dash0/src/components/checks/form/types/index.ts:34`). `applySample` calls it when it exists and falls back to `fromConfig` otherwise.
   - `jsModule.fromSample` returns `fromConfig(config)`, with `secrets` set to one row per key of `config.secrets` and the value always `""`, even if the sample config somehow carries one. Leave `secretsDirty: false`. Typing a value marks the section dirty through the existing `onChange`.
   - `fromConfig` stays as it is. It still seeds `secrets: []` for existing checks.
   - Wire it in `applySample` (`web/dash0/src/components/shared/check-form.tsx:1142`).
3. **Docs** (`web/docs/docs/features/javascript-checks.md:843` and `:877`): where each sample is mentioned, add one sentence saying that picking it adds an empty `PASSWORD` / `REDIS_PASSWORD` row to the **Secrets** editor, to be filled in before saving.

## Tests

- `server/internal/checkers/checkjs/` (new or existing samples test): each of the three samples declares exactly the secret keys its script reads (`secrets\.([A-Z_]+)` matched against the script), and every declared value is `""`. Negative case: the `JS: HTTP Health Check` and `JS: Aggregate Sub-checks` samples declare no `Secrets`.
- `server/internal/checkers/checkjs/docs_examples_test.go`: still passes. The scripts are unchanged, only the config is.
- `web/dash0/src/components/checks/form/types/misc.test.ts`:
  - `jsModule.fromSample({ script, secrets: { REDIS_PASSWORD: "" } })` gives one secret row with key `REDIS_PASSWORD` and an empty value.
  - `fromSample` with `secrets: { PASSWORD: "leak" }` still gives an empty value, never `"leak"`.
  - Negative case: `jsModule.fromConfig` with a `secrets` map still gives `secrets: []`.
- `web/dash0/e2e/check-js-env-secrets.spec.ts`: new case. On a new `js` check, pick the **JS: Redis AUTH + PING** sample and check that the Secrets editor shows a `REDIS_PASSWORD` row with an empty value, and that the Env editor shows `REDIS_ADDR`. The existing test, "env is shown and secrets are not, and an untouched save keeps both", must still pass. It covers the negative case of editing an existing check.

## To verify

- Does `GET` on check-type samples (behind `useSampleConfigs`) return the `secrets` map? Or does it strip private keys the way check `GET`s do? If it strips them, the samples endpoint must keep the keys for samples. Sample values are always empty, so that is safe.
- Does `rowsToMap` (`misc.tsx`) drop rows with an empty value? That matters for what gets saved when a user fills only some of the rows.
- Does creating a check with an empty-valued secret (the user picks the sample and saves without filling it) pass validation and store `""`? Or should validation reject an empty secret value? The recommended behaviour is to accept it: the check then fails at runtime with a clear `auth` step error, the same as today.
