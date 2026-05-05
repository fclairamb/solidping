-- Remove the dead "from" key from email connection settings. The frontend
-- used to expose a per-channel From address input that never reached the
-- SMTP layer; the field has now been removed from the UI. Stripping the
-- saved key here prevents it from re-surfacing if a future feature reads
-- settings.from. Idempotent.

UPDATE integration_connections
   SET settings = settings - 'from'
 WHERE type = 'email' AND settings ? 'from';
