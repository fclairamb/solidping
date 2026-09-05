---
sidebar_position: 2
title: Database
---

# Database Configuration

SolidPing supports multiple database backends. PostgreSQL is recommended for production, while SQLite is great for testing and small deployments.

## Database Types

| Type | Description | Use Case |
|------|-------------|----------|
| `postgres` | External PostgreSQL | Production, multi-instance |
| `sqlite` | SQLite file database | Single instance, simple deployments |
| `sqlite-memory` | In-memory SQLite | Testing only |
| `postgres-embedded` | Embedded PostgreSQL | Testing only |

:::note `postgres-embedded` connection ceiling
The embedded PostgreSQL server starts with `max_connections=10` (3 reserved for
superusers), and SolidPing bounds its own client pool to 5 connections against it.
Both numbers are fixed and not configurable via `SP_DB_MAX_OPEN_CONNS` or similar —
this mode is sized for tests and light local use, not for meaningful concurrent load.
:::

## PostgreSQL (Recommended)

PostgreSQL 15+ is recommended for production deployments.

### Configuration

```bash
SP_DB_TYPE=postgres
SP_DB_URL=postgresql://user:password@host:port/database?sslmode=disable
```

### Connection String Format

```
postgresql://[user]:[password]@[host]:[port]/[database]?[options]
```

**Options:**
- `sslmode=disable` - No SSL (development only)
- `sslmode=require` - Require SSL
- `sslmode=verify-full` - Verify SSL certificate

### Examples

```bash
# Local development
SP_DB_URL=postgresql://solidping:solidping@localhost:5432/solidping?sslmode=disable

# Docker network
SP_DB_URL=postgresql://solidping:password@postgres:5432/solidping?sslmode=disable

# Cloud provider with SSL
SP_DB_URL=postgresql://user:password@db.example.com:5432/solidping?sslmode=require

# With connection pooling
SP_DB_URL=postgresql://user:password@pgbouncer:6432/solidping?sslmode=disable
```

:::caution PgBouncer and LISTEN/NOTIFY
SolidPing keeps one PostgreSQL `LISTEN` session per API instance for
[live dashboard updates](../features/live-updates.md) and job wake-ups.
`LISTEN` binds notification delivery to the database session, so **PgBouncer
in transaction-pooling mode is not supported** — use session pooling or point
SolidPing at PostgreSQL directly. With transaction pooling, live updates and
instant job pickup degrade to polling.
:::

### PostgreSQL Setup

Create the database and user:

```sql
-- Create user
CREATE USER solidping WITH PASSWORD 'your-secure-password';

-- Create database
CREATE DATABASE solidping OWNER solidping;

-- Grant privileges
GRANT ALL PRIVILEGES ON DATABASE solidping TO solidping;
```

### Recommended PostgreSQL Settings

For production, consider these PostgreSQL settings in `postgresql.conf`:

```ini
# Memory
shared_buffers = 256MB
effective_cache_size = 768MB
work_mem = 16MB

# Connections
max_connections = 100

# Write performance
wal_buffers = 16MB
checkpoint_completion_target = 0.9

# Query planning
random_page_cost = 1.1  # For SSDs
```

## SQLite

SQLite is suitable for single-instance deployments or testing.

### Configuration

```bash
SP_DB_TYPE=sqlite
SP_DB_DIR=/path/to/data/directory
```

The database file will be created at `$SP_DB_DIR/solidping.db`.

### Examples

```bash
# Current directory
SP_DB_TYPE=sqlite
SP_DB_DIR=.

# Specific directory
SP_DB_TYPE=sqlite
SP_DB_DIR=/var/lib/solidping

# Docker volume
SP_DB_TYPE=sqlite
SP_DB_DIR=/data
```

### SQLite Limitations

- Single writer at a time (concurrent reads are fine)
- Not suitable for distributed deployments
- Limited to ~1TB database size
- No built-in replication

### When to Use SQLite

- Single-instance deployments
- Development and testing
- Low to medium check volume (under 1000 checks/minute)
- Simple infrastructure requirements

## Migrations

Migrations run automatically on startup. You can also run them manually:

```bash
./solidping migrate
```

### Migration Commands

```bash
# Run pending migrations
./solidping migrate up

# Rollback last migration
./solidping migrate down

# Check migration status
./solidping migrate status
```

## Backup and Restore

### PostgreSQL

```bash
# Backup
pg_dump -U solidping -h localhost solidping > backup.sql

# Restore
psql -U solidping -h localhost solidping < backup.sql
```

### SQLite

```bash
# Backup (while running - uses SQLite backup API)
sqlite3 /data/solidping.db ".backup '/backup/solidping-$(date +%Y%m%d).db'"

# Simple copy (stop the server first)
cp /data/solidping.db /backup/solidping-backup.db
```

## Troubleshooting

### Connection Issues

```bash
# Test PostgreSQL connection
psql -U solidping -h localhost -d solidping -c "SELECT 1"

# Check if port is open
nc -zv localhost 5432
```

### Permission Errors

For PostgreSQL:
```sql
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO solidping;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO solidping;
```

For SQLite:
```bash
# Ensure the directory is writable
chmod 755 /data
chown solidping:solidping /data
```

### Performance

If experiencing slow queries:

1. Check database size: `SELECT pg_database_size('solidping');`
2. Run VACUUM: `VACUUM ANALYZE;`
3. Check for missing indexes
4. Review connection pool settings
