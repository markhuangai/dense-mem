import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { TeamProfile, Team, SecuritySettings } from "./api";

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

function keyA(profileId = profileA.id): TeamProfile {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    team_id: profileA.id,
    name: "default profile",
    key_suffix: "abc123",
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
    await screen.findByRole("button", { name: "Default" });
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
    await screen.findByRole("button", { name: "Default" });
    await userEvent.type(screen.getByLabelText("Name", { selector: "#new-team-name" }), "Work Team");
    await userEvent.type(screen.getByLabelText("Description", { selector: "#new-team-description" }), "for work");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("heading", { name: "Work Team" })).toBeInTheDocument();
  });

  it("creates an API key and shows plaintext once", async () => {
    mockPortalFetch({ teams: [profileA], keys: [] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: "Default" });
    await userEvent.click(screen.getByRole("button", { name: /create profile/i }));

    expect(await screen.findByText("dm_plain_once")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /dismiss api key/i }));
    await waitFor(() => expect(screen.queryByText("dm_plain_once")).not.toBeInTheDocument());
  });

  it("shows team profiles with suffix, last used time, and delete action", async () => {
    const fetchMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: "Default" });

    expect(await screen.findByText("******abc123")).toBeInTheDocument();
    const keyRow = screen.getByText("******abc123").closest("tr");
    expect(keyRow).not.toBeNull();
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

  it("deletes a team", async () => {
    const deleteMock = mockPortalFetch({ teams: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<App />);
    await screen.findByRole("button", { name: "Default" });
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
}: {
  teams: Team[];
  keys: TeamProfile[];
  createdProfile?: Team;
}) {
  let currentProfiles = teams;
  let currentKeys = keys;
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url.endsWith("/session")) {
      return jsonResponse({ data: { authenticated: true } });
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
    if (url.includes("/profiles") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      expect(body.label).toBeUndefined();
      const created = { ...keyA(), name: body.name };
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
      return jsonResponse(page([]));
    }
    if (url.endsWith("/security/bans") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      return jsonResponse({ data: { ip: body.ip, reason: body.reason, source: "manual", failure_count: 0, banned_at: "2026-05-01T12:00:00Z", expires_at: null, last_failed_at: null, metadata: {}, revoked_at: null } }, 201);
    }
    if (url.includes("/security/bans/") && method === "DELETE") {
      return jsonResponse({ data: { status: "deleted" } });
    }
    if (method === "PATCH") {
      return jsonResponse({ data: currentProfiles[0] });
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
