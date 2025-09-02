Introduce a `jobs` table to the current migration (do not create a new one).


### Table
```sql
create table jobs (
    uid uuid unique primary key default uuid_generate_v4(),
    organization_uid int references organizations(uid),
    type text not null constraint type_format check (type ~ '^[a-z0-9-]{3,}$'),
    config jsonb,
    nb_tries int,
    scheduled_at timestamptz default now() constraint scheduled_at_requires_deleted check (scheduled_at is not null or deleted_at is not null),
    final_status text constraint final_status_format check (final_status ~ '^success|failed|cancelled$'),
    output jsonb,
    previous_job_uid uuid,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    deleted_at timestamptz
);

create index idx_jobs_scheduled_at on jobs (scheduled_at) where deleted_at is null;
create index idx_jobs_type_config on jobs (organization_uid, type, config) where deleted_at is null;
create index idx_jobs_deleted_at on jobs(deleted_at) where deleted_at is not null;
```
