---
sidebar_position: 5
title: Security & Encryption
---

# Security & Encryption

SolidPing stores sensitive data — notification connection secrets, check credentials, integration tokens — encrypted at rest. Encryption uses an **envelope scheme** anchored by an out-of-band master key that you provide.

## How It Works

```mermaid
flowchart LR
    KEK["Master Key (KEK)<br/>(from env)"] -->|encrypts| DEK["Per-org Data Key (DEK)<br/>(generated per org)"]
    DEK -->|encrypts| Secrets["Stored secrets<br/>(AES-256-GCM)"]
```

1. You supply a 32-byte **master key** (the Key Encryption Key) via the environment.
2. SolidPing generates a random **data encryption key** per organization and stores it encrypted with the master key.
3. Individual secrets are encrypted with their organization's data key using **AES-256-GCM**.

Because the master key lives outside the database, a database dump alone never reveals plaintext secrets.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_ENCRYPTION_MASTER_KEY` | - | Base64-encoded 32-byte master key (KEK) |
| `SP_ENCRYPTION_MASTER_KEY_FILE` | - | Path to a file containing the base64 master key (wins over `SP_ENCRYPTION_MASTER_KEY` when both are set) |
| `SP_ENCRYPTION_AUTO_MIGRATE` | `true` | Encrypt any existing plaintext credentials on startup |

Generate a key with:

```bash
openssl rand -base64 32
```

```bash
SP_ENCRYPTION_MASTER_KEY=$(openssl rand -base64 32)
```

For Kubernetes, mount the key as a secret file and point `SP_ENCRYPTION_MASTER_KEY_FILE` at it.

:::warning Keep the master key safe
The master key is required to decrypt stored secrets. **Back it up** and keep it stable — losing it means losing access to every encrypted credential. Rotating it requires re-encrypting existing data.
:::

## Secrets Are Never Echoed Back

Secret fields (passwords, tokens, webhook secrets) are **never returned to the dashboard or API** after they're saved. Responses mask them with a placeholder and list which fields are secret, so the UI can show "a value is set" without exposing it. Secrets are decrypted server-side only when actually needed to perform a check or send a notification.

## Optional, with a Plaintext Fallback

If no master key is configured, encryption is disabled and credentials are stored as-is (the original behavior). Setting a master key enables encryption and — with `SP_ENCRYPTION_AUTO_MIGRATE` — transparently migrates existing data. Configuring the master key is strongly recommended for any production deployment.
