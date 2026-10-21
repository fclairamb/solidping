-- v0.41.0 — SQLite rollback (parity only; downs are never run in production).

-- SECTION: user-preferences
alter table users drop column preferences;
