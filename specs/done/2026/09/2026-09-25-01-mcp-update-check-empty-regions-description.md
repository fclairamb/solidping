---
model: sonnet
effort: low
---

# MCP `update_check` says an empty `regions` array pauses the check, but the check keeps running

## Problem

The `regions` property of the MCP `update_check` tool
(`server/internal/mcp/tools_checks.go:254-257`) says:

> Pass an empty array to run from no regions (effectively pauses execution).

That is false. The call path is:

1. `getStringSliceArg` (`server/internal/mcp/handler.go:531`) turns `[]` into a
   non-nil empty slice, so `req.Regions` is set (`tools_checks.go:302-304`).
2. `checks.Service.UpdateCheck` (`server/internal/handlers/checks/service.go:1878-1884`)
   passes it to `ResolveRegionsForCheck`.
3. `ResolveRegionsForCheck` (`server/internal/regions/regions.go:248`) treats an
   empty list as "not set" and falls back to the org `default_regions`
   parameter, then the system `default_regions`, then every declared region.
4. The resolved set is stored on the check.

So the check keeps running from the default regions. An AI client that follows
the description to pause a check leaves it running, and reports back that it
paused it.

Note on step 4: the resolved set is stored as-is. The check is pinned to the
defaults *as they are at update time*; it does not follow later changes to
`default_regions`. The corrected description must not claim otherwise.

Related, smaller inaccuracy: the `create_check` `regions` description
(`tools_checks.go:172-175`) says "Defaults to all org regions when omitted".
The real fallback is the same chain as above (org default, system default, all
declared regions).

## Proposal

1. **Fix `update_check`'s `regions` description.** Something like:
   > Replace the region list, e.g. ["eu-west-1","us-east-1"]. An empty array
   > resets the check to the default regions (org `default_regions`, else the
   > system default, else every region). It does NOT pause the check: use
   > `enabled: false` for that.

2. **Make `enabled` point the other way too.** Extend the `enabled` description
   on `update_check` (`tools_checks.go:258`) to say it is the way to pause /
   resume a check, so a client looking for "pause" finds it on the right field.

3. **Fix `create_check`'s `regions` description** to describe the same fallback
   chain instead of "all org regions".

4. **Sweep for the same claim elsewhere** and correct any hit:
   - `server/internal/app/openapi/openapi.yaml` (check create/update/validate
     `regions` properties; the dry-run `regions` at ~line 10783 was checked and
     is fine).
   - `web/docs/docs/` (MCP page, checks/regions pages). An initial grep found no
     "empty regions pauses" claim, but re-check after any doc regeneration.
   - `server/internal/mcp/*_test.go`.

5. **Pin it with a test** in `server/internal/mcp/tools_checks_test.go`,
   following `TestListChecksDef_LabelsIsObject` (line 78):
   - `updateCheckDef()` `regions` description does **not** contain "pause"
     (case-insensitive) and does mention "default".
   - `updateCheckDef()` `enabled` description mentions "pause".
   - `createCheckDef()` `regions` description does not say "all org regions".

   Optional, if cheap with existing fixtures: a behavioral test that calls the
   `update_check` tool with `regions: []` and asserts the check's stored regions
   are non-empty and `enabled` is unchanged. This proves the description matches
   reality rather than only asserting on wording.

## Out of scope

- Changing the behavior itself (e.g. making `[]` really mean "no regions").
  The fallback is intentional and shared with the REST API and dash0.
- Making the stored region set dynamically follow `default_regions`.
