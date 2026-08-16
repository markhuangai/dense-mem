import { ApiError, Credential } from "../api";

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

export function displayKeySuffix(credential: Credential): string {
  const suffix = credential.key_suffix?.trim();
  return suffix ? `******${suffix}` : "Unavailable";
}

export function credentialPermissionLabel(scopes: string[] | null | undefined): string {
  const label = scopes?.includes("write") ? "Read/write" : "Read only";
  return scopes?.includes("feedback:read") ? `${label} + feedback` : label;
}

export function membershipGrantLabel(grants: string[] | null | undefined): string {
  const label = grants?.includes("write") ? "Read/write" : "Read only";
  return grants?.includes("feedback:read") ? `${label} + feedback` : label;
}

export function credentialRoleLabel(role: Credential["role"] | null | undefined): string {
  return role === "manager" ? "Manager" : "Member";
}

export function membershipRoleLabel(role: "manager" | "member" | null | undefined): string {
  return role === "manager" ? "Manager" : "Member";
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
