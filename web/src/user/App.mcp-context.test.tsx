import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserPortalApp } from "./App";
import { expectCurrentWorkspace, jsonResponse } from "./App.test-helpers";
import type { UserSession } from "./api";

const baseSession: UserSession = {
  mcp_public_base_url: "https://memory.example.test",
  team: {
    id: "11111111-1111-4111-8111-111111111111",
    name: "Research Team",
    description: "",
    created_at: "2026-05-01T12:00:00Z",
    updated_at: "2026-05-01T12:00:00Z",
  },
  membership: {
    team_id: "11111111-1111-4111-8111-111111111111",
    name: "Mine",
    grants: ["read"],
    role: "member",
  },
  credential: {
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
    memory_binding: "shared_only",
    memory_space_kind: "team_shared",
  },
  teams: [],
  personal_credentials: [],
};

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.mocked(navigator.clipboard.writeText).mockClear();
});

describe("UserPortalApp MCP context", () => {
  it("shows and copies the configured team-scoped MCP URL", async () => {
    mockUserSession(baseSession);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    expect(screen.getByText(baseSession.team.id)).toBeInTheDocument();
    const expectedURL = `https://memory.example.test/teams/${baseSession.team.id}/mcp`;
    expect(screen.getByText(expectedURL)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Copy MCP URL" }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(expectedURL);
    expect(screen.queryByText(/browser origin/i)).not.toBeInTheDocument();
  });

  it("labels the browser-origin fallback for MCP URLs", async () => {
    mockUserSession({ ...baseSession, mcp_public_base_url: "" });

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    expect(screen.getByText(`${window.location.origin}/teams/${baseSession.team.id}/mcp`)).toBeInTheDocument();
    expect(screen.getByText("Using this browser origin because MCP_PUBLIC_BASE_URL is not configured.")).toBeInTheDocument();
  });
});

function mockUserSession(session: UserSession) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url === "/ui/api/sso/providers" && method === "GET") {
      return jsonResponse({ data: [] });
    }
    if (url === "/ui/api/session" && method === "GET") {
      return jsonResponse({ data: session });
    }
    return jsonResponse({ message: "not found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
}
