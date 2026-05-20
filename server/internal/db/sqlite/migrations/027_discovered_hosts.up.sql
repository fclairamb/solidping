CREATE TABLE discovered_hosts (
    uid                    TEXT PRIMARY KEY,
    organization_uid       TEXT NOT NULL REFERENCES organizations(uid),
    job_uid                TEXT NOT NULL REFERENCES jobs(uid),
    ip                     TEXT NOT NULL,
    hostname               TEXT,
    open_ports             TEXT NOT NULL DEFAULT '[]',
    icmp_reachable         INTEGER NOT NULL DEFAULT 0,
    suggested_checks       TEXT NOT NULL DEFAULT '[]',
    promoted_to_check_uid  TEXT REFERENCES checks(uid),
    discovered_at          TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at             TEXT
);

CREATE INDEX idx_discovered_hosts_org_job ON discovered_hosts (organization_uid, job_uid)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX idx_discovered_hosts_org_ip_active ON discovered_hosts (organization_uid, ip)
    WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL;
