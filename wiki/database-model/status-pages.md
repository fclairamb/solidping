# Status Page Tables

Public-facing service-health pages, their layout, their subscribers, and the
human-written updates published on them. See [README.md](README.md) for the full
index.

### status_pages
Public-facing status pages displaying service health to end users.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| name | text | Page title displayed to visitors |
| slug | text | URL-friendly identifier, unique per organization |
| description | text | Subtitle or description shown on the page |
| visibility | text | Access control: public or private |
| is_default | boolean | At most one default page per org |
| enabled | boolean | Whether the page is accessible |
| show_availability | boolean | Whether to display uptime percentage |
| show_response_time | boolean | Whether to display response time charts |
| history_period | text | History window rendered: 24h (hourly), 7d, 30d, 90d. Source of truth for bucketing |
| history_days | integer | Legacy day count (default 90), kept for backward compatibility |
| language | varchar(10) | ISO language code (e.g., en, fr). NULL uses system default |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**: unique on (organization_uid, slug) where not deleted; unique on (organization_uid) where is_default and not deleted

---

### status_page_sections
Grouping sections within a status page.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| status_page_uid | uuid | FK to status_pages |
| name | text | Section heading displayed on the page |
| slug | text | URL-friendly identifier, unique per status page |
| position | integer | Display order (lower = higher on page) |

**Foreign Keys**: `status_page_uid` → status_pages(uid)

---

### status_page_resources
Checks displayed within a status page section.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| section_uid | uuid | FK to status_page_sections |
| check_uid | uuid | FK to checks |
| public_name | text | Override display name (NULL uses check name) |
| explanation | text | Optional description visible on the public page |
| position | integer | Display order within section (lower = higher) |

**Foreign Keys**:
- `section_uid` → status_page_sections(uid)
- `check_uid` → checks(uid)

**Indexes**: unique on (section_uid, check_uid)

---

### status_page_subscriber
Email subscribers to a status page, or to one specific incident on it.
Double opt-in via `confirm_token`; one-click opt-out via `unsubscribe_token`.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| status_page_uid | uuid | FK to status_pages |
| email | text | Subscriber address |
| confirmed_at | timestamptz | When the subscription was confirmed (NULL = pending) |
| confirm_token | text | Single-use confirmation token (globally unique) |
| unsubscribe_token | text | Unsubscribe token (globally unique) |
| scope | text | page (all updates) or incident (one incident only) |
| incident_uid | uuid | FK to incidents; set when scope = incident |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `status_page_uid` → status_pages(uid)
- `incident_uid` → incidents(uid)

**Indexes**: unique on confirm_token; unique on unsubscribe_token; unique on
(status_page_uid, email, scope, coalesce(incident_uid, '000…0')) where not
deleted — the `coalesce` keeps page-scope rows properly unique.

---

### status_updates
Human-written updates published on a status page — the incident narrative
visitors read.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| status_page_uid | uuid | FK to status_pages |
| section_uid | uuid | FK to status_page_sections (optional scope) |
| check_uid | uuid | FK to checks (optional scope) |
| incident_uid | uuid | FK to incidents (optional link) |
| title | text | Update headline |
| body_markdown | text | Update body, Markdown |
| link_url | text | Optional "read more" link |
| kind | text | investigating, identified, monitoring, resolved, maintenance, info |
| published_at | timestamptz | Publication time (default now) |
| author_uid | uuid | FK to users (author) |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `status_page_uid` → status_pages(uid)
- `section_uid` → status_page_sections(uid)
- `check_uid` → checks(uid)
- `incident_uid` → incidents(uid)
- `author_uid` → users(uid)

**Indexes**: (organization_uid, status_page_uid, published_at desc) where not deleted
