Question:
I wonder if we should drop SQLite support. Because this database lacks lots of features and forces me to use a lot of workarounds.

I like the idea of having a tool that starts instantly and doesn't consume any memory.

---

## Analysis & Feedback

After analyzing the codebase, here's the **maintenance burden** of SQLite support:

### Current Technical Debt

**Code Duplication**: ~1,200 lines of SQLite-specific code
- `back/internal/db/sqlite/sqlite.go`: 982 lines (nearly identical to postgres.go's 986 lines)
- All 89 `db.Service` interface methods duplicated across both implementations
- Separate migration files that must stay in sync

**Workarounds Required**:

1. **Type System Limitations** (in migrations):
   - No native UUID → stored as `text`
   - No `timestamptz` → stored as `text` (ISO8601)
   - No `interval` → stored as `text` (e.g., `'00:01:00'`)
   - No `jsonb` → stored as `text` (no indexing support)
   - No arrays → requires workarounds
   - No boolean → uses `integer` (0/1)

2. **SQL Dialect Differences**:
   - No `DISTINCT ON` → manual deduplication in Go code (sqlite.go:823-829)
   - No `IS DISTINCT FROM` → must use `!= OR IS NULL` workaround (sqlite.go:871)
   - No `COMMENT ON` → 39 table/column comments lost
   - No regex in CHECK constraints → weaker validation

3. **Feature Gaps**:
   - No `LISTEN/NOTIFY` → falls back to polling-based notifier (checkrunner/notifier/notifier.go:40-45)
   - Single writer limitation → `SetMaxOpenConns(1)` (sqlite.go:75)
   - Must explicitly enable foreign keys via pragma

4. **Maintenance Overhead**:
   - Dedicated sync command (`.claude/commands/sync-pg-to-sqlite.md`)
   - 2x migration files for every schema change
   - Must test both implementations separately
   - Type conversion bugs harder to catch

### The Trade-offs

**Reasons to Keep SQLite**:
- ✅ **Fast tests**: In-memory SQLite is used extensively in test suite
- ✅ **Zero setup**: No Docker/PostgreSQL required for quick evaluation
- ✅ **Low resource usage**: Minimal memory footprint
- ✅ **Instant startup**: No database server to wait for
- ✅ **Simpler deployment**: Single binary deployment option

**Reasons to Drop SQLite**:
- ❌ **Maintenance burden**: ~1,200 lines to maintain + migration sync
- ❌ **Technical limitations**: Limited scalability, no real-time events
- ❌ **Query workarounds**: Manual deduplication, SQL dialect differences
- ❌ **Type system friction**: All timestamps/UUIDs/JSON stored as text
- ❌ **Documentation gap**: Lost 39 schema comments
- ❌ **Testing complexity**: 2x implementations to test

### Recommendation

**Consider a hybrid approach**:

1. **For Production**: PostgreSQL only
   - Simplifies codebase to ~986 lines (50% reduction)
   - Eliminates all migration sync overhead
   - Better performance, real-time events (LISTEN/NOTIFY)
   - Use embedded PostgreSQL for single-binary deployment if needed

2. **For Tests**: Keep in-memory SQLite OR use embedded Postgres
   - Current tests rely heavily on fast in-memory SQLite
   - Alternative: Use testcontainers with PostgreSQL (more realistic but slower)
   - Alternative: Embedded PostgreSQL libraries exist (e.g., embedded-postgres-binaries)

3. **For Quick Evaluation**: Docker Compose
   - Already have `docker-compose.yml` with PostgreSQL
   - Simple one-liner: `docker-compose up -d`
   - Can add embedded PostgreSQL mode later if single-binary is critical

### Migration Path (if dropping SQLite)

```bash
# 1. Remove SQLite implementation
rm -rf back/internal/db/sqlite/

# 2. Simplify config
# Remove: sqlite, sqlite-memory from supported DB types

# 3. Update tests
# Replace sqlite-memory with testcontainers PostgreSQL

# 4. Update notifier
# Remove SQLite-specific polling fallback

# 5. Clean up
# Remove sync command, update docs
```

**Estimated effort**: 2-3 hours to remove, but eliminates ongoing maintenance burden.

### Final Verdict

**Drop SQLite** unless single-binary deployment with zero dependencies is a hard requirement for your target users. The maintenance burden (~50% extra code + migration sync) outweighs the convenience benefits, especially since you already have Docker Compose setup.

**Note on Embedded PostgreSQL**: While it eliminates SQL dialect workarounds, embedded PostgreSQL libraries have a **major limitation for upgrades**. They handle version changes by reinitializing (deleting and recreating) the database rather than using pg_upgrade. For major version upgrades, you'd need to:
1. Dump data from the old version
2. Allow the library to reinitialize with the new version
3. Restore the data from the dump

This means **embedded PostgreSQL may not be suitable for production deployments** where seamless major version upgrades are important. For production use, stick with Docker/external PostgreSQL which supports proper pg_upgrade workflows.
