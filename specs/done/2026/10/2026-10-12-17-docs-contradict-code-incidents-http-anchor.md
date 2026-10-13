---
model: haiku
effort: low
---

# Four docs statements contradict the code (incidents API, MTTR/MTTA, HTTP body, broken anchor)

## Problem
Public docs say things the code does not do:

1. `web/docs/docs/features/incidents.md:332-337` (List Incidents) documents `status`, `check_uid`, `offset`. The handler reads `state`, `checkUid`, `cursor` (`server/internal/handlers/incidents/handler.go:92,104,126`). It also accepts `checkGroupUid`, `memberCheckUid`, `kind`, `since`, `until` and `limit` (max 100, default 20).
2. `web/docs/docs/features/incidents.md:402-403` lists MTTR and MTTA under "Metrics". Neither exists in `server/` (grep for `mttr|mtta` found no Go match; only the docs mention them).
3. `web/docs/docs/features/check-types.md:40` says the HTTP Body is sent "for POST/PUT/PATCH/QUERY". `server/internal/checkers/checkhttp/checker.go:295-296` builds the body reader whenever `cfg.Body != ""`, whatever the method.
4. `web/docs/docs/features/javascript-checks.md:413` links to `./check-types.md#warning-authenticated-runs-are-real-windows-logons`. That anchor does not exist: the target in `check-types.md:409` is a `:::warning Authenticated runs are real Windows logons` admonition, and admonitions get no anchor.

## Proposal
1. `incidents.md` List Incidents: replace the parameter list with what the handler parses. `state` (comma-separated), `checkUid` (comma-separated), `checkGroupUid`, `memberCheckUid`, `kind`, `since`/`until` (RFC 3339), `cursor`, `limit`. Check `server/internal/app/openapi/openapi.yaml` for the exact names, allowed `state`/`kind` values and the `{ "data": [...] }` response shape, and copy them. Document `cursor` as the opaque value from the previous response.
2. `incidents.md` Metrics section: remove the MTTR and MTTA bullets. Keep Incident Count and Availability. Do not invent replacements. `acknowledgedAt` and `acknowledgedBy` exist on the incident response (`service.go:2231`), mention them only if a one-line fit is natural.
3. `check-types.md:40`: reword to "Request body, sent with any method" (keep the example). Say nothing about method restrictions.
4. Anchor: add a real heading above the admonition in `check-types.md` (for example `#### Authenticated runs are real Windows logons`, matching the neighbouring heading level) and point the link at its slug, or point the link at an existing heading of the RDP section. Prefer the first, so the link text stays accurate.
5. Build the docs to a scratch dir (see `web/docs/package.json` for the build script and its output flag) and confirm zero broken links or anchors. Commit with `docs: ...`, push the branch (no branch switch).

## Tests
- Docs build passes with no broken link or broken anchor warning (the Docusaurus `onBrokenLinks`/`onBrokenAnchors` settings decide whether it fails; check them and read the log for warnings).
- Negative: `grep -rn -i "mttr\|mtta" web/docs/docs` returns nothing, and `grep -n "check_uid\|offset" web/docs/docs/features/incidents.md` returns nothing for the incidents list section.

## To verify
- Accepted `state` and `kind` values and the response/cursor field names (`openapi.yaml` and `parseIncidentKinds` in `handler.go`).
- Whether other docs pages (`web/docs/docs/`, `llms` outputs, the OpenAPI-derived reference) repeat the wrong parameter names or the MTTR/MTTA claim: grep before closing.
- Whether Docusaurus is configured to fail on broken anchors (`web/docs/docusaurus.config.*`).
