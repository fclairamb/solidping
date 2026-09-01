---
sidebar_position: 11
title: File Storage
---

# File Storage

SolidPing stores a handful of binary blobs outside the database: organization
logos, status-page brand assets (logo and favicon), and incident screenshots
captured by browser checks. This is what "file storage" configures — it never
holds check data, incidents, or any other row data. Metadata for each blob
(name, size, MIME type, checksum) always lives in the database `files` table;
only the bytes move between backends.

## Backends

Two backends are supported, selected by `SP_FILESTORAGE_TYPE`:

| Type | `SP_FILESTORAGE_TYPE` | Storage |
|---|---|---|
| Local filesystem (default) | `local` | A directory on the machine running SolidPing |
| S3-compatible object storage | `s3` | AWS S3, or any S3-compatible store — MinIO, OVHcloud, Garage, Ceph/RGW, R2, Backblaze B2, … |

## `local` (default)

| Variable | Default | Notes |
|---|---|---|
| `SP_FILESTORAGE_TYPE` | `local` | |
| `SP_FILESTORAGE_LOCAL_ROOT` | `./data/files` | Root directory for blobs. **Relative to the process's working directory** — see the warning below. |

:::warning In a container, this path must be a mounted volume
`./data/files` is relative to the working directory **inside the image**. If
that path is not backed by a mounted volume, every upload lives on the
container's ephemeral writable layer and is destroyed the next time the
container is recreated — a restart, a rollout, a redeploy — **silently**.
Nothing fails at write time: the upload succeeds and the dashboard shows it.
The loss only surfaces later, when a read returns
`500 read file: file not found in storage`.

Either mount a volume at `SP_FILESTORAGE_LOCAL_ROOT` (set it to an absolute
path, e.g. `/data/files`, to remove any working-directory ambiguity), or use
the `s3` backend below. See the worked volume setups in the installation
guides: [Kubernetes](/installation/kubernetes#file-storage),
[Docker](/installation/docker), and
[Docker Compose](/installation/docker-compose).
:::

## `s3`

| Variable | Required | Notes |
|---|---|---|
| `SP_FILESTORAGE_S3_BUCKET` | **yes** | Bucket name |
| `SP_FILESTORAGE_S3_REGION` | **yes** | Required by the AWS SDK even for stores that ignore it (MinIO, Garage, …) — pick any value if the provider doesn't care |
| `SP_FILESTORAGE_S3_PREFIX` | no | Optional key prefix — lets several SolidPing deployments share one bucket |
| `SP_FILESTORAGE_S3_ENDPOINT` | no | Custom endpoint. Empty = AWS S3. Set it for MinIO, Garage, Ceph/RGW, R2, Backblaze B2, OVHcloud, etc. |
| `SP_FILESTORAGE_S3_USE_PATH_STYLE` | no | `true` / `false`. Path-style addressing never needs per-bucket DNS, so it's the safe choice when unsure — **required** by MinIO/Garage/Ceph, optional elsewhere |
| `SP_FILESTORAGE_S3_ACCESS_KEY` | no | Static access key. **Secret** — never logged |
| `SP_FILESTORAGE_S3_SECRET_KEY` | no | Static secret key. **Secret** — never logged |

### Credentials

Leave `SP_FILESTORAGE_S3_ACCESS_KEY` and `SP_FILESTORAGE_S3_SECRET_KEY` **both
empty** to use the standard AWS credential chain (environment, IAM role,
shared config) — this is what you want on EKS or an EC2 instance with an
attached role. Set both to pin static credentials, which is what any
S3-compatible store that isn't AWS itself needs.

### Worked examples

**MinIO** (self-hosted, e.g. alongside SolidPing in Docker Compose):

```bash
SP_FILESTORAGE_TYPE=s3
SP_FILESTORAGE_S3_BUCKET=solidping
SP_FILESTORAGE_S3_REGION=us-east-1
SP_FILESTORAGE_S3_ENDPOINT=http://localhost:9000
SP_FILESTORAGE_S3_USE_PATH_STYLE=true
SP_FILESTORAGE_S3_ACCESS_KEY=minioadmin
SP_FILESTORAGE_S3_SECRET_KEY=minioadmin
```

**OVHcloud Object Storage**:

```bash
SP_FILESTORAGE_TYPE=s3
SP_FILESTORAGE_S3_BUCKET=solidping
SP_FILESTORAGE_S3_REGION=gra
SP_FILESTORAGE_S3_ENDPOINT=https://s3.gra.io.cloud.ovh.net
SP_FILESTORAGE_S3_USE_PATH_STYLE=true
SP_FILESTORAGE_S3_ACCESS_KEY=your-access-key
SP_FILESTORAGE_S3_SECRET_KEY=your-secret-key
```

**Plain AWS S3** (IAM role, no static credentials):

```bash
SP_FILESTORAGE_TYPE=s3
SP_FILESTORAGE_S3_BUCKET=solidping-prod
SP_FILESTORAGE_S3_REGION=eu-west-1
```

## Switching backends

Each blob's URI records the scheme it was written under (`file://` for the
local backend, `s3://` for S3), and a read always resolves through that
recorded scheme rather than through the currently configured
`SP_FILESTORAGE_TYPE`. So flipping `SP_FILESTORAGE_TYPE` from `local` to `s3`
(or back) only changes where **new** uploads land — it does not orphan what
is already stored.

:::note The old backend must stay reachable
An existing blob keeps resolving through its original backend for as long as
that backend's configuration and storage remain in place. If you switch from
`local` to `s3` and later remove the local volume (or stop mounting it),
every blob still recorded as `file://` becomes unreadable. Migrating existing
blobs to the new backend is not automated today — keep the old backend
reachable until you've migrated (or accepted the loss of) anything written
before the switch.
:::

## Next steps

- [Kubernetes installation](/installation/kubernetes#file-storage)
- [Docker installation](/installation/docker)
- [Docker Compose installation](/installation/docker-compose)
