import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { CommunityDetectionConfig, ControlMetrics, Credential, Dream, DreamRun, DreamStatus, DreamingConfig, GeneralConfig, OperationLog, OperationLogConfig, SecurityBan, SecuritySettings, SSOConfig, Team } from "./api";

import { communityDetectionConfigSnapshot, dreamRationale, dreamRunSnapshot, dreamSnapshot, dreamStatusSnapshot, generalConfigSnapshot, jsonResponse, keyA, metricsSnapshot, mockPortalFetch, operationLogConfigSnapshot, operationLogsSnapshot, page, profileA, securityBan, securitySettings, ssoConfigSnapshot } from "./App.test-helpers";

beforeEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
  vi.mocked(navigator.clipboard.writeText).mockClear();
});

describe("App", () => {
  it("validates the token before opening the portal", async () => {
    const fetchMock = vi.fn()
      .mockImplementation(() => Promise.resolve(jsonResponse({ message: "invalid token" }, 401)));
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
    await userEvent.click(screen.getByRole("button", { name: /team credentials/i }));
    await userEvent.click(screen.getByLabelText("Recall feedback"));
    await userEvent.click(screen.getByRole("button", { name: /create credential/i }));

    expect(await screen.findByDisplayValue("dm_plain_once")).toHaveAccessibleName("Generated API key");
    await userEvent.click(screen.getByRole("button", { name: /copy api key/i }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("dm_plain_once");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/credentials`),
        expect.objectContaining({
          method: "POST",
          body: expect.stringContaining(`"scopes":["read","write","feedback:read"]`),
        }),
      );
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/credentials`),
        expect.objectContaining({
          method: "POST",
          body: expect.stringContaining(`"role":"manager"`),
        }),
      );
    });
    await userEvent.click(screen.getByRole("button", { name: /dismiss api key/i }));
    await waitFor(() => expect(screen.queryByDisplayValue("dm_plain_once")).not.toBeInTheDocument());
  });

  it("updates team and credential names and regenerates a key", async () => {
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

    await userEvent.click(screen.getByRole("button", { name: /team credentials/i }));
    const credentialName = await screen.findByLabelText("Credential name default credential");
    await userEvent.clear(credentialName);
    await userEvent.type(credentialName, "Research credential");
    await userEvent.click(screen.getByRole("button", { name: /save credential default credential/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/credentials/${keyA().id}`),
        expect.objectContaining({ method: "PATCH" }),
      );
    });
    expect(await screen.findByDisplayValue("Research credential")).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Credential role Research credential"), "member");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/credentials/${keyA().id}`),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"role":"member"`),
        }),
      );
    });

    const credentialRow = (await screen.findByDisplayValue("Research credential")).closest("tr");
    expect(credentialRow).not.toBeNull();
    await userEvent.click(within(credentialRow as HTMLElement).getByLabelText("Recall feedback"));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/credentials/${keyA().id}`),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"scopes":["read","write","feedback:read"]`),
        }),
      );
    });

    await userEvent.click(screen.getByRole("button", { name: /regenerate api key for credential Research credential/i }));
    expect(await screen.findByDisplayValue("dm_rotated_once")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/credentials/${keyA().id}/rotate`),
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

  it("shows team credentials with suffix, last used time, and delete action", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /team credentials/i }));

    expect(await screen.findByText("******abc123")).toBeInTheDocument();
    const keyRow = screen.getByText("******abc123").closest("tr");
    expect(keyRow).not.toBeNull();
    expect(within(keyRow as HTMLElement).getByLabelText("Read")).toBeChecked();
    expect(within(keyRow as HTMLElement).getByLabelText("Write")).toBeChecked();
    expect(within(keyRow as HTMLElement).getByLabelText("Credential role default credential")).toHaveValue("manager");
    expect(within(keyRow as HTMLElement).getByText(/May/i)).toBeInTheDocument();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: /delete credential default credential/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/credentials/${keyA().id}`),
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

  it("keeps team-overview request summaries without the retired Metrics tab", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    expect(screen.queryByRole("button", { name: /^metrics$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open Metrics" })).not.toBeInTheDocument();
    expect(await screen.findByLabelText("Team activity")).toHaveTextContent("42");
    expect(screen.getByLabelText("Recent alerts")).toHaveTextContent("Request errors detected");

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

    await waitFor(() => expect(screen.getByLabelText("Team overview")).toHaveTextContent("Metrics unavailable"));
    expect(screen.getByLabelText("Team activity")).toHaveTextContent("unavailable");
    expect(screen.getByLabelText("Top signals")).toHaveTextContent("n/a");
  });

  it("keeps dependency diagnostics in a dedicated wrapping metric row", async () => {
    const degradedMetrics: ControlMetrics = {
      ...metricsSnapshot,
      dependencies: [
        { name: "postgres", status: "ok", latency_ms: 3 },
        { name: "search_readiness", status: "error", reason_code: "check_failed", latency_ms: 161 },
        { name: "redis", status: "degraded", reason_code: "single_node_mode", latency_ms: null },
      ],
    };
    mockPortalFetch({ teams: [profileA], keys: [keyA()], metrics: degradedMetrics });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });

    const topSignals = screen.getByLabelText("Top signals");
    const dependencyDetails = await within(topSignals).findByText(/search_readiness/);
    const dependencyRow = dependencyDetails.closest(".metric-row");
    expect(dependencyRow).not.toBeNull();
    expect(within(dependencyRow as HTMLElement).getByText("Dependency checks")).toHaveClass("metric-label");
    expect(dependencyDetails).toHaveClass("metric-detail");
    expect(dependencyRow).toHaveTextContent("1/3");
    expect(dependencyRow).toHaveTextContent("redis · single_node_mode · latency n/a");
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
    expect(screen.getByText("submission failed")).toBeInTheDocument();
    expect(screen.getByText("reference_type=submission")).toBeInTheDocument();
	expect(screen.getByText("reason_code=provider_unavailable")).toBeInTheDocument();

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

  it("clears a related-log query on normal Logs navigation", async () => {
    const invocation = {
      team_id: profileA.id, owner_profile_id: "owner-1", invocation_id: "invocation-app", canonical_attempt_id: "",
      request_hash: "hash-app", correlation_id: "stale-correlation", classification: "execution", outcome: "completed",
      phase: "", protected_cause: "", delivery_stage: "unknown_receipt", retryable: false,
      duration_ms: 1, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    };
    mockPortalFetch({
      teams: [profileA],
      keys: [keyA()],
      rememberInvocation: {
        summary: invocation,
        detail: { ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured" },
      },
    });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /^Remember Attempts$/ }));
    await userEvent.click(await screen.findByRole("button", { name: "Inspect Remember call invocation-app" }));
    await userEvent.click(await screen.findByRole("button", { name: "View related logs" }));
    expect(await screen.findByRole("heading", { name: "Operation Logs" })).toBeInTheDocument();
    expect(screen.getByLabelText("Correlation ID")).toHaveValue("stale-correlation");
    await userEvent.click(screen.getByRole("button", { name: /^Teams$/i }));
    await userEvent.click(screen.getByRole("button", { name: /^Logs$/i }));
    expect(await screen.findByRole("heading", { name: "Operation Logs" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Correlation ID")).toHaveValue(""));
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
