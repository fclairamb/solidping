#!/bin/bash
set -e

# Create the solidping database, solidping user, and grant permissions
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE solidping;
    CREATE USER solidping WITH PASSWORD 'solidping';
    GRANT ALL PRIVILEGES ON DATABASE solidping TO solidping;
EOSQL

# Connect to solidping database to set schema permissions
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "solidping" <<-EOSQL
    -- Grant schema permissions (PostgreSQL 15+)
    GRANT ALL ON SCHEMA public TO solidping;

    -- Grant default privileges for future tables
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO solidping;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO solidping;
EOSQL

echo "Database initialization complete: 'solidping' database created, user 'solidping' granted access"
