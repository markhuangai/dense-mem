import { expect, vi } from "vitest";
import { CommunityDetectionConfig, ControlMetrics, Credential, Dream, DreamRun, DreamStatus, DreamingConfig, GeneralConfig, OperationLog, OperationLogConfig, SecurityBan, SecuritySettings, SSOConfig, Team } from "./api";

export const profileA: Team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Default",
  description: "",
  metadata: null,
  config: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

export const securitySettings: SecuritySettings = {
  enabled: true,
  failure_threshold: 10,
  failure_window_seconds: 600,
  ban_duration_seconds: 0,
  updated_at: "2026-05-01T12:00:00Z",
};

export const securityBan: SecurityBan = {
  ip: "203.0.113.10",
  reason: "auth failures: AUTH_INVALID",
  source: "auto",
  failure_count: 11,
  banned_at: "2026-05-02T12:00:00Z",
  expires_at: null,
  last_failed_at: "2026-05-02T12:00:00Z",
  metadata: {},
  revoked_at: null,
};

export const metricsSnapshot: ControlMetrics = {
  window: {
    from: "2026-05-02T12:00:00Z",
    to: "2026-05-02T13:00:00Z",
    bucket_seconds: 60,
    retention_days: 30,
  },
  system: {
    requests: 42,
    errors: 2,
    avg_latency_ms: 18.5,
    max_latency_ms: 90,
  },
  dependencies: [
    { name: "postgres", status: "ok", latency_ms: 3 },
    { name: "redis", status: "ok", latency_ms: 8 },
  ],
  teams: [
    { team_id: profileA.id, team_name: "Default", requests: 42, errors: 2, avg_latency_ms: 18.5, max_latency_ms: 90 },
  ],
  keys: [
    { team_id: profileA.id, team_name: "Default", key_id: keyA().id, key_name: "default credential", key_suffix: "abc123", requests: 40, errors: 1, avg_latency_ms: 17, max_latency_ms: 80 },
  ],
  routes: [
    { route: "/ui/api/evidence/:id", method: "GET", status_class: "2xx", requests: 39, errors: 0, avg_latency_ms: 16, max_latency_ms: 70 },
  ],
};

export const telemetrySnapshot = {
  available: true,
  window: {
    key: "1h",
    from: "2026-05-02T12:00:00Z",
    to: "2026-05-02T13:00:00Z",
    step_seconds: 60,
    retention_days: 30,
  },
  scope: { type: "system" },
  cards: [
    { id: "http_requests", label: "HTTP requests", unit: "requests", value: 42 },
    { id: "verifier_tokens", label: "Verifier tokens", unit: "tokens", value: 1200 },
  ],
  series: [
    {
      id: "http_rps",
      label: "HTTP requests",
      unit: "rps",
      points: [
        { timestamp: "2026-05-02T12:00:00Z", value: 0.5 },
        { timestamp: "2026-05-02T13:00:00Z", value: 0.8 },
      ],
    },
  ],
};

export const ssoConfigSnapshot: SSOConfig = {
  update_time: "2026-06-09T12:00:00Z",
  items: [
    { key: "SSO_PUBLIC_BASE_URL", value: "", effective_value: "", updated_at: "2026-06-09T12:00:00Z" },
    { key: "SSO_ENTITLEMENT_CACHE_TTL_SECONDS", value: "", effective_value: "300", updated_at: "2026-06-09T12:00:00Z" },
    { key: "SSO_SESSION_TTL_SECONDS", value: "", effective_value: "28800", updated_at: "2026-06-09T12:00:00Z" },
    { key: "SSO_STATE_TTL_SECONDS", value: "", effective_value: "600", updated_at: "2026-06-09T12:00:00Z" },
    { key: "SSO_HTTP_TIMEOUT_SECONDS", value: "", effective_value: "10", updated_at: "2026-06-09T12:00:00Z" },
    { key: "SSO_COOKIE_SECURE", value: "", effective_value: "false", updated_at: "2026-06-09T12:00:00Z" },
  ],
};

