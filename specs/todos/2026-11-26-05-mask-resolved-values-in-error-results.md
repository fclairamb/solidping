---
model: opus
effort: medium
---

# Mask resolved secret reference values in pre-probe error results

## Problem

Found by the security scan of 2026-11-26 (finding AY1).

- **Severity:** Low.
- **Who can trigger it:** a member with the `user` role who writes a check whose config references a `${param:…}` or an allowlisted `${env:…}` value. Every org member, the viewer role included, then reads the value in the check's result history (and in an incident built from that row).
- **Condition:** the resolved value has to make the config fail to parse, e.g. a value with a `%` that is not a valid escape, or a control character, substituted into a URL.

#1045 (a4dec9b4) masks the resolved reference values in every result that goes through `buildSubmitRequest` (`server/internal/checkworker/worker.go:1966-1976`, `result_masking.go`). The error results take another path that skips it:

- `saveErrorResult` (`server/internal/checkworker/worker.go:2055-2075`) stores `Output: {"error": err.Error()}` verbatim (`:2069`), with no call to `maskResolvedValues`.
- Its config-parse caller (`worker.go:1416`) runs after `materializeConfig` (`worker.go:1381`) has replaced `checkJob.Config` with the resolved copy. So the parse error describes the resolved text.
- `urlparse.Parse` (`server/internal/checkers/urlparse/urlparse.go:93`) wraps the `url.Parse` error. That error quotes the whole raw URL. `checkhttp` (`server/internal/checkers/checkhttp/config/config.go:231-232`), `checkdns` (`checkdns/config/config.go:128-130`), `checktcp` (`checktcp/config/config.go:85-87`) and `checkicmp` (`checkicmp/config/config.go:57-59`) put it in a `ConfigError` unchanged.
- Save-time validation does not catch it: the stored URL holds the literal `${param:…}`, which parses. Only the resolved value breaks it.

The result is the clear secret in `results.output.error`, which the masking was meant to rule out. Private agents run the same worker code, so their results are affected too.

The other `saveErrorResult` callers carry no resolved value today: `hostLocalRefusal` returns static errors (`worker.go:2020-2033`), and the reference-resolution errors name the reference, not its value. Masking in `saveErrorResult` covers them anyway if that changes.

## Proposal

1. In `saveErrorResult` (`server/internal/checkworker/worker.go:2065-2072`), build the output through `maskResolvedValues(checkJob, …)` (`result_masking.go`) instead of storing `err.Error()` raw. `checkJob.ResolvedRefValues` is already populated when `materializeConfig` succeeded, and is empty before that, so the early callers stay unchanged.
2. Leave `urlparse.Parse` and the checker config errors as they are: the masking in one place covers every checker, present and future. The error then reads `parse "https://***…": invalid URL escape …`, which still tells the user what is wrong.

## Tests

- `server/internal/checkworker/result_masking_test.go` (or a worker test next to `TestResultOutputMasksResolvedReferenceValues`): **regression**: a job whose HTTP check URL is `https://acme.com/?k=${param:key}`, with the parameter resolving to a value that breaks `url.Parse` (a `%` followed by non-hex characters), produces an error result whose stored `output.error` does not contain the value and does contain `***`. It fails before the fix.
- **Positive control**: the same job with a value that parses produces a normal result, and an error result for a job without any resolved reference keeps its error text unchanged (no `***`).
