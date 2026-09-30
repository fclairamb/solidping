#!/usr/bin/env node
"use strict";

// Launcher for the SolidPing MCP extension. It finds (or downloads once) the sp
// release binary for this platform, checks it against the SHA-256 pinned in
// checksums.json at build time, then runs `sp mcp` with stdio passed through.

const fs = require("fs");
const os = require("os");
const path = require("path");
const https = require("https");
const crypto = require("crypto");
const zlib = require("zlib");
const { spawn, spawnSync } = require("child_process");

const pinned = require("./checksums.json");

const OS = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
const ARCH = { x64: "amd64", arm64: "arm64" }[process.arch];

function fail(message) {
  process.stderr.write(`solidping-mcpb: ${message}\n`);
  process.exit(1);
}

if (!OS || !ARCH) {
  fail(`unsupported platform ${process.platform}/${process.arch}`);
}

const ext = OS === "windows" ? "zip" : "gz";
const asset = `sp-${OS}-${ARCH}.${ext}`;
const expected = pinned.assets[asset];
if (!expected) {
  fail(`no sp release published for ${OS}/${ARCH}`);
}

const cacheRoot = process.env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache");
const dir = path.join(cacheRoot, "solidping-mcpb", pinned.tag);
const binary = path.join(dir, OS === "windows" ? "sp.exe" : "sp");

function download(url, redirects = 5) {
  return new Promise((resolve, reject) => {
    https
      .get(url, { headers: { "User-Agent": "solidping-mcpb" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location && redirects > 0) {
          res.resume();
          resolve(download(res.headers.location, redirects - 1));
          return;
        }
        if (res.statusCode !== 200) {
          res.resume();
          reject(new Error(`GET ${url} returned ${res.statusCode}`));
          return;
        }
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      })
      .on("error", reject);
  });
}

async function ensureBinary() {
  if (fs.existsSync(binary)) {
    return;
  }
  const url = `https://github.com/fclairamb/solidping/releases/download/${pinned.tag}/${asset}`;
  process.stderr.write(`solidping-mcpb: downloading ${asset} (${pinned.tag})\n`);
  const data = await download(url);
  const actual = crypto.createHash("sha256").update(data).digest("hex");
  if (actual !== expected) {
    throw new Error(`checksum mismatch for ${asset}: expected ${expected}, got ${actual}`);
  }
  fs.mkdirSync(dir, { recursive: true });
  const tmp = `${binary}.part`;
  if (ext === "gz") {
    fs.writeFileSync(tmp, zlib.gunzipSync(data), { mode: 0o755 });
  } else {
    // Windows ships a zip; the bsdtar bundled with Windows 10+ reads it.
    const archive = path.join(dir, asset);
    fs.writeFileSync(archive, data);
    const result = spawnSync("tar", ["-xf", archive, "-C", dir], { stdio: "inherit" });
    fs.rmSync(archive, { force: true });
    if (result.status !== 0 || !fs.existsSync(path.join(dir, "sp.exe"))) {
      throw new Error("could not extract the Windows archive");
    }
    return;
  }
  fs.renameSync(tmp, binary);
}

ensureBinary()
  .then(() => {
    const child = spawn(binary, ["mcp"], { stdio: "inherit", env: process.env });
    for (const sig of ["SIGINT", "SIGTERM"]) {
      process.on(sig, () => child.kill(sig));
    }
    child.on("exit", (code, signal) => process.exit(signal ? 1 : code === null ? 1 : code));
    child.on("error", (err) => fail(`could not start sp: ${err.message}`));
  })
  .catch((err) => fail(err.message));
