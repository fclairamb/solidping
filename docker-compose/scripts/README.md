# Local PostgreSQL Setup

This directory contains scripts for setting up a local PostgreSQL instance for development.

## Quick Start

Start the local PostgreSQL instance:

```bash
docker-compose -f docker-compose-local.yml up -d
```

Stop the instance:

```bash
docker-compose -f docker-compose-local.yml down
```

Remove the instance and data:

```bash
docker-compose -f docker-compose-local.yml down -v
```

## Configuration

- **Host**: localhost
- **Port**: 55432
- **Database**: solidping
- **Admin User**: postgres / postgres
- **App User**: soliduser / solidpass

## Connection Strings

### For the application (soliduser):
```
postgresql://soliduser:solidpass@localhost:55432/solidping?sslmode=disable
```

### For admin tasks (postgres):
```
postgresql://postgres:postgres@localhost:55432/solidping?sslmode=disable
```

## Connecting with psql

```bash
# As soliduser
psql -h localhost -p 55432 -U soliduser -d solidping

# As postgres admin
psql -h localhost -p 55432 -U postgres -d solidping
```

## Health Check

Check if the database is ready:

```bash
docker exec solidping-postgres-local pg_isready -U postgres
```
