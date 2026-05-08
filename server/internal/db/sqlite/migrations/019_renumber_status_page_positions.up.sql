-- Renumber status_page_resources and status_page_sections so each row in a
-- group has a distinct, sequential position. Earlier creates all defaulted to
-- position=0 which made the swap-based reorder UI a no-op.
--
-- Idempotent: re-running on already-renumbered data produces the same result
-- because ROW_NUMBER is deterministic given the (created_at, uid) ordering.
--
-- Requires SQLite 3.33+ (UPDATE … FROM) and 3.25+ (window functions). The
-- bundled modernc.org/sqlite driver provides both.

UPDATE status_page_resources AS r
SET position = sub.rn
FROM (
  SELECT uid,
         ROW_NUMBER() OVER (
           PARTITION BY section_uid
           ORDER BY created_at, uid
         ) AS rn
  FROM status_page_resources
) AS sub
WHERE r.uid = sub.uid;

UPDATE status_page_sections AS s
SET position = sub.rn
FROM (
  SELECT uid,
         ROW_NUMBER() OVER (
           PARTITION BY status_page_uid
           ORDER BY created_at, uid
         ) AS rn
  FROM status_page_sections
  WHERE deleted_at IS NULL
) AS sub
WHERE s.uid = sub.uid;
