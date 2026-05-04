-- Bump check slug max length from 40 to 50.
-- SQLite cannot ALTER an inline CHECK constraint, so we patch the table
-- definition stored in sqlite_master. This is the SQLite-recommended
-- approach when the schema change is purely a constraint relaxation that
-- preserves existing rows.
PRAGMA writable_schema = 1;
UPDATE sqlite_master
  SET sql = REPLACE(sql, 'length(slug) <= 40', 'length(slug) <= 50')
  WHERE type = 'table' AND name = 'checks';
PRAGMA writable_schema = 0;
