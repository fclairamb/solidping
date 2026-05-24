DROP INDEX IF EXISTS idx_discovered_hosts_org_ip_source_active;
CREATE UNIQUE INDEX idx_discovered_hosts_org_ip_active
    ON discovered_hosts (organization_uid, ip)
    WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL;
ALTER TABLE discovered_hosts DROP COLUMN source;
