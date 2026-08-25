import path from "path";
import { fileURLToPath } from "url";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { TanStackRouterVite } from "@tanstack/router-plugin/vite";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Base URL can be configured via VITE_BASE_URL env var
// Default is "/dash0/" for both dev and production
const getBaseUrl = () => {
  const envBase = process.env.VITE_BASE_URL;
  if (envBase) {
    // Ensure it ends with "/" for Vite
    return envBase.endsWith("/") ? envBase : envBase + "/";
  }
  return "/dash0/";
};

export default defineConfig(() => {
  const base = getBaseUrl();

  return {
    plugins: [TanStackRouterVite(), react(), tailwindcss()],
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    base,
    define: {
      // Expose base URL to the app for router configuration
      "import.meta.env.VITE_BASE_URL": JSON.stringify(base.replace(/\/$/, "")),
    },
    server: {
      port: 5174,
      // Extra hostnames the dev server will answer for, beyond localhost.
      // Set VITE_DEV_ALLOWED_HOSTS (comma-separated) when reaching the dev
      // server through a tunnel or a remote hostname. No deployment host is
      // hardcoded here.
      allowedHosts: (process.env.VITE_DEV_ALLOWED_HOSTS ?? "")
        .split(",")
        .map((h) => h.trim())
        .filter(Boolean),
      proxy: {
        // ws: true lets this proxy also forward the realtime v2 WebSocket
        // upgrade (GET /api/v1/orgs/:org/events/ws) to the backend dev
        // server. Vite's own HMR client uses a separate websocket
        // (its own dev server, not proxied through here), so this doesn't
        // affect HMR.
        "/api": {
          target: "http://localhost:4000",
          changeOrigin: true,
          ws: true,
        },
      },
    },
  };
});
