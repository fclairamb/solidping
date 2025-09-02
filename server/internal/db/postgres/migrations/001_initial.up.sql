create table organizations (
  uid               uuid primary key default gen_random_uuid(),
  slug              text not null check (slug ~ '^[a-z0-9-]{3,20}$'),
  name              text,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index on organizations (slug) where deleted_at is null;

comment on table organizations is 'Top-level tenant container. All monitoring resources are scoped to an organization.';
comment on column organizations.slug is 'URL-friendly unique identifier (3-20 chars, lowercase alphanumeric and hyphens).';
comment on column organizations.name is 'Human-readable display name.';

create table parameters (
    uid               uuid primary key default gen_random_uuid(),
    organization_uid  uuid references organizations(uid) on delete cascade,
    key               text not null check (key ~ '^[a-z0-9_\.]+$'),
    value             jsonb not null,
    secret            boolean,
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),
    deleted_at        timestamptz
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

create table organization_providers (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  provider_type     text not null check (provider_type in ('slack', 'google', 'github', 'gitlab', 'microsoft', 'saml', 'oidc')),
  provider_id       text not null,
  provider_name     text,
  metadata          jsonb,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index idx_org_providers_type_id on organization_providers (provider_type, provider_id) where deleted_at is null;
create index idx_org_providers_org on organization_providers (organization_uid) where deleted_at is null;

comment on table organization_providers is 'Maps organizations to external identity providers. One provider identity belongs to exactly one org.';
comment on column organization_providers.organization_uid is 'Owning organization.';
comment on column organization_providers.provider_type is 'External provider type: slack, google, github, gitlab, microsoft, saml, oidc.';
comment on column organization_providers.provider_id is 'Unique identifier from the provider (e.g., Slack Team ID T0123456789).';
comment on column organization_providers.provider_name is 'Human-readable provider name (e.g., "Acme Corp Slack Workspace").';
comment on column organization_providers.metadata is 'Provider-specific metadata as JSON.';

create table users (
  uid               uuid primary key default gen_random_uuid(),
  email             text not null,
  name              text,
  avatar_url        text,
  password_hash     text,
  email_verified_at timestamptz,
  super_admin       boolean not null default false,
  last_active_at    timestamptz,
  totp_secret       text,
  totp_enabled      boolean not null default false,
  totp_recovery_codes jsonb,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
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

create table user_providers (
  uid               uuid primary key default gen_random_uuid(),
  user_uid          uuid not null references users(uid) on delete cascade,
  provider_type     text not null check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'saml', 'oidc')),
  provider_id       text not null,
  metadata          jsonb,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now()
);

create unique index user_providers_provider_idx on user_providers (provider_type, provider_id);
create index user_providers_user_idx on user_providers (user_uid);

comment on table user_providers is 'Links user accounts to external OAuth/SAML/OIDC providers.';
comment on column user_providers.user_uid is 'User this provider identity belongs to.';
comment on column user_providers.provider_type is 'External provider: google, github, gitlab, microsoft, twitter, slack, saml, oidc.';
comment on column user_providers.provider_id is 'Unique identifier from the provider (e.g., OAuth sub claim).';
comment on column user_providers.metadata is 'Provider-specific data (profile info, tokens, etc.).';

create table organization_members (
  uid               uuid primary key default gen_random_uuid(),
  user_uid          uuid not null references users(uid) on delete cascade,
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  role              text not null check (role in ('admin', 'user', 'viewer')),
  invited_by_uid    uuid references users(uid) on delete set null,
  invited_at        timestamptz,
  joined_at         timestamptz,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
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

create table user_tokens (
  uid               uuid primary key default gen_random_uuid(),
  user_uid          uuid not null references users(uid) on delete cascade,
  organization_uid  uuid references organizations(uid) on delete cascade,
  token             text not null,
  type              text not null check (type in ('pat', 'refresh')),
  properties        jsonb,
  expires_at        timestamptz,
  last_active_at    timestamptz,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
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

create table workers (
  uid               uuid primary key default gen_random_uuid(),
  slug              text not null check (slug ~ '^[a-z][a-z0-9-]{2,20}$'),
  name              text not null,
  region            text check (region ~ '^[a-z][a-z0-9-]{3,20}$'),
  token             text,
  last_active_at    timestamptz,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index workers_slug_idx on workers (slug) where deleted_at is null;
create index idx_workers_token on workers (token) where token is not null and deleted_at is null;

comment on table workers is 'Distributed check executors. At least one per region.';
comment on column workers.slug is 'Unique system identifier (e.g., hostname, container ID).';
comment on column workers.name is 'Human-readable worker name.';
comment on column workers.region is 'Region identifier (e.g., eu-west-1, us-east-1). Determines which checks this worker executes.';
comment on column workers.token is 'Authentication token for edge worker registration. NULL for manually registered workers.';
comment on column workers.last_active_at is 'Last heartbeat timestamp.';

create table check_groups (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  name              text not null,
  slug              text not null check (slug ~ '^[a-z][a-z0-9-]{2,39}$'),
  description       text,
  sort_order        smallint not null default 0,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index check_groups_org_slug_idx on check_groups (organization_uid, slug)
  where deleted_at is null;

create index check_groups_org_idx on check_groups (organization_uid)
  where deleted_at is null;

comment on table check_groups is 'Flat organizational grouping for checks. A check belongs to zero or one group.';
comment on column check_groups.organization_uid is 'Owning organization.';
comment on column check_groups.name is 'Display name for the group.';
comment on column check_groups.slug is 'URL-friendly identifier, unique per organization.';
comment on column check_groups.description is 'Optional description of what this group contains.';
comment on column check_groups.sort_order is 'Display order (lower = higher). Default 0.';

create table checks (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  check_group_uid   uuid references check_groups(uid) on delete set null,
  name              text,
  slug              text check (slug is null or slug ~ '^[a-z][a-z0-9-]{3,40}$'),
  description       text,
  type              text not null,
  config            jsonb,
  regions           text[],
  enabled           boolean not null default true,
  internal          boolean not null default false,
  period            interval not null default '1 minute',
  -- Incident tracking thresholds
  incident_threshold   integer not null default 3,
  escalation_threshold integer not null default 10,
  recovery_threshold   integer not null default 3,
  -- Adaptive resolution
  reopen_cooldown_multiplier integer,
  max_adaptive_increase      integer,
  -- Status tracking
  status            smallint not null default 0,
  status_streak     integer not null default 0,
  status_changed_at timestamptz,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index checks_slug_idx on checks (organization_uid, slug) where deleted_at is null and slug is not null;
create index checks_group_idx on checks (check_group_uid) where check_group_uid is not null and deleted_at is null;

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
comment on column checks.incident_threshold is 'Consecutive failures before opening an incident.';
comment on column checks.escalation_threshold is 'Consecutive failures before escalating an incident.';
comment on column checks.recovery_threshold is 'Consecutive successes before resolving an incident.';
comment on column checks.reopen_cooldown_multiplier is 'Multiplier for adaptive cooldown before reopening a resolved incident. NULL uses system default.';
comment on column checks.max_adaptive_increase is 'Maximum multiplier for adaptive resolution increase. NULL uses system default.';
comment on column checks.status is 'Current check status: 0=unknown, 1=up, 2=down, 3=timeout, 4=error.';
comment on column checks.status_streak is 'Consecutive results with the same status.';
comment on column checks.status_changed_at is 'When the status last changed.';

create table labels (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  key               text not null check (key ~ '^[a-z][a-z0-9-]{3,50}$'),
  value             text not null check (length(value) <= 200),
  created_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index labels_org_key_value_idx on labels (organization_uid, key, value) where deleted_at is null;
create index labels_org_key_idx on labels (organization_uid, key) where deleted_at is null;

comment on table labels is 'Key-value pairs for organizing and filtering checks.';
comment on column labels.organization_uid is 'Owning organization.';
comment on column labels.key is 'Label key (e.g., environment, team, tier).';
comment on column labels.value is 'Label value (max 200 characters).';

create table check_labels (
  uid               uuid primary key default gen_random_uuid(),
  check_uid         uuid not null references checks(uid) on delete cascade,
  label_uid         uuid not null references labels(uid) on delete cascade,
  created_at        timestamptz not null default now()
);

create unique index check_labels_check_label_idx on check_labels (check_uid, label_uid);
create index check_labels_label_idx on check_labels (label_uid);

comment on table check_labels is 'Junction table linking checks to labels (many-to-many).';
comment on column check_labels.check_uid is 'Tagged check.';
comment on column check_labels.label_uid is 'Applied label.';

create table check_jobs (
  uid                 uuid primary key default gen_random_uuid(),
  organization_uid    uuid not null references organizations(uid) on delete cascade,
  check_uid           uuid references checks(uid) on delete cascade,
  region              text,
  type                text,
  config              jsonb,
  encrypted           boolean not null default false,
  period              interval not null,
  scheduled_at        timestamptz,
  lease_worker_uid    uuid references workers(uid) on delete set null,
  lease_expires_at    timestamptz,
  lease_starts        smallint not null default 0,
  updated_at          timestamptz not null default now()
);

create index check_jobs_scheduled_at_idx on check_jobs (scheduled_at);
create unique index check_jobs_check_region_idx on check_jobs (check_uid, region) where region is not null;
create unique index check_jobs_check_null_region_idx on check_jobs (check_uid) where region is null;
create index check_jobs_check_uid_idx on check_jobs (check_uid);

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

create table results (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  check_uid         uuid not null references checks(uid) on delete cascade,
  period_type       text not null default 'raw' check (period_type in ('raw', 'hour', 'day', 'month', 'year')),
  period_start      timestamptz not null,
  period_end        timestamptz,
  region            text,

  -- Raw result fields (period_type = 'raw')
  worker_uid        uuid references workers(uid) on delete set null,
  status            smallint check (status in (0, 1, 2, 3, 4, 5)),
  duration          real,
  metrics           jsonb,
  output            jsonb,
  last_for_status   boolean,

  -- Aggregated fields (period_type = 'hour', 'day', 'month', 'year')
  total_checks      int,
  successful_checks int,
  availability_pct  double precision,
  duration_min      real,
  duration_max      real,
  duration_p95      real,

  created_at        timestamptz not null default now()
);

create index results_raw_idx on results (organization_uid, check_uid, period_start desc) where period_type = 'raw';
create index results_aggregated_idx on results (organization_uid, check_uid, period_type, period_start desc) where period_type != 'raw';
create unique index results_aggregated_unique_idx on results (organization_uid, check_uid, region, period_type, period_start) where period_type != 'raw';
create index idx_results_last_for_status on results(check_uid, status) where last_for_status = true;

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

create table incidents (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  check_uid         uuid not null references checks(uid) on delete cascade,
  region            text,
  state             smallint not null default 1,
  started_at        timestamptz not null,
  resolved_at       timestamptz,
  escalated_at      timestamptz,
  acknowledged_at   timestamptz,
  acknowledged_by   uuid references users(uid),
  failure_count     integer not null default 1,
  relapse_count     integer not null default 0,
  last_reopened_at  timestamptz,
  title             text,
  description       text,
  details           jsonb,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create index incidents_organization_check_started_at_idx on incidents (organization_uid, check_uid, started_at desc);
create index idx_incidents_org_check_state on incidents (organization_uid, check_uid, state) where state = 1;
create index idx_incidents_org_started on incidents (organization_uid, started_at desc);
create index idx_incidents_org_state_started on incidents (organization_uid, state, started_at desc);
create index idx_incidents_check_resolved on incidents (check_uid, resolved_at desc) where state = 2 and deleted_at is null;

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

create table events (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  incident_uid      uuid references incidents(uid) on delete cascade,
  check_uid         uuid references checks(uid) on delete cascade,
  job_uid           uuid,
  event_type        varchar(50) not null,
  actor_type        varchar(20) not null check (actor_type in ('system', 'user')),
  actor_uid         uuid references users(uid),
  payload           jsonb,
  created_at        timestamptz not null default now()
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

create table jobs (
    uid uuid primary key default gen_random_uuid(),
    organization_uid uuid references organizations(uid) on delete cascade,
    type text not null check (type ~ '^[a-z][a-z0-9-]{3,20}$'),
    config jsonb,
    retry_count int not null default 0,
    scheduled_at timestamptz not null default now(),
    status text not null default 'pending' check (status in ('pending', 'running', 'success', 'retried', 'failed')),
    output jsonb,
    previous_job_uid uuid references jobs(uid),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    deleted_at timestamptz
);

create index idx_jobs_queue on jobs(scheduled_at, status)
    where deleted_at is null and status = 'pending';

create index idx_jobs_organization on jobs(organization_uid, created_at desc)
    where deleted_at is null;

create index idx_jobs_previous on jobs(previous_job_uid)
    where previous_job_uid is not null;

comment on table jobs is 'Background task queue for asynchronous processing (notifications, webhooks, etc.).';
comment on column jobs.organization_uid is 'Owning organization. NULL for system-wide jobs.';
comment on column jobs.type is 'Job type identifier (e.g., email, webhook, slack-notify).';
comment on column jobs.config is 'Job-specific input configuration as JSON.';
comment on column jobs.retry_count is 'Number of retry attempts so far (0 for first attempt).';
comment on column jobs.scheduled_at is 'When this job should be picked up for execution.';
comment on column jobs.status is 'Job status: pending, running, success, retried (spawned a retry), failed.';
comment on column jobs.output is 'Execution output as JSON (result data or error details).';
comment on column jobs.previous_job_uid is 'Link to previous job in a retry chain. NULL for first attempts.';

create table state_entries (
    uid               uuid primary key default gen_random_uuid(),
    organization_uid  uuid references organizations(uid) on delete cascade,
    user_uid          uuid references users(uid) on delete cascade,
    key               text not null check (length(key) <= 255),
    value             jsonb,
    expires_at        timestamptz,
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),
    deleted_at        timestamptz,
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

create table integration_connections (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  type              varchar(50) not null,
  name              varchar(255) not null,
  enabled           boolean not null default true,
  is_default        boolean not null default true,
  settings          jsonb not null default '{}',
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create index idx_integration_connections_org_type on integration_connections (organization_uid, type)
    where deleted_at is null;

create index idx_integration_connections_org_default on integration_connections (organization_uid)
    where deleted_at is null and is_default = true;

create index idx_integration_connections_settings_team_id on integration_connections
    ((settings->>'team_id'))
    where type = 'slack' and deleted_at is null;

comment on table integration_connections is 'Notification and integration connections (Slack, Discord, webhook, email, etc.).';
comment on column integration_connections.organization_uid is 'Owning organization.';
comment on column integration_connections.type is 'Integration type: slack, discord, webhook, email, betterstack, etc.';
comment on column integration_connections.name is 'Human-readable connection name.';
comment on column integration_connections.enabled is 'Whether this connection actively sends notifications.';
comment on column integration_connections.is_default is 'If true, auto-attach to new checks for notifications.';
comment on column integration_connections.settings is 'Type-specific configuration as JSON (e.g., webhook URL, Slack channel, email recipients).';

create table check_connections (
  uid               uuid primary key default gen_random_uuid(),
  check_uid         uuid not null references checks(uid) on delete cascade,
  connection_uid    uuid not null references integration_connections(uid) on delete cascade,
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  settings          jsonb,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now()
);

create unique index check_connections_check_connection_idx on check_connections (check_uid, connection_uid);
create index check_connections_connection_idx on check_connections (connection_uid);
create index check_connections_org_idx on check_connections (organization_uid);

comment on table check_connections is 'Junction table linking checks to integration connections for notifications.';
comment on column check_connections.check_uid is 'Check that triggers notifications.';
comment on column check_connections.connection_uid is 'Integration connection to notify.';
comment on column check_connections.organization_uid is 'Owning organization (denormalized for query performance).';
comment on column check_connections.settings is 'Per-check override settings (e.g., Slack channel override).';

create table status_pages (
  uid               uuid primary key default gen_random_uuid(),
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  name              text not null,
  slug              text not null check (slug ~ '^[a-z][a-z0-9-]{2,39}$'),
  description       text,
  visibility        text not null default 'public' check (visibility in ('public', 'private')),
  is_default        boolean not null default false,
  enabled           boolean not null default true,
  show_availability boolean not null default true,
  show_response_time boolean not null default true,
  history_days      integer not null default 90,
  language          varchar(10),
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
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
  uid               uuid primary key default gen_random_uuid(),
  status_page_uid   uuid not null references status_pages(uid) on delete cascade,
  name              text not null,
  slug              text not null check (slug ~ '^[a-z][a-z0-9-]{2,39}$'),
  position          integer not null default 0,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz
);

create unique index status_page_sections_page_slug_idx on status_page_sections (status_page_uid, slug) where deleted_at is null;
create index status_page_sections_page_idx on status_page_sections (status_page_uid) where deleted_at is null;

comment on table status_page_sections is 'Grouping sections within a status page.';
comment on column status_page_sections.status_page_uid is 'Parent status page.';
comment on column status_page_sections.name is 'Section heading displayed on the page.';
comment on column status_page_sections.slug is 'URL-friendly identifier, unique per status page.';
comment on column status_page_sections.position is 'Display order (lower = higher on page).';

create table status_page_resources (
  uid               uuid primary key default gen_random_uuid(),
  section_uid       uuid not null references status_page_sections(uid) on delete cascade,
  check_uid         uuid not null references checks(uid) on delete cascade,
  public_name       text,
  explanation       text,
  position          integer not null default 0,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now()
);

create unique index status_page_resources_section_check_idx on status_page_resources (section_uid, check_uid);
create index status_page_resources_check_idx on status_page_resources (check_uid);

comment on table status_page_resources is 'Checks displayed within a status page section.';
comment on column status_page_resources.section_uid is 'Parent section.';
comment on column status_page_resources.check_uid is 'Check to display.';
comment on column status_page_resources.public_name is 'Override display name on the status page. NULL uses the check name.';
comment on column status_page_resources.explanation is 'Optional description visible on the public status page.';
comment on column status_page_resources.position is 'Display order within the section (lower = higher).';

create table maintenance_windows (
  uid uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  title text not null,
  description text,
  start_at timestamptz not null,
  end_at timestamptz not null,
  recurrence text not null default 'none' check (recurrence in ('none', 'daily', 'weekly', 'monthly')),
  recurrence_end timestamptz,
  created_by text,
  created_at timestamptz not null default current_timestamp,
  updated_at timestamptz not null default current_timestamp,
  deleted_at timestamptz,
  check (end_at > start_at)
);

create index idx_mw_org on maintenance_windows(organization_uid) where deleted_at is null;
create index idx_mw_active on maintenance_windows(organization_uid, start_at, end_at) where deleted_at is null;

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
  uid uuid primary key default gen_random_uuid(),
  maintenance_window_uid uuid not null references maintenance_windows(uid) on delete cascade,
  check_uid uuid references checks(uid) on delete cascade,
  check_group_uid uuid references check_groups(uid) on delete cascade,
  created_at timestamptz not null default current_timestamp,
  check ((check_uid is not null and check_group_uid is null) or (check_uid is null and check_group_uid is not null))
);

create unique index idx_mwc_check on maintenance_window_checks(maintenance_window_uid, check_uid) where check_uid is not null;
create unique index idx_mwc_group on maintenance_window_checks(maintenance_window_uid, check_group_uid) where check_group_uid is not null;

comment on table maintenance_window_checks is 'Links maintenance windows to individual checks or check groups. Exactly one of check_uid or check_group_uid must be set.';
comment on column maintenance_window_checks.maintenance_window_uid is 'Parent maintenance window.';
comment on column maintenance_window_checks.check_uid is 'Individual check affected. NULL if targeting a group.';
comment on column maintenance_window_checks.check_group_uid is 'Check group affected (all checks in the group). NULL if targeting an individual check.';
