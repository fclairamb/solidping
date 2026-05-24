-- Add a discovery-source discriminator to discovered_hosts. The
-- NOT NULL DEFAULT 'lan' backfills every existing row to the LAN scanner,
-- the only source that wrote this table before this migration.
ALTER TABLE discovered_hosts ADD COLUMN source TEXT NOT NULL DEFAULT 'lan';

-- Widen the active-host uniqueness to be per-source so the same IP can be
-- tracked independently by the LAN scanner and by Freebox discovery.
DROP INDEX IF EXISTS idx_discovered_hosts_org_ip_active;
CREATE UNIQUE INDEX idx_discovered_hosts_org_ip_source_active
    ON discovered_hosts (organization_uid, ip, source)
    WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL;
