import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserPortalApp } from "./App";
import type { UserSession } from "./api";

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
  personal_key: null,
  can_create_personal_key: false,
  can_rotate_personal_key: false,
  personal_key_max_scopes: [],
};

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});

describe("UserPortalApp cookie sessions", () => {
  it("exchanges a remembered API-key login without storing the raw key", async () => {
    let portalSessionCreated = false;
    const cookieSession: UserSession = { ...baseSession, auth_method: "api_key_session" };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const auth = new Headers(init?.headers).get("Authorization") ?? "";
      if (url === "/ui/api/sso/providers" && method === "GET") {
        return jsonResponse({ data: [] });
      }
      if (url === "/ui/api/session" && method === "GET") {
        if (auth === "Bearer dm_key") {
          return jsonResponse({ data: baseSession });
        }
        return portalSessionCreated
          ? jsonResponse({ data: cookieSession })
          : jsonResponse({ code: "AUTH_MISSING", message: "authentication required", details: null }, 401);
      }
      if (url === "/ui/api/session" && method === "POST") {
        expect(auth).toBe("Bearer dm_key");
        expect(init?.body).toBe(JSON.stringify({ remember: true }));
        portalSessionCreated = true;
        return jsonResponse({ data: { status: "signed_in" } });
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<UserPortalApp />);
    await userEvent.type(screen.getByLabelText(/api key/i), "dm_key");
    await userEvent.click(screen.getByRole("checkbox", { name: /7 days/i }));
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    const workspace = await screen.findByLabelText("Current workspace");
    expect(workspace).toHaveTextContent("Research Team");
    expect(sessionStorage.getItem("denseMem.userApiKey")).toBeNull();
    expect(fetchMock.mock.calls.some(([url, request]) => String(url) === "/ui/api/session" && request?.method === "POST")).toBe(true);
  });
});

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
