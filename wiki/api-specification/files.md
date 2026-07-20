# Files

Generic file storage. Bytes live behind a pluggable backend (local FS or S3); metadata lives in the `files` table. Authenticated read/list/delete are scoped to the requesting organization. Public access is via signed URL only.

### GET /api/v1/orgs/:org/files
List files for an organization. Query: `q`, `limit`, `offset`. Auth: required.

### GET /api/v1/orgs/:org/files/:uid
Get file metadata. Auth: required.

### GET /api/v1/orgs/:org/files/:uid/content
Stream file bytes (org-scoped). Auth: required.

### DELETE /api/v1/orgs/:org/files/:uid
Soft-delete a file (the blob in storage is left in place). Auth: required.

### GET /pub/files/:uid?exp=&sig=
Public read via HMAC-signed URL. `exp` (unix seconds) and `sig` are required. Returns 403 on bad signature, 410 on expired, 404 on unknown / soft-deleted file. Auth: public (signature gates access).
