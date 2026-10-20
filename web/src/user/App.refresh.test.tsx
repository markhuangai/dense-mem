import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { profileA } from "../App.test-helpers";
import { UserPortalApp } from "./App";
import { jsonResponse } from "./App.test-helpers";
import type { UserSession } from "./api";

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  sessionStorage.setItem("denseMem.userApiKey", "dm_manager");
  vi.restoreAllMocks();
});

it.each(["unchanged", "changed", "failed"])("discards a confirmed user team draft before a %s session refresh", async (outcome) => {
  const memoryAccess = { private: { read: true, write: true }, team_shared: { read: true, write: true } };
  const session: UserSession = {
    team: profileA, mcp_public_base_url: "", teams: [], personal_credentials: [],
    credential: {
      id: "22222222-2222-4222-8222-222222222222", team_id: profileA.id, name: "Manager",
      key_suffix: "abc123", scopes: ["read", "write"], role: "manager", rate_limit: 120,
      last_used_at: null, expires_at: null, created_at: profileA.created_at, memory_access: memoryAccess,
    },
    memory_access: memoryAccess,
    membership: { team_id: profileA.id, name: "Manager", role: "manager", grants: ["read", "write"] },
  };
  let refreshing = false;
  let finish!: (response: Response) => void;
  const refreshed = new Promise<Response>((resolve) => { finish = resolve; });
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/ui/api/session") {
      if (refreshing) return refreshed;
      if (!new Headers(init?.headers).get("Authorization")) return jsonResponse({ message: "unauthorized" }, 401);
      return jsonResponse({ data: session });
    }
    if (String(input) === "/ui/api/sso/providers") return jsonResponse({ data: [] });
    if (String(input) === "/ui/api/team/credentials") return jsonResponse({ data: [], pagination: { limit: 20, offset: 0, total: 0 } });
    return jsonResponse({ message: "unexpected request" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<UserPortalApp />);
  await screen.findByLabelText("Current workspace");
  await userEvent.click(await screen.findByRole("link", { name: "Team" }));
  const name = await screen.findByLabelText("Name", { exact: true });
  await userEvent.type(name, " edited");
  await userEvent.type(screen.getByLabelText("Description", { exact: true }), "discard this");
  const count = fetchMock.mock.calls.length;
  await userEvent.click(screen.getByRole("button", { name: "Refresh session" }));
  await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
  expect(name).toHaveValue("Default edited");
  expect(fetchMock.mock.calls.length).toBe(count);
  refreshing = true;
  await userEvent.click(screen.getByRole("button", { name: "Refresh session" }));
  await userEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
  await waitFor(() => expect(name).toHaveValue("Default"));
  expect(name).toBeDisabled();
  expect(screen.getByLabelText("Description", { exact: true })).toHaveValue("");
  const team = outcome === "changed" ? { ...profileA, name: "Updated on server", description: "New description" } : profileA;
  finish(outcome === "failed" ? jsonResponse({ message: "refresh unavailable" }, 503) : jsonResponse({ data: { ...session, team } }));
  await waitFor(() => expect(name).toBeEnabled());
  await waitFor(() => expect(name).toHaveValue(team.name));
  if (outcome === "failed") expect(await screen.findByRole("alert")).toHaveTextContent("refresh unavailable");
  await userEvent.click(screen.getByRole("link", { name: "Search memory" }));
  expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Name", { exact: true })).not.toBeInTheDocument();
});
