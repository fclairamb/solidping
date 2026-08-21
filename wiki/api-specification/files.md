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

## Attachments (`files.topic` / `files.details`)

A `files` row can say what it is attached to (spec 2026-08-21-01):

- `topic` — a path-like attachment key, `<entity>/<uid>/<kind>`, e.g.
  `incidents/<uid>/screenshot`. `NULL` for every file that is not an attachment
  (org logos, feedback screenshots), which is the common case.
- `details` — a free JSON metadata bag for the attachment kind. For a screenshot:
  `capturedAt`, `region`, `checkUid`, `trigger`.

The path shape makes both accesses the feature needs cheap on one partial index
(`files_org_topic_idx`): an EXACT match lists one entity's attachments of one
kind; a PREFIX match (`incidents/<uid>/`) reaps everything hanging off an entity
when it is deleted. The trailing slash is load-bearing — without it,
`incidents/abc/` would also match incident `abcdef`.

Attachments are reachable two ways: the owning entity's detail endpoint embeds
them with a short-lived **signed** download URL (see
[results-incidents.md](results-incidents.md)), and a deported agent writes one
through `POST /api/v1/agent/attachments` (see [agents.md](agents.md)).

**Never public.** An attachment is org-operational evidence exactly like
`incidents.details` — an incident screenshot is a picture of whatever the
failing page showed. Neither the attachment nor its signed URL may appear on a
status page or in a subscriber payload; a structural audit
(`internal/handlers/statuspages/details_never_public_test.go`) pins that.

**GC.** The periodic state-cleanup job sweeps attachment rows whose
`incidents/<uid>/…` topic points at an incident that no longer exists, after a
grace window. Without a reaper, every deleted entity would leave its blob on the
storage bill forever.

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
