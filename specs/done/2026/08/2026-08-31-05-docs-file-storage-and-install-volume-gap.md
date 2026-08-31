---
model: sonnet
effort: medium
---

# File storage is undocumented, and the install guides silently lose every upload

## Problem

`grep -ril filestorage web/docs/` returns **nothing**. The docs site has no page
for file storage at all, and the `SP_FILESTORAGE_*` variables appear in neither
`web/docs/docs/configuration/index.md` nor any installation guide.

That omission is not merely a gap — combined with the default, it actively
leads self-hosters into silent data loss:

- `FileStorageConfig` defaults to `Type: "local"`, `LocalRoot: "./data/files"`
  ([server/internal/config/config.go:1549](server/internal/config/config.go:1549)).
  That path is relative to the container's working directory.
- [web/docs/docs/installation/kubernetes.md](web/docs/docs/installation/kubernetes.md)
  mounts a `volumeClaimTemplates` PVC at `/var/lib/postgresql/data` (line ~197)
  and an `emptyDir` at `/dev/shm` for Chrome (line ~280), and **nothing** for
  the SolidPing container's `./data/files`. Anyone following it stores org
  logos, status-page assets and incident screenshots on the pod's ephemeral
  writable layer.
- [web/docs/docs/installation/docker-compose.md](web/docs/docs/installation/docker-compose.md)
  has the same gap in both the quick-start (line ~34) and the **Production
  Setup** stack (line ~72): the `postgres` service gets `postgres-data`, the
  `solidping` service gets no volume at all.
- [web/docs/docs/installation/docker.md](web/docs/docs/installation/docker.md)
  is half-covered by accident: the SQLite quick start mounts
  `-v solidping-data:/data` but only points `SP_DB_DIR` at it, so files still
  land in the ephemeral `./data/files`; the PostgreSQL "recommended for
  production" `docker run` has no `-v` whatsoever.

The failure is silent. Nothing errors at write time; the upload succeeds and
the UI shows it. The loss only surfaces much later, when a read returns
`500 read file: file not found in storage`. Confirmed live on the k8xp dev
cluster on 2026-08-31: a logo uploaded at 07:22 was unreadable after a 12:36
rollout.

`.env.example` was updated on 2026-08-31 (lines 76–104) and now documents every
variable plus the container warning. The docs site must not contradict it.

## Proposal

### 1. New page: `web/docs/docs/configuration/file-storage.md`

Front matter matching its siblings (`sidebar_position: 11`, after
`telegram.md`'s 10; `title: File Storage`). Match the prose register of
`sms.md` / `telegram.md` — explain the model first, table the variables second,
worked examples last. No `_category_.json` change is needed: the category has no
explicit item list, so `sidebar_position` alone places the page.

Content:

- **What is stored** — org logos, status-page assets, incident screenshots.
  Blobs, not check data; the database is unaffected by this setting.
- **`local` (default)** — `SP_FILESTORAGE_TYPE=local`,
  `SP_FILESTORAGE_LOCAL_ROOT` (default `./data/files`, relative to the working
  directory). A prominent Docusaurus `:::warning` reusing the `.env.example`
  wording: in a container this path **must** be a mounted volume, or every
  upload dies with the container — silently, with no error until a later read.
- **`s3`** — table of `SP_FILESTORAGE_S3_BUCKET` (required), `_REGION`
  (required by the SDK even for stores that ignore it), `_PREFIX` (optional;
  lets several deployments share a bucket), `_ENDPOINT` (empty = AWS S3),
  `_USE_PATH_STYLE`, `_ACCESS_KEY`, `_SECRET_KEY`.
- **Credentials** — leaving access/secret **both** empty uses the ambient AWS
  credential chain (env, IAM role, shared config), which is what an EKS/EC2
  deployment wants; setting them pins static credentials, which is what every
  S3-compatible store needs. Mark them as secrets, never logged.
- **Worked examples**, at least:
  - **MinIO** — `SP_FILESTORAGE_S3_ENDPOINT=http://localhost:9000`,
    `SP_FILESTORAGE_S3_USE_PATH_STYLE=true`, static keys.
  - One hosted S3-compatible provider — **OVHcloud**
    (`https://s3.gra.io.cloud.ovh.net`, region `gra`), per `.env.example:102`.
  - Plain **AWS S3** with no endpoint and no keys (IAM role).
  Note that path-style never needs per-bucket DNS, so it is the safe choice
  when unsure (required by MinIO/Garage/Ceph, optional elsewhere).
- **Switching backends is safe for existing blobs** — each blob is addressed by
  the scheme recorded at write time (`file://` vs `s3://`), so flipping
  `SP_FILESTORAGE_TYPE` does not orphan what is already stored: old blobs keep
  resolving through their original backend as long as it stays reachable.
  Verify this claim against the storage code before writing it as fact, and
  state the caveat (the old backend must remain available) explicitly.

Also add a **File Storage** row group to the Quick Reference tables in
`web/docs/docs/configuration/index.md`, linking to the new page.

### 2. Fix the installation guides

- **`kubernetes.md`** — give the SolidPing container a real home for uploads.
  Preferred: add a `PersistentVolumeClaim` mounted at an absolute path with
  `SP_FILESTORAGE_LOCAL_ROOT` pointing at it (an absolute path removes the
  working-directory ambiguity). Because a PVC-backed `Deployment` does not
  scale past one replica with `ReadWriteOnce`, call that out and present the
  **S3 backend as the multi-replica answer**, with the env block to switch.
- **`docker.md`** — add a volume to the PostgreSQL `docker run` example, and
  set `SP_FILESTORAGE_LOCAL_ROOT` explicitly in both examples so the existing
  `solidping-data:/data` mount actually covers uploads.
- **`docker-compose.md`** — add a named volume for the `solidping` service in
  **both** the quick start and the Production Setup stack, plus
  `SP_FILESTORAGE_LOCAL_ROOT`.
- Cross-link all three (and `linux.md` / `windows.md` if they touch storage
  paths — check them) to `/configuration/file-storage`.

### 3. Consistency

Reuse the `.env.example` (lines 76–104) wording rather than reinventing it; the
two must not contradict each other. If a fact there turns out to be wrong,
fix `.env.example` in the same change rather than diverging.

## Verification

- `cd web/docs && bun run build` must pass — this catches a broken sidebar
  entry, a bad relative link, and MDX syntax errors in the new page.
- Confirm the new page appears in the Configuration sidebar in the built
  output, and that no cross-link 404s (Docusaurus fails the build on broken
  internal links).
- Grep the repo for any other install/deploy doc that mounts volumes and omits
  the files path.

## Out of scope

- Changing the default (`local` / `./data/files`) or adding a startup warning
  when the path looks ephemeral. Worth considering separately — this spec is
  documentation only.
- The `k8xp` deployment manifests (separate repo); this fixes the *published*
  guide.
