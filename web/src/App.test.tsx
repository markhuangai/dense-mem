import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { CommunityDetectionConfig, ControlMetrics, Dream, DreamRun, DreamStatus, DreamingConfig, GeneralConfig, OperationLog, OperationLogConfig, SecurityBan, SecuritySettings, SSOConfig, Team, TeamProfile } from "./api";

const profileA: Team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Default",
  description: "",
  metadata: null,
  config: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

const securitySettings: SecuritySettings = {
  enabled: true,
  failure_threshold: 10,
  failure_window_seconds: 600,
  ban_duration_seconds: 0,
  updated_at: "2026-05-01T12:00:00Z",
};

const securityBan: SecurityBan = {
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

const metricsSnapshot: ControlMetrics = {
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
    { name: "neo4j", status: "ok", latency_ms: 8 },
  ],
  teams: [
    { team_id: profileA.id, team_name: "Default", requests: 42, errors: 2, avg_latency_ms: 18.5, max_latency_ms: 90 },
  ],
  keys: [
    { team_id: profileA.id, team_name: "Default", key_id: keyA().id, key_name: "default profile", key_suffix: "abc123", requests: 40, errors: 1, avg_latency_ms: 17, max_latency_ms: 80 },
  ],
  routes: [
    { route: "/api/v1/fragments/:id", method: "GET", status_class: "2xx", requests: 39, errors: 0, avg_latency_ms: 16, max_latency_ms: 70 },
  ],
};

