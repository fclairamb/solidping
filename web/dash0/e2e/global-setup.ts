/**
 * Global setup for Playwright tests.
 *
 * This script:
 * 1. Builds the frontend (embedded into backend resources)
 * 2. Builds the backend binary with embedded frontend
 * 3. Starts PostgreSQL via docker-compose
 * 4. Starts the solidping server
 * 5. Waits for the server to be ready
 * 6. Stores server process info for teardown
 *
 * In CI environments (when CI=true), the setup is skipped because:
 * - The binary is already built by the CI pipeline
 * - PostgreSQL is already started by the CI workflow
 * - The solidping server is already running
 * Only server readiness check is performed.
 */
import { spawn, type ChildProcess } from "node:child_process";
import { writeFileSync, unlinkSync } from "node:fs";
import { join } from "node:path";
import { API_BASE } from "./fixtures";

const PROJECT_ROOT = join(import.meta.dirname, "../../..");
const PID_FILE = join(import.meta.dirname, ".test-server.pid");
// API_BASE honors E2E_BASE_URL (side-car test server) like playwright.config.ts
// does; without it, the readiness check below would silently poll :4000
// instead of the actual target server.
const SERVER_URL = `${API_BASE}/api/mgmt/health`;
const MAX_RETRIES = 30; // 30 seconds
const RETRY_DELAY = 1000; // 1 second

// Connection info for the PostgreSQL container started below (docker-compose.yml's
// `postgres` service, host-mapped port + the `solidping` role provisioned by
// docker-compose/scripts/init-db.sh). Without this, SP_RUNMODE=test with no
// SP_DB_TYPE silently falls back to sqlite-memory (config.go's
// `cfg.RunMode == "test" && cfg.Database.Type == ""` default), which caps the
// connection pool at 1 (sqlite.go's SetMaxOpenConns(1)) and serializes every
// DB-backed request — including concurrent test logins. See spec
// 2026-07-06-02.
const POSTGRES_TEST_DB_URL =
  "postgres://solidping:solidping@localhost:55432/solidping?sslmode=disable";

// Check if running in CI environment
const IS_CI = process.env.CI === "true";

/**
 * Execute a command and return a promise that resolves when it completes.
 */
function execCommand(
  command: string,
  args: string[],
  options: { cwd?: string; env?: NodeJS.ProcessEnv } = {}
): Promise<void> {
  return new Promise((resolve, reject) => {
    console.log(`[setup] Running: ${command} ${args.join(" ")}`);
    const proc = spawn(command, args, {
      cwd: options.cwd || PROJECT_ROOT,
      env: { ...process.env, ...options.env },
      stdio: "inherit",
    });

    proc.on("close", (code) => {
      if (code === 0) {
        resolve();
      } else {
        reject(new Error(`Command failed with code ${code}`));
      }
    });

    proc.on("error", (err) => {
      reject(err);
    });
  });
}

/**
 * Start a background process and return the ChildProcess object.
 */
function startBackgroundProcess(
  command: string,
  args: string[],
  options: { cwd?: string; env?: NodeJS.ProcessEnv } = {}
): ChildProcess {
  console.log(`[setup] Starting: ${command} ${args.join(" ")}`);
  const proc = spawn(command, args, {
    cwd: options.cwd || PROJECT_ROOT,
    env: { ...process.env, ...options.env },
    stdio: "inherit",
    detached: false,
  });

  return proc;
}

/**
 * Wait for the server to be ready by polling the health endpoint.
 */
async function waitForServer(url: string, maxRetries: number): Promise<void> {
  console.log(`[setup] Waiting for server at ${url}...`);

  for (let i = 0; i < maxRetries; i++) {
    try {
      const response = await fetch(url);
      if (response.ok || response.status === 404) {
        // 404 is OK - it means the server is up, just not found at this exact path
        console.log("[setup] Server is ready!");
        return;
      }
    } catch {
      // Server not ready yet
    }

    await new Promise((resolve) => setTimeout(resolve, RETRY_DELAY));
  }

  throw new Error(
    `Server did not become ready after ${maxRetries * RETRY_DELAY}ms`
  );
}

