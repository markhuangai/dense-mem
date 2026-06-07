import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserPortalApp } from "./App";
import { UserKey, UserSession } from "./api";

const baseSession: UserSession = {
  team: {
    id: "11111111-1111-4111-8111-111111111111",
    name: "Research Team",
    description: "",
    created_at: "2026-05-01T12:00:00Z",
    updated_at: "2026-05-01T12:00:00Z",
  },
  key: {
    id: "22222222-2222-4222-8222-222222222222",
    team_id: "11111111-1111-4111-8111-111111111111",
    name: "Mine",
    key_suffix: "abc123",
    scopes: ["read"],
    role: "member",
    rate_limit: 120,
    last_used_at: null,
    expires_at: null,
    created_at: "2026-05-01T12:00:00Z",
  },
  can_rotate: false,
  can_manage_team: false,
};

const memberProfile: UserKey = {
  id: "33333333-3333-4333-8333-333333333333",
  team_id: baseSession.team.id,
  name: "Reader",
  key_suffix: "def456",
  scopes: ["read"],
  role: "member",
  rate_limit: 120,
  last_used_at: null,
  expires_at: null,
  created_at: "2026-05-01T12:00:00Z",
};

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("UserPortalApp", () => {
  it("logs in with an API key and does not call team profile list APIs", async () => {
    const fetchMock = mockUserFetch(baseSession);
    render(<UserPortalApp />);

    await userEvent.type(screen.getByLabelText(/api key/i), "dm_key");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    expect(await screen.findByText("Research Team")).toBeInTheDocument();
    expect(await screen.findByText("Mine")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/profiles"))).toBe(false);
  });

  it("disables self rotation for read-only keys", async () => {
    mockUserFetch(baseSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /my key/i }));

    expect(await screen.findByRole("button", { name: /regenerate key/i })).toBeDisabled();
  });

  it("rotates the current write-scoped key and stores the replacement", async () => {
    const writeSession = {
      ...baseSession,
      key: { ...baseSession.key, scopes: ["read", "write"] },
      can_rotate: true,
    };
    const fetchMock = mockUserFetch(writeSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_old");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /my key/i }));
    await userEvent.click(await screen.findByRole("button", { name: /regenerate key/i }));

    expect(await screen.findByText("dm_new_plaintext")).toBeInTheDocument();
    expect(sessionStorage.getItem("denseMem.userApiKey")).toBe("dm_new_plaintext");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/ui/api/key/rotate", expect.objectContaining({ method: "POST" }));
    });
  });

  it("lets a manager update the team and manage member profiles", async () => {
    const managerSession: UserSession = {
      ...baseSession,
      key: {
        ...baseSession.key,
        name: "Manager",
        scopes: ["read", "write"],
        role: "manager",
      },
      can_rotate: true,
      can_manage_team: true,
    };
    const managerProfile: UserKey = { ...managerSession.key };
    const fetchMock = mockUserFetch(managerSession, [managerProfile, memberProfile]);
    sessionStorage.setItem("denseMem.userApiKey", "dm_manager");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /^team$/i }));

    expect(await screen.findByLabelText("Profile name Manager")).toBeDisabled();
    expect(screen.getByRole("button", { name: /regenerate key for profile Manager/i })).toBeDisabled();

    const teamName = screen.getByLabelText("Name", { selector: "#user-team-name" });
    await userEvent.clear(teamName);
    await userEvent.type(teamName, "Renamed Team");
    await userEvent.click(screen.getByRole("button", { name: /save team/i }));
    expect(await screen.findByText("Renamed Team")).toBeInTheDocument();

    const newProfileName = screen.getByLabelText("Profile name", { selector: "#managed-profile-name" });
    await userEvent.clear(newProfileName);
    await userEvent.type(newProfileName, "Writer");
    await userEvent.click(screen.getByRole("button", { name: /create member profile/i }));
    expect(await screen.findByText("dm_member_plaintext")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/api/v1/teams/${baseSession.team.id}/profiles`),
        expect.objectContaining({
          method: "POST",
          body: expect.not.stringContaining("role"),
        }),
      );
    });

    const memberName = await screen.findByLabelText("Profile name Reader");
    await userEvent.clear(memberName);
    await userEvent.type(memberName, "Reader Updated");
    await userEvent.click(screen.getByRole("button", { name: /save profile Reader/i }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/api/v1/teams/${baseSession.team.id}/profiles/${memberProfile.id}`),
        expect.objectContaining({
          method: "PATCH",
          body: expect.stringContaining(`"name":"Reader Updated"`),
        }),
      );
    });

    await userEvent.click(screen.getByRole("button", { name: /regenerate key for profile Reader Updated/i }));
    expect(await screen.findByText("dm_member_rotated")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/api/v1/teams/${baseSession.team.id}/profiles/${memberProfile.id}/rotate`),
        expect.objectContaining({ method: "POST" }),
      );
    });

    await userEvent.click(screen.getByRole("button", { name: /delete profile Reader Updated/i }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/api/v1/teams/${baseSession.team.id}/profiles/${memberProfile.id}`),
        expect.objectContaining({ method: "DELETE" }),
      );
    });
  });
});

