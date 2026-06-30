-- Rollback for the status-page history_period enum column (SQLite).

alter table status_pages drop column history_period;
