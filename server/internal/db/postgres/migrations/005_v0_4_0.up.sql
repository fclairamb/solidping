-- solidping v0.4.0 — drop the unused slug from escalation_policies and
-- on_call_schedules (spec 2026-07-11-10). Both resources are now addressed by
-- uid only: the slug was never public (all routes sit behind RequireAuth, and
-- neither resource appears on status pages, in the OpenAPI contract, or in the
-- MCP tools). DROP COLUMN also drops the dependent (organization_uid, slug)
-- unique constraint automatically.
alter table escalation_policies drop column slug;
alter table on_call_schedules drop column slug;
