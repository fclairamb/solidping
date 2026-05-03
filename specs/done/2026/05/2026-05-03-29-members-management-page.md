# Members management page

## Context

The org settings layout at `web/dash0/src/routes/orgs/$org/organization.tsx` currently exposes two tabs: **Invitations** and **Settings**. There is no UI to see who is a member, change a member's role, or remove someone — even though the backend has shipped the full CRUD at `/api/v1/orgs/:org/members` for a while:

```
GET    /api/v1/orgs/:org/members              → ListMembers
POST   /api/v1/orgs/:org/members              → AddMember (by email of an existing user)
GET    /api/v1/orgs/:org/members/:uid         → GetMember
PATCH  /api/v1/orgs/:org/members/:uid         → UpdateMember (role only)
DELETE /api/v1/orgs/:org/members/:uid         → RemoveMember
```

Handlers live at `server/internal/handlers/members/handler.go` and `service.go`. The `MemberResponse` shape is `{ uid, userUid, email, name, avatarUrl, role, joinedAt, createdAt }` (`service.go:43-69`). Roles are `admin`, `user`, `viewer` (`server/internal/db/models/auth.go:15-20`). Two backend guardrails matter:

- **Cannot remove the last admin** (`service.go:314-324`) → `ErrCannotRemoveLastAdmin`.
- **Cannot demote the last admin** (`service.go:252-256`) → `ErrCannotDemoteLastAdmin`.

The reference implementation is the Invitations page (`web/dash0/src/routes/orgs/$org/organization.invitations.tsx`): same Card → Table → AlertDialog → Sonner toast pattern, hooks colocated in `web/dash0/src/api/hooks.ts` (invitation hooks live around lines 1150-1194).

Today admins onboard people by sending invitations and then have no way to inspect the result, change anyone's role, or remove a stale account without going to the database. This spec closes that loop with a Members tab.

## Honest opinion

Five non-obvious calls — each is a deliberate design choice, not just polish:

1. **Don't surface `POST /members` in the UI.** The backend's AddMember path re-attaches an existing user account by email with no consent flow, no notification email, and `ErrUserNotFound` if the email doesn't match an account (`service.go:174-178`). Surfacing it next to Invitations creates a "which button do I click?" problem and a real foot-gun (silently attaching a stranger's account to your org). Invitations stay the canonical onboarding flow. The empty state copy points users at the Invitations tab. The hook isn't even added.

2. **Inline role editing with a Select, not a per-row dialog.** Three roles, low cost — a shadcn `Select` in the row is the right tool. The "demote last admin" guardrail returns an error from PATCH; on mutation failure, revert the Select value optimistically and surface the backend message via Sonner toast. Dialogs would be overkill for the common case (admin↔user swaps). One exception: demoting *to* `viewer` (loses write access) shows a small confirm dialog because the consequence is qualitatively different.

3. **Lock the current user's own row.** Backend only blocks the *last admin* path, so an admin with peers can demote or remove themselves and lose access mid-session. Disable both controls on the current user's row with a tooltip pointing to profile settings. Self-detection is by email match — `AuthContext` does not expose a `userUid`, only `email` (`web/dash0/src/contexts/AuthContext.tsx:11-18`), and emails are unique per user.

4. **Flag the JWT/session revocation gap as a known limitation.** `RemoveMember` (`service.go:289-327`) is a row delete only. A removed user's JWT remains valid until expiry and can still hit org-scoped endpoints. This is a real security gap, but a separate fix (the right place is auth-side: invalidate active sessions on membership delete). The Members page is shipped with a documented limitation and a follow-up ticket; we don't want to block useful UI on a backend rework, and we don't want to silently ship a "remove" that doesn't actually revoke access.

5. **Skip search and pagination.** Backend list returns the full slice; typical orgs are 10–50 members; the Invitations page has neither and works fine. YAGNI. If we ever scale, add `q` + pagination together when the backend gains them.

Tab order: **Members | Invitations | Settings**. Members is the steady-state view; Invitations is transient.

## Scope

