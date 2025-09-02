# Check Management API Enhancements

## 1. PUT API for Upsert Operations

Add a `PUT /api/v1/orgs/{org}/checks/{slug}` endpoint to enable idempotent check creation/updates.

### Behavior
- If a check with the given slug doesn't exist: **create** it
- If a check with the given slug exists: **update** it
- This allows declarative infrastructure-as-code patterns for check management

### Example Usage

This enables bulk check provisioning via scripts:

```bash
for env in dev prod; do
  if [ "$env" = "dev" ]; then
    base_url="http://www.mywebsite-dev.com"
  elif [ "$env" = "prod" ]; then
    base_url="http://www.mywebsite-prod.com"
  fi

  for app in app1 app2 app3; do
    curl -X PUT "https://solidping.com/api/v1/orgs/example/checks/http-${env}-${app}" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer $API_TOKEN" \
      -d "{\"type\": \"http\", \"url\": \"${base_url}/${app}\", \"name\": \"${app} (${env})\", \"interval\": 60}"
  done
done
```

**Note**: Using double quotes for the JSON payload allows proper shell variable interpolation.

## 2. Labels Support

Add labeling functionality to organize and filter checks using key-value pairs.

### API Representation
- **Request/Response**: Dictionary/object of key-value pairs (e.g., `{"env": "production", "priority": "critical", "type": "web"}`)
- **Example**: `"labels": {"env": "prod", "app": "app1", "protocol": "http"}`
- **Keys**: Lowercase alphanumeric with hyphens, 1-50 characters (pattern: `^[a-z0-9-]{1,50}$`)
- **Values**: Any string, max 200 characters
- **Constraint**: Each key can only have ONE value per check (standard dictionary behavior)

### Filtering by Labels
- **Query Parameter**: `labels` - Filter checks by label key-value pairs
- **Format**: `key:value` pairs separated by commas
- **Example**: `GET /api/v1/orgs/{org}/checks?labels=env:prod,app:app1`
- **Behavior**: Returns checks that have ALL specified labels (AND logic)
- Multiple label filters can be combined: `?labels=env:prod,priority:critical,type:http`

### Database Design
- Store labels with their keys and values as separate entities with proper normalization
- Many-to-many relationship: `checks` ↔ `check_labels` ↔ `labels`
- This enables efficient label-based queries (by key, by value, or by key-value pair)

### SQL Schema

```sql
-- Labels table: stores unique label key-value pairs per organization
create table labels (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  key               text not null check (key ~ '^[a-z0-9-]{1,50}$'),
  value             text not null check (length(value) <= 200),
  created_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index labels_org_key_value_idx on labels (organization_uid, key, value) where deleted_at is null;
create index labels_org_key_idx on labels (organization_uid, key) where deleted_at is null;

comment on table labels is 'Labels (key-value pairs) for organizing and filtering checks.';
comment on column labels.key is 'Label key: lowercase alphanumeric with hyphens, 1-50 characters.';
comment on column labels.value is 'Label value: any string, max 200 characters.';

-- Check-labels junction table: many-to-many relationship
create table check_labels (
  uid               uuid primary key default gen_random_uuid(),
  check_uid         uuid not null references checks(uid) on delete cascade,
  label_uid         uuid not null references labels(uid) on delete cascade,
  created_at        timestamptz not null default now()
);

create unique index check_labels_check_label_idx on check_labels (check_uid, label_uid);
create index check_labels_label_idx on check_labels (label_uid);

comment on table check_labels is 'Junction table linking checks to labels (many-to-many).';
```

## 3. Description Field

Add an optional `description` field to checks for documentation purposes.

### API
- Field: `description` (string, optional)
- Example: `"description": "Monitors the main application health endpoint"`
- Use case: Provide context about what the check monitors and why it exists

### SQL Schema

```sql
-- Add description column to checks table
alter table checks add column description text;

comment on column checks.description is 'Optional documentation describing what this check monitors and why it exists.';
```
