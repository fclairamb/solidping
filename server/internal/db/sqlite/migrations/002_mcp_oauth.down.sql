-- Rollback for the MCP OAuth 2.1 authorization server tables (SQLite).
drop table if exists oauth_refresh_tokens;
drop table if exists oauth_auth_codes;
drop table if exists oauth_clients;
