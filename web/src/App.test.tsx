import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { ApiKey, Profile, SecuritySettings } from "./api";

const profileA: Profile = {
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

function keyA(profileId = profileA.id): ApiKey {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    profile_id: profileId,
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

  it("shows profile validation states", async () => {
    mockPortalFetch({ profiles: [profileA], keys: [] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: "Default" });
    await userEvent.type(screen.getByLabelText("Name", { selector: "#new-profile-name" }), "ab");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Name must be at least 3 characters.");
  });

  it("creates a profile and selects it", async () => {
    const created: Profile = {
      ...profileA,
      id: "33333333-3333-4333-8333-333333333333",
      name: "Work Profile",
      description: "for work",
    };
    mockPortalFetch({ profiles: [profileA], keys: [], createdProfile: created });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: "Default" });
    await userEvent.type(screen.getByLabelText("Name", { selector: "#new-profile-name" }), "Work Profile");
    await userEvent.type(screen.getByLabelText("Description", { selector: "#new-profile-description" }), "for work");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("heading", { name: "Work Profile" })).toBeInTheDocument();
  });

  it("creates an API key and shows plaintext once", async () => {
    mockPortalFetch({ profiles: [profileA], keys: [] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: "Default" });
    await userEvent.click(screen.getByRole("button", { name: /create key/i }));

    expect(await screen.findByText("dm_plain_once")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /dismiss api key/i }));
    await waitFor(() => expect(screen.queryByText("dm_plain_once")).not.toBeInTheDocument());
  });

  it("shows API keys with suffix, last used time, and delete action", async () => {
    const fetchMock = mockPortalFetch({ profiles: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: "Default" });

    expect(await screen.findByText("******abc123")).toBeInTheDocument();
    const keyRow = screen.getByText("******abc123").closest("tr");
    expect(keyRow).not.toBeNull();
    expect(within(keyRow as HTMLElement).getByText(/May/i)).toBeInTheDocument();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: /delete api key \*\*\*\*\*\*abc123/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/profiles/${profileA.id}/api-keys/${keyA().id}`),
        expect.objectContaining({ method: "DELETE" }),
      );
    });
    await waitFor(() => expect(screen.queryByText("******abc123")).not.toBeInTheDocument());
  });

  it("deletes a profile", async () => {
    const deleteMock = mockPortalFetch({ profiles: [profileA], keys: [keyA()] });
    sessionStorage.setItem("denseMem.controlToken", "secret");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<App />);
    await screen.findByRole("button", { name: "Default" });
    await userEvent.click(screen.getByRole("button", { name: /^delete$/i }));

    await waitFor(() => {
      expect(deleteMock).toHaveBeenCalledWith(expect.stringContaining(`/profiles/${profileA.id}`), expect.objectContaining({ method: "DELETE" }));
    });
  });
});

function mockPortalFetch({
  profiles,
  keys,
  createdProfile,
}: {
  profiles: Profile[];
  keys: ApiKey[];
  createdProfile?: Profile;
}) {
  let currentProfiles = profiles;
  let currentKeys = keys;
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url.endsWith("/session")) {
      return jsonResponse({ data: { authenticated: true } });
    }
    if (url.endsWith("/profiles") && method === "GET") {
      return jsonResponse(page(currentProfiles));
    }
    if (url.endsWith("/profiles") && method === "POST") {
      const profile = createdProfile ?? {
        ...profileA,
        id: "33333333-3333-4333-8333-333333333333",
        name: JSON.parse(String(init?.body)).name,
      };
      currentProfiles = [...currentProfiles, profile];
      return jsonResponse({ data: profile }, 201);
    }
    if (url.includes("/api-keys") && method === "GET") {
      return jsonResponse(page(currentKeys));
    }
    if (url.includes("/api-keys") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      expect(body.label).toBeUndefined();
      const created = keyA();
      currentKeys = [created, ...currentKeys];
      return jsonResponse({ data: { api_key: "dm_plain_once", key: created } }, 201);
    }
    if (url.includes("/api-keys") && method === "DELETE") {
      currentKeys = currentKeys.filter((key) => !url.endsWith(`/api-keys/${key.id}`));
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
      currentProfiles = currentProfiles.filter((profile) => !url.endsWith(`/profiles/${profile.id}`));
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
