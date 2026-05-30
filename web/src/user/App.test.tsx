import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserPortalApp } from "./App";
import { UserSession } from "./api";

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
    rate_limit: 120,
    last_used_at: null,
    expires_at: null,
    created_at: "2026-05-01T12:00:00Z",
  },
  can_rotate: false,
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
});

function mockUserFetch(session: UserSession) {
  const rotatedSession = {
    ...session,
    key: { ...session.key, key_suffix: "new123", last_used_at: null },
  };
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url === "/ui/api/session" && method === "GET") {
      const auth = (init?.headers as Record<string, string> | undefined)?.Authorization ?? "";
      return jsonResponse({ data: auth.includes("dm_new_plaintext") ? rotatedSession : session });
    }
    if (url === "/ui/api/key/rotate" && method === "POST") {
      return jsonResponse({
        data: {
          api_key: "dm_new_plaintext",
          key: rotatedSession.key,
        },
      });
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
