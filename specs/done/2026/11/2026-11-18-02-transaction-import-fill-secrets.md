---
model: opus
effort: medium
---

# Imported transaction checks keep typed passwords in the script, which every viewer can read

## Problem

Found by the security scan of 2026-11-18 (finding AQ2). Severity: Low. No attacker is needed: an org admin who imports a Pingdom transaction check, or a Chrome Recorder flow, unknowingly exposes the login password it types. Every member of the org can then read it, viewers included.

- **Pingdom TMS.** `fill` / `wpm_fill` steps become `page.fill(selector, "<value>")`, with the value written into the script as a plain string literal (`server/internal/handlers/checks/importers/pingdom_tms.go:565-567`). Nothing checks whether the field is a password. Logging in to an application is the main use of a transaction check. So a typical import, with steps like "fill `Email`", "fill `Password`", "click Sign in", puts the password into the script.
- **Chrome Recorder.** A `change` value goes to the check's encrypted secrets only when one of its selectors matches `passwordHint` (`(?i)password|passwd|pwd|mot de passe`, `server/internal/handlers/checks/importers/chromerecorder.go:51-52`, used at `:467-473`). Any other typed value stays in the script, for example a field selected by its id (`#cred`, `#pin`), a label in another language, or an API key pasted into a form. The import then warns that "password value(s) were moved to the check's encrypted secrets" (`chromerecorder.go:301-303`), which suggests every credential was handled. When no selector matches, there is no warning at all.
- The script is plain config. It is stored unencrypted and returned by `GET /orgs/:org/checks/:checkUid`, by the export, by MCP, and in the dash0 check view, to every role. It is also sent to the AI provider when someone uses AI authoring on the check. The other importers either drop credentials with a warning or move them to the encrypted secrets (`importers/mapping.md:69, 136, 181, 269`). The Pingdom TMS `basic_auth` step does the same (`pingdom_tms.go:458-498`). The `fill` path is the exception.
- This is the same class as the earlier findings where a credential was kept as plain config instead of a secret (for example, an SNMP community string not treated as a secret).

## Proposal

1. Share one rule between both importers, in a helper in `importers/`. A typed value moves to the check's encrypted `secrets` and is referenced as `secrets.<KEY>` when any of these is true:
   - the selector or input name matches the password hint, widened to common names (`password`, `passwd`, `pwd`, `pass`, `pin`, `otp`, `secret`, `token`, `api[-_ ]?key`, `mot de passe`, `passwort`, `contraseña`);
   - for Chrome Recorder, the step's selectors include `type=password` or `[type="password"]`;
   - the value itself looks like a credential: a `Bearer ` prefix, or a long high-entropy string with no spaces.
2. Pingdom TMS: apply the rule to the `fill` / `wpm_fill` steps (`pingdom_tms.go:565-567`), with keys `PINGDOM_FILL_<n>`, attached through the same `config["secrets"]` path as basic auth (`pingdom_tms.go:238-244`).
3. Both importers: when any typed value is left in the script, add a warning that says so and counts them, for example "N typed value(s) stayed in the script; move any credential to `secrets.NAME`". Today there is either the "moved" warning or nothing.
4. Update `importers/mapping.md` (`fill` / `wpm_fill`, line 289, and the Recorder `change` row) and the two migration guides (`web/docs/docs/features/migrate/from-pingdom.md`, `from-chrome-recorder.md`) to describe the rule and the warning.

## Tests

- `pingdom_tms_internal_test.go`: a flow whose steps fill `Email` and then `Password`. The password value must not appear in the generated script, it must be in the check's `secrets`, and the script must reference `secrets.PINGDOM_FILL_1`. This fails before the fix. Positive control: the `Email` value is still a literal in the script, and the golden test (`testdata/pingdom-tms-script.golden.json`) changes only where the rule applies.
- `chromerecorder_test.go`: a `change` step on `#cred` with a `type=password` selector goes to secrets (fails before the fix). A `change` step on `#search` with value `hello` stays in the script and produces the new "typed value(s) stayed in the script" warning. Positive control: the existing `aria/Password` case still moves to secrets.
- A test that goes through `ApplyChecks` on an imported check: the stored public config has no plain-text password, and `GET` of the check as a viewer doesn't return it.

## To verify

- That `GET /tms/check/{cid}` from Pingdom returns `fill` values in clear text. The fixtures (`testdata/pingdom-tms-script.json:14`) assume it does. If Pingdom masks them, step 2 only covers pasted payloads, but it is still worth doing.