export const generalConfigSnapshot: GeneralConfig = {
  update_time: "2026-06-16T09:00:00Z",
  items: [
    { key: "APP_TIMEZONE", value: "Local", effective_value: "Local", updated_at: "2026-06-16T09:00:00Z" },
  ],
  effective: {
    timezone: "Local",
  },
};

export const dreamingConfigSnapshot: DreamingConfig = {
  update_time: "2026-06-11T03:00:00Z",
  items: [
    { key: "DREAMING_ENABLED", value: "false", effective_value: "false", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_FORCE_ENABLED", value: "false", effective_value: "false", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_START_TIME_LOCAL", value: "03:00", effective_value: "03:00", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_MAX_OUTPUTS", value: "5", effective_value: "5", updated_at: "2026-06-11T03:00:00Z" },
  ],
  effective: {
    enabled: false,
    force_enabled: false,
    start_time_local: "03:00",
    timezone: "Local",
    max_outputs: 5,
  },
};

export const operationLogConfigSnapshot: OperationLogConfig = {
  update_time: "2026-06-14T12:00:00Z",
  items: [
    { key: "OPERATION_LOG_RETENTION_DAYS", value: "30", effective_value: "30", updated_at: "2026-06-14T12:00:00Z" },
  ],
  effective: {
    retention_days: 30,
  },
};

export const communityDetectionConfigSnapshot: CommunityDetectionConfig = {
  update_time: "2026-06-15T03:30:00Z",
  items: [
    { key: "COMMUNITY_DETECTION_ENABLED", value: "false", effective_value: "false", updated_at: "2026-06-15T03:30:00Z" },
    { key: "COMMUNITY_DETECTION_START_TIME_LOCAL", value: "03:30", effective_value: "03:30", updated_at: "2026-06-15T03:30:00Z" },
    { key: "COMMUNITY_DETECTION_MAX_CONCURRENCY", value: "1", effective_value: "1", updated_at: "2026-06-15T03:30:00Z" },
    { key: "COMMUNITY_DETECTION_JITTER_SECONDS", value: "600", effective_value: "600", updated_at: "2026-06-15T03:30:00Z" },
  ],
  effective: {
    enabled: false,
    start_time_local: "03:30",
    timezone: "Local",
    max_concurrency: 1,
    jitter_seconds: 600,
  },
};

export const operationLogsSnapshot: OperationLog[] = [
  {
    id: "33333333-3333-4333-8333-333333333333",
    timestamp: "2026-06-15T10:30:00Z",
    severity: "INFO",
    severity_rank: 20,
    message: "control_http_request",
    source: "/home/mark/dense-mem/internal/observability/logger.go:186",
    team_id: null,
    profile_id: null,
    correlation_id: "",
    error: "",
    attrs: {
      method: "GET",
      uri: "/control/api/logs",
      route: "/control/api/logs",
      status: 200,
      request_id: "req-control-1",
    },
  },
  {
    id: "44444444-4444-4444-8444-444444444444",
    timestamp: "2026-06-15T10:29:40Z",
    severity: "DEBUG",
    severity_rank: 10,
    message: "sso login oidc claims read",
    source: "/home/mark/dense-mem/internal/service/sso_service.go:462",
    team_id: profileA.id,
    profile_id: keyA().id,
    correlation_id: "corr-sso-1",
    error: "",
    attrs: {
      provider_found: true,
      provider_kind: "generic_oidc",
      provider_enabled: true,
      group_count: 1,
      groups_from_userinfo: true,
      id_token_claim_count: 12,
      userinfo_claim_count: 11,
    },
  },
  {
    id: "55555555-5555-4555-8555-555555555555",
    timestamp: "2026-06-15T10:29:30Z", severity: "WARN", severity_rank: 30,
    message: "submission_failed", source: "submission_assessment_worker",
    team_id: profileA.id, profile_id: keyA().id,
    correlation_id: "corr-submission-1", error: "",
    attrs: {
      reference_type: "submission", reference_id: "submission-1",
      from: "processing", to: "failed", attempts: 5, max_attempts: 5,
		stage: "assessment", reason_code: "provider_unavailable",
    },
  },
];

export const dreamRationale = "Generated by pairing same-team knowledge that is not already the same predicate/type, then keeping it as a hypothesis until user feedback confirms or rejects it.";

export const dreamRunSnapshot: DreamRun = {
  run_id: "run-1",
  team_id: profileA.id,
  run_date: "2026-06-14",
  started_at: "2026-06-14T14:30:00Z",
  completed_at: "2026-06-14T14:31:00Z",
  input_relationships: 0,
  created_dreams: 1,
  rejected_dreams: 0,
  status: "completed",
};

export const dreamStatusSnapshot: DreamStatus = {
  effective_config: {
    enabled: true,
    force_enabled: true,
    start_time_local: "03:00",
    timezone: "UTC",
    max_outputs: 5,
    team_enabled: true,
    source: "global_force",
  },
  latest_run: dreamRunSnapshot,
  pending_count: 0,
};

export const dreamSnapshot: Dream = {
  dream_id: "dream-1",
  team_id: profileA.id,
  hypothesis: "A may affect B.",
  what_if: "What if A and B are related?",
  possible_outcome: "Future decisions should check A against B.",
  rationale: dreamRationale,
  likelihood: 0.45,
  confidence: 0.55,
  status: "proposed",
  cycle_run_id: dreamRunSnapshot.run_id,
  generator_model: "heuristic",
  source_refs: [],
  created_at: "2026-06-14T14:30:00Z",
  updated_at: "2026-06-14T14:31:00Z",
};

export function keyA(profileId = profileA.id): Credential {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    team_id: profileA.id,
    name: "default credential",
    key_suffix: "abc123",
    scopes: ["read", "write"],
    role: "manager",
    rate_limit: 120,
    last_used_at: "2026-05-02T13:00:00Z",
    expires_at: null,
    created_at: "2026-04-30T12:00:00Z",
  };
}

