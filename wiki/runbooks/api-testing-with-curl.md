# API testing with curl

Read when exercising the REST API by hand against a local server.

## Quick Start
The easiest way to test the API is to get a JWT token and save it to a file:

```bash
# 1. Login and save token to file (org is optional in body)
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.io","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' \
  | jq -r '.accessToken' > /tmp/token.txt

# 2. View the token (optional)
cat /tmp/token.txt

# 3. Use the token in subsequent requests
TOKEN=$(cat /tmp/token.txt)
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

## Common API Examples

**List all checks:**
```bash
curl -s -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

**Create a check:**
```bash
curl -s -X POST \
  -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Google","slug":"google","type":"http","config":{"url":"https://google.com"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

**Get check by UID or slug:**
```bash
# By UID
curl -s -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  'http://localhost:4000/api/v1/orgs/default/checks/63d49e55-97e3-4e8c-b7ab-c862de7a43f3' | jq '.'

# By slug
curl -s -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  'http://localhost:4000/api/v1/orgs/default/checks/google' | jq '.'
```

**Update a check:**
```bash
curl -s -X PATCH \
  -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Google Updated"}' \
  'http://localhost:4000/api/v1/orgs/default/checks/google' | jq '.'
```

**Delete a check:**
```bash
curl -s -X DELETE \
  -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  'http://localhost:4000/api/v1/orgs/default/checks/google'
```

## Tips for curl Testing

1. **Always save tokens to files** to avoid shell parsing issues with `$(...)` substitutions
2. **Use single-line commands** - avoid backslash line continuations in complex shells
3. **Pipe to jq** for pretty-printed JSON responses
4. **Use `-s` flag** to suppress curl progress output
5. **Check HTTP status** with `-w "\nHTTP: %{http_code}\n"` when needed

**Example with inline token (single line):**
```bash
curl -s -H "Authorization: Bearer eyJhbGci..." 'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

## Default Credentials
- **Email**: `admin@solidping.io`
- **Password**: `solidpass`
- **Organization**: `default`

On a **fresh database** this account is seeded with `must_change_password`, so
the login above succeeds but every other endpoint answers `403` /
`PASSWORD_CHANGE_REQUIRED` until you rotate:

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"currentPassword":"solidpass","newPassword":"something-else"}' \
  'http://localhost:4000/api/v1/auth/change-password'
```

The flag is a general user-level capability (`internal/handlers/auth/password_rotation.go`),
enforced in `RequireAuth`, `RequireMCPAuth` and the realtime WebSocket handshake —
not a special case for the seeded admin. Test mode (`test@test.com`) is not flagged.

## Troubleshooting
- If token expires, re-run the login command to get a fresh token
- Check server is running: `curl -s http://localhost:4000/api/mgmt/health`
- Enable debug logging: `LOG_LEVEL=debug ./solidping serve`