**In:**
- New route `web/dash0/src/routes/orgs/$org/organization.members.tsx` modeled on the invitations page.
- Three new hooks in `web/dash0/src/api/hooks.ts`: `useMembers`, `useUpdateMember`, `useRemoveMember`, plus a `MemberResponse` interface.
- Tab insertion in `web/dash0/src/routes/orgs/$org/organization.tsx` — Members becomes the first tab.
- i18n keys for the new page (column headers, role labels, tooltip, empty state, dialog copy, toast strings) in the existing translation files alongside the invitations keys.

**Out:**
- Direct AddMember UI (kept hidden — see Honest opinion §1).
- Search, filter, sort UI, pagination (see Honest opinion §5).
- JWT/session revocation on remove (separate spec — see Honest opinion §4).
- Audit log of membership changes — the service emits no events for membership today; out of scope.
- Any backend changes.

## Implementation

### 1. Hooks (`web/dash0/src/api/hooks.ts`)

Add alongside the invitation hooks (~line 1150). Mirror their structure exactly:

```ts
export interface MemberResponse {
  uid: string;
  userUid: string;
  email: string;
  name?: string;
  avatarUrl?: string;
  role: "admin" | "user" | "viewer";
  joinedAt?: string;
  createdAt: string;
}

export function useMembers(org: string) {
  return useQuery({
    queryKey: ["members", org],
    queryFn: () =>
      apiFetch<{ data: MemberResponse[] }>(`/api/v1/orgs/${org}/members`),
    enabled: !!org,
  });
}

export function useUpdateMember(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ uid, role }: { uid: string; role: MemberResponse["role"] }) =>
      apiFetch<MemberResponse>(`/api/v1/orgs/${org}/members/${uid}`, {
        method: "PATCH",
        body: JSON.stringify({ role }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", org] }),
  });
}

export function useRemoveMember(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch(`/api/v1/orgs/${org}/members/${uid}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", org] }),
  });
}
```

No `useAddMember`. Intentional — see Honest opinion §1.

### 2. Tabs (`web/dash0/src/routes/orgs/$org/organization.tsx`)

Insert Members as the first tab in the existing tabs array:

```ts
const tabs = [
  { label: t("nav:members"),     path: "/orgs/$org/organization/members" },
  { label: t("nav:invitations"), path: "/orgs/$org/organization/invitations" },
  { label: t("nav:settings"),    path: "/orgs/$org/organization/settings" },
];
```

The existing `useAuth()` admin gate at the layout level (`organization.tsx:21`) already protects the new route — no extra permission code needed.

### 3. Page (`web/dash0/src/routes/orgs/$org/organization.members.tsx`)

Modeled on `organization.invitations.tsx`. Structure:

```
Card
  CardHeader: title + description
  CardContent:
    if isLoading → <Skeleton />s
    if error → Alert with error.message
    if data.length === 0 → empty state (with link to Invitations tab)
    else Table:
      columns: avatar | name | email | role (Select) | joinedAt | remove