function mockUserFetch(session: UserSession, profiles: UserKey[] = []) {
  let currentTeam = session.team;
  let currentProfiles = profiles;
  const rotatedSession = {
    ...session,
    key: { ...session.key, key_suffix: "new123", last_used_at: null },
  };
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url === "/ui/api/session" && method === "GET") {
      const auth = (init?.headers as Record<string, string> | undefined)?.Authorization ?? "";
      const selectedSession = auth.includes("dm_new_plaintext") ? rotatedSession : session;
      return jsonResponse({ data: { ...selectedSession, team: currentTeam } });
    }
    if (url === "/ui/api/key/rotate" && method === "POST") {
      return jsonResponse({
        data: {
          api_key: "dm_new_plaintext",
          key: rotatedSession.key,
        },
      });
    }
    if (url === `/api/v1/teams/${currentTeam.id}` && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentTeam = { ...currentTeam, name: body.name, description: body.description };
      return jsonResponse({ data: currentTeam });
    }
    if (url === `/api/v1/teams/${currentTeam.id}/profiles` && method === "GET") {
      return jsonResponse({ data: currentProfiles, pagination: { limit: 20, offset: 0, total: currentProfiles.length } });
    }
    if (url === `/api/v1/teams/${currentTeam.id}/profiles` && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const created: UserKey = {
        ...memberProfile,
        id: "44444444-4444-4444-8444-444444444444",
        name: body.name,
        scopes: body.scopes,
        role: "member",
        key_suffix: "new456",
      };
      currentProfiles = [created, ...currentProfiles];
      return jsonResponse({ data: { api_key: "dm_member_plaintext", key: created } }, 201);
    }
    if (url.includes(`/api/v1/teams/${currentTeam.id}/profiles/`) && url.endsWith("/rotate") && method === "POST") {
      const rotated = { ...(currentProfiles.find((profile) => url.includes(profile.id)) ?? memberProfile), key_suffix: "rot789" };
      currentProfiles = currentProfiles.map((profile) => (profile.id === rotated.id ? rotated : profile));
      return jsonResponse({ data: { api_key: "dm_member_rotated", key: rotated } });
    }
    if (url.includes(`/api/v1/teams/${currentTeam.id}/profiles/`) && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      const current = currentProfiles.find((profile) => url.endsWith(`/profiles/${profile.id}`)) ?? memberProfile;
      const updated = { ...current, name: body.name ?? current.name };
      currentProfiles = currentProfiles.map((profile) => (profile.id === updated.id ? updated : profile));
      return jsonResponse({ data: updated });
    }
    if (url.includes(`/api/v1/teams/${currentTeam.id}/profiles/`) && method === "DELETE") {
      currentProfiles = currentProfiles.filter((profile) => !url.endsWith(`/profiles/${profile.id}`));
      return jsonResponse({ data: { status: "deleted" } });
    }
    if (url.startsWith("/api/v1/communities")) {
      return jsonResponse({ items: [] });
    }
    if (url.startsWith("/api/v1/recall")) {
      return jsonResponse({ data: [] });
    }
    return jsonResponse({ message: "not found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
