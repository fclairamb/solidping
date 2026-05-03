# Members page integration (invite button, breadcrumb, command palette)

## Context

Spec `2026-05-03-29-members-management-page.md` defines the new Members page. While that spec was correct in its own scope, it leaves three gaps that make the page feel like a second-class part of the app:

1. **Invitations live one click away.** The previous spec's empty state points users at the Invitations tab. Forcing a tab switch to perform the most common action — inviting someone — is the kind of friction nobody asks about but everyone resents.
2. **The page is invisible to the command palette.** dash0 has a `⌘K` palette at `web/dash0/src/components/CommandMenu.tsx` with all the other org pages registered (`pages` array, lines 28-38). Members would be the only org page missing.
3. **The breadcrumb stops at "Organization".** dash0 has a custom breadcrumb in `web/dash0/src/routes/orgs/$org.tsx:70-259` that shows `Organization > Invitations` and `Organization > Settings`. Without an entry for Members, the trail collapses to a bare "Organization" on the new page.

All three are small, mechanical additions to existing systems — no new infrastructure required.

## Honest opinion

These three additions are obviously correct — there is nothing to push back on. The only design choice worth being deliberate about is **what to do with the empty state and where to put the "+ Invite" button**:

- **Move the action to where the user is looking.** The previous spec's empty state ("Invite someone from the Invitations tab") is now wrong. Replace it with `"No members yet — invite your first teammate."` and rely on the same `+ Invite` button at the page header. The Invitations tab continues to exist for revoking pending invites, but it stops being the place you go to *send* one.
- **Extract the invitation dialog before reusing it.** The dialog is currently inlined at `web/dash0/src/routes/orgs/$org/organization.invitations.tsx:119-227` (form, success-with-copyable-URL, error states — all self-contained). Lift it to `web/dash0/src/components/shared/create-invitation-dialog.tsx` with props `{ org, trigger?, onCreated? }`. The Invitations page then re-imports its own dialog. Behavior identical, ~30 mins of refactor, and we don't end up with two copies of the form drifting apart.
- **Open question on sequencing — and a recommendation.** This spec strictly depends on `2026-05-03-29` (the page must exist before the Invite button can sit on it). Two clean paths:
  1. **Fold this into 2026-05-03-29 before either is implemented.** This is what I'd actually do. The two specs are one coherent piece of work and will ship in one PR; splitting them adds rebase pain and the risk that whoever ships 29 inlines the invite dialog a second time on Members instead of extracting it. *If 29 hasn't been started yet, ask the user whether to merge specs.*
  2. **Sequence 29 → 30 as written.** Acceptable if 29 is already mid-flight or implemented. The cost is one extra refactor (extract the dialog) and one trivial copy change (empty state).

Beyond that, three things deliberately out of scope: don't redesign the palette (it's a flat, hardcoded array — fine for the size of the app), don't generalize the breadcrumb (the existing switch-style logic is clear enough), don't add a "+ Invite" affordance anywhere besides the Members page header (every dropdown site is a foot-gun in disguise).

## Scope

**In:**
- Extract the invitation creation dialog from `organization.invitations.tsx:119-227` into `web/dash0/src/components/shared/create-invitation-dialog.tsx`. Preserve every existing behavior: form → success view with copyable URL → close.
- Re-use the extracted dialog from `organization.invitations.tsx` (no behavior change).
- Place a `+ Invite` button at the top-right of the new Members page (`organization.members.tsx`), wiring it to the same dialog.
- Update the Members page empty state copy: drop the "go to Invitations" pointer.
- Add a `Members` entry to `CommandMenu.tsx` in the `organization` group, sorted first within the group.
- Add a `Members` case to the breadcrumb logic in `routes/orgs/$org.tsx`.

**Out:**
- Anything beyond palette / breadcrumb / invite-button.
- Reworking the dialog UI itself (only extraction, no redesign).
- A reusable breadcrumb library (the existing inline component is still fine).
- A command-palette registry pattern (the hardcoded array is still fine at this app size).
- Putting "+ Invite" on any other surface (tab nav, sidebar, dashboard).

## Implementation

### 1. Extract the invitation dialog

Create `web/dash0/src/components/shared/create-invitation-dialog.tsx`. Move every line of the dialog block from `organization.invitations.tsx:119-227` plus its associated state (`dialogOpen`, `email`, `role`, `inviteUrl`, `error`, `copied`) and helpers (`handleCreate`, `handleCopy`, `handleDialogClose`). Use the existing `useCreateInvitation(org)` hook from `web/dash0/src/api/hooks.ts`.

Component shape:

```ts
interface CreateInvitationDialogProps {
  org: string;
  trigger?: React.ReactNode;       // optional custom trigger; default is the "+ Invite" button
  onCreated?: (inviteUrl: string) => void; // optional hook for callers
}
```

Default trigger when `trigger` is not provided:

```tsx
<Button>
  <Plus className="mr-2 h-4 w-4" />
  {t("invitations.invite", { ns: "org" })}
</Button>
```

Rewire `organization.invitations.tsx` to render `<CreateInvitationDialog org={org} />` in the same layout slot. Drop the now-unused state, imports, and helpers from that file. No translation changes — keys stay where they are.

### 2. "+ Invite" button on Members page

In `web/dash0/src/routes/orgs/$org/organization.members.tsx`, add to the page header:

```tsx
<div className="flex items-center justify-end">
  <CreateInvitationDialog org={org} />
</div>
```