/**
 * Stop a server this setup started: SIGTERM, then SIGKILL if it is still there.
 * Best-effort — a failure to kill must never mask the error that got us here.
 */
async function stopServer(pid: number): Promise<void> {
  console.log(`[setup] Stopping server process ${pid} before teardown...`);

  try {
    process.kill(pid, "SIGTERM");
    await new Promise((resolve) => setTimeout(resolve, 2000));
    try {
      process.kill(pid, 0);
      process.kill(pid, "SIGKILL");
    } catch {
      // Already gone.
    }
  } catch (err) {
    console.warn(`[setup] Could not stop server ${pid}:`, err);
  }

  try {
    unlinkSync(PID_FILE);
  } catch {
    // No PID file yet, or already removed.
  }
}

/**
 * Global setup function executed before all tests.
 */
export default async function globalSetup(): Promise<void> {
  console.log("[setup] Starting global setup for E2E tests...\n");

  // Tracked so the failure path below can stop the server BEFORE tearing the
  // database down. Playwright does not run globalTeardown when globalSetup
  // throws, so anything left running here is left running for good.
  let serverProcess: ChildProcess | undefined;

  // In CI, the server is already started by the CI workflow
  // We only need to wait for it to be ready
  if (IS_CI) {
    console.log(
      "[setup] Running in CI environment - skipping build and server start.\n"
    );
    console.log("[setup] Waiting for server to be ready...");
    await waitForServer(SERVER_URL, MAX_RETRIES);
    console.log("[setup] Global setup completed successfully!\n");
    return;
  }

  try {
    // Step 1: Build the application
    console.log("[setup] Step 1: Building application...");
    await execCommand("make", ["build"], { cwd: PROJECT_ROOT });
    console.log("[setup] Application built successfully.\n");

    // Step 2: Start PostgreSQL with docker-compose
    console.log("[setup] Step 2: Starting PostgreSQL...");
    await execCommand("docker", ["compose", "up", "-d", "postgres"], {
      cwd: PROJECT_ROOT,
    });
    console.log("[setup] PostgreSQL started successfully.\n");

    // Wait for PostgreSQL to be ready
    console.log("[setup] Waiting for PostgreSQL to be ready...");
    await new Promise((resolve) => setTimeout(resolve, 5000)); // Wait 5 seconds
    console.log("[setup] PostgreSQL should be ready.\n");

    // Step 3: Start solidping server in test mode with database reset
    console.log("[setup] Step 3: Starting solidping server...");
    serverProcess = startBackgroundProcess("./solidping", ["serve"], {
      cwd: PROJECT_ROOT,
      env: {
        ...process.env,
        SOLIDPING_LISTEN: ":4000",
        SP_RUNMODE: "test",
        SP_DB_RESET: "true",
        SP_DB_TYPE: "postgres",
        SP_DB_URL: POSTGRES_TEST_DB_URL,
        // Belt and braces for the case no teardown can cover: if this
        // Playwright process is killed outright, the server would otherwise be
        // reparented to PID 1 and keep running against a database we are about
        // to destroy. One such orphan spent seventeen hours logging
        // "no such table: jobs" until the disk filled (spec 2026-08-12-05).
        SP_EXIT_WITH_PARENT: "true",
      },
    });

    // Store server PID for teardown
    writeFileSync(PID_FILE, serverProcess.pid!.toString());
    console.log(
      `[setup] Server process started with PID ${serverProcess.pid}\n`
    );

    // Step 4: Wait for server to be ready
    await waitForServer(SERVER_URL, MAX_RETRIES);

    console.log("[setup] Global setup completed successfully!\n");
  } catch (err) {
    console.error("[setup] Global setup failed:", err);
    // Clean up on failure. ORDER MATTERS: stop the server we started before
    // tearing its database down. Doing it the other way round leaves a live
    // server pointed at a database that no longer exists — the exact shape of
    // the incident in spec 2026-08-12-05 — and Playwright will not run
    // globalTeardown to fix it, because this setup threw.
    if (serverProcess?.pid) {
      await stopServer(serverProcess.pid);
    }
    try {
      await execCommand("docker", ["compose", "down"], { cwd: PROJECT_ROOT });
    } catch {
      // Ignore cleanup errors
    }
    throw err;
  }
}