const telemetrySnapshot = {
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

const ssoConfigSnapshot: SSOConfig = {
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

const generalConfigSnapshot: GeneralConfig = {
  update_time: "2026-06-16T09:00:00Z",
  items: [
    { key: "APP_TIMEZONE", value: "Local", effective_value: "Local", updated_at: "2026-06-16T09:00:00Z" },
  ],
  effective: {
    timezone: "Local",
  },
};

const dreamingConfigSnapshot: DreamingConfig = {
  update_time: "2026-06-11T03:00:00Z",
  items: [
    { key: "DREAMING_ENABLED", value: "false", effective_value: "false", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_FORCE_ENABLED", value: "false", effective_value: "false", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_START_TIME_LOCAL", value: "03:00", effective_value: "03:00", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_REFLECT_ENABLED", value: "true", effective_value: "true", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_REEVALUATE_ENABLED", value: "true", effective_value: "true", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_DREAM_ENABLED", value: "true", effective_value: "true", updated_at: "2026-06-11T03:00:00Z" },
    { key: "DREAMING_MAX_OUTPUTS", value: "5", effective_value: "5", updated_at: "2026-06-11T03:00:00Z" },
  ],
  effective: {
    enabled: false,
    force_enabled: false,
    start_time_local: "03:00",
    timezone: "Local",
    reflect_enabled: true,
    reevaluate_enabled: true,
    dream_enabled: true,
    max_outputs: 5,
  },
};

const operationLogConfigSnapshot: OperationLogConfig = {
  update_time: "2026-06-14T12:00:00Z",
  items: [
    { key: "OPERATION_LOG_RETENTION_DAYS", value: "30", effective_value: "30", updated_at: "2026-06-14T12:00:00Z" },
  ],
  effective: {
    retention_days: 30,
  },
};

const communityDetectionConfigSnapshot: CommunityDetectionConfig = {
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

const operationLogsSnapshot: OperationLog[] = [
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
];

const dreamRationale = "Generated by pairing same-profile knowledge that is not already the same predicate/type, then keeping it as a hypothesis until user feedback confirms or rejects it.";

const dreamRunSnapshot: DreamRun = {
  run_id: "run-1",
  team_id: profileA.id,
  run_date: "2026-06-14",
  started_at: "2026-06-14T14:30:00Z",
  completed_at: "2026-06-14T14:31:00Z",
  reflect_ran: true,
  reevaluate_ran: true,
  dream_ran: true,
  stale_facts: 0,
  candidate_claims: 0,
  disputed_claims: 0,
  clarifications: 0,
  reevaluated_dreams: 0,
  created_dreams: 1,
  status: "completed",
};

const dreamStatusSnapshot: DreamStatus = {
  effective_config: {
    enabled: true,
    force_enabled: true,
    start_time_local: "03:00",
    timezone: "UTC",
    reflect_enabled: true,
    reevaluate_enabled: true,
    dream_enabled: true,
    max_outputs: 5,
    team_enabled: true,
    source: "global_force",
  },
  latest_run: dreamRunSnapshot,
  pending_count: 0,
};

const dreamSnapshot: Dream = {
  dream_id: "dream-1",
  team_id: profileA.id,
  hypothesis: "A may affect B.",
  what_if: "What if A and B are related?",
  possible_outcome: "Future decisions should check A against B.",
  rationale: dreamRationale,
  likelihood: 0.45,
  confidence: 0.55,
  status: "proposed",
  cycle: "2026-06-14",
  cycle_run_id: dreamRunSnapshot.run_id,
  generator_model: "heuristic",
  source_refs: [],
  created_at: "2026-06-14T14:30:00Z",
  updated_at: "2026-06-14T14:31:00Z",
};

function keyA(profileId = profileA.id): TeamProfile {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    team_id: profileA.id,
    name: "default profile",
    key_suffix: "abc123",
    scopes: ["read", "write"],
    role: "manager",
    rate_limit: 120,
    last_used_at: "2026-05-02T13:00:00Z",
    expires_at: null,
    created_at: "2026-04-30T12:00:00Z",
  };
}

beforeEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
  vi.mocked(navigator.clipboard.writeText).mockClear();
});

describe("App", () => {
  it("validates the token before opening the portal", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ message: "invalid token" }, 401));
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    await userEvent.type(screen.getByLabelText(/control token/i), "bad-token");
    await userEvent.click(screen.getByRole("button", { name: /unlock/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("invalid token");
  });

  it("shows team validation states", async () => {
    mockPortalFetch({ teams: [profileA], keys: [] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: "New Team" }));
    await userEvent.type(screen.getByLabelText("Name", { selector: "input#new-team-name" }), "ab");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Name must be at least 3 characters.");
  });

  it("creates a team and selects it", async () => {
    const created: Team = {
      ...profileA,
      id: "33333333-3333-4333-8333-333333333333",
      name: "Work Team",
      description: "for work",
    };
    mockPortalFetch({ teams: [profileA], keys: [], createdProfile: created });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: "New Team" }));
    await userEvent.type(screen.getByLabelText("Name", { selector: "input#new-team-name" }), "Work Team");
    await userEvent.type(screen.getByLabelText("Description", { selector: "input#new-team-description" }), "for work");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("heading", { name: "Work Team" })).toBeInTheDocument();
  });

  it("creates an API key and shows plaintext once", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /team profiles/i }));
    await userEvent.click(screen.getByLabelText("Recall feedback"));
    await userEvent.click(screen.getByRole("button", { name: /create profile/i }));

    expect(await screen.findByDisplayValue("dm_plain_once")).toHaveAccessibleName("Generated API key");
    await userEvent.click(screen.getByRole("button", { name: /copy api key/i }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("dm_plain_once");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles`),
        expect.objectContaining({
          method: "POST",
          body: expect.stringContaining(`"scopes":["read","write","feedback:read"]`),
        }),
      );
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles`),
        expect.objectContaining({
          method: "POST",
          body: expect.stringContaining(`"role":"manager"`),
        }),
      );
    });
    await userEvent.click(screen.getByRole("button", { name: /dismiss api key/i }));
    await waitFor(() => expect(screen.queryByDisplayValue("dm_plain_once")).not.toBeInTheDocument());
  });

  it("updates team and profile names and regenerates a key", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /team settings/i }));

    const teamName = screen.getByLabelText("Name", { selector: "#team-name" });
    await userEvent.clear(teamName);
    await userEvent.type(teamName, "Renamed Team");
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));
    expect(await screen.findByRole("heading", { name: "Renamed Team" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /team profiles/i }));
    const profileName = await screen.findByLabelText("Profile name default profile");
    await userEvent.clear(profileName);
    await userEvent.type(profileName, "Research profile");
    await userEvent.click(screen.getByRole("button", { name: /save profile default profile/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles/${keyA().id}`),
        expect.objectContaining({ method: "PATCH" }),
      );
    });
    expect(await screen.findByDisplayValue("Research profile")).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Profile role Research profile"), "member");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles/${keyA().id}`),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"role":"member"`),
        }),
      );
    });

    const profileRow = (await screen.findByDisplayValue("Research profile")).closest("tr");
    expect(profileRow).not.toBeNull();
    await userEvent.click(within(profileRow as HTMLElement).getByLabelText("Recall feedback"));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles/${keyA().id}`),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"scopes":["read","write","feedback:read"]`),
        }),
      );
    });

    await userEvent.click(screen.getByRole("button", { name: /regenerate key for profile Research profile/i }));
    expect(await screen.findByDisplayValue("dm_rotated_once")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles/${keyA().id}/rotate`),
        expect.objectContaining({ method: "POST" }),
      );
    });
  });

  it("edits team dreaming config without dropping other team config", async () => {
    const configuredTeam: Team = {
      ...profileA,
      config: {
        retention: "standard",
        dreaming: {
          enabled: false,
          timezone: "UTC",
          max_outputs: 3,
          provider: "manual",
        },
      },
    };
    const fetchMock = mockPortalFetch({ teams: [configuredTeam], keys: [] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /team settings/i }));

    const scheduledToggle = screen.getByLabelText("Scheduled cycle", { selector: "input" });
    expect(scheduledToggle).not.toBeChecked();
    await userEvent.click(scheduledToggle);
    await userEvent.click(screen.getByRole("button", { name: /save dreaming/i }));

    const patchCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith(`/teams/${configuredTeam.id}`) && init?.method === "PATCH");
    expect(patchCall).toBeDefined();
    const body = JSON.parse(String(patchCall?.[1]?.body));
    expect(body.config.retention).toBe("standard");
    expect(body.config.dreaming).toMatchObject({
      enabled: true,
      provider: "manual",
    });
    expect(body.config.dreaming.timezone).toBeUndefined();
    expect(body.config.dreaming.max_outputs).toBeUndefined();
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("shows team profiles with suffix, last used time, and delete action", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /team profiles/i }));

    expect(await screen.findByText("******abc123")).toBeInTheDocument();
    const keyRow = screen.getByText("******abc123").closest("tr");
    expect(keyRow).not.toBeNull();
    expect(within(keyRow as HTMLElement).getByLabelText("Read")).toBeChecked();
    expect(within(keyRow as HTMLElement).getByLabelText("Write")).toBeChecked();
    expect(within(keyRow as HTMLElement).getByLabelText("Profile role default profile")).toHaveValue("manager");
    expect(within(keyRow as HTMLElement).getByText(/May/i)).toBeInTheDocument();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: /delete profile default profile/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles/${keyA().id}`),
        expect.objectContaining({ method: "DELETE" }),
      );
    });
    await waitFor(() => expect(screen.queryByText("******abc123")).not.toBeInTheDocument());
  });

  it("shows IP ban attempts and clears a ban with strikes reset", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()], bans: [securityBan] });
    sessionStorage.setItem("denseMem.controlToken", "secret");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /ip bans/i }));

    expect(await screen.findByText("203.0.113.10")).toBeInTheDocument();
    expect(screen.getByText("11")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /clear ip ban and reset strikes for 203.0.113.10/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/security/bans/203.0.113.10"),
        expect.objectContaining({ method: "DELETE" }),
      );
    });
    await waitFor(() => expect(screen.queryByText("203.0.113.10")).not.toBeInTheDocument());
  });

  it("shows operational metrics", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /^metrics$/i }));

    expect(await screen.findByRole("heading", { name: "Telemetry" })).toBeInTheDocument();
    expect((await screen.findAllByText("42")).length).toBeGreaterThan(0);
    expect(screen.getByText("postgres")).toBeInTheDocument();
    expect(screen.getByText("default profile")).toBeInTheDocument();

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/metrics?window_minutes=60"),
        expect.objectContaining({ method: "GET" }),
      );
    });
  });

  it("marks team overview metrics unavailable when metrics cannot load", async () => {
    mockPortalFetch({ teams: [profileA], keys: [keyA()], metrics: "error" });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });

    expect(await screen.findByLabelText("Team overview")).toHaveTextContent("Metrics unavailable");
    expect(screen.getByLabelText("Team activity")).toHaveTextContent("unavailable");
    expect(screen.getByLabelText("Top signals")).toHaveTextContent("n/a");
  });

  it("shows operation log details, raw log expansion, and page size selection", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /^logs$/i }));

    expect(await screen.findByText("GET /control/api/logs status 200")).toBeInTheDocument();
    expect(screen.getByText("event=control_http_request")).toBeInTheDocument();
    expect(screen.getByText("sso login oidc claims read")).toBeInTheDocument();
    expect(screen.getByText("provider_kind=generic_oidc")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /view raw log GET \/control\/api\/logs status 200/i }));
    expect(await screen.findByLabelText(/Raw log body GET \/control\/api\/logs status 200/i)).toHaveTextContent('"msg": "control_http_request"');
    expect(screen.getByLabelText(/Raw log body GET \/control\/api\/logs status 200/i)).toHaveTextContent('"request_id": "req-control-1"');

    await userEvent.selectOptions(screen.getByLabelText("Rows"), "25");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/logs?limit=25"),
        expect.objectContaining({ method: "GET" }),
      );
    });
  });

  it("shows dream rationale behind an info tooltip", async () => {
    mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /team dreams/i }));

    expect(await screen.findByText("A may affect B.")).toBeInTheDocument();
    expect(screen.getByLabelText("Dreaming status")).toHaveTextContent("Global force");
    const rationale = screen.getByText(dreamRationale);
    expect(rationale.closest("small")).toBeNull();
    expect(rationale.closest(".info-tooltip")).not.toBeNull();
    expect(screen.getByRole("button", { name: /why this hypothesis: A may affect B\./i })).toBeInTheDocument();
  });

  it("edits SSO runtime config", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /^Config$/i }));
    await userEvent.click(within(await screen.findByRole("tablist", { name: /config sections/i })).getByRole("tab", { name: /^sso$/i }));

    expect((await screen.findByRole("tablist", { name: /config sections/i })).closest(".surface")).toBeNull();
    expect(await screen.findByRole("heading", { name: "SSO" })).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Public base URL"), "https://portal.example.com");
    await userEvent.click(screen.getByRole("button", { name: /save config/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/config/sso"),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"SSO_PUBLIC_BASE_URL"`),
        }),
      );
    });
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("edits global timezone config", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /^Config$/i }));

    expect(await screen.findByRole("heading", { name: "General" })).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Timezone", { selector: "select" }), "America/New_York");
    await userEvent.click(screen.getByRole("button", { name: /save config/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/config/general"),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"APP_TIMEZONE"`),
        }),
      );
    });
    const patchCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith("/config/general") && init?.method === "PATCH");
    expect(patchCall).toBeDefined();
    const body = JSON.parse(String(patchCall?.[1]?.body));
    expect(body.items).toEqual([{ key: "APP_TIMEZONE", value: "America/New_York" }]);
  });

  it("edits operation log runtime config from the config subnavigation", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /^Config$/i }));
    await userEvent.click(within(await screen.findByRole("tablist", { name: /config sections/i })).getByRole("tab", { name: /^logs$/i }));

    expect(await screen.findByRole("heading", { name: "Operation Logs" })).toBeInTheDocument();
    const retention = screen.getByLabelText("Retention days");
    await userEvent.clear(retention);
    await userEvent.type(retention, "45");
    await userEvent.click(screen.getByRole("button", { name: /save config/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/config/operation-logs"),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"OPERATION_LOG_RETENTION_DAYS"`),
        }),
      );
    });
  });

  it("edits dreaming runtime config", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /^Config$/i }));
    await userEvent.click(await screen.findByRole("tab", { name: /dreaming/i }));

    expect(await screen.findByRole("heading", { name: "Dreaming" })).toBeInTheDocument();
    expect(screen.queryByText(/^effective /i)).not.toBeInTheDocument();
    const enabledToggle = screen.getByLabelText("Enable scheduled cycle", { selector: "input" });
    expect(enabledToggle).toHaveAttribute("type", "checkbox");
    expect(enabledToggle).not.toBeChecked();
    await userEvent.click(enabledToggle);
    await userEvent.clear(screen.getByLabelText("Cycle start time"));
    await userEvent.type(screen.getByLabelText("Cycle start time"), "02:30");
    await userEvent.click(screen.getByRole("button", { name: /save config/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/config/dreaming"),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"DREAMING_START_TIME_LOCAL"`),
        }),
      );
    });
    const patchCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith("/config/dreaming") && init?.method === "PATCH");
    expect(patchCall).toBeDefined();
    const body = JSON.parse(String(patchCall?.[1]?.body));
    expect(body.items).toEqual(expect.arrayContaining([
      { key: "DREAMING_ENABLED", value: "true" },
      { key: "DREAMING_FORCE_ENABLED", value: "false" },
      { key: "DREAMING_START_TIME_LOCAL", value: "02:30" },
    ]));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("edits community detection runtime config", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /^Config$/i }));
    await userEvent.click(await screen.findByRole("tab", { name: /community/i }));

    expect(await screen.findByRole("heading", { name: "Community Detection" })).toBeInTheDocument();
    const enabledToggle = screen.getByLabelText("Enable scheduled detection", { selector: "input" });
    expect(enabledToggle).not.toBeChecked();
    await userEvent.click(enabledToggle);
    await userEvent.clear(screen.getByLabelText("Jitter seconds"));
    await userEvent.type(screen.getByLabelText("Jitter seconds"), "0");
    await userEvent.click(screen.getByRole("button", { name: /save config/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/config/community-detection"),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"COMMUNITY_DETECTION_ENABLED"`),
        }),
      );
    });
    const patchCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith("/config/community-detection") && init?.method === "PATCH");
    expect(patchCall).toBeDefined();
    const body = JSON.parse(String(patchCall?.[1]?.body));
    expect(body.items).toEqual(expect.arrayContaining([
      { key: "COMMUNITY_DETECTION_ENABLED", value: "true" },
      { key: "COMMUNITY_DETECTION_JITTER_SECONDS", value: "0" },
    ]));
  });

  it("deletes a team", async () => {
    const deleteMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /team settings/i }));
    await userEvent.click(screen.getByRole("button", { name: /^delete$/i }));

    await waitFor(() => {
      expect(deleteMock).toHaveBeenCalledWith(expect.stringContaining(`/teams/${profileA.id}`), expect.objectContaining({ method: "DELETE" }));
    });
  });
});

