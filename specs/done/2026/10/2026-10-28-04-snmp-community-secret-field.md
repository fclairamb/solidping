---
model: sonnet
effort: low
---

# SNMP community string is stored and returned as public check config

## Problem

Found by the security scan of 2026-10-28 (finding X4).

**Severity: low.** Any member of the org can read it, viewers included. Anyone who receives an export of the org's checks can read it too.

- In SNMP v1/v2c, the community string is the protocol's only credential. A read-write community also lets its holder change the device.
- The SNMP checker declares only the v3 passwords as secret (`server/internal/checkers/checksnmp/config/secret_fields.go:5-7`, `return []string{"authPassword", "privPassword"}`). `community` (`config/config.go:40`) is treated as ordinary config.
- The value therefore:
  - is stored in the public `config` column, not the encrypted secret store (`applyEncryption` splits on `SecretFields()`);
  - comes back in clear on `GET /orgs/:org/checks` and `/checks/:uid` for every member, and through the MCP check tools. `redactSecretConfig` strips only declared secret keys (`server/internal/handlers/checks/service.go:4380-4405`);
  - is kept in exports and in check version snapshots. `stripSecretKeysForExport` (`service.go:4353-4366`) and `hiddenExportConfigKeys` / `checkversion.HiddenConfigKeys` (`diff.go:526-530`) use the same declared set.
- SNMP is the only outlier. Every other checker declares its credential fields, e.g. `password` for rdp, vnc, ftp and the databases, `secretHeaders` and `basicAuth` for http, `secretMetadata` for grpc, `saslPassword` for kafka.

## Proposal

1. Add `"community"` to `SNMPConfig.SecretFields()` (`server/internal/checkers/checksnmp/config/secret_fields.go`).
2. Make sure existing rows move the value out of the public column. The credentials migration (`server/internal/credmigrate`) should re-split rows whose type gained a secret field, and the read path already redacts declared keys.
3. Dash0 SNMP form (`web/dash0/src/components/checks/form/types/infra.tsx`): render `community` as a secret input, like the v3 passwords. An unchanged edit must keep the stored value.
4. The default `public` community applied when the field is empty (`checksnmp/checker.go:128-133`) stays as it is. It is not stored.

## Tests

- `checksnmp/config`: `SecretFields()` contains `community` (update the existing table test).
- `server/internal/handlers/checks` (both engines):
  - **Regression (fails before the fix):** create an SNMP v2c check with community `s3cr3t-acme`.
    - `GET` as a viewer returns no `community` value, and `configPrivateKeys` lists it.
    - The export does not contain `s3cr3t-acme`.
    - The public `config` column of the row does not contain it.
  - **Positive control:** the check still runs with the stored community. An edit that leaves the field untouched keeps it.
- Migration/credmigrate: a row written before the change, with `community` in the public column, ends up with the value in the secret store and out of `config`.

## To verify

- Whether `credmigrate` re-splits existing rows when a checker's `SecretFields()` grows, or only runs once. If only once, add a step for SNMP rows.
- Whether past check version snapshots (`checkversion`) already stored the community. If so, scrub it there too.