AlertDialog (remove confirm, controlled by removeUid state)
AlertDialog (demote-to-viewer confirm, controlled by demoteTarget state)
```

Behavior details:

- **Sort**: client-side, admins first, then by `name || email` ascending. Backend returns insertion order.
- **Avatar**: shadcn `Avatar` with `avatarUrl` if present, initials from `name || email` as fallback.
- **Role Select**: Three options. On change:
  - If the new role is `viewer` and current role isn't, open the demote confirm dialog; only fire mutation on confirm.
  - Otherwise, optimistically update local state, fire `useUpdateMember`. On error, revert and `toast.error(err.message)`. On success, `toast.success(t("members:roleUpdated"))`.
- **Self-row** (where `member.email === user.email`): role Select is `disabled`, remove button is `disabled`, both wrapped in a `Tooltip` with `t("members:cannotEditSelf")`.
- **Remove button**: trash icon; opens AlertDialog with body `t("members:removeConfirm", { email: member.email, org })`. On confirm, fire `useRemoveMember`. Toast on success/error.

### 4. i18n

Add a `members` namespace alongside the existing translations. Required keys:

```
nav:members                      → "Members"
members:title                    → "Members"
members:subtitle                 → "Manage who has access to this organization."
members:column.member            → "Member"
members:column.email             → "Email"
members:column.role              → "Role"
members:column.joinedAt          → "Joined"
members:role.admin               → "Admin"
members:role.user                → "User"
members:role.viewer              → "Viewer"
members:empty                    → "No members yet. Invite someone from the Invitations tab."
members:cannotEditSelf           → "Use your profile settings to change your own membership."
members:removeConfirm.title      → "Remove member"
members:removeConfirm.body       → "Remove {{email}} from {{org}}? They will lose access to this organization."
members:demoteConfirm.title      → "Change role to viewer"
members:demoteConfirm.body       → "{{email}} will lose write access. Continue?"
members:roleUpdated              → "Role updated"
members:memberRemoved            → "Member removed"
```

Keep parity in any other locale files that already carry the invitations strings.

## Critical files

- `server/internal/handlers/members/service.go` — backend behavior to map error messages from. (Last-admin guards: 252, 314.)
- `server/internal/db/models/auth.go:15-20` — `MemberRole` enum.
- `web/dash0/src/routes/orgs/$org/organization.tsx` — tabs array.
- `web/dash0/src/routes/orgs/$org/organization.invitations.tsx` — reference page; copy its structure for the new file.
- `web/dash0/src/api/hooks.ts` — invitation hooks at ~1150 are the template for the three new hooks.
- `web/dash0/src/contexts/AuthContext.tsx:11-18` — `User` shape (no `userUid`; match by email).
- `web/dash0/src/components/shared/tab-nav.tsx` — `TabNav` already handles active-state.
- `web/dash0/src/components/ui/alert-dialog.tsx` — confirm dialog primitive.

## Verification

Run `make dev-test` and exercise the page end-to-end:

1. Log in as `admin@solidping.com` / `solidpass`. Navigate to **Organization → Members**. The table lists at least the admin user.
2. Invite a second user via the Invitations tab and accept the invite in another browser session (or seed a second user). Refresh Members → both rows visible, admins sorted first.
3. On the second user's row, change role from `user` → `admin`. Verify success toast and that the row's Select reflects the new role on refresh.
4. Demote the second admin back to `user`. Verify success.
5. Try to demote yourself: the Select on your own row is disabled with the tooltip.
6. Remove the second user via the trash icon. The confirm dialog includes their email. Confirm → row disappears, success toast.
7. Try to remove the last admin (yourself): button is disabled. (Even if you bypass the disable, the backend returns `ErrCannotRemoveLastAdmin` and the table would not change — verify by toggling the button via DevTools.)
8. Demote a `user` → `viewer`: the demote-confirm dialog appears; cancel leaves role unchanged, confirm applies it.
9. Disconnect the backend mid-mutation (stop the server briefly) → mutation fails → role Select reverts to its previous value, error toast shown with the network error message.
10. Run `make lint` and `make test` — both clean.

Known limitation to document in the PR description (not a verification step): a removed member's existing JWT remains valid until expiry. Track in a separate spec for session/JWT invalidation on membership delete.

## Implementation Plan

1. Extend `web/dash0/src/api/hooks.ts` with `MemberResponse`, `useMembers`, `useUpdateMember`, `useRemoveMember` (no `useAddMember`). Place next to the invitation hooks.
2. Extend the four locale files (`en/`, `fr/`, `de/`, `es/`) — add `nav:members` and the new `members.*` keys to `org.json`. Re-export them in `i18n.ts` if needed (the org namespace is already wired).
3. Insert the Members tab as the first tab in `organization.tsx`.
4. Create `organization.members.tsx` — Card + Table + AlertDialogs + Tooltip; inline role Select with optimistic update; disable both controls on the current user's row by email match; demote-to-viewer confirmation gate.
5. Lint + build the dash0 bundle to confirm types.
6. `make build-backend build-dash0 lint-back test` clean.
