# SolidPing MCPB bundle

An [MCPB](https://github.com/anthropics/mcpb) extension for MCP clients that install
local servers from a bundle (Claude Desktop, Smithery). It runs `sp mcp`, the stdio
bridge to a SolidPing instance, so it works with self-hosted instances and solidping.io.

The bundle is about 60 KB. It carries a small Node launcher (`server/index.js`) that
downloads the `sp` release binary for the platform once, checks its SHA-256 against the
value pinned in `server/checksums.json`, caches it under
`~/.cache/solidping-mcpb/<tag>/` and runs `sp mcp`. A binary that does not match is
refused and nothing is written.

## Build

```bash
deploy/mcpb/build.sh v0.36.1        # writes deploy/mcpb/dist/solidping-0.36.1.mcpb
npx @anthropic-ai/mcpb validate deploy/mcpb/manifest.json
```

`build.sh` needs a published release: it reads that release's `sp-checksums.txt`, pins
the tag and the checksums, and sets the manifest version.

## What users are asked for

- **SolidPing URL**, default `https://solidping.io`
- **Personal Access Token** (sensitive), passed to `sp` as `SP_TOKEN`, never written to disk

## Publish

```bash
smithery mcp publish deploy/mcpb/dist/solidping-0.36.1.mcpb -n <namespace>/solidping
```

or upload the file at smithery.ai/new (Local). Rebuild and republish per release; the
pinned tag is why the bundle is versioned with the release.

## Tested

macOS arm64: download, checksum check, second run from cache, tampered checksum refused,
and a JSON-RPC `initialize` round trip through `sp mcp` to solidping.io. The manifest passes
`mcpb validate`. Linux uses the same path as macOS. The Windows path (zip extracted with the
`tar` that ships with Windows 10+) has not been run on Windows.
