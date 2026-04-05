-- Reverse the result status reordering
-- Current: 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error
-- Restore: 0=initial, 1=up, 2=down, 3=timeout, 4=error, 5=running

UPDATE results SET status = CASE status
  WHEN 1 THEN 100  -- created → initial (0)
  WHEN 2 THEN 105  -- running (2 → 5)
  WHEN 3 THEN 101  -- up (3 → 1)
  WHEN 4 THEN 102  -- down (4 → 2)
  WHEN 5 THEN 103  -- timeout (5 → 3)
  WHEN 6 THEN 104  -- error (6 → 4)
  ELSE status
END
WHERE status IN (1, 2, 3, 4, 5, 6);

UPDATE results SET status = status - 100 WHERE status >= 100 AND status <= 105;

-- Reverse checks.status
-- Current: 1=created, 3=up, 4=down, 7=degraded
-- Restore: 0=unknown, 1=up, 2=down, 3=degraded
UPDATE checks SET status = CASE status
  WHEN 1 THEN 100  -- created → unknown (1 → 0)
  WHEN 3 THEN 101  -- up (3 → 1)
  WHEN 4 THEN 102  -- down (4 → 2)
  WHEN 7 THEN 103  -- degraded (7 → 3)
  ELSE status
END
WHERE status IN (1, 3, 4, 7);

UPDATE checks SET status = status - 100 WHERE status >= 100 AND status <= 103;
