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

## Serving headers

Every route that streams stored bytes (`/content` and the public routes,
including the org-logo route in [orgs.md](orgs.md)) goes through one helper, so
the security headers cannot drift:

- `X-Content-Type-Options: nosniff` — always. The stored MIME type comes from
  the uploader's multipart part header, so a browser must never be allowed to
  second-guess it.
- `Content-Disposition: inline` only for a raster-image allowlist (`png`,
  `jpeg`, `gif`, `webp`, `avif`, `bmp`); **everything else is `attachment`**.
  `image/svg+xml` is deliberately excluded: an SVG is an XML document that can
  carry `<script>`, so serving an uploaded one as an inline document would be
  stored XSS on the application's own origin. Subresource loads are unaffected —
  an `<img src="…svg">` still renders, and scripts inside an SVG referenced that
  way never execute.
- The filename is stripped of quotes, backslashes and control characters before
  it is interpolated into the header.
