-- migrate: up
-- Rename the legacy webhook_url key to url in integration_connections.settings
-- for rows where type = 'webhook'. The backend notification sender reads from
-- settings['url']; older rows stored the value under settings['webhook_url'].
UPDATE integration_connections
SET settings = (settings - 'webhook_url') || jsonb_build_object('url', settings->>'webhook_url')
WHERE type = 'webhook'
  AND settings ? 'webhook_url'
  AND settings->>'webhook_url' <> '';
