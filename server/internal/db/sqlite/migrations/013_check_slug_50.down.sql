PRAGMA writable_schema = 1;
UPDATE sqlite_master
  SET sql = REPLACE(sql, 'length(slug) <= 50', 'length(slug) <= 40')
  WHERE type = 'table' AND name = 'checks';
PRAGMA writable_schema = 0;
