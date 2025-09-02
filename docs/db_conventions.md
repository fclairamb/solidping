# Table conventions
- All entities have a `uid` UUID (external/API)
- All entities except global ones have an `organization_uid` reference
- All tables should be in plural (`organizations`, `users`, `checks`)
- All link tables should have the plural-plural forms (`properties_assets` that links `properties` and `assets`)
- Soft deletes: `deleted_at` timestamp, never hard delete
- Audit trail: `created_at` and `updated_at` timestamps

# Indexes conventions
Indexes should have the name `${table}_${columns}_idx`, the columns should not contain `_uid`.
So for example `incidents_organization`.
