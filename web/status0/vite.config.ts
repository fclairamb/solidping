import path from "path";
import { fileURLToPath } from "url";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { TanStackRouterVite } from "@tanstack/router-plugin/vite";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Default base "/s/" — must stay in sync with config.StatusBasePath
// (server/internal/config/config.go), which is where the backend mounts this
// app and which every published status-page URL is built from.
const getBaseUrl = () => {
  const envBase = process.env.VITE_BASE_URL;
  if (envBase) {
    return envBase.endsWith("/") ? envBase : envBase + "/";
  }
  return "/s/";
};

export default defineConfig(() => {
  const base = getBaseUrl();

  return {
    plugins: [TanStackRouterVite(), react(), tailwindcss()],
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
      dedupe: ["react", "react-dom"],
    },
    base,
    define: {
      "import.meta.env.VITE_BASE_URL": JSON.stringify(base.replace(/\/$/, "")),
    },
    server: {
      port: 5175,
      proxy: {
        "/api": {
          target: "http://localhost:4000",
          changeOrigin: true,
        },
      },
    },
  };
});
