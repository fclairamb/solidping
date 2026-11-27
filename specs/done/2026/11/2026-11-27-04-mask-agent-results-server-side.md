---
model: opus     # completes a secret-masking fix (#1045, #1061)
effort: medium
---

# Results from private agents are stored unmasked: agents older than v0.56.0 leak resolved `${param:…}` values

## Problem

Found by the security scan of 2026-11-27 (finding AZ3). Severity: Low. Who can read the leaked value:
any member of the org, viewers included, through results, incidents and notifications. Who sets it
up: an org admin who uses a `${param:…}` secret in a check that runs on a private location whose
agent predates v0.56.0.

Masking of resolved secret references (#1045 a4dec9b4, #1061 5d8d2447, both first in v0.56.0) only
runs in the process that executes the check: `maskResolvedValues`, `maskErrorText`, `maskMetricKeys`
and `maskDiagnostics` (`server/internal/checkworker/result_masking.go:61-200`), called from
`worker.go`. For a private agent that is the agent binary. The server stores what the agent sends:

- `agentws.handleResult` passes `frame.Output`, `frame.Metrics` and `frame.Diagnostics` to
  `SubmitResult` unchanged (`server/internal/handlers/agentws/handler.go:1643-1664`).
- `workers.Service.RecordResult` stores them as-is (`server/internal/handlers/workers/service.go:194-208`).

An agent from v0.41.0 (`agents.MinSupportedAgentVersion`, `server/internal/agents/protocol.go:25`) to
v0.55.x is still supported (compatibility window, `wiki/conventions/agent-compatibility.md`) and has
none of that masking. It stores the resolved value wherever the checker echoes it: the
`url.Parse` error of AY1, transport errors, final URLs. The server itself resolved those
`${param:…}` values and shipped them to the agent in the sealed overlay
(`checkjobsvc.ParamOverlay`, `handler.go:1484`), so it can mask them at receipt.

Current agents are not affected: `materializeConfig` reads the references from `StoredConfig` after
the overlay merge (`server/internal/checkworker/worker.go:1200-1266`), so they mask correctly.
`${env:…}` values resolved on the agent are unknown to the server and stay the agent's
responsibility.

## Proposal

1. Move the pure masking helpers (`maskableValues`, `maskString`, `maskJSONValue`, `maskMetricKeys`,
   `maskDiagnostics`) out of `checkworker` into an exported package that both `checkworker` and
   `agentws` can import (for example `server/internal/secretref/mask`). `checkworker` behaviour is unchanged.
2. In `agentws.handleResult`, after the `agentOwnsJob` check: when the job's public config holds a
   `${param:…}` reference (`secretref.ContainsInConfig`), recompute its values with
   `checkjobsvc.ParamOverlay` and mask `frame.Output`, `frame.Metrics` (keys) and `frame.Diagnostics`
   before `SubmitResult`. A job with no reference costs nothing extra.
3. If the overlay cannot be recomputed (a parameter deleted since the claim), store the result as is
   and log a warning. Do not drop the result: masking is a best-effort defence there, and the value
   is no longer current.

## Tests

- `server/internal/handlers/agentws`: a result frame for a job whose config is
  `https://acme.com/x?k=${param:token}` carries the clear token in `output.error`, a nested output
  value and a metric key. The stored result holds none of them, only the masked form. Fails before the
  fix (stored as sent).
- Positive control: a frame for a job with no reference is stored byte for byte, and a frame whose
  output carries an unrelated string equal to no parameter value is unchanged.
- `checkworker` masking tests (`error_result_masking_test.go` and the #1045 tests) keep passing after
  the helpers move.
