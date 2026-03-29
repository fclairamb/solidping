import path from "path";
import { fileURLToPath } from "url";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { TanStackRouterVite } from "@tanstack/router-plugin/vite";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const getBaseUrl = () => {
  const envBase = process.env.VITE_BASE_URL;
  if (envBase) {
    return envBase.endsWith("/") ? envBase : envBase + "/";
  }
  return "/status0/";
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
