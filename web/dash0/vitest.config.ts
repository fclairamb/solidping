import path from "path";
import { fileURLToPath } from "url";
import { defineConfig } from "vitest/config";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Minimal Vitest setup for dash0 unit tests. The code under test is pure date
// math (no DOM), so the default node environment is fine. The `@` alias mirrors
// vite.config.ts / tsconfig so `@/...` imports resolve in tests.
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    environment: "node",
    include: [
      "src/**/*.test.ts",
      "src/**/*.test.tsx",
      // The showcase pipeline's pure helpers (cue list → crop window) are
      // unit-tested too — they are the part of that pipeline that can be
      // checked without a browser or ffmpeg.
      "showcase/**/*.test.ts",
    ],
  },
});
