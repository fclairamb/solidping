# Clone check action button in dash0

## Context

Backend `POST /api/v1/orgs/:org/checks/:checkUid/clone` shipped via `specs/done/2026/05/2026-05-02-20-check-clone.md` and is wired in `server/internal/handlers/checks/handler.go:314-334`. It returns a freshly-created check (defaulted to `enabled=false`, with a `(copy)` name suffix and a unique `-copy` slug). No UI surfaces it today.

The user wants a one-click clone that lands them on the **edit page** of the new check so they can rename, retarget, and enable.

## Scope

**In scope:**
- Add a `useCloneCheck` mutation hook to `web/dash0/src/api/hooks.ts`.
- Add a Clone button on the check detail page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) next to Edit / Refresh / Delete (lines 505–561). On success, navigate to the cloned check's edit page and show a toast.
- E2E test (Playwright in `web/dash0/e2e/`) covering: open a check → click Clone → land on edit page of new check → assert `enabled=false` and slug ends with `-copy`.

**Out of scope:**
- Bulk clone, custom-name dialog, clone-into-different-org (already explicit "out of scope" in the backend spec).
- Adding Clone to the row dropdown on the list page (`checks.index.tsx`) — defer; the detail page is the natural place for this action.

## Approach

### 1. Mutation hook

`web/dash0/src/api/hooks.ts` — add near other check mutations (e.g. `useUpdateCheck`):

```ts
export function useCloneCheck(org: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (sourceUid: string): Promise<Check> => {
      const res = await api.post<Check>(`/api/v1/orgs/${org}/checks/${sourceUid}/clone`, {});
      return res.data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["checks", org] });
    },
  });
}
```

Empty body for now (no name/slug overrides). The backend already produces sensible defaults.

### 2. Button on check detail page

`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`:
- `Copy` icon is already imported on line 9.
- Insert a button between Edit (line 512) and Refresh in the action row.

```tsx
<Button
  variant="outline"
  size="icon"
  onClick={async () => {
    const newCheck = await cloneCheck.mutateAsync(check.uid);
    toast.success(t("check.cloned"));
    navigate({
      to: "/orgs/$org/checks/$checkUid/edit",
      params: { org, checkUid: newCheck.uid },
    });
  }}
  disabled={cloneCheck.isPending}
  title={t("check.clone")}
>
  <Copy className="h-4 w-4" />
</Button>
```

`cloneCheck` comes from `useCloneCheck(org)` at the top of the component.

Add the i18n strings `check.clone` ("Clone") and `check.cloned` ("Check cloned — ready to edit") to the relevant locale files under `web/dash0/src/locales/`.

### 3. Tests

E2E (`web/dash0/e2e/check-clone.spec.ts`):
1. Log in as test user, ensure a check exists.
2. Open `/orgs/test/checks/<uid>`.
3. Click the Clone button.
4. Assert URL changes to `/orgs/test/checks/<newUid>/edit`.
5. Assert the form's "Enabled" toggle is off.
6. Assert the slug field ends with `-copy`.

## Verification

1. `make test-dash` passes.
2. `make dev`, log in to dash0, open any check, click Clone — land on edit page with `(copy)` in the name and toggle off.
3. Toast appears.
4. The list page now shows two checks; the new one is paused/disabled.
