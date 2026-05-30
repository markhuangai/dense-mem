import { ApiError, TeamProfile } from "../api";

export function readError(error: unknown): string {
  if (error instanceof ApiError || error instanceof Error) {
    return error.message;
  }
  return "Request failed.";
}

export function formatDate(value: string): string {
  if (!value) {
    return "";
  }
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
}

export function displayKeySuffix(key: TeamProfile): string {
  const suffix = key.key_suffix?.trim();
  return suffix ? `******${suffix}` : "Unavailable";
}

export function profilePermissionLabel(scopes: string[] | null | undefined): string {
  return scopes?.includes("write") ? "Read/write" : "Read only";
}

export function formatCount(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(value);
}

export function formatLatency(value: number): string {
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: value < 10 ? 1 : 0 }).format(value)} ms`;
}

export function formatPercent(value: number, total: number): string {
  if (total <= 0) {
    return "0%";
  }
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format((value / total) * 100)}%`;
}

export function dependencyStatusClass(status: string): string {
  if (status === "ok") {
    return "neutral";
  }
  if (status === "degraded") {
    return "warning";
  }
  return "error";
}

export function shortId(value: string): string {
  return value.slice(0, 8);
}
