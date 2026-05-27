CREATE TABLE discovered_hosts (
    uid                    UUID PRIMARY KEY,
    organization_uid       UUID NOT NULL REFERENCES organizations(uid),
    job_uid                UUID NOT NULL REFERENCES jobs(uid),
    ip                     INET NOT NULL,
    hostname               TEXT,
    open_ports             JSONB NOT NULL DEFAULT '[]'::jsonb,
    icmp_reachable         BOOLEAN NOT NULL DEFAULT FALSE,
    suggested_checks       JSONB NOT NULL DEFAULT '[]'::jsonb,
    promoted_to_check_uid  UUID REFERENCES checks(uid),
    discovered_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at             TIMESTAMPTZ
);

CREATE INDEX idx_discovered_hosts_org_job ON discovered_hosts (organization_uid, job_uid)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX idx_discovered_hosts_org_ip_active ON discovered_hosts (organization_uid, ip)
    WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL;
