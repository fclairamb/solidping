import path from "path";
import { fileURLToPath } from "url";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react-swc";
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
      allowedHosts: ["solidping.k8xp.com"],
      proxy: {
        "/api": {
          target: "http://localhost:4000",
          changeOrigin: true,
        },
      },
    },
  };
});
