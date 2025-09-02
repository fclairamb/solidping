# Dash0 i18n Completion

## Context

The dash0 app has i18n support via `i18next` + `react-i18next` with English (en) and French (fr) locales. Currently only a small subset of UI strings are translated (sidebar navigation, error views, theme toggle). The vast majority of pages still have hardcoded English strings.

### Current state

**Translated:**
- Sidebar navigation labels (`nav` namespace)
- Error views (permission denied, not found, connection error)
- Theme toggle, user menu labels (`common` namespace)

**Translation files:** `apps/dash0/src/locales/{en,fr}/{common,nav}.json`

## Pages to translate

### Authentication
| File | Key hardcoded strings |
|------|----------------------|
| `routes/orgs/$org/login.tsx` | "Sign in", "Email", "Password", "Signing in...", "Your session has expired", "Select an organization", "Use another account", "Don't have an account?", "Create one", "or" |
| `routes/orgs/$org/register.tsx` | "Create an account", "Sign up for SolidPing", "Check your email", "We've sent a confirmation link...", "Back to login", validation messages |
| `routes/confirm-registration.$token.tsx` | Confirmation messages |
| `routes/invite.$token.tsx` | Invitation acceptance messages |
| `routes/no-org.tsx` | No organization messages |

### Core features
| File | Key hardcoded strings |
|------|----------------------|
| `routes/orgs/$org/checks.index.tsx` | "Checks", "Manage your monitoring checks", "New Group", "New Check", "Search checks...", table headers (Name, Type, Target, Status, Response), context menu items, delete/rename/move dialogs |
| `routes/orgs/$org/checks.new.tsx` | New check form labels |
| `routes/orgs/$org/checks.$checkUid.index.tsx` | Check detail view labels |
| `routes/orgs/$org/checks.$checkUid.edit.tsx` | Edit check form labels |
| `routes/orgs/$org/incidents.index.tsx` | "Incidents", "Track and manage monitoring incidents", filter labels (All/Active/Resolved) |
| `routes/orgs/$org/incidents.$incidentUid.tsx` | Incident detail labels |
| `routes/orgs/$org/events.tsx` | "Events", "Audit log of system activities", event type labels |
| `routes/orgs/$org/index.tsx` | Dashboard page labels |

### Status pages
| File | Key hardcoded strings |
|------|----------------------|
| `routes/orgs/$org/status-pages.index.tsx` | "View Details", "Edit", "Enabled"/"Disabled" |
| `routes/orgs/$org/status-pages.new.tsx` | New status page form |
| `routes/orgs/$org/status-pages.$statusPageUid.index.tsx` | Status page detail |
| `routes/orgs/$org/status-pages.$statusPageUid.edit.tsx` | Edit status page form |

### Account & organization
| File | Key hardcoded strings |
|------|----------------------|
| `routes/orgs/$org/account.profile.tsx` | "Profile", "Update your display name...", "Name", "Save", "Saving...", "Profile saved." |
| `routes/orgs/$org/account.tokens.tsx` | "Unnamed", "Never", "Expired", dialog strings |
| `routes/orgs/$org/organization.invitations.tsx` | Invitation management strings |
| `routes/orgs/$org/organization.settings.tsx` | "Auto-join", "Settings saved.", "Email pattern (regex)" |

### Server settings
| File | Key hardcoded strings |
|------|----------------------|
| `routes/orgs/$org/server.tsx` | "Server Settings", "Manage server-wide configuration parameters" |
| `routes/orgs/$org/server.web.tsx` | Web settings form labels |
| `routes/orgs/$org/server.mail.tsx` | Mail settings form labels |
| `routes/orgs/$org/server.auth.tsx` | Auth settings form labels |
| `routes/orgs/$org/server.performance.tsx` | Performance settings form labels |

### Other
| File | Key hardcoded strings |
|------|----------------------|
| `routes/orgs/$org/badges.tsx` | "Status", "Availability", badge style/period labels, "Copied" |
| `routes/orgs/$org/test.*.tsx` | Test tools labels |

### Shared components
| File | Key hardcoded strings |
|------|----------------------|
| `components/shared/check-form.tsx` | Check type labels/descriptions ("HTTP", "Monitor HTTP/HTTPS endpoints"...), interval options ("10 seconds", "1 minute"...), period units, form field labels |

## Implementation approach

### 1. Add new translation namespaces

Organize translations by domain to keep files manageable:

| Namespace | Scope |
|-----------|-------|
| `common` | Shared UI: buttons, dialogs, validation, status labels |
| `nav` | Navigation (already exists) |
| `auth` | Login, register, invitation, confirmation |
| `checks` | Checks list, detail, form |
| `incidents` | Incidents list and detail |
| `events` | Events page |
| `statusPages` | Status pages management |
| `account` | Profile, tokens |
| `org` | Organization settings, invitations |
| `server` | Server settings (web, mail, auth, performance) |
| `badges` | Badges page |

### 2. Per-page process

For each page:
1. Extract all hardcoded strings into the appropriate namespace JSON files (both `en/` and `fr/`)
2. Import `useTranslation` with the relevant namespace
3. Replace hardcoded strings with `t('key')` calls
4. Use interpolation for dynamic values: `t('greeting', { name })`

### 3. Translation key conventions

- Use dot notation for grouping: `checks.table.name`, `checks.dialog.deleteTitle`
- Use camelCase for leaf keys: `searchPlaceholder`, `noResults`
- Reuse `common` namespace keys for shared strings: "Cancel", "Delete", "Save", "Loading..."

### 4. Common strings to add to `common` namespace

These appear across many pages and should go in `common.json`:
- `save`, `saving`, `cancel`, `delete`, `edit`, `create`, `search`
- `enabled`, `disabled`
- `confirmDelete`, `actionCannotBeUndone`
- `unexpectedError`
- `loading`, `noResults`
- `name`, `email`, `password`
- `copied`

### 5. Suggested implementation order

1. **Common namespace expansion** — add shared keys used everywhere
2. **Auth pages** (login, register) — first thing users see
3. **Checks** (list, detail, form) — core feature, most strings
4. **Incidents & events** — core features
5. **Account & organization** — settings pages
6. **Status pages management** — CRUD pages
7. **Server settings** — admin pages
8. **Badges & test tools** — less critical pages

### 6. i18n config update

Register new namespaces in `apps/dash0/src/i18n.ts`:

```typescript
ns: ['common', 'nav', 'auth', 'checks', 'incidents', 'events', 'statusPages', 'account', 'org', 'server', 'badges'],
```

## Estimated scope

- ~30 route/component files to update
- ~300+ strings to extract and translate
- 10 new translation files per language (20 total)

## Implementation Plan

### Step 1: Common namespace expansion + i18n config update
### Step 2: Auth pages (login, register)
### Step 3: Checks pages (list, detail, form, new, edit)
### Step 4: Incidents & Events pages
### Step 5: Account & Organization pages
### Step 6: Status pages management
### Step 7: Server settings
### Step 8: Badges & other pages
