-- solidping v0.1.0 — consolidated baseline schema
-- Replaces incremental migrations 001–036 with the net final schema.
-- Do NOT run this file on a database that already has the incremental migrations applied.

create table organizations (
  uid          uuid primary key default gen_random_uuid(),
  slug         text not null check (slug ~ '^[a-z0-9-]{3,20}$'),
  name         text,
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now(),
  deleted_at   timestamptz
);

create unique index organizations_slug_idx on organizations (slug) where deleted_at is null;

comment on table organizations is 'Top-level tenant container. All monitoring resources are scoped to an organization.';
comment on column organizations.slug is 'URL-friendly unique identifier (3-20 chars, lowercase alphanumeric and hyphens).';
comment on column organizations.name is 'Human-readable display name.';

create table parameters (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid references organizations(uid) on delete cascade,
  key              text not null check (key ~ '^[a-z0-9_\.]+$'),
  value            jsonb not null,
  secret           boolean,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create unique index parameters_org_key_idx on parameters (organization_uid, key)
  where deleted_at is null and organization_uid is not null;
create unique index parameters_system_key_idx on parameters (key)
  where deleted_at is null and organization_uid is null;

comment on table parameters is 'Key-value configuration store. When organization_uid is NULL, the parameter is system-wide.';
comment on column parameters.organization_uid is 'Owning organization. NULL for system-wide parameters.';
comment on column parameters.key is 'Dot-separated configuration key (e.g., smtp.host, slack.default_channel).';
comment on column parameters.value is 'Configuration value as JSON.';
comment on column parameters.secret is 'Whether this value is sensitive and should be masked in API responses.';

create table users (
  uid                  uuid primary key default gen_random_uuid(),
  email                text not null,
  name                 text,
  avatar_url           text,
  password_hash        text,
  email_verified_at    timestamptz,
  super_admin          boolean not null default false,
  last_active_at       timestamptz,
  totp_secret          text,
  totp_enabled         boolean not null default false,
  totp_recovery_codes  jsonb,
  created_at           timestamptz not null default now(),
  updated_at           timestamptz not null default now(),
  deleted_at           timestamptz
);

create unique index users_email_idx on users (lower(email)) where deleted_at is null;

comment on table users is 'Global user accounts. One account per email across all organizations.';
comment on column users.email is 'Globally unique email address (case-insensitive).';
comment on column users.name is 'Display name.';
comment on column users.avatar_url is 'URL to user profile picture.';
comment on column users.password_hash is 'Argon2id hash. NULL for SSO-only users.';
comment on column users.email_verified_at is 'When the email was verified. NULL if not yet verified.';
comment on column users.super_admin is 'Super admins can access and manage all organizations.';
comment on column users.last_active_at is 'Timestamp of last API or UI activity.';
comment on column users.totp_secret is 'Base32-encoded TOTP secret for 2FA. NULL if 2FA not configured.';
comment on column users.totp_enabled is 'Whether TOTP two-factor authentication is active.';
comment on column users.totp_recovery_codes is 'JSON array of hashed one-time recovery codes for 2FA bypass.';

create table organization_members (
  uid              uuid primary key default gen_random_uuid(),
  user_uid         uuid not null references users(uid) on delete cascade,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  role             text not null check (role in ('admin', 'user', 'viewer')),
  invited_by_uid   uuid references users(uid) on delete set null,
  invited_at       timestamptz,
  joined_at        timestamptz,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create unique index organization_members_user_org_idx on organization_members (user_uid, organization_uid) where deleted_at is null;
create index organization_members_org_idx on organization_members (organization_uid) where deleted_at is null;
create index organization_members_user_idx on organization_members (user_uid) where deleted_at is null;

comment on table organization_members is 'Junction table linking users to organizations with role-based access.';
comment on column organization_members.user_uid is 'Member user.';
comment on column organization_members.organization_uid is 'Organization the user belongs to.';
comment on column organization_members.role is 'Role: admin (full access), user (read/write), viewer (read-only).';
comment on column organization_members.invited_by_uid is 'User who sent the invitation. NULL for founders or migrated users.';
comment on column organization_members.invited_at is 'When the invitation was sent. NULL for immediate additions.';
comment on column organization_members.joined_at is 'When the user accepted the invitation. NULL means pending.';

create table organization_providers (
  uid                  uuid primary key default gen_random_uuid(),
  organization_uid     uuid not null references organizations(uid) on delete cascade,
  provider_type        text not null check (provider_type in ('slack', 'google', 'github', 'gitlab', 'microsoft', 'discord', 'saml', 'oidc')),
  provider_id          text not null,
  provider_name        text,
  metadata             jsonb,
  created_at           timestamptz not null default now(),
  updated_at           timestamptz not null default now(),
  deleted_at           timestamptz,
  metadata_private     text,
  metadata_private_keys text
);

create unique index idx_org_providers_type_id on organization_providers (provider_type, provider_id) where deleted_at is null;
create index idx_org_providers_org on organization_providers (organization_uid) where deleted_at is null;

comment on table organization_providers is 'Maps organizations to external identity providers. One provider identity belongs to exactly one org.';
comment on column organization_providers.organization_uid is 'Owning organization.';
comment on column organization_providers.provider_type is 'External provider type: slack, google, github, gitlab, microsoft, discord, saml, oidc.';
comment on column organization_providers.provider_id is 'Unique identifier from the provider (e.g., Slack Team ID T0123456789).';
comment on column organization_providers.provider_name is 'Human-readable provider name (e.g., "Acme Corp Slack Workspace").';
comment on column organization_providers.metadata is 'Provider-specific metadata as JSON.';

create table user_tokens (
  uid              uuid primary key default gen_random_uuid(),
  user_uid         uuid not null references users(uid) on delete cascade,
  organization_uid uuid references organizations(uid) on delete cascade,
  token            text not null,
  type             text not null check (type in ('pat', 'refresh')),
  properties       jsonb,
  expires_at       timestamptz,
  last_active_at   timestamptz,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create unique index user_tokens_token_idx on user_tokens (token) where deleted_at is null;
create index user_tokens_user_uid_idx on user_tokens (user_uid) where deleted_at is null;
create index user_tokens_expires_at_idx on user_tokens (expires_at) where deleted_at is null and expires_at is not null;

comment on table user_tokens is 'Authentication tokens: Personal Access Tokens (PAT) and JWT refresh tokens.';
comment on column user_tokens.user_uid is 'Token owner.';
comment on column user_tokens.organization_uid is 'Organization scope for PAT tokens. NULL for global refresh tokens.';
comment on column user_tokens.token is 'Hashed token value.';
comment on column user_tokens.type is 'Token type: pat (Personal Access Token) or refresh (JWT refresh token).';
comment on column user_tokens.properties is 'Token metadata (e.g., name, scopes, IP restrictions).';
comment on column user_tokens.expires_at is 'Expiration timestamp. NULL means never expires.';
comment on column user_tokens.last_active_at is 'Last time this token was used for authentication.';

create table user_providers (
  uid           uuid primary key default gen_random_uuid(),
  user_uid      uuid not null references users(uid) on delete cascade,
  provider_type text not null check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'discord', 'saml', 'oidc')),
  provider_id   text not null,
  metadata      jsonb,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now()
);

create unique index user_providers_provider_idx on user_providers (provider_type, provider_id);
create index user_providers_user_idx on user_providers (user_uid);

comment on table user_providers is 'Links user accounts to external OAuth/SAML/OIDC providers.';
comment on column user_providers.user_uid is 'User this provider identity belongs to.';
comment on column user_providers.provider_type is 'External provider: google, github, gitlab, microsoft, twitter, slack, saml, oidc.';
comment on column user_providers.provider_id is 'Unique identifier from the provider (e.g., OAuth sub claim).';
comment on column user_providers.metadata is 'Provider-specific data (profile info, tokens, etc.).';

create table workers (
  uid            uuid primary key default gen_random_uuid(),
  slug           text not null check (slug ~ '^[a-z][a-z0-9-]{2,20}$'),
  name           text not null,
  region         text check (region ~ '^[a-z][a-z0-9-]{3,20}$'),
  token          text,
  last_active_at timestamptz,
  created_at     timestamptz not null default now(),
  updated_at     timestamptz not null default now(),
  deleted_at     timestamptz
);

create unique index workers_slug_idx on workers (slug) where deleted_at is null;
create index idx_workers_token on workers (token) where token is not null and deleted_at is null;

comment on table workers is 'Distributed check executors. At least one per region.';
comment on column workers.slug is 'Unique system identifier (e.g., hostname, container ID).';
comment on column workers.name is 'Human-readable worker name.';
comment on column workers.region is 'Region identifier (e.g., eu-west-1, us-east-1). Determines which checks this worker executes.';
comment on column workers.token is 'Authentication token for edge worker registration. NULL for manually registered workers.';
comment on column workers.last_active_at is 'Last heartbeat timestamp.';

create table severities (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  slug             text not null,
  name             text not null,
  description      text,
  channels         jsonb not null default '[]',
  is_default       boolean not null default false,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create unique index severities_org_slug_alive_idx on severities (organization_uid, slug) where deleted_at is null;
create unique index severities_org_default_alive_idx on severities (organization_uid) where is_default and deleted_at is null;

create table escalation_policies (
  uid                  uuid primary key,
  organization_uid     uuid not null references organizations(uid) on delete cascade,
  slug                 text not null,
  name                 text not null,
  description          text,
  repeat_max           integer not null default 0,
  created_at           timestamptz not null default now(),
  updated_at           timestamptz not null default now(),
  deleted_at           timestamptz,
  repeat_after_seconds integer,
  unique (organization_uid, slug)
);

create index idx_escalation_policies_org on escalation_policies (organization_uid) where deleted_at is null;

create table escalation_policy_steps (
  uid           uuid primary key,
  policy_uid    uuid not null references escalation_policies(uid) on delete cascade,
  position      integer not null,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now(),
  severity_uid  uuid references severities(uid) on delete set null,
  delay_seconds integer not null default 0,
  unique (policy_uid, position)
);

create table escalation_policy_targets (
  uid         uuid primary key,
  step_uid    uuid not null references escalation_policy_steps(uid) on delete cascade,
  target_type text not null check (target_type in ('user', 'schedule', 'connection', 'all_admins')),
  target_uid  uuid,
  position    integer not null default 0
);

create index idx_escalation_targets_step on escalation_policy_targets (step_uid);

create table on_call_schedules (
  uid              uuid primary key,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  slug             text not null,
  name             text not null,
  description      text,
  timezone         text not null,
  rotation_type    text not null check (rotation_type in ('daily', 'weekly')),
  handoff_time     text not null,
  handoff_weekday  integer,
  start_at         timestamptz not null,
  ical_secret      text,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz,
  unique (organization_uid, slug)
);

create index idx_on_call_schedules_org on on_call_schedules (organization_uid) where deleted_at is null;

create table on_call_schedule_users (
  uid          uuid primary key,
  schedule_uid uuid not null references on_call_schedules(uid) on delete cascade,
  user_uid     uuid not null references users(uid) on delete cascade,
  position     integer not null,
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now(),
  unique (schedule_uid, position),
  unique (schedule_uid, user_uid)
);

create table on_call_schedule_overrides (
  uid             uuid primary key,
  schedule_uid    uuid not null references on_call_schedules(uid) on delete cascade,
  user_uid        uuid not null references users(uid) on delete cascade,
  start_at        timestamptz not null,
  end_at          timestamptz not null,
  reason          text,
  created_by_uid  uuid references users(uid) on delete set null,
  created_at      timestamptz not null default now()
);

create index idx_on_call_overrides_lookup on on_call_schedule_overrides (schedule_uid, start_at, end_at);

create table check_groups (
  uid                   uuid primary key default gen_random_uuid(),
  organization_uid      uuid not null references organizations(uid) on delete cascade,
  name                  text not null,
  slug                  text not null check (slug ~ '^[a-z][a-z0-9-]{2,39}$'),
  description           text,
  sort_order            smallint not null default 0,
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  deleted_at            timestamptz,
  escalation_policy_uid uuid references escalation_policies(uid) on delete set null
);

create unique index check_groups_org_slug_idx on check_groups (organization_uid, slug) where deleted_at is null;
create index check_groups_org_idx on check_groups (organization_uid) where deleted_at is null;

comment on table check_groups is 'Flat organizational grouping for checks. A check belongs to zero or one group.';
comment on column check_groups.organization_uid is 'Owning organization.';
comment on column check_groups.name is 'Display name for the group.';
comment on column check_groups.slug is 'URL-friendly identifier, unique per organization.';
comment on column check_groups.description is 'Optional description of what this group contains.';
comment on column check_groups.sort_order is 'Display order (lower = higher). Default 0.';

create table checks (
  uid                          uuid primary key default gen_random_uuid(),
  organization_uid             uuid not null references organizations(uid) on delete cascade,
  check_group_uid              uuid references check_groups(uid) on delete set null,
  name                         text,
  slug                         text check (slug is null or slug ~ '^[a-z][a-z0-9-]{2,49}$'),
  description                  text,
  type                         text not null,
  config                       jsonb,
  regions                      text[],
  enabled                      boolean not null default true,
  internal                     boolean not null default false,
  period                       interval not null default '00:01:00',
  escalation_threshold         integer not null default 10,
  reopen_cooldown_multiplier   integer,
  max_adaptive_increase        integer,
  status                       smallint not null default 0,
  status_streak                integer not null default 0,
  status_changed_at            timestamptz,
  created_at                   timestamptz not null default now(),
  updated_at                   timestamptz not null default now(),
  deleted_at                   timestamptz,
  escalation_policy_uid        uuid references escalation_policies(uid) on delete set null,
  config_private               text,
  config_private_keys          text,
  confirmation_period_seconds  integer not null default 0,
  recovery_period_seconds      integer not null default 0,
  first_failure_at             timestamp without time zone,
  first_success_since_failure_at timestamp without time zone
);

create unique index checks_slug_idx on checks (organization_uid, slug) where deleted_at is null and slug is not null;
create index checks_group_idx on checks (check_group_uid) where check_group_uid is not null and deleted_at is null;
create index idx_checks_email_token on checks ((config->>'token')) where type = 'email' and deleted_at is null;

comment on table checks is 'Monitoring target configurations. Defines what to monitor, how often, and incident thresholds.';
comment on column checks.organization_uid is 'Owning organization.';
comment on column checks.check_group_uid is 'Optional group this check belongs to. NULL means ungrouped.';
comment on column checks.name is 'Human-readable check name.';
comment on column checks.slug is 'URL-friendly identifier, unique per organization. NULL allowed.';
comment on column checks.description is 'Documentation describing what this check monitors and why.';
comment on column checks.type is 'Check protocol: http, tcp, ping, dns, ssl, etc.';
comment on column checks.config is 'Protocol-specific configuration (URL, port, timeout, expected status, etc.).';
comment on column checks.regions is 'Regions where this check runs. NULL or empty means all regions.';
comment on column checks.enabled is 'Whether the check is actively scheduled for execution.';
comment on column checks.internal is 'Internal checks are hidden from public status pages.';
comment on column checks.period is 'Execution frequency (e.g., 1 minute, 5 minutes).';
comment on column checks.escalation_threshold is 'Consecutive failures before escalating an incident.';
comment on column checks.reopen_cooldown_multiplier is 'Multiplier for adaptive cooldown before reopening a resolved incident. NULL uses system default.';
comment on column checks.max_adaptive_increase is 'Maximum multiplier for adaptive resolution increase. NULL uses system default.';
comment on column checks.status is 'Current check status: 0=unknown, 1=up, 2=down, 3=timeout, 4=error.';
comment on column checks.status_streak is 'Consecutive results with the same status.';
comment on column checks.status_changed_at is 'When the status last changed.';

create table check_jobs (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  check_uid         uuid references checks(uid) on delete cascade,
  region            text,
  type              text,
  config            jsonb,
  encrypted         boolean not null default false,
  period            interval not null,
  scheduled_at      timestamptz,
  lease_worker_uid  uuid references workers(uid) on delete set null,
  lease_expires_at  timestamptz,
  lease_starts      smallint not null default 0,
  updated_at        timestamptz not null default now(),
  config_private    text,
  config_private_keys text
);

create unique index check_jobs_check_null_region_idx on check_jobs (check_uid) where region is null;
create unique index check_jobs_check_region_idx on check_jobs (check_uid, region) where region is not null;
create index check_jobs_check_uid_idx on check_jobs (check_uid);
create index check_jobs_scheduled_at_idx on check_jobs (scheduled_at);

comment on table check_jobs is 'Scheduler state for distributed check execution. One row per check per region.';
comment on column check_jobs.organization_uid is 'Owning organization (denormalized from checks for query performance).';
comment on column check_jobs.check_uid is 'Check this job executes.';
comment on column check_jobs.region is 'Target region for this job. NULL means any region.';
comment on column check_jobs.type is 'Check type (denormalized from checks for performance).';
comment on column check_jobs.config is 'Check configuration (denormalized from checks for performance).';
comment on column check_jobs.encrypted is 'Whether the config contains encrypted values.';
comment on column check_jobs.period is 'Execution interval.';
comment on column check_jobs.scheduled_at is 'Next scheduled execution time.';
comment on column check_jobs.lease_worker_uid is 'Worker currently holding the execution lease. NULL if unleased.';
comment on column check_jobs.lease_expires_at is 'When the lease expires and another worker can claim the job.';
comment on column check_jobs.lease_starts is 'Execution attempt counter. 0-1 normal, high values indicate repeated crashes.';

create table labels (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  key              text not null check (key ~ '^[a-z][a-z0-9-]{2,50}$'),
  value            text not null check (length(value) <= 200),
  created_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create unique index labels_org_key_value_idx on labels (organization_uid, key, value) where deleted_at is null;
create index labels_org_key_idx on labels (organization_uid, key) where deleted_at is null;

comment on table labels is 'Key-value pairs for organizing and filtering checks.';
comment on column labels.organization_uid is 'Owning organization.';
comment on column labels.key is 'Label key (e.g., environment, team, tier).';
comment on column labels.value is 'Label value (max 200 characters).';

create table check_labels (
  uid        uuid primary key default gen_random_uuid(),
  check_uid  uuid not null references checks(uid) on delete cascade,
  label_uid  uuid not null references labels(uid) on delete cascade,
  created_at timestamptz not null default now()
);

create unique index check_labels_check_label_idx on check_labels (check_uid, label_uid);
create index check_labels_label_idx on check_labels (label_uid);

comment on table check_labels is 'Junction table linking checks to labels (many-to-many).';
comment on column check_labels.check_uid is 'Tagged check.';
comment on column check_labels.label_uid is 'Applied label.';

create table results (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  check_uid         uuid not null references checks(uid) on delete cascade,
  period_type       text not null default 'raw' check (period_type in ('raw', 'hour', 'day', 'month', 'year')),
  period_start      timestamptz not null,
  period_end        timestamptz,
  region            text,
  worker_uid        uuid references workers(uid) on delete set null,
  status            smallint check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8)), -- 6=error, 7=degraded (aggregated), 8=warning
  duration          real,
  metrics           jsonb,
  output            jsonb,
  last_for_status   boolean,
  total_checks      integer,
  successful_checks integer,
  availability_pct  double precision,
  duration_min      real,
  duration_max      real,
  duration_p95      real,
  created_at        timestamptz not null default now(),
  duration_avg      real
);

create index results_raw_idx on results (organization_uid, check_uid, period_start desc) where period_type = 'raw';
create index results_aggregated_idx on results (organization_uid, check_uid, period_type, period_start desc) where period_type != 'raw';
create unique index results_aggregated_unique_idx on results (organization_uid, check_uid, region, period_type, period_start) where period_type != 'raw';
create index idx_results_last_for_status on results (check_uid, status) where last_for_status = true;

comment on table results is 'Check execution results: raw data points (period_type=raw) and pre-aggregated SLA data (hour/day/month/year).';
comment on column results.organization_uid is 'Owning organization.';
comment on column results.check_uid is 'Check that produced this result.';
comment on column results.period_type is 'Granularity: raw (individual execution), hour, day, month, year (aggregated).';
comment on column results.period_start is 'Execution timestamp (raw) or aggregation period start.';
comment on column results.period_end is 'Aggregation period end. NULL for raw results.';
comment on column results.region is 'Region where the check was executed.';
comment on column results.worker_uid is 'Worker that executed this check (raw only).';
comment on column results.status is '0=initial, 1=up, 2=down, 3=timeout, 4=error, 5=running (raw only).';
comment on column results.duration is 'Total check duration in milliseconds (raw only).';
comment on column results.metrics is 'Numerical metrics: ttfb, dnsTime, tlsHandshake, etc. (raw only).';
comment on column results.output is 'Diagnostic output: error messages, HTTP status, headers (raw only).';
comment on column results.last_for_status is 'Marks the most recent result per check+status combination (raw only).';
comment on column results.total_checks is 'Number of check executions in this period (aggregated only).';
comment on column results.successful_checks is 'Number of successful executions in this period (aggregated only).';
comment on column results.availability_pct is 'Uptime percentage for this period (aggregated only).';
comment on column results.duration_min is 'Minimum duration in this period (aggregated only).';
comment on column results.duration_max is 'Maximum duration in this period (aggregated only).';
comment on column results.duration_p95 is '95th percentile duration in this period (aggregated only).';
comment on column results.duration_avg is 'Average duration in this period (aggregated only).';

create table jobs (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid references organizations(uid) on delete cascade,
  type             text not null check (type ~ '^[a-z][a-z0-9_-]{2,49}$'),
  config           jsonb,
  retry_count      integer not null default 0,
  scheduled_at     timestamptz not null default now(),
  status           text not null default 'pending' check (status in ('pending', 'running', 'success', 'retried', 'failed')),
  output           jsonb,
  previous_job_uid uuid references jobs(uid),
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create index idx_jobs_queue on jobs (scheduled_at, status) where deleted_at is null and status = 'pending';
create index idx_jobs_organization on jobs (organization_uid, created_at desc) where deleted_at is null;
create index idx_jobs_previous on jobs (previous_job_uid) where previous_job_uid is not null;
create index idx_jobs_incident_uid_pending on jobs ((config->>'incidentUid'))
  where status in ('pending', 'running') and config ? 'incidentUid' and deleted_at is null;

comment on table jobs is 'Background task queue for asynchronous processing (notifications, webhooks, etc.).';
comment on column jobs.organization_uid is 'Owning organization. NULL for system-wide jobs.';
comment on column jobs.type is 'Job type identifier (e.g., email, webhook, slack-notify).';
comment on column jobs.config is 'Job-specific input configuration as JSON.';
comment on column jobs.retry_count is 'Number of retry attempts so far (0 for first attempt).';
comment on column jobs.scheduled_at is 'When this job should be picked up for execution.';
comment on column jobs.status is 'Job status: pending, running, success, retried (spawned a retry), failed.';
comment on column jobs.output is 'Execution output as JSON (result data or error details).';
comment on column jobs.previous_job_uid is 'Link to previous job in a retry chain. NULL for first attempts.';

create table incidents (
  uid                    uuid primary key default gen_random_uuid(),
  organization_uid       uuid not null references organizations(uid) on delete cascade,
  check_uid              uuid not null references checks(uid) on delete cascade,
  region                 text,
  state                  smallint not null default 1,
  started_at             timestamptz not null,
  resolved_at            timestamptz,
  escalated_at           timestamptz,
  acknowledged_at        timestamptz,
  acknowledged_by        uuid references users(uid),
  failure_count          integer not null default 1,
  relapse_count          integer not null default 0,
  last_reopened_at       timestamptz,
  title                  text,
  description            text,
  details                jsonb,
  created_at             timestamptz not null default now(),
  updated_at             timestamptz not null default now(),
  deleted_at             timestamptz,
  check_group_uid        uuid references check_groups(uid) on delete set null,
  snoozed_until          timestamptz,
  snoozed_by             text,
  snooze_reason          text,
  resolved_by            text,
  resolution_type        text,
  caused_by_incident_uid uuid references incidents(uid) on delete set null,
  paging_suppressed      boolean not null default false
);

create index incidents_organization_check_started_at_idx on incidents (organization_uid, check_uid, started_at desc);
create index idx_incidents_org_check_state on incidents (organization_uid, check_uid, state) where state = 1;
create index idx_incidents_org_started on incidents (organization_uid, started_at desc);
create index idx_incidents_org_state_started on incidents (organization_uid, state, started_at desc);
create index idx_incidents_check_resolved on incidents (check_uid, resolved_at desc) where state = 2 and deleted_at is null;
create index idx_incidents_active_by_group on incidents (check_group_uid, state) where check_group_uid is not null and deleted_at is null;
create index idx_incidents_caused_by on incidents (caused_by_incident_uid) where caused_by_incident_uid is not null and deleted_at is null;
create index idx_incidents_snoozed_until on incidents (snoozed_until) where snoozed_until is not null and deleted_at is null;
create unique index uq_active_group_incident on incidents (organization_uid, check_group_uid)
  where state = 1 and check_group_uid is not null and deleted_at is null;

comment on table incidents is 'Tracks when a check goes down and when it recovers.';
comment on column incidents.organization_uid is 'Owning organization.';
comment on column incidents.check_uid is 'Failing check.';
comment on column incidents.region is 'Region where the failure occurred.';
comment on column incidents.state is 'Incident state: 1=active, 2=resolved.';
comment on column incidents.started_at is 'When the check first started failing.';
comment on column incidents.resolved_at is 'When the check recovered. NULL means still ongoing.';
comment on column incidents.escalated_at is 'When escalation was triggered. NULL if not yet escalated.';
comment on column incidents.acknowledged_at is 'When someone acknowledged the incident.';
comment on column incidents.acknowledged_by is 'User who acknowledged the incident.';
comment on column incidents.failure_count is 'Total number of consecutive failures during this incident.';
comment on column incidents.relapse_count is 'Number of times this incident was reopened after brief recoveries.';
comment on column incidents.last_reopened_at is 'When this incident was last reopened. NULL if never reopened.';
comment on column incidents.title is 'Auto-generated title (e.g., "my-api-check is down").';
comment on column incidents.description is 'Human-readable description of what happened.';
comment on column incidents.details is 'Structured data about the incident (error messages, affected metrics).';
comment on column incidents.check_group_uid is 'NULL = traditional per-check incident.';
comment on column incidents.snoozed_until is 'NULL = not snoozed. Sweeper unsnoozes when NOW() > snoozed_until.';
comment on column incidents.resolution_type is 'auto | manual | expired. NULL until resolved_at is set.';
comment on column incidents.caused_by_incident_uid is 'Root-cause incident this one was rolled up under at open time.';
comment on column incidents.paging_suppressed is 'TRUE when notifications/escalation must skip; flips back to FALSE on parent resolve if still down.';

create table events (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  incident_uid     uuid references incidents(uid) on delete cascade,
  check_uid        uuid references checks(uid) on delete cascade,
  job_uid          uuid,
  event_type       varchar(50) not null,
  actor_type       varchar(20) not null check (actor_type in ('system', 'user')),
  actor_uid        uuid references users(uid),
  payload          jsonb,
  created_at       timestamptz not null default now()
);

create index idx_events_org_created on events (organization_uid, created_at desc);
create index idx_events_org_incident_created on events (organization_uid, incident_uid, created_at) where incident_uid is not null;
create index idx_events_check_created on events (check_uid, created_at desc) where check_uid is not null;
create index idx_events_type_created on events (event_type, created_at desc);
create index idx_events_actor on events (actor_uid, created_at desc) where actor_uid is not null;

comment on table events is 'Append-only audit log for incident lifecycle and system events.';
comment on column events.organization_uid is 'Owning organization.';
comment on column events.incident_uid is 'Related incident. NULL for non-incident events.';
comment on column events.check_uid is 'Related check. NULL for non-check events.';
comment on column events.job_uid is 'Related background job (e.g., notification delivery).';
comment on column events.event_type is 'Event type: check.created, incident.created, incident.resolved, notification.sent, etc.';
comment on column events.actor_type is 'Who triggered the event: system or user.';
comment on column events.actor_uid is 'User who triggered the event. NULL for system events.';
comment on column events.payload is 'Event-specific data as JSON.';

create table state_entries (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid references organizations(uid) on delete cascade,
  user_uid         uuid references users(uid) on delete cascade,
  key              text not null check (length(key) <= 255),
  value            jsonb,
  expires_at       timestamptz,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz,
  unique (organization_uid, key)
);

create index idx_state_entries_expires on state_entries (expires_at) where expires_at is not null and deleted_at is null;
create index idx_state_entries_org on state_entries (organization_uid) where deleted_at is null;
create index idx_state_entries_user on state_entries (user_uid) where user_uid is not null and deleted_at is null;

comment on table state_entries is 'Key-value state storage for notifications, user tokens (email confirm, password reset), and distributed locking.';
comment on column state_entries.organization_uid is 'Organization scope. NULL for user-scoped or global entries.';
comment on column state_entries.user_uid is 'User scope (email confirmation, password reset). NULL for org-scoped entries.';
comment on column state_entries.key is 'Namespaced key using slash separators (e.g., email_confirm/{token}, slack_thread/{channel}).';
comment on column state_entries.value is 'State data as JSON.';
comment on column state_entries.expires_at is 'Optional TTL for automatic cleanup. NULL means never expires.';

create table integrations (
  uid                   uuid primary key default gen_random_uuid(),
  organization_uid      uuid not null references organizations(uid) on delete cascade,
  type                  varchar(50) not null,
  name                  varchar(255) not null,
  enabled               boolean not null default true,
  is_default            boolean not null default false,
  settings              jsonb not null default '{}',
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  deleted_at            timestamptz,
  settings_private      text,
  settings_private_keys text
);

create index idx_integrations_org_type on integrations (organization_uid, type) where deleted_at is null;
create index idx_integrations_org_default on integrations (organization_uid) where deleted_at is null and is_default = true;
create index idx_integrations_settings_team_id on integrations ((settings->>'team_id'))
  where type = 'slack' and deleted_at is null;

comment on table integrations is 'Notification and integration connections (Slack, Discord, webhook, email, etc.).';
comment on column integrations.organization_uid is 'Owning organization.';
comment on column integrations.type is 'Integration type: slack, discord, webhook, email, betterstack, etc.';
comment on column integrations.name is 'Human-readable connection name.';
comment on column integrations.enabled is 'Whether this connection actively sends notifications.';
comment on column integrations.is_default is 'If true, auto-attach to new checks for notifications.';
comment on column integrations.settings is 'Type-specific configuration as JSON (e.g., webhook URL, Slack channel, email recipients).';

create table check_channels (
  uid              uuid primary key default gen_random_uuid(),
  check_uid        uuid not null references checks(uid) on delete cascade,
  integration_uid  uuid not null references integrations(uid) on delete cascade,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  settings         jsonb,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now()
);

create unique index check_channels_check_integration_idx on check_channels (check_uid, integration_uid);
create index check_channels_integration_idx on check_channels (integration_uid);
create index check_channels_org_idx on check_channels (organization_uid);

comment on table check_channels is 'Junction table linking checks to integration connections for notifications.';
comment on column check_channels.check_uid is 'Check that triggers notifications.';
comment on column check_channels.integration_uid is 'Integration connection to notify.';
comment on column check_channels.organization_uid is 'Owning organization (denormalized for query performance).';
comment on column check_channels.settings is 'Per-check override settings (e.g., Slack channel override).';

create table status_pages (
  uid                uuid primary key default gen_random_uuid(),
  organization_uid   uuid not null references organizations(uid) on delete cascade,
  name               text not null,
  slug               text not null check (slug ~ '^[a-z][a-z0-9-]{2,39}$'),
  description        text,
  visibility         text not null default 'public' check (visibility in ('public', 'private')),
  is_default         boolean not null default false,
  enabled            boolean not null default true,
  show_availability  boolean not null default true,
  show_response_time boolean not null default true,
  history_days       integer not null default 90,
  language           varchar(10),
  created_at         timestamptz not null default now(),
  updated_at         timestamptz not null default now(),
  deleted_at         timestamptz
);

create unique index status_pages_org_slug_idx on status_pages (organization_uid, slug) where deleted_at is null;
create unique index status_pages_org_default_idx on status_pages (organization_uid) where is_default = true and deleted_at is null;

comment on table status_pages is 'Public-facing status pages displaying service health to end users.';
comment on column status_pages.organization_uid is 'Owning organization.';
comment on column status_pages.name is 'Page title displayed to visitors.';
comment on column status_pages.slug is 'URL-friendly identifier, unique per organization.';
comment on column status_pages.description is 'Subtitle or description shown on the page.';
comment on column status_pages.visibility is 'Access control: public (anyone) or private (authenticated only).';
comment on column status_pages.is_default is 'At most one default page per org, used when accessing status without a slug.';
comment on column status_pages.enabled is 'Whether the page is accessible.';
comment on column status_pages.show_availability is 'Whether to display uptime percentage on the page.';
comment on column status_pages.show_response_time is 'Whether to display response time charts on the page.';
comment on column status_pages.history_days is 'Number of days of history to display (default 90).';
comment on column status_pages.language is 'ISO language code for the page (e.g., en, fr). NULL uses system default.';

create table status_page_sections (
  uid             uuid primary key default gen_random_uuid(),
  status_page_uid uuid not null references status_pages(uid) on delete cascade,
  name            text not null,
  slug            text not null check (slug ~ '^[a-z][a-z0-9-]{2,39}$'),
  position        integer not null default 0,
  created_at      timestamptz not null default now(),
  updated_at      timestamptz not null default now(),
  deleted_at      timestamptz
);

create unique index status_page_sections_page_slug_idx on status_page_sections (status_page_uid, slug) where deleted_at is null;
create index status_page_sections_page_idx on status_page_sections (status_page_uid) where deleted_at is null;

comment on table status_page_sections is 'Grouping sections within a status page.';
comment on column status_page_sections.status_page_uid is 'Parent status page.';
comment on column status_page_sections.name is 'Section heading displayed on the page.';
comment on column status_page_sections.slug is 'URL-friendly identifier, unique per status page.';
comment on column status_page_sections.position is 'Display order (lower = higher on page).';

create table status_page_resources (
  uid         uuid primary key default gen_random_uuid(),
  section_uid uuid not null references status_page_sections(uid) on delete cascade,
  check_uid   uuid not null references checks(uid) on delete cascade,
  public_name text,
  explanation text,
  position    integer not null default 0,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now()
);

create unique index status_page_resources_section_check_idx on status_page_resources (section_uid, check_uid);
create index status_page_resources_check_idx on status_page_resources (check_uid);

comment on table status_page_resources is 'Checks displayed within a status page section.';
comment on column status_page_resources.section_uid is 'Parent section.';
comment on column status_page_resources.check_uid is 'Check to display.';
comment on column status_page_resources.public_name is 'Override display name on the status page. NULL uses the check name.';
comment on column status_page_resources.explanation is 'Optional description visible on the public status page.';
comment on column status_page_resources.position is 'Display order within the section (lower = higher).';

create table status_updates (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid),
  status_page_uid  uuid not null references status_pages(uid),
  section_uid      uuid references status_page_sections(uid),
  check_uid        uuid references checks(uid),
  incident_uid     uuid references incidents(uid),
  title            text not null,
  body_markdown    text not null,
  link_url         text,
  kind             text not null check (kind in ('investigating', 'identified', 'monitoring', 'resolved', 'maintenance', 'info')),
  published_at     timestamptz not null default now(),
  author_uid       uuid not null references users(uid),
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create index idx_status_updates_org_page_pub on status_updates (organization_uid, status_page_uid, published_at desc) where deleted_at is null;
create index idx_status_updates_incident on status_updates (incident_uid) where incident_uid is not null and deleted_at is null;
create index idx_status_updates_check on status_updates (check_uid) where check_uid is not null and deleted_at is null;

create table status_page_subscriber (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid),
  status_page_uid   uuid not null references status_pages(uid),
  email             text not null,
  confirmed_at      timestamptz,
  confirm_token     text not null,
  unsubscribe_token text not null,
  scope             text not null check (scope in ('page', 'incident')),
  incident_uid      uuid references incidents(uid),
  created_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index idx_status_page_subscriber_confirm_token on status_page_subscriber (confirm_token);
create unique index idx_status_page_subscriber_unsub_token on status_page_subscriber (unsubscribe_token);
create index idx_status_page_subscriber_page_confirmed on status_page_subscriber (status_page_uid, confirmed_at) where deleted_at is null;
create unique index idx_status_page_subscriber_live on status_page_subscriber
  (status_page_uid, email, scope, coalesce(incident_uid, '00000000-0000-0000-0000-000000000000'::uuid))
  where deleted_at is null;

create table maintenance_windows (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  title            text not null,
  description      text,
  start_at         timestamptz not null,
  end_at           timestamptz not null,
  recurrence       text not null default 'none' check (recurrence in ('none', 'daily', 'weekly', 'monthly')),
  recurrence_end   timestamptz,
  created_by       text,
  created_at       timestamptz not null default current_timestamp,
  updated_at       timestamptz not null default current_timestamp,
  deleted_at       timestamptz,
  check (end_at > start_at)
);

create index idx_mw_org on maintenance_windows (organization_uid) where deleted_at is null;
create index idx_mw_active on maintenance_windows (organization_uid, start_at, end_at) where deleted_at is null;

comment on table maintenance_windows is 'Scheduled maintenance periods that suppress incident alerts for affected checks.';
comment on column maintenance_windows.organization_uid is 'Owning organization.';
comment on column maintenance_windows.title is 'Maintenance window title shown in notifications and status pages.';
comment on column maintenance_windows.description is 'Detailed description of the planned maintenance.';
comment on column maintenance_windows.start_at is 'When the maintenance window begins.';
comment on column maintenance_windows.end_at is 'When the maintenance window ends. Must be after start_at.';
comment on column maintenance_windows.recurrence is 'Recurrence pattern: none (one-time), daily, weekly, monthly.';
comment on column maintenance_windows.recurrence_end is 'When the recurring schedule stops. NULL means indefinite.';
comment on column maintenance_windows.created_by is 'Identifier of the user or system that created this window.';

create table maintenance_window_checks (
  uid                    uuid primary key default gen_random_uuid(),
  maintenance_window_uid uuid not null references maintenance_windows(uid) on delete cascade,
  check_uid              uuid references checks(uid) on delete cascade,
  check_group_uid        uuid references check_groups(uid) on delete cascade,
  created_at             timestamptz not null default current_timestamp,
  check ((check_uid is not null and check_group_uid is null) or (check_uid is null and check_group_uid is not null))
);

create unique index idx_mwc_check on maintenance_window_checks (maintenance_window_uid, check_uid) where check_uid is not null;
create unique index idx_mwc_group on maintenance_window_checks (maintenance_window_uid, check_group_uid) where check_group_uid is not null;

comment on table maintenance_window_checks is 'Links maintenance windows to individual checks or check groups. Exactly one of check_uid or check_group_uid must be set.';
comment on column maintenance_window_checks.maintenance_window_uid is 'Parent maintenance window.';
comment on column maintenance_window_checks.check_uid is 'Individual check affected. NULL if targeting a group.';
comment on column maintenance_window_checks.check_group_uid is 'Check group affected (all checks in the group). NULL if targeting an individual check.';

create table incident_member_checks (
  incident_uid      uuid not null references incidents(uid) on delete cascade,
  check_uid         uuid not null references checks(uid) on delete cascade,
  joined_at         timestamptz not null default now(),
  first_failure_at  timestamptz not null,
  last_failure_at   timestamptz not null,
  last_recovery_at  timestamptz,
  failure_count     integer not null default 1,
  currently_failing boolean not null default true,
  primary key (incident_uid, check_uid)
);

create index idx_incident_member_checks_check on incident_member_checks (check_uid) where currently_failing = true;

comment on table incident_member_checks is 'Per-member state inside a group incident.';

create table org_entitlements (
  uid              uuid primary key,
  organization_uid uuid not null references organizations(uid) on delete cascade unique,
  payload          jsonb not null default '{}',
  external_ref     text,
  expires_at       timestamptz,
  last_synced_at   timestamptz,
  metadata         jsonb not null default '{}',
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now()
);

create index org_entitlements_external_ref_idx on org_entitlements (external_ref) where external_ref is not null;

create table org_entitlement_audits (
  uid              uuid primary key,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  source           text not null,
  actor            text not null,
  before_snapshot  jsonb,
  after_snapshot   jsonb not null,
  reason           text,
  created_at       timestamptz not null default now()
);

create index org_entitlement_audits_org_idx on org_entitlement_audits (organization_uid, created_at desc);

create table user_passkeys (
  uid                uuid primary key,
  user_uid           uuid not null references users(uid) on delete cascade,
  name               text not null,
  credential_id      bytea not null,
  public_key         bytea not null,
  aaguid             text,
  sign_count         bigint not null default 0,
  transports         jsonb,
  backup_eligible    boolean not null default false,
  backup_state       boolean not null default false,
  user_verified      boolean not null default false,
  attestation_format text,
  last_used_at       timestamptz,
  created_at         timestamptz not null default now(),
  updated_at         timestamptz not null default now(),
  deleted_at         timestamptz,
  unique (user_uid, credential_id)
);

create index idx_user_passkeys_credential_id on user_passkeys (credential_id);
create index idx_user_passkeys_user_uid_active on user_passkeys (user_uid) where deleted_at is null;

create table files (
  uid              uuid primary key,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  name             text not null,
  mime_type        text not null,
  size             bigint not null,
  file_uri         text not null,
  sha256           text,
  created_by       uuid references users(uid) on delete set null,
  created_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create index files_org_created_idx on files (organization_uid, created_at desc) where deleted_at is null;

create table membership_requests (
  uid              uuid primary key,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  user_uid         uuid not null references users(uid) on delete cascade,
  message          text,
  status           text not null check (status in ('pending', 'approved', 'rejected', 'canceled')),
  decision_reason  text,
  decided_at       timestamptz,
  decided_by_uid   uuid references users(uid) on delete set null,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  unique (organization_uid, user_uid)
);

create index membership_requests_org_status_idx on membership_requests (organization_uid, status);
create index membership_requests_user_status_idx on membership_requests (user_uid, status);

create table check_dependencies (
  uid              uuid primary key,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  parent_check_uid uuid not null references checks(uid) on delete cascade,
  child_check_uid  uuid not null references checks(uid) on delete cascade,
  kind             text not null check (kind in ('hard', 'soft')),
  description      text,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz,
  unique (parent_check_uid, child_check_uid),
  check (parent_check_uid <> child_check_uid)
);

create index idx_check_dependencies_child on check_dependencies (child_check_uid) where deleted_at is null;
create index idx_check_dependencies_org on check_dependencies (organization_uid) where deleted_at is null;
create index idx_check_dependencies_parent on check_dependencies (parent_check_uid) where deleted_at is null;

create table user_contacts (
  uid              uuid primary key,
  user_uid         uuid not null references users(uid) on delete cascade,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  type             text not null,
  value            text not null,
  label            text not null default '',
  verified_at      timestamptz,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  deleted_at       timestamptz,
  unique (user_uid, organization_uid, type, value)
);

create index idx_uc_user_org on user_contacts (user_uid, organization_uid) where deleted_at is null;

create table user_notification_routes (
  uid         uuid primary key,
  user_uid    uuid not null references users(uid) on delete cascade,
  org_uid     uuid not null references organizations(uid) on delete cascade,
  contact_uid uuid not null references user_contacts(uid) on delete cascade,
  enabled     boolean not null default true,
  position    integer not null default 0,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now()
);

create unique index idx_unr_contact on user_notification_routes (contact_uid);
create index idx_unr_user_org on user_notification_routes (user_uid, org_uid);

create table discovered_hosts (
  uid                   uuid primary key,
  organization_uid      uuid not null references organizations(uid),
  job_uid               uuid not null references jobs(uid),
  ip                    inet not null,
  hostname              text,
  open_ports            jsonb not null default '[]',
  icmp_reachable        boolean not null default false,
  suggested_checks      jsonb not null default '[]',
  promoted_to_check_uid uuid references checks(uid),
  discovered_at         timestamptz not null default now(),
  deleted_at            timestamptz,
  source                text not null default 'lan'
);

create unique index idx_discovered_hosts_org_ip_source_active on discovered_hosts (organization_uid, ip, source)
  where deleted_at is null and promoted_to_check_uid is null;
create index idx_discovered_hosts_org_job on discovered_hosts (organization_uid, job_uid) where deleted_at is null;

create table incident_notifications (
  uid              uuid primary key,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  incident_uid     uuid not null references incidents(uid) on delete cascade,
  event_type       text not null,
  step_uid         uuid references escalation_policy_steps(uid) on delete set null,
  repeat_index     integer,
  source           text not null,
  user_uid         uuid references users(uid) on delete set null,
  connection_uid   uuid references integrations(uid) on delete set null,
  channel_type     text not null,
  status           text not null,
  skip_reason      text,
  error            text,
  job_uid          uuid,
  message_id       text,
  created_at       timestamptz not null default now(),
  sent_at          timestamptz,
  cancelled_at     timestamptz,
  failed_at        timestamptz,
  delivery_details jsonb
);

create index idx_in_incident on incident_notifications (incident_uid, created_at desc);
create index idx_in_user on incident_notifications (user_uid, created_at desc) where user_uid is not null;
create index idx_in_org_time on incident_notifications (organization_uid, created_at desc);
create index idx_in_job on incident_notifications (job_uid) where job_uid is not null;

comment on column incident_notifications.delivery_details is 'Structured per-attempt delivery artifacts (status, url, capped bodies, duration); secrets never stored.';

create table app_settings (
  key        text primary key,
  value      text not null,
  updated_at timestamptz not null default now()
);
