declare const __DENSE_MEM_VERSION__: string;

function formatVersion(version: string) {
  const trimmed = version.trim();
  if (!trimmed) {
    return "dev";
  }
  return /^\d+\.\d+\.\d+(?:[-+].*)?$/.test(trimmed) ? `v${trimmed}` : trimmed;
}

export const APP_VERSION = formatVersion(__DENSE_MEM_VERSION__);
