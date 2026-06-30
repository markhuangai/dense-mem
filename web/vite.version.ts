import { execFileSync } from "node:child_process";
import { resolve } from "node:path";

export function denseMemVersion() {
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
