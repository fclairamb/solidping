-- Reorder result status values to follow lifecycle order:
-- 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error
--
-- Old mapping: 0=initial, 1=up, 2=down, 3=timeout, 4=error, 5=running, 6=created
-- New mapping: 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error

-- Use offset +100 to avoid collisions during remapping
UPDATE results SET status = CASE status
  WHEN 0 THEN 101  -- initial → created (1)
  WHEN 1 THEN 103  -- up (1 → 3)
  WHEN 2 THEN 104  -- down (2 → 4)
  WHEN 3 THEN 105  -- timeout (3 → 5)
  WHEN 4 THEN 106  -- error (4 → 6)
  WHEN 5 THEN 102  -- running (5 → 2)
  WHEN 6 THEN 101  -- created (6 → 1)
  ELSE status
END
WHERE status IN (0, 1, 2, 3, 4, 5, 6);

UPDATE results SET status = status - 100 WHERE status >= 100 AND status <= 106;

-- Also update checks.status
-- Old: 0=unknown, 1=up, 2=down, 3=degraded
-- New: 0=unknown, 3=up, 4=down, 7=degraded
UPDATE checks SET status = CASE status
  WHEN 1 THEN 103  -- up (1 → 3)
  WHEN 2 THEN 104  -- down (2 → 4)
  WHEN 3 THEN 107  -- degraded (3 → 7)
  ELSE status
END
WHERE status IN (1, 2, 3);

UPDATE checks SET status = status - 100 WHERE status >= 103 AND status <= 107;
