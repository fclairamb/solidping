-- The forward migration only renumbers existing data; the original positions
-- (all zero) are not preserved. Rolling back to position=0 restores the
-- pre-migration state best-effort.
UPDATE status_page_resources SET position = 0;
UPDATE status_page_sections SET position = 0 WHERE deleted_at IS NULL;
