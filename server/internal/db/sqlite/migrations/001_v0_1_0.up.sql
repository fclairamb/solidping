-- solidping v0.1.0 — consolidated baseline schema (SQLite)
-- Replaces incremental migrations 001–034 with the net final schema.
-- Do NOT run this file on a database that already has the incremental migrations applied.

create table organizations (
  uid               text primary key,
  slug              text not null check (length(slug) >= 3 and length(slug) <= 20), -- URL-friendly unique identifier (3-20 chars, lowercase alphanumeric and hyphens)
  name              text, -- Human-readable display name
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index organizations_slug_idx on organizations (slug) where deleted_at is null;

create table parameters (
  uid               text primary key,
  organization_uid  text references organizations(uid) on delete cascade, -- Owning organization. NULL for system-wide parameters
  key               text not null, -- Dot-separated configuration key (e.g., smtp.host, slack.default_channel)
  value             text not null, -- Configuration value as JSON
  secret            integer, -- Whether this value is sensitive and should be masked in API responses
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index parameters_org_key_idx on parameters (organization_uid, key)
  where deleted_at is null and organization_uid is not null;
create unique index parameters_system_key_idx on parameters (key)
  where deleted_at is null and organization_uid is null;

create table users (
  uid                  text primary key,
  email                text not null, -- Globally unique email address (case-insensitive)
  name                 text, -- Display name
  avatar_url           text, -- URL to user profile picture
  password_hash        text, -- Argon2id hash. NULL for SSO-only users
  email_verified_at    text, -- When the email was verified. NULL if not yet verified
  super_admin          integer not null default 0, -- Super admins can access and manage all organizations
  last_active_at       text, -- Timestamp of last API or UI activity
  totp_secret          text, -- Base32-encoded TOTP secret for 2FA. NULL if 2FA not configured
  totp_enabled         integer not null default 0, -- Whether TOTP two-factor authentication is active
  totp_recovery_codes  text, -- JSON array of hashed one-time recovery codes for 2FA bypass
  created_at           text not null default (datetime('now')),
  updated_at           text not null default (datetime('now')),
  deleted_at           text
);

create unique index users_email_idx on users (email collate nocase) where deleted_at is null;

create table organization_members (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade, -- Member user
  organization_uid  text not null references organizations(uid) on delete cascade, -- Organization the user belongs to
  role              text not null check (role in ('admin', 'user', 'viewer')), -- Role: admin (full access), user (read/write), viewer (read-only)
  invited_by_uid    text references users(uid) on delete set null, -- User who sent the invitation. NULL for founders or migrated users
  invited_at        text, -- When the invitation was sent. NULL for immediate additions
  joined_at         text, -- When the user accepted the invitation. NULL means pending
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index organization_members_user_org_idx on organization_members (user_uid, organization_uid) where deleted_at is null;
create index organization_members_org_idx on organization_members (organization_uid) where deleted_at is null;
create index organization_members_user_idx on organization_members (user_uid) where deleted_at is null;

create table organization_providers (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  provider_type     text not null check (provider_type in ('slack', 'google', 'github', 'gitlab', 'microsoft', 'discord', 'saml', 'oidc')),
  provider_id       text not null,
  provider_name     text,
  metadata          text,
  metadata_private  text,
  metadata_private_keys text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index idx_org_providers_type_id on organization_providers (provider_type, provider_id) where deleted_at is null;
create index idx_org_providers_org on organization_providers (organization_uid) where deleted_at is null;

create table user_tokens (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade, -- Token owner
  organization_uid  text references organizations(uid) on delete cascade, -- Organization scope for PAT tokens. NULL for global refresh tokens
  token             text not null, -- Hashed token value
  type              text not null check (type in ('pat', 'refresh')), -- Token type: pat or refresh
  properties        text, -- Token metadata (e.g., name, scopes, IP restrictions)
  expires_at        text, -- Expiration timestamp. NULL means never expires
  last_active_at    text, -- Last time this token was used for authentication
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index user_tokens_token_idx on user_tokens (token) where deleted_at is null;
create index user_tokens_user_uid_idx on user_tokens (user_uid) where deleted_at is null;
create index user_tokens_expires_at_idx on user_tokens (expires_at) where deleted_at is null and expires_at is not null;

create table user_providers (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade,
  provider_type     text not null check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'discord', 'saml', 'oidc')),
  provider_id       text not null,
  metadata          text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now'))
);

create unique index user_providers_provider_idx on user_providers (provider_type, provider_id);
create index user_providers_user_idx on user_providers (user_uid);

create table workers (
  uid               text primary key,
  slug              text not null check (length(slug) >= 3 and length(slug) <= 20), -- Unique system identifier (e.g., hostname, container ID)
  name              text not null, -- Human-readable worker name
  region            text, -- Region identifier (e.g., eu-west-1). Determines which checks this worker executes
  token             text, -- Authentication token for edge worker registration. NULL for manually registered workers
  last_active_at    text, -- Last heartbeat timestamp
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index workers_slug_idx on workers (slug) where deleted_at is null;
create index idx_workers_token on workers (token);

create table severities (
  uid               text not null primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  slug              text not null,
  name              text not null,
  description       text,
  -- channels is a JSON array of channel-type strings, e.g.
  -- ["email","slack"] or ["email","slack","sms","voice","critical_push"].
  -- Validation happens in the service layer against the union of
  -- ConnectionType values and the direct-channel synthetic types
  -- (sms/voice/push/critical_push).
  channels          text not null default '[]',
  is_default        integer not null default 0,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index severities_org_slug_alive_idx on severities (organization_uid, slug) where deleted_at is null;
create unique index severities_org_default_alive_idx on severities (organization_uid) where is_default = 1 and deleted_at is null;

create table escalation_policies (
  uid                    text primary key,
  organization_uid       text not null references organizations(uid) on delete cascade,
  slug                   text not null,
  name                   text not null,
  description            text,
  repeat_max             integer not null default 0,
  repeat_after_seconds   integer,
  created_at             text not null default (datetime('now')),
  updated_at             text not null default (datetime('now')),
  deleted_at             text,
  unique (organization_uid, slug)
);

create index idx_escalation_policies_org on escalation_policies (organization_uid) where deleted_at is null;

create table escalation_policy_steps (
  uid           text primary key,
  policy_uid    text not null references escalation_policies(uid) on delete cascade,
  position      integer not null,
  severity_uid  text references severities(uid) on delete set null,
  delay_seconds integer not null default 0,
  created_at    text not null default (datetime('now')),
  updated_at    text not null default (datetime('now')),
  unique (policy_uid, position)
);

create table escalation_policy_targets (
  uid         text primary key,
  step_uid    text not null references escalation_policy_steps(uid) on delete cascade,
  target_type text not null check (target_type in ('user', 'schedule', 'connection', 'all_admins')),
  target_uid  text,
  position    integer not null default 0
);

create index idx_escalation_targets_step on escalation_policy_targets (step_uid);

create table on_call_schedules (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  slug              text not null,
  name              text not null,
  description       text,
  timezone          text not null,
  rotation_type     text not null check (rotation_type in ('daily', 'weekly')),
  handoff_time      text not null,
  handoff_weekday   integer,
  start_at          text not null,
  ical_secret       text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text,
  unique (organization_uid, slug)
);

create index idx_on_call_schedules_org on on_call_schedules (organization_uid) where deleted_at is null;

create table on_call_schedule_users (
  uid           text primary key,
  schedule_uid  text not null references on_call_schedules(uid) on delete cascade,
  user_uid      text not null references users(uid) on delete cascade,
  position      integer not null,
  created_at    text not null default (datetime('now')),
  updated_at    text not null default (datetime('now')),
  unique (schedule_uid, position),
  unique (schedule_uid, user_uid)
);

create table on_call_schedule_overrides (
  uid             text primary key,
  schedule_uid    text not null references on_call_schedules(uid) on delete cascade,
  user_uid        text not null references users(uid) on delete cascade,
  start_at        text not null,
  end_at          text not null,
  reason          text,
  created_by_uid  text references users(uid) on delete set null,
  created_at      text not null default (datetime('now'))
);

create index idx_on_call_overrides_lookup on on_call_schedule_overrides (schedule_uid, start_at, end_at);

create table check_groups (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid) on delete cascade, -- Owning organization
  name                  text not null, -- Display name for the group
  slug                  text not null check (length(slug) >= 3 and length(slug) <= 40), -- URL-friendly identifier, unique per organization
  description           text, -- Optional description of what this group contains
  sort_order            integer not null default 0, -- Display order (lower = higher). Default 0
  escalation_policy_uid text references escalation_policies(uid) on delete set null,
  created_at            text not null default (datetime('now')),
  updated_at            text not null default (datetime('now')),
  deleted_at            text
);

create unique index check_groups_org_slug_idx on check_groups (organization_uid, slug) where deleted_at is null;
create index check_groups_org_idx on check_groups (organization_uid) where deleted_at is null;

create table checks (
  uid                            text primary key,
  organization_uid               text not null references organizations(uid) on delete cascade, -- Owning organization
  check_group_uid                text references check_groups(uid) on delete set null, -- Optional group. NULL means ungrouped
  name                           text, -- Human-readable check name
  slug                           text check (slug is null or (length(slug) >= 3 and length(slug) <= 50)), -- URL-friendly identifier, unique per org. NULL allowed
  description                    text, -- Documentation describing what this check monitors and why
  type                           text not null, -- Check protocol: http, tcp, ping, dns, ssl, etc.
  config                         text, -- Protocol-specific configuration (URL, port, timeout, expected status, etc.)
  regions                        text, -- Regions where this check runs. NULL or empty means all regions
  enabled                        integer not null default 1, -- Whether the check is actively scheduled for execution
  internal                       integer not null default 0, -- Internal checks are hidden from public status pages
  period                         text not null default '00:01:00', -- Execution frequency (e.g., 00:01:00 = 1 minute)
  escalation_threshold           integer not null default 10, -- Consecutive failures before escalating an incident
  reopen_cooldown_multiplier     integer, -- Multiplier for adaptive cooldown before reopening. NULL uses system default
  max_adaptive_increase          integer, -- Maximum multiplier for adaptive resolution increase. NULL uses system default
  status                         integer not null default 0, -- Current status: 0=unknown, 1=up, 2=down, 3=timeout, 4=error
  status_streak                  integer not null default 0, -- Consecutive results with the same status
  status_changed_at              text, -- When the status last changed
  escalation_policy_uid          text references escalation_policies(uid) on delete set null,
  config_private                 text,
  config_private_keys            text,
  confirmation_period_seconds    integer not null default 0,
  recovery_period_seconds        integer not null default 0,
  first_failure_at               text,
  first_success_since_failure_at text,
  created_at                     text not null default (datetime('now')),
  updated_at                     text not null default (datetime('now')),
  deleted_at                     text
);

create unique index checks_slug_idx on checks (organization_uid, slug) where deleted_at is null and slug is not null;
create index checks_group_idx on checks (check_group_uid) where check_group_uid is not null and deleted_at is null;
create index idx_checks_email_token on checks (json_extract(config, '$.token'))
  where type = 'email' and deleted_at is null;

create table labels (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  key               text not null check (length(key) >= 1 and length(key) <= 50), -- Label key (e.g., environment, team, tier)
  value             text not null check (length(value) <= 200), -- Label value (max 200 characters)
  created_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index labels_org_key_value_idx on labels (organization_uid, key, value) where deleted_at is null;
create index labels_org_key_idx on labels (organization_uid, key) where deleted_at is null;

create table check_labels (
  uid               text primary key,
  check_uid         text not null references checks(uid) on delete cascade, -- Tagged check
  label_uid         text not null references labels(uid) on delete cascade, -- Applied label
  created_at        text not null default (datetime('now'))
);

create unique index check_labels_check_label_idx on check_labels (check_uid, label_uid);
create index check_labels_label_idx on check_labels (label_uid);

create table check_jobs (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization (denormalized from checks for query performance)
  check_uid         text references checks(uid) on delete cascade, -- Check this job executes
  region            text, -- Target region. NULL means any region
  type              text, -- Check type (denormalized from checks for performance)
  config            text, -- Check configuration (denormalized from checks for performance)
  encrypted         integer not null default 0, -- Whether the config contains encrypted values
  period            text not null, -- Execution interval
  scheduled_at      text, -- Next scheduled execution time
  lease_worker_uid  text references workers(uid) on delete set null, -- Worker holding the execution lease. NULL if unleased
  lease_expires_at  text, -- When the lease expires and another worker can claim the job
  lease_starts      integer not null default 0, -- Execution attempt counter. 0-1 normal, high values indicate crashes
  config_private    text,
  config_private_keys text,
  updated_at        text not null default (datetime('now'))
);

create index check_jobs_scheduled_at_idx on check_jobs (scheduled_at);
create unique index check_jobs_check_region_idx on check_jobs (check_uid, region);
create index check_jobs_check_uid_idx on check_jobs (check_uid);

create table results (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  check_uid         text not null references checks(uid) on delete cascade, -- Check that produced this result
  period_type       text not null default 'raw' check (period_type in ('raw', 'hour', 'day', 'month', 'year')), -- Granularity: raw or aggregated
  period_start      text not null, -- Execution timestamp (raw) or aggregation period start
  period_end        text, -- Aggregation period end. NULL for raw results
  region            text, -- Region where the check was executed

  -- Raw result fields (period_type = 'raw')
  worker_uid        text references workers(uid) on delete set null, -- Worker that executed this check (raw only)
  status            integer check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8)), -- 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 7=degraded (aggregated), 8=warning
  duration          real, -- Total check duration in milliseconds (raw only)
  metrics           text, -- Numerical metrics: ttfb, dnsTime, tlsHandshake, etc. (raw only)
  output            text, -- Diagnostic output: error messages, HTTP status, headers (raw only)
  last_for_status   integer, -- Marks the most recent result per check+status combination (raw only)

  -- Aggregated fields (period_type = 'hour', 'day', 'month', 'year')
  total_checks      integer, -- Number of check executions in this period
  successful_checks integer, -- Number of successful executions in this period
  availability_pct  real, -- Uptime percentage for this period
  duration_min      real, -- Minimum duration in this period
  duration_max      real, -- Maximum duration in this period
  duration_p95      real, -- 95th percentile duration in this period
  duration_avg      real, -- Average duration in this period

  created_at        text not null default (datetime('now'))
);

create index results_raw_idx on results (organization_uid, check_uid, period_start desc) where period_type = 'raw';
create index results_aggregated_idx on results (organization_uid, check_uid, period_type, period_start desc) where period_type != 'raw';
create unique index results_aggregated_unique_idx on results (organization_uid, check_uid, region, period_type, period_start) where period_type != 'raw';
create index idx_results_last_for_status on results (check_uid, status) where last_for_status = 1;

create table jobs (
  uid              text primary key,
  organization_uid text references organizations(uid) on delete cascade, -- Owning organization. NULL for system-wide jobs
  type             text not null check (length(type) >= 3), -- Job type identifier (e.g., email, webhook, slack-notify)
  config           text, -- Job-specific input configuration as JSON
  retry_count      integer not null default 0, -- Number of retry attempts so far (0 for first attempt)
  scheduled_at     text not null default (datetime('now')), -- When this job should be picked up for execution
  status           text not null default 'pending' check (status in ('pending', 'running', 'success', 'retried', 'failed')), -- Job status
  output           text, -- Execution output as JSON (result data or error details)
  previous_job_uid text references jobs(uid), -- Link to previous job in a retry chain. NULL for first attempts
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  deleted_at       text
);

create index idx_jobs_queue on jobs (scheduled_at, status) where deleted_at is null and status = 'pending';
create index idx_jobs_organization on jobs (organization_uid, created_at desc) where deleted_at is null;
create index idx_jobs_previous on jobs (previous_job_uid) where previous_job_uid is not null;
create index idx_jobs_incident_uid_pending on jobs (json_extract(config, '$.incidentUid'))
  where status in ('pending', 'running') and deleted_at is null;

create table incidents (
  uid                      text primary key,
  organization_uid         text not null references organizations(uid) on delete cascade, -- Owning organization
  check_uid                text not null references checks(uid) on delete cascade, -- Failing check
  region                   text, -- Region where the failure occurred
  state                    integer not null default 1, -- Incident state: 1=active, 2=resolved
  started_at               text not null, -- When the check first started failing
  resolved_at              text, -- When the check recovered. NULL means still ongoing
  escalated_at             text, -- When escalation was triggered. NULL if not yet escalated
  acknowledged_at          text, -- When someone acknowledged the incident
  acknowledged_by          text references users(uid), -- User who acknowledged the incident
  failure_count            integer not null default 1, -- Total number of consecutive failures during this incident
  relapse_count            integer not null default 0, -- Number of times this incident was reopened after brief recoveries
  last_reopened_at         text, -- When this incident was last reopened. NULL if never reopened
  title                    text, -- Auto-generated title (e.g., "my-api-check is down")
  description              text, -- Human-readable description of what happened
  details                  text, -- Structured data about the incident (error messages, affected metrics)
  check_group_uid          text references check_groups(uid) on delete set null,
  snoozed_until            text, -- NULL = not snoozed. Sweeper unsnoozes when NOW() > snoozed_until
  snoozed_by               text,
  snooze_reason            text,
  resolved_by              text,
  resolution_type          text, -- auto | manual | expired. NULL until resolved_at is set
  caused_by_incident_uid   text references incidents(uid) on delete set null, -- Root-cause incident this one was rolled up under at open time
  paging_suppressed        integer not null default 0, -- 1 when notifications/escalation must skip
  created_at               text not null default (datetime('now')),
  updated_at               text not null default (datetime('now')),
  deleted_at               text
);

create index incidents_organization_check_started_at_idx on incidents (organization_uid, check_uid, started_at desc);
create index idx_incidents_org_check_state on incidents (organization_uid, check_uid, state) where state = 1;
create index idx_incidents_org_started on incidents (organization_uid, started_at desc);
create index idx_incidents_org_state_started on incidents (organization_uid, state, started_at desc);
create index idx_incidents_check_resolved on incidents (check_uid, resolved_at desc) where state = 2 and deleted_at is null;
create index idx_incidents_active_by_group on incidents (check_group_uid, state) where check_group_uid is not null and deleted_at is null;
create index idx_incidents_snoozed_until on incidents (snoozed_until) where snoozed_until is not null and deleted_at is null;
create index idx_incidents_caused_by on incidents (caused_by_incident_uid) where caused_by_incident_uid is not null and deleted_at is null;
create unique index uq_active_group_incident on incidents (organization_uid, check_group_uid)
  where state = 1 and check_group_uid is not null and deleted_at is null;

create table events (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  incident_uid      text references incidents(uid) on delete cascade, -- Related incident. NULL for non-incident events
  check_uid         text references checks(uid) on delete cascade, -- Related check. NULL for non-check events
  job_uid           text, -- Related background job (e.g., notification delivery)
  event_type        text not null, -- Event type: check.created, incident.created, incident.resolved, etc.
  actor_type        text not null check (actor_type in ('system', 'user')), -- Who triggered the event: system or user
  actor_uid         text references users(uid), -- User who triggered the event. NULL for system events
  payload           text, -- Event-specific data as JSON
  created_at        text not null default (datetime('now'))
);

create index idx_events_org_created on events (organization_uid, created_at desc);
create index idx_events_org_incident_created on events (organization_uid, incident_uid, created_at) where incident_uid is not null;
create index idx_events_check_created on events (check_uid, created_at desc) where check_uid is not null;
create index idx_events_type_created on events (event_type, created_at desc);
create index idx_events_actor on events (actor_uid, created_at desc) where actor_uid is not null;

create table state_entries (
  uid               text primary key,
  organization_uid  text references organizations(uid) on delete cascade, -- Organization scope. NULL for user-scoped or global entries
  user_uid          text references users(uid) on delete cascade, -- User scope (email confirmation, password reset). NULL for org-scoped entries
  key               text not null check (length(key) <= 255), -- Namespaced key using slash separators (e.g., email_confirm/{token})
  value             text, -- State data as JSON
  expires_at        text, -- Optional TTL for automatic cleanup. NULL means never expires
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text,
  unique (organization_uid, key)
);

create index idx_state_entries_expires on state_entries (expires_at) where expires_at is not null and deleted_at is null;
create index idx_state_entries_org on state_entries (organization_uid) where deleted_at is null;
create index idx_state_entries_user on state_entries (user_uid) where user_uid is not null and deleted_at is null;

create table integrations (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid) on delete cascade, -- Owning organization
  type                  text not null, -- Integration type: slack, discord, webhook, email, betterstack, etc.
  name                  text not null, -- Human-readable connection name
  enabled               integer not null default 1, -- Whether this connection actively sends notifications
  is_default            integer not null default 0, -- If true, auto-attach to new checks for notifications
  settings              text not null default '{}', -- Type-specific configuration as JSON (webhook URL, Slack channel, etc.)
  settings_private      text,
  settings_private_keys text,
  created_at            text not null default (datetime('now')),
  updated_at            text not null default (datetime('now')),
  deleted_at            text
);

create index idx_integrations_org_type on integrations (organization_uid, type) where deleted_at is null;
create index idx_integrations_org_default on integrations (organization_uid) where deleted_at is null and is_default = 1;

create table check_channels (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization (denormalized for query performance)
  check_uid         text not null references checks(uid) on delete cascade, -- Check that triggers notifications
  integration_uid   text not null references integrations(uid) on delete cascade, -- Integration connection to notify
  settings          text, -- Per-check override settings (e.g., Slack channel override)
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now'))
);

create unique index check_channels_check_integration_idx on check_channels (check_uid, integration_uid);
create index check_channels_integration_idx on check_channels (integration_uid);
create index check_channels_org_idx on check_channels (organization_uid);

create table status_pages (
  uid                text primary key,
  organization_uid   text not null references organizations(uid) on delete cascade, -- Owning organization
  name               text not null, -- Page title displayed to visitors
  slug               text not null check (slug is null or (length(slug) >= 3 and length(slug) <= 40)), -- URL-friendly identifier, unique per organization
  description        text, -- Subtitle or description shown on the page
  visibility         text not null default 'public' check (visibility in ('public', 'private')), -- Access control: public or private
  is_default         integer not null default 0, -- At most one default page per org
  enabled            integer not null default 1, -- Whether the page is accessible
  show_availability  integer not null default 1, -- Whether to display uptime percentage
  show_response_time integer not null default 1, -- Whether to display response time charts
  history_days       integer not null default 90, -- Number of days of history to display
  language           text, -- ISO language code (e.g., en, fr). NULL uses system default
  created_at         text not null default (datetime('now')),
  updated_at         text not null default (datetime('now')),
  deleted_at         text
);

create unique index status_pages_org_slug_idx on status_pages (organization_uid, slug) where deleted_at is null;
create unique index status_pages_org_default_idx on status_pages (organization_uid) where is_default = 1 and deleted_at is null;

create table status_page_sections (
  uid               text primary key,
  status_page_uid   text not null references status_pages(uid) on delete cascade, -- Parent status page
  name              text not null, -- Section heading displayed on the page
  slug              text not null check (slug is null or (length(slug) >= 3 and length(slug) <= 40)), -- URL-friendly identifier, unique per status page
  position          integer not null default 0, -- Display order (lower = higher on page)
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index status_page_sections_page_slug_idx on status_page_sections (status_page_uid, slug) where deleted_at is null;
create index status_page_sections_page_idx on status_page_sections (status_page_uid) where deleted_at is null;

create table status_page_resources (
  uid             text primary key,
  section_uid     text not null references status_page_sections(uid) on delete cascade, -- Parent section
  check_uid       text not null references checks(uid) on delete cascade, -- Check to display
  public_name     text, -- Override display name. NULL uses the check name
  explanation     text, -- Optional description visible on the public status page
  position        integer not null default 0, -- Display order within the section (lower = higher)
  created_at      text not null default (datetime('now')),
  updated_at      text not null default (datetime('now'))
);

create unique index status_page_resources_section_check_idx on status_page_resources (section_uid, check_uid);
create index status_page_resources_check_idx on status_page_resources (check_uid);

create table status_updates (
  uid              text primary key,
  organization_uid text not null references organizations(uid),
  status_page_uid  text not null references status_pages(uid),
  section_uid      text references status_page_sections(uid),
  check_uid        text references checks(uid),
  incident_uid     text references incidents(uid),
  title            text not null,
  body_markdown    text not null,
  link_url         text,
  kind             text not null check (kind in ('investigating', 'identified', 'monitoring', 'resolved', 'maintenance', 'info')),
  published_at     text not null default (datetime('now')),
  author_uid       text not null references users(uid),
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  deleted_at       text
);

create index idx_status_updates_org_page_pub on status_updates (organization_uid, status_page_uid, published_at desc);
create index idx_status_updates_incident on status_updates (incident_uid);
create index idx_status_updates_check on status_updates (check_uid);

create table status_page_subscriber (
  uid               text primary key,
  organization_uid  text not null references organizations(uid),
  status_page_uid   text not null references status_pages(uid),
  email             text not null,
  confirmed_at      text,
  confirm_token     text not null,
  unsubscribe_token text not null,
  scope             text not null check (scope in ('page', 'incident')),
  incident_uid      text references incidents(uid),
  created_at        text not null default (datetime('now')),
  deleted_at        text
);

create unique index idx_status_page_subscriber_confirm_token on status_page_subscriber (confirm_token);
create unique index idx_status_page_subscriber_unsub_token on status_page_subscriber (unsubscribe_token);
create index idx_status_page_subscriber_page_confirmed on status_page_subscriber (status_page_uid, confirmed_at);
create unique index idx_status_page_subscriber_live
  on status_page_subscriber (status_page_uid, email, scope, coalesce(incident_uid, ''))
  where deleted_at is null;

create table maintenance_windows (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  title             text not null, -- Maintenance window title shown in notifications and status pages
  description       text, -- Detailed description of the planned maintenance
  start_at          text not null, -- When the maintenance window begins
  end_at            text not null, -- When the maintenance window ends. Must be after start_at
  recurrence        text not null default 'none' check (recurrence in ('none', 'daily', 'weekly', 'monthly')), -- Recurrence pattern
  recurrence_end    text, -- When the recurring schedule stops. NULL means indefinite
  created_by        text, -- Identifier of the user or system that created this window
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text,
  check (end_at > start_at)
);

create index idx_mw_org on maintenance_windows (organization_uid) where deleted_at is null;
create index idx_mw_active on maintenance_windows (organization_uid, start_at, end_at) where deleted_at is null;

create table maintenance_window_checks (
  uid                    text primary key,
  maintenance_window_uid text not null references maintenance_windows(uid) on delete cascade, -- Parent maintenance window
  check_uid              text references checks(uid) on delete cascade, -- Individual check affected. NULL if targeting a group
  check_group_uid        text references check_groups(uid) on delete cascade, -- Check group affected. NULL if targeting an individual check
  created_at             text not null default (datetime('now')),
  check ((check_uid is not null and check_group_uid is null) or (check_uid is null and check_group_uid is not null))
);

create unique index idx_mwc_check on maintenance_window_checks (maintenance_window_uid, check_uid) where check_uid is not null;
create unique index idx_mwc_group on maintenance_window_checks (maintenance_window_uid, check_group_uid) where check_group_uid is not null;

create table incident_member_checks (
  incident_uid       text not null references incidents(uid) on delete cascade,
  check_uid          text not null references checks(uid) on delete cascade,
  joined_at          text not null default (datetime('now')),
  first_failure_at   text not null,
  last_failure_at    text not null,
  last_recovery_at   text,
  failure_count      integer not null default 1,
  currently_failing  integer not null default 1,
  primary key (incident_uid, check_uid)
);

create index idx_incident_member_checks_check on incident_member_checks (check_uid) where currently_failing = 1;

create table org_entitlements (
  uid               text primary key,
  organization_uid  text not null unique references organizations(uid) on delete cascade,
  payload           text not null default '{}',
  external_ref      text,
  expires_at        text,
  last_synced_at    text,
  metadata          text not null default '{}',
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now'))
);

create index org_entitlements_external_ref_idx on org_entitlements (external_ref);

create table org_entitlement_audits (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  source            text not null,
  actor             text not null,
  before_snapshot   text,
  after_snapshot    text not null,
  reason            text,
  created_at        text not null default (datetime('now'))
);

create index org_entitlement_audits_org_idx on org_entitlement_audits (organization_uid, created_at desc);

create table user_passkeys (
  uid                text primary key,
  user_uid           text not null references users(uid) on delete cascade,
  name               text not null,
  credential_id      blob not null,
  public_key         blob not null,
  aaguid             text,
  sign_count         integer not null default 0,
  transports         text,
  backup_eligible    integer not null default 0,
  backup_state       integer not null default 0,
  user_verified      integer not null default 0,
  attestation_format text,
  last_used_at       text,
  created_at         text not null default (datetime('now')),
  updated_at         text not null default (datetime('now')),
  deleted_at         text,
  unique (user_uid, credential_id)
);

create index idx_user_passkeys_credential_id on user_passkeys (credential_id);
create index idx_user_passkeys_user_uid_active on user_passkeys (user_uid) where deleted_at is null;

create table files (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  name              text not null,
  mime_type         text not null,
  size              integer not null,
  file_uri          text not null,
  sha256            text,
  created_by        text references users(uid) on delete set null,
  created_at        text not null default (datetime('now')),
  deleted_at        text
);

create index files_org_created_idx on files (organization_uid, created_at desc) where deleted_at is null;

create table membership_requests (
  uid              text primary key,
  organization_uid text not null references organizations(uid) on delete cascade,
  user_uid         text not null references users(uid) on delete cascade,
  message          text,
  status           text not null check (status in ('pending', 'approved', 'rejected', 'canceled')),
  decision_reason  text,
  decided_at       text,
  decided_by_uid   text references users(uid) on delete set null,
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  unique (organization_uid, user_uid)
);

create index membership_requests_org_status_idx on membership_requests (organization_uid, status);
create index membership_requests_user_status_idx on membership_requests (user_uid, status);

create table check_dependencies (
  uid              text primary key,
  organization_uid text not null references organizations(uid) on delete cascade,
  parent_check_uid text not null references checks(uid) on delete cascade,
  child_check_uid  text not null references checks(uid) on delete cascade,
  kind             text not null check (kind in ('hard', 'soft')),
  description      text,
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  deleted_at       text,
  check (parent_check_uid <> child_check_uid),
  unique (parent_check_uid, child_check_uid)
);

create index idx_check_dependencies_child on check_dependencies (child_check_uid) where deleted_at is null;
create index idx_check_dependencies_parent on check_dependencies (parent_check_uid) where deleted_at is null;
create index idx_check_dependencies_org on check_dependencies (organization_uid) where deleted_at is null;

create table user_contacts (
  uid              text primary key,
  user_uid         text not null references users(uid) on delete cascade,
  organization_uid text not null references organizations(uid) on delete cascade,
  type             text not null,
  value            text not null,
  label            text not null default '',
  verified_at      text,
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  deleted_at       text,
  unique (user_uid, organization_uid, type, value)
);

create index idx_uc_user_org on user_contacts (user_uid, organization_uid) where deleted_at is null;

create table user_notification_routes (
  uid          text primary key,
  user_uid     text not null references users(uid) on delete cascade,
  org_uid      text not null references organizations(uid) on delete cascade,
  contact_uid  text not null references user_contacts(uid) on delete cascade unique,
  enabled      integer not null default 1,
  position     integer not null default 0,
  created_at   text not null default (datetime('now')),
  updated_at   text not null default (datetime('now'))
);

create unique index idx_unr_contact on user_notification_routes (contact_uid);
create index idx_unr_user_org on user_notification_routes (user_uid, org_uid);

create table discovered_hosts (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid),
  job_uid               text not null references jobs(uid),
  ip                    text not null,
  hostname              text,
  open_ports            text not null default '[]',
  icmp_reachable        integer not null default 0,
  suggested_checks      text not null default '[]',
  promoted_to_check_uid text references checks(uid),
  discovered_at         text not null default (datetime('now')),
  deleted_at            text,
  source                text not null default 'lan'
);

create unique index idx_discovered_hosts_org_ip_source_active on discovered_hosts (organization_uid, ip, source)
  where deleted_at is null and promoted_to_check_uid is null;
create index idx_discovered_hosts_org_job on discovered_hosts (organization_uid, job_uid) where deleted_at is null;

create table incident_notifications (
  uid              text primary key,
  organization_uid text not null references organizations(uid) on delete cascade,
  incident_uid     text not null references incidents(uid) on delete cascade,
  event_type       text not null,
  step_uid         text references escalation_policy_steps(uid) on delete set null,
  repeat_index     integer,
  source           text not null,
  user_uid         text references users(uid) on delete set null,
  connection_uid   text references integrations(uid) on delete set null,
  channel_type     text not null,
  status           text not null,
  skip_reason      text,
  error            text,
  job_uid          text,
  message_id       text,
  delivery_details text,
  created_at       text not null default (datetime('now')),
  sent_at          text,
  cancelled_at     text,
  failed_at        text
);

create index idx_in_incident on incident_notifications (incident_uid, created_at desc);
create index idx_in_user on incident_notifications (user_uid, created_at desc) where user_uid is not null;
create index idx_in_org_time on incident_notifications (organization_uid, created_at desc);
create index idx_in_job on incident_notifications (job_uid) where job_uid is not null;

create table app_settings (
  key        text primary key,
  value      text not null,
  updated_at text not null default (datetime('now'))
);
