-- Renumber status_page_resources and status_page_sections so each row in a
-- group has a distinct, sequential position. Earlier creates all defaulted to
-- position=0 which made the swap-based reorder UI a no-op.
--
-- Idempotent: re-running on already-renumbered data produces the same result
-- because ROW_NUMBER is deterministic given the (created_at, uid) ordering.

UPDATE status_page_resources r
SET position = sub.rn
FROM (
  SELECT uid,
         ROW_NUMBER() OVER (
           PARTITION BY section_uid
           ORDER BY created_at, uid
         ) AS rn
  FROM status_page_resources
) sub
WHERE r.uid = sub.uid;

UPDATE status_page_sections s
SET position = sub.rn
FROM (
  SELECT uid,
         ROW_NUMBER() OVER (
           PARTITION BY status_page_uid
           ORDER BY created_at, uid
         ) AS rn
  FROM status_page_sections
  WHERE deleted_at IS NULL
) sub
WHERE s.uid = sub.uid;
