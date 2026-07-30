import { resolve } from "node:path";
import { readFileSync } from "node:fs";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";

function serveUserPortalAtUi(): Plugin {
  return {
    name: "dense-mem-user-ui-entry",
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        const path = req.url?.split("?")[0];
        if (path !== "/ui" && path !== "/ui/") {
          next();
          return;
        }

        try {
          const html = readFileSync(resolve(__dirname, "user.html"), "utf8");
          const transformed = await server.transformIndexHtml(req.url ?? "/ui/", html);
          res.statusCode = 200;
          res.setHeader("Content-Type", "text/html");
          res.end(transformed);
        } catch (error) {
          next(error);
        }
      });
    },
  };
}

function manualVendorChunks(id: string) {
  if (!id.includes("node_modules")) {
    return undefined;
  }
  if (id.includes("/recharts/")) {
    return "vendor-charts";
  }
  if (id.includes("/lucide-react/")) {
    return "vendor-icons";
  }
  if (id.includes("/react/") || id.includes("/react-dom/") || id.includes("/scheduler/")) {
    return "vendor-react";
  }
  return undefined;
}

export default defineConfig({
  base: "/ui/",
  plugins: [serveUserPortalAtUi(), react()],
  build: {
    outDir: "user-dist",
    emptyOutDir: true,
    rollupOptions: {
      input: {
        index: resolve(__dirname, "user.html"),
      },
      output: {
        manualChunks: manualVendorChunks,
      },
    },
  },
  server: {
    proxy: {
      "/ui/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
    },
  },
});
