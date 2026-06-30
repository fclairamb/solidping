-- Rollback for the status-page history_period enum column.

alter table status_pages drop column if exists history_period;
