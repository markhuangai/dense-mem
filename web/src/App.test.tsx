import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { TeamProfile, Team, SecurityBan, SecuritySettings, ControlMetrics } from "./api";

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

function keyA(profileId = profileA.id): TeamProfile {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    team_id: profileA.id,
    name: "default profile",
    key_suffix: "abc123",
    scopes: ["read", "write"],
    rate_limit: 120,
    last_used_at: "2026-05-02T13:00:00Z",
    expires_at: null,
    created_at: "2026-04-30T12:00:00Z",
  };
}

beforeEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
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
    await userEvent.type(screen.getByLabelText("Name", { selector: "#new-team-name" }), "ab");
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
    await userEvent.type(screen.getByLabelText("Name", { selector: "#new-team-name" }), "Work Team");
    await userEvent.type(screen.getByLabelText("Description", { selector: "#new-team-description" }), "for work");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("heading", { name: "Work Team" })).toBeInTheDocument();
  });

  it("creates an API key and shows plaintext once", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /profiles & api keys/i }));
    await userEvent.selectOptions(screen.getByLabelText(/permission/i), "read");
    await userEvent.click(screen.getByRole("button", { name: /create profile/i }));

    expect(await screen.findByText("dm_plain_once")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles`),
        expect.objectContaining({
          method: "POST",
          body: expect.stringContaining(`"scopes":["read"]`),
        }),
      );
    });
    await userEvent.click(screen.getByRole("button", { name: /dismiss api key/i }));
    await waitFor(() => expect(screen.queryByText("dm_plain_once")).not.toBeInTheDocument());
  });

  it("updates team and profile names and regenerates a key", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });

    const teamName = screen.getByLabelText("Name", { selector: "#team-name" });
    await userEvent.clear(teamName);
    await userEvent.type(teamName, "Renamed Team");
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));
    expect(await screen.findByRole("heading", { name: "Renamed Team" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /profiles & api keys/i }));
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

    await userEvent.click(screen.getByRole("button", { name: /regenerate key for profile Research profile/i }));
    expect(await screen.findByText("dm_rotated_once")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/teams/${profileA.id}/profiles/${keyA().id}/rotate`),
        expect.objectContaining({ method: "POST" }),
      );
    });
  });

  it("shows team profiles with suffix, last used time, and delete action", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: /profiles & api keys/i }));

    expect(await screen.findByText("******abc123")).toBeInTheDocument();
    const keyRow = screen.getByText("******abc123").closest("tr");
    expect(keyRow).not.toBeNull();
    expect(within(keyRow as HTMLElement).getByText("Read/write")).toBeInTheDocument();
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

  it("deletes a team", async () => {
    const deleteMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
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
}: {
  teams: Team[];
  keys: TeamProfile[];
  createdProfile?: Team;
  bans?: SecurityBan[];
}) {
  let currentProfiles = teams;
  let currentKeys = keys;
  let currentBans = bans;
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url.endsWith("/session")) {
      return jsonResponse({ data: { authenticated: true } });
    }
    if (url.includes("/telemetry") && method === "GET") {
      return jsonResponse({ data: telemetrySnapshot });
    }
    if (url.includes("/metrics") && method === "GET") {
      return jsonResponse({ data: metricsSnapshot });
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
      const updated = { ...(currentKeys.find((key) => url.endsWith(`/profiles/${key.id}`)) ?? keyA()), name: body.name };
      currentKeys = currentKeys.map((key) => (key.id === updated.id ? updated : key));
      return jsonResponse({ data: updated });
    }
    if (url.endsWith("/profiles") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      expect(body.label).toBeUndefined();
      const created = { ...keyA(), name: body.name, scopes: body.scopes };
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
      const updated = { ...(currentProfiles.find((team) => url.endsWith(`/teams/${team.id}`)) ?? currentProfiles[0]), name: body.name, description: body.description };
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