export function mockPortalFetch({
  teams,
  keys,
  createdProfile,
  bans = [],
  logs = operationLogsSnapshot,
  dreams = [dreamSnapshot],
  metrics = metricsSnapshot,
}: {
  teams: Team[];
  keys: Credential[];
  createdProfile?: Team;
  bans?: SecurityBan[];
  logs?: OperationLog[];
  dreams?: Dream[];
  metrics?: ControlMetrics | "error";
}) {
  let currentProfiles = teams;
  let currentKeys = keys;
  let currentBans = bans;
  let currentGeneralConfig = structuredClone(generalConfigSnapshot);
  let currentSSOConfig = structuredClone(ssoConfigSnapshot);
  let currentDreamingConfig = structuredClone(dreamingConfigSnapshot);
  let currentCommunityDetectionConfig = structuredClone(communityDetectionConfigSnapshot);
  let currentOperationLogConfig = structuredClone(operationLogConfigSnapshot);
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const parsedUrl = new URL(url, "http://localhost");

    if (url.endsWith("/session")) {
      return jsonResponse({ data: { authenticated: true } });
    }
    if (url.includes("/telemetry") && method === "GET") {
      return jsonResponse({ data: telemetrySnapshot });
    }
    if (url.includes("/metrics") && method === "GET") {
      if (metrics === "error") {
        return jsonResponse({ code: "METRICS_UNAVAILABLE", message: "metrics unavailable", details: null }, 503);
      }
      return jsonResponse({ data: metrics });
    }
    if (url.endsWith("/config/general") && method === "GET") {
      return jsonResponse({ data: currentGeneralConfig });
    }
    if (url.endsWith("/config/general") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentGeneralConfig = {
        ...currentGeneralConfig,
        update_time: "2026-06-16T09:01:00Z",
        items: currentGeneralConfig.items.map((item) => {
          const update = body.items.find((candidate: { key: string }) => candidate.key === item.key);
          return update ? { ...item, value: update.value, updated_at: "2026-06-16T09:01:00Z" } : item;
        }),
      };
      return jsonResponse({ data: currentGeneralConfig });
    }
    if (url.endsWith("/config/sso") && method === "GET") {
      return jsonResponse({ data: currentSSOConfig });
    }
    if (url.endsWith("/config/sso") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentSSOConfig = {
        update_time: "2026-06-09T12:01:00Z",
        items: currentSSOConfig.items.map((item) => {
          const update = body.items.find((candidate: { key: string }) => candidate.key === item.key);
          return update ? { ...item, value: update.value, updated_at: "2026-06-09T12:01:00Z" } : item;
        }),
      };
      return jsonResponse({ data: currentSSOConfig });
    }
    if (url.endsWith("/config/dreaming") && method === "GET") {
      return jsonResponse({ data: currentDreamingConfig });
    }
    if (url.endsWith("/config/dreaming") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentDreamingConfig = {
        ...currentDreamingConfig,
        update_time: "2026-06-11T03:01:00Z",
        items: currentDreamingConfig.items.map((item) => {
          const update = body.items.find((candidate: { key: string }) => candidate.key === item.key);
          return update ? { ...item, value: update.value, updated_at: "2026-06-11T03:01:00Z" } : item;
        }),
      };
      return jsonResponse({ data: currentDreamingConfig });
    }
    if (url.endsWith("/config/community-detection") && method === "GET") {
      return jsonResponse({ data: currentCommunityDetectionConfig });
    }
    if (url.endsWith("/config/community-detection") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentCommunityDetectionConfig = {
        ...currentCommunityDetectionConfig,
        update_time: "2026-06-15T03:31:00Z",
        items: currentCommunityDetectionConfig.items.map((item) => {
          const update = body.items.find((candidate: { key: string }) => candidate.key === item.key);
          return update ? { ...item, value: update.value, updated_at: "2026-06-15T03:31:00Z" } : item;
        }),
      };
      return jsonResponse({ data: currentCommunityDetectionConfig });
    }
    if (url.endsWith("/config/operation-logs") && method === "GET") {
      return jsonResponse({ data: currentOperationLogConfig });
    }
    if (url.endsWith("/config/operation-logs") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentOperationLogConfig = {
        ...currentOperationLogConfig,
        update_time: "2026-06-14T12:01:00Z",
        items: currentOperationLogConfig.items.map((item) => {
          const update = body.items.find((candidate: { key: string }) => candidate.key === item.key);
          return update ? { ...item, value: update.value, updated_at: "2026-06-14T12:01:00Z" } : item;
        }),
      };
      return jsonResponse({ data: currentOperationLogConfig });
    }
    if (parsedUrl.pathname.endsWith("/control/api/logs") && method === "GET") {
      const limit = Number(parsedUrl.searchParams.get("limit") ?? "100");
      const offset = Number(parsedUrl.searchParams.get("offset") ?? "0");
      const severity = parsedUrl.searchParams.get("severity") ?? "";
      const filtered = severity ? logs.filter((log) => log.severity === severity) : logs;
      return jsonResponse({
        data: filtered.slice(offset, offset + limit),
        pagination: { limit, offset, total: filtered.length },
      });
    }
    if (url.includes(`/teams/${profileA.id}/dreaming/status`) && method === "GET") {
      return jsonResponse({ data: dreamStatusSnapshot });
    }
    if (url.includes(`/teams/${profileA.id}/dreaming/runs`) && method === "GET") {
      return jsonResponse({ data: [dreamRunSnapshot] });
    }
    if (url.includes(`/teams/${profileA.id}/dreams`) && method === "GET") {
      return jsonResponse({ data: { items: dreams, next_cursor: "" } });
    }
    if (url.endsWith("/teams") && method === "GET") {
      return jsonResponse(page(currentProfiles));
    }
    if (url.endsWith("/teams") && method === "POST") {
      const team = createdProfile ?? {
        ...profileA,
        id: "33333333-3333-4333-8333-333333333333",
        name: JSON.parse(String(init?.body)).name,
      };
      currentProfiles = [...currentProfiles, team];
      return jsonResponse({ data: team }, 201);
    }
    if (url.includes("/credentials") && method === "GET") {
      return jsonResponse(page(currentKeys));
    }
    if (url.includes("/credentials/") && url.endsWith("/rotate") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const rotated = { ...(currentKeys.find((key) => url.includes(key.id)) ?? keyA()), name: body.name, key_suffix: "rot8ed", last_used_at: null };
      currentKeys = currentKeys.map((key) => (key.id === rotated.id ? rotated : key));
      return jsonResponse({ data: { api_key: "dm_rotated_once", credential: rotated } });
    }
    if (url.includes("/credentials/") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      const current = currentKeys.find((key) => url.endsWith(`/credentials/${key.id}`)) ?? keyA();
      const updated = { ...current, name: body.name ?? current.name, role: body.role ?? current.role, scopes: body.scopes ?? current.scopes };
      currentKeys = currentKeys.map((key) => (key.id === updated.id ? updated : key));
      return jsonResponse({ data: updated });
    }
    if (url.endsWith("/credentials") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      expect(body.label).toBeUndefined();
      const created = { ...keyA(), name: body.name, scopes: body.scopes, role: body.role };
      currentKeys = [created, ...currentKeys];
      return jsonResponse({ data: { api_key: "dm_plain_once", credential: created } }, 201);
    }
    if (url.includes("/credentials/") && method === "DELETE") {
      currentKeys = currentKeys.filter((key) => !url.endsWith(`/credentials/${key.id}`));
      return jsonResponse({ data: { status: "deleted" } });
    }
    if (url.endsWith("/security/settings") && method === "GET") {
      return jsonResponse({ data: securitySettings });
    }
    if (url.endsWith("/security/settings") && method === "PATCH") {
      return jsonResponse({ data: { ...securitySettings, ...JSON.parse(String(init?.body)) } });
    }
    if (url.includes("/security/bans") && method === "GET") {
      return jsonResponse(page(currentBans));
    }
    if (url.endsWith("/security/bans") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const created = { ip: body.ip, reason: body.reason, source: "manual", failure_count: 0, banned_at: "2026-05-01T12:00:00Z", expires_at: null, last_failed_at: null, metadata: {}, revoked_at: null } as SecurityBan;
      currentBans = [created, ...currentBans];
      return jsonResponse({ data: created }, 201);
    }
    if (url.includes("/security/bans/") && method === "DELETE") {
      currentBans = currentBans.filter((ban) => !url.endsWith(`/security/bans/${ban.ip}`));
      return jsonResponse({ data: { status: "deleted" } });
    }
    if (url.includes("/teams/") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      const current = currentProfiles.find((team) => url.endsWith(`/teams/${team.id}`)) ?? currentProfiles[0];
      const updated = { ...current, name: body.name ?? current.name, description: body.description ?? current.description, config: body.config ?? current.config };
      currentProfiles = currentProfiles.map((team) => (team.id === updated.id ? updated : team));
      return jsonResponse({ data: updated });
    }
    if (method === "DELETE") {
      currentProfiles = currentProfiles.filter((team) => !url.endsWith(`/teams/${team.id}`));
      return jsonResponse({ data: { status: "deleted" } });
    }
    return jsonResponse({ message: "not found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

export function page<T>(data: T[]) {
  return { data, pagination: { limit: 20, offset: 0, total: data.length } };
}

export function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
