-- Add the average response time per aggregation bucket so the response-time
-- graph badge row can plot mean latency. Nullable: raw rows and buckets with
-- no measured durations leave it NULL. No backfill (the product is not live).
ALTER TABLE results ADD COLUMN duration_avg real;

comment on column results.duration_avg is 'Average duration in this period (aggregated only).';
