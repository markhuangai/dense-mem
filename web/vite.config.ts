import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

function denseMemVersion() {
  const configuredVersion = process.env.DENSE_MEM_VERSION ?? process.env.VITE_DENSE_MEM_VERSION ?? process.env.IMAGE_VERSION;
  if (configuredVersion?.trim()) {
    return configuredVersion.trim();
  }

  try {
    const version = execFileSync("git", ["describe", "--tags", "--always", "--dirty"], {
      cwd: resolve(__dirname, ".."),
      stdio: ["ignore", "pipe", "ignore"],
    }).toString().trim();
    return version || "dev";
  } catch {
    return "dev";
  }
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
  plugins: [react()],
  define: {
    __DENSE_MEM_VERSION__: JSON.stringify(denseMemVersion()),
  },
  build: {
    rollupOptions: {
      output: {
        manualChunks: manualVendorChunks,
      },
    },
  },
  server: {
    proxy: {
      "/control/api": {
        target: "http://127.0.0.1:8090",
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    exclude: ["tests/**", "node_modules/**", "dist/**"],
    setupFiles: ["./src/test/setup.ts"],
    globals: true,
  },
});
