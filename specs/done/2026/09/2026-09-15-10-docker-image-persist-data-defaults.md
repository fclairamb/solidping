---
model: sonnet
effort: medium
---

# The published Docker image loses its data unless three env vars are set

## Problem

The intended one-liner is:

```bash
docker run -p 4000:4000 -v solidping-data:/data ghcr.io/fclairamb/solidping
```

Today that command boots but persists nothing. The final stage of `Dockerfile`
(`Dockerfile:118-129`) is `gcr.io/distroless/base-debian13:nonroot` with
`WORKDIR /app`, no `ENV`, no `VOLUME`. The config defaults are
`Type: DatabaseTypeSQLite, Dir: "."` (`server/internal/config/config.go:1616-1617`)
and `LocalRoot: "./data/files"` (`config.go:1682`). So the SQLite file lands in
`/app` and uploads (org logos, status-page assets, screenshots) land in
`/app/data/files`. Neither is under the mounted `/data`. Both vanish on the next
`docker run`.

Every doc works around it by hand:

- `web/docs/docs/intro.md:80-83` quickstart passes `-e SP_DB_TYPE=sqlite -e SP_DB_DIR=/data -e SP_FILESTORAGE_LOCAL_ROOT=/data/files`.
- `web/docs/docs/installation/docker.md:17-27` (SQLite) and `:30-40` (Postgres) do the same.
- `web/docs/docs/installation/docker.md:56-60` env table documents the binary defaults (`.` and `./data/files`) as if they were the image's.
- `web/docs/docs/installation/docker-compose.md:39-44` and the compose example in `docker.md:89-93` set `SP_FILESTORAGE_LOCAL_ROOT` explicitly.
- `web/docs/docs/configuration/file-storage.md:29-41` explains the relative-path trap at length.

A user who copies the image name from GitHub and skips the docs gets silent
data loss. That is the wrong default for a monitoring tool.

## Proposal

Make the image self-describing: the container's defaults point at `/data`,
the directory exists and is owned by the runtime user, and the docs shrink to
the one-liner.

### 1. Dockerfile

In the final stage:

```dockerfile
ENV SP_DB_TYPE=sqlite \
    SP_DB_DIR=/data \
    SP_FILESTORAGE_LOCAL_ROOT=/data/files
VOLUME /data
```

`/data` must exist in the image and be writable by `nonroot` (uid/gid 65532).
Distroless has no shell, so it cannot be created in the final stage. Create
`/data/files` in `backend-builder` (`RUN mkdir -p /data/files`) and copy it
across with `COPY --from=backend-builder --chown=65532:65532 /data /data`.
This matters because Docker seeds a fresh named volume from the image
directory's content and ownership. Without the pre-created directory Docker
creates `/data` as `root:root` and the nonroot process gets `permission denied`
on first write.

Keep `SP_DB_TYPE=sqlite` even though it is already the config default. It
documents intent in `docker inspect` and stays correct if the binary default
ever changes.

Bind mounts (`-v ./data:/data`) keep the host directory's ownership. Add one
sentence to `docker.md` saying to `chown 65532:65532` the host directory or
run with `--user`.

### 2. Docs and compose

- `intro.md` quickstart becomes the bare one-liner. Replace the
  `SP_FILESTORAGE_LOCAL_ROOT` paragraph with one sentence: the image stores the
  database and uploads under `/data`, mount a volume there. Keep the link to
  File Storage for S3.
- `installation/docker.md`: SQLite example drops the three `-e` flags. Postgres
  example keeps `SP_DB_TYPE=postgres` and `SP_DB_URL`, drops
  `SP_FILESTORAGE_LOCAL_ROOT`, and mounts `-v solidping-data:/data` (the
  simpler mount, uploads land in `/data/files` by default). Env table: show the
  image defaults (`/data`, `/data/files`) and note the bare-binary defaults
  differ. Rewrite the "Don't skip the volume" warning around `/data`. Keep one
  sentence that every value can be overridden with `-e`, Postgres being the
  usual reason.
- `installation/docker-compose.md` and the compose block in `docker.md`: drop
  `SP_FILESTORAGE_LOCAL_ROOT`, mount `/data`.
- `configuration/file-storage.md:29-41`: the relative-path warning now applies
  to the bare binary only. Say so and point Docker users at `/data/files`.
- Root `docker-compose.yml`: verified, it is the dev loop (golang + air +
  Postgres, `docker-compose.yml:40-63`) and never references the published
  image or the SQLite env vars. Nothing to strip. Leave it alone.

### 3. Config env parsing

Already handled, but prove it:

- `SP_DB_TYPE` / `SP_DB_DIR` are single-word leaves under `db`, so the koanf
  auto-loader (`config.go:1797-1800`, `SP_DB_DIR` → `db.dir`) covers them.
- `SP_FILESTORAGE_LOCAL_ROOT` is a multi-word key and already has its manual
  reader, `applyFileStorageEnv` (`config.go:2599-2605`).

Add or extend a test in `server/internal/config` that sets all three env vars
and asserts `cfg.Database.Type`, `cfg.Database.Dir` and
`cfg.FileStorage.LocalRoot`. If one already exists, cite it in the PR and move
on.

While there, grep the config defaults for other relative paths (`"./`, `"."`)
that hold persistent state (embedded Postgres data dir, agent keys, caches).
List them in the PR. Move one under `/data` only if it is state a user would
expect to survive a restart. Do not widen scope beyond that.

### 4. Changelog

Release-please builds the entry from the commit. Commit as
`fix(docker): persist data with zero env vars`. `docker` is not in
`SCOPE_LABELS` (`web/docs/src/lib/changelog.ts`), so add `docker` → `Docker
image` in the same PR, per `wiki/conventions/changelog.md`. The release-PR
expansion should say: the image now stores the SQLite database and uploads
under `/data`, a single `-v solidping-data:/data` is enough, and the old `-e`
flags keep working as overrides.

## Verification

- `make lint`, `make test` green.
- Local image build and a bare run:

```bash
docker build -t solidping:local .
docker volume create sp-smoke
docker run -d --name sp-smoke -p 4000:4000 -v sp-smoke:/data solidping:local
curl -sf http://localhost:4000/api/mgmt/version
docker run --rm -v sp-smoke:/data alpine ls -la /data /data/files
```

  Expect the SQLite file under `/data` and `files/` present, both owned by
  `65532`. Then `docker rm -f sp-smoke`, run again on the same volume, and
  confirm the admin login still lands on the password-rotation step rather
  than a fresh seed (proof the DB was reused).

- Add the same smoke test as a CI step after the `docker/build-push-action`
  job in `.github/workflows/ci.yml`: run the built image with a volume, wait
  for `/api/mgmt/version`, assert a file exists under `/data`. That guards
  the regression the docs currently paper over.

## Delivery

Branch `fix/docker-image-defaults`. PR title
`fix(docker): persist data with zero env vars`.
