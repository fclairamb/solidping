import { config } from "zod";

/**
 * The dashboard is served with a Content-Security-Policy that has no
 * 'unsafe-eval' (spec 2026-09-25-28). Zod 4 decides whether to JIT-compile
 * object parsers by probing `Function("")` inside a try/catch: under the
 * policy the probe throws and zod falls back to its interpreted path, but the
 * browser still reports a `script-src` violation for the attempt. `jitless`
 * skips the probe and selects that same interpreted path up front.
 *
 * Imported for its side effect from main.tsx, before anything parses.
 */
config({ jitless: true });
