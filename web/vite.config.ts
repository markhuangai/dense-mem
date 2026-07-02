import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

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
