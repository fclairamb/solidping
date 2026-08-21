---
model: sonnet
effort: medium
---

# "Send me a test" reports failure on success: apiFetch can't parse a 202 with an empty body

## Problem

On `/dash0/orgs/$org/organization/report-schedules`, the row's **Send me a test**
button fires, the backend queues the mail correctly, returns `202 Accepted` —
and the UI shows a red toast: *"Failed to send the test report"*.

The request succeeded. The error is manufactured entirely on the client.

### Root cause

[client.ts:266-311](web/dash0/src/api/client.ts#L266) special-cases exactly one
empty-body status:

```ts
if (response.status === 204) {
  return undefined as T;
}

return response.json();   // ← a 202 with an empty body lands here
```

`TestSend` writes a bare `writer.WriteHeader(http.StatusAccepted)` with no body
([reportschedules/handler.go:130](server/internal/handlers/reportschedules/handler.go#L130)),
so `response.json()` is called on a zero-length stream and throws
`SyntaxError: Unexpected end of JSON input`. The mutation's `catch` in
[organization.report-schedules.index.tsx:148-155](web/dash0/src/routes/orgs/$org/organization.report-schedules.index.tsx#L148)
tests `err instanceof ApiError` — a `SyntaxError` is not one — so it falls
through to the generic `t("reports.testFailed")` string.

**This is a general client bug, not a report-schedules bug.** Any endpoint that
returns an empty body under a status other than 204 hits it. The endpoint just
happens to be the only current caller: the other `202` in the codebase
([statussubscribers/handler.go:75](server/internal/handlers/statussubscribers/handler.go#L75))
writes a JSON body and is therefore unaffected.

### 202 is the correct status — do not "fix" this by changing it to 204

It is tempting to change the handler to `204 No Content` and close the ticket.
That would hide the client bug rather than fix it, and it would also be *less*
accurate: `Mailer.SendReport` **enqueues** a job rather than sending inline
([uptime_report_mailer.go:19-25](server/internal/app/uptime_report_mailer.go#L19)),
so at the moment the handler returns, the mail genuinely has not been sent yet.
`202 Accepted` is the honest code, the OpenAPI contract already documents it as
`Report queued` ([openapi.yaml:5214-5216](server/internal/app/openapi/openapi.yaml#L5214)),
and the success toast already says "queued" rather than "sent"
(`slos.json` → `reports.testSent`). Keep the 202; fix the parser.

### Secondary defect found while tracing this: the suppressed-recipient silent lie

[reportschedules/service.go:292-299](server/internal/handlers/reportschedules/service.go#L292)
returns `nil` when the caller's own address is on the org's suppression list:

```go
if suppressed {
    return nil
}
```

The handler then writes 202 and the UI toasts *"Test report queued to your
address"* — but nothing was queued and nothing will ever arrive. An operator
who unsubscribed themselves at some point gets a green toast and an empty inbox,
with no way to tell this apart from a working send.

Honoring the suppression list here is correct and must stay (it is what keeps a
single code path able to mail a suppressed address). What is wrong is reporting
it as success.

## Proposal

### 1. Fix `handleResponse` to tolerate any empty body (the real fix)

In [client.ts:307](web/dash0/src/api/client.ts#L307), replace the
`status === 204` special case with a general empty-body guard, so this class of
bug cannot recur on the next endpoint that returns 202/205/or a bodyless 200:

- Return `undefined as T` for `204` and `205` (defined as bodiless by spec).
- Otherwise, treat the body as absent when `Content-Length` is `"0"`, **or**
  when the `Content-Type` is missing / is not a JSON media type. Go's
  `WriteHeader` with no write sets neither header to something JSON-ish, so this
  catches the case.
- As a final backstop, prefer `await response.text()` and `JSON.parse` only on a
  non-empty string, rather than letting `response.json()` throw on `""`. This
  is the version that is robust regardless of what headers a proxy adds or
  strips.

The guard must **not** swallow malformed JSON on a response that really did
carry a body — a truncated or invalid payload should still throw, otherwise a
genuinely broken endpoint starts silently resolving to `undefined`. Keep the
error surfaced as an `ApiError` (or let the `SyntaxError` propagate) in that
case, and cover it with a test.

### 2. Report the suppressed case instead of faking success

Introduce a distinct sentinel (e.g. `ErrRecipientSuppressed`) returned by
`Service.TestSend` when the resolved recipient is suppressed, map it in
`handleError` to a `409 CONFLICT` (or a `400 VALIDATION_ERROR` — pick one and
state it in the OpenAPI file), and add a matching `reports.testSuppressed`
string to `slos.json` in **all four** locales (en/fr/de/es) so the row's catch
can tell the operator *why* nothing will arrive. Add the new response code to
the `testReportSchedule` operation in
[openapi.yaml:5214](server/internal/app/openapi/openapi.yaml#L5214).

### 3. Tests

- **Unit (dash0):** a `handleResponse` test matrix over `204`, `202` with an
  empty body, `202` with a JSON body, and `200` with a malformed body. The
  malformed-body row is the positive control that proves the new guard didn't
  just turn every parse failure into a silent `undefined`.
- **E2E:** [report-schedules.spec.ts](web/dash0/e2e/report-schedules.spec.ts)
  currently never clicks `report-row-test`. Add a case that clicks it and
  asserts the **success** toast appears and the failure toast does not — the
  latter assertion is the one that would have caught this bug.
- **Backend:** a `Service.TestSend` test asserting the suppressed recipient now
  returns `ErrRecipientSuppressed` rather than `nil`, alongside a positive
  control that a non-suppressed recipient still enqueues exactly one mail.