Update the empty state copy (defined in spec 29's i18n section):

```diff
- members:empty → "No members yet. Invite someone from the Invitations tab."
+ members:empty → "No members yet — invite your first teammate."
```

(The `nav:members` and other `members:*` keys already exist in `web/dash0/src/locales/{en,fr}/{nav,org}.json` per the previous spec; only this one string changes.)

### 3. Command palette entry

In `web/dash0/src/components/CommandMenu.tsx`:

```diff
  import {
    LayoutDashboard,
    ListChecks,
    AlertTriangle,
    Calendar,
    Globe,
    Activity,
    User2,
    KeyRound,
    Mail,
+   Users,
    Settings,
  } from "lucide-react";

  const pages: PageEntry[] = [
    { titleKey: "dashboard", path: "/orgs/$org", icon: LayoutDashboard, group: "pages" },
    // ... unchanged ...
    { titleKey: "tokens", path: "/orgs/$org/account/tokens", icon: KeyRound, group: "account" },
+   { titleKey: "members", path: "/orgs/$org/organization/members", icon: Users, group: "organization" },
    { titleKey: "invitations", path: "/orgs/$org/organization/invitations", icon: Mail, group: "organization" },
    { titleKey: "settings", path: "/orgs/$org/organization/settings", icon: Settings, group: "organization" },
  ];
```

Sort within the `organization` group matches the tab order from spec 29 (Members first).

### 4. Breadcrumb entry

In `web/dash0/src/routes/orgs/$org.tsx`, the organization branch (~lines 183-203):

```diff
  const isOrganization = matches.some((m) => m.routeId.startsWith("/orgs/$org/organization"));
  if (isOrganization) {
+   const isMembers = routeIds.has("/orgs/$org/organization/members");
    const isInvitations = routeIds.has("/orgs/$org/organization/invitations");
    const isSettings = routeIds.has("/orgs/$org/organization/settings");
-   const subLabel = isInvitations ? t("invitations") : isSettings ? t("settings") : null;
+   const subLabel = isMembers
+     ? t("members")
+     : isInvitations
+     ? t("invitations")
+     : isSettings
+     ? t("settings")
+     : null;
```

The link target inside the branch (currently hardcoded to invitations) should still resolve correctly because the parent label ("Organization") links to a real page; verify with the verification step below. If it points at invitations specifically, leave it — the breadcrumb pattern is "click the parent to go to the section root", and Invitations is a reasonable root.

## Sequencing & dependencies

- **Hard dependency**: spec `2026-05-03-29-members-management-page.md` must be implemented (or implemented in the same PR). The Members page must exist before the Invite button can sit on it, the breadcrumb case has anything to match, and the palette entry has a route to navigate to.
- **Refactor sequencing**: extract the dialog first (step 1), then add it to Members (step 2), then palette + breadcrumb (steps 3 + 4). The dialog extraction should be its own commit so it can be reverted independently if it surfaces a regression.
- **Recommendation, repeating from Honest opinion**: if spec 29 hasn't been started yet, fold this spec into it. Two specs is a process cost when the work is one coherent change.

## Critical files

- `web/dash0/src/routes/orgs/$org/organization.invitations.tsx` — source of the dialog being extracted (lines 119-227).
- `web/dash0/src/components/shared/create-invitation-dialog.tsx` — **new file**.
- `web/dash0/src/routes/orgs/$org/organization.members.tsx` — created by spec 29; this spec adds the header button and updates empty-state copy.
- `web/dash0/src/components/CommandMenu.tsx` — `pages` array + Lucide imports.
- `web/dash0/src/routes/orgs/$org.tsx` — breadcrumb branch around lines 183-203.
- `web/dash0/src/api/hooks.ts` — `useCreateInvitation` (no change, just consumed by the extracted component).
- `web/dash0/src/locales/{en,fr}/org.json` — update the `members.empty` string.

## Verification

Run `make dev-test`, log in as `admin@solidping.com` / `solidpass`, and exercise:

1. **Palette.** Press `⌘K` (or `Ctrl+K`). Type "mem". The Members entry appears under the **Organization** group with a `Users` icon. Pressing it navigates to `/orgs/default/organization/members`.
2. **Breadcrumb.** On `/orgs/default/organization/members`, the breadcrumb reads `… > Organization > Members`. Clicking "Organization" navigates to the section root (Invitations). Switching to the Invitations tab updates the trail to `Organization > Invitations`. Switching to Settings updates to `Organization > Settings`. The current segment is rendered as text, not a link, on each page.
3. **Invite button.** On the Members page, the `+ Invite` button is at the top-right. Click → dialog opens. Submit a valid email + role → success view shows a copyable invite URL. Click the copy button → clipboard contains the URL. Close → toast not required (invitations page didn't have one). Switch to the Invitations tab → the new pending invite is in the list.
4. **Equivalence with the Invitations page.** Open the same `+ Invite` flow from the Invitations tab. Same dialog, same form fields, same error display, same success view. (Sanity-check that the extraction didn't drop any behavior.)
5. **Empty state.** If an org has zero members visible to the table (this is essentially a synthetic case — the viewing admin is always present — but seed it via `SP_DB_RESET=true` and a test fixture if needed): the empty state reads "No members yet — invite your first teammate." with no pointer to the Invitations tab.
6. **Lints + tests.** `make lint` and `make test` clean.
