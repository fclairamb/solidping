Ensure SQLite matches PostgreSQL implementation.

**Verify**:
1. Migration files have SQLite equivalents with matching `NNN` prefix (both engines share the same sequence number per release)
2. SQL queries work on both databases
3. Data types are compatible
4. Tests run on both backends

**At release time (consolidation)**: both engines are squashed together into `NNN_vX_Y_Z.up.sql` / `.down.sql`, which re-syncs their numbering. The scratch migrations in both dirs are deleted at the same time.