function mockPortalFetch({
  teams,
  keys,
  createdProfile,
  bans = [],
  logs = operationLogsSnapshot,
  dreams = [dreamSnapshot],
  metrics = metricsSnapshot,
}: {
  teams: Team[];
  keys: TeamProfile[];
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
    if (url.includes("/profiles") && method === "GET") {
      return jsonResponse(page(currentKeys));
    }
    if (url.includes("/profiles/") && url.endsWith("/rotate") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const rotated = { ...(currentKeys.find((key) => url.includes(key.id)) ?? keyA()), name: body.name, key_suffix: "rot8ed", last_used_at: null };
      currentKeys = currentKeys.map((key) => (key.id === rotated.id ? rotated : key));
      return jsonResponse({ data: { api_key: "dm_rotated_once", key: rotated } });
    }
    if (url.includes("/profiles/") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      const current = currentKeys.find((key) => url.endsWith(`/profiles/${key.id}`)) ?? keyA();
      const updated = { ...current, name: body.name ?? current.name, role: body.role ?? current.role, scopes: body.scopes ?? current.scopes };
      currentKeys = currentKeys.map((key) => (key.id === updated.id ? updated : key));
      return jsonResponse({ data: updated });
    }
    if (url.endsWith("/profiles") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      expect(body.label).toBeUndefined();
      const created = { ...keyA(), name: body.name, scopes: body.scopes, role: body.role };
      currentKeys = [created, ...currentKeys];
      return jsonResponse({ data: { api_key: "dm_plain_once", key: created } }, 201);
    }
    if (url.includes("/profiles/") && method === "DELETE") {
      currentKeys = currentKeys.filter((key) => !url.endsWith(`/profiles/${key.id}`));
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

function page<T>(data: T[]) {
  return { data, pagination: { limit: 20, offset: 0, total: data.length } };
}

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
