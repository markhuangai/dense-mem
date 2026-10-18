import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import {
  dreamStatusSnapshot,
  jsonResponse,
  metricsSnapshot,
  mockPortalFetch,
  page,
  profileA,
} from "./App.test-helpers";

beforeEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
  vi.mocked(navigator.clipboard.writeText).mockClear();
});

describe("App coverage additions", () => {
  it("derives control SSO sessions and signs out cleanly", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === "/control/auth/providers") return jsonResponse({ data: [] });
      if (url.endsWith("/session")) return jsonResponse({ data: { authenticated: true } });
      if (url.endsWith("/teams") && (init?.method ?? "GET") === "GET") return jsonResponse(page([]));
      if (url === "/control/auth/logout") return jsonResponse({});
      return jsonResponse({ data: {} });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    expect(await screen.findByRole("heading", { name: "Create your first team" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByLabelText("Control token")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/control/auth/logout", expect.objectContaining({ method: "POST" }));
    cleanup();
  });

  it("loads secondary control sections and team rail empty states", async () => {
    mockPortalFetch({ teams: [profileA], keys: [] });
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    const teamSearch = screen.getByLabelText("Search teams");
    await userEvent.type(teamSearch, "no matching team");
    expect(screen.getByText("No matching teams")).toBeInTheDocument();
    await userEvent.clear(teamSearch);

    await userEvent.click(screen.getByRole("link", { name: "Recall feedback" }));
    expect(await screen.findByRole("heading", { name: "Recall Feedback" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "Identity & access" }));
    expect(await screen.findByRole("heading", { name: /SSO providers/i })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "All teams" }));
    await userEvent.click(screen.getByRole("link", { name: "Dream history" }));
    expect(await screen.findByRole("heading", { name: "Dream history" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "All teams" }));
    await userEvent.click(screen.getByRole("link", { name: "Conflicts" }));
    expect(await screen.findByRole("heading", { name: "Conflict queue" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "All teams" }));
    await userEvent.click(screen.getByRole("link", { name: "Remember history" }));
    expect(await screen.findByRole("heading", { name: "Remember Attempts" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "Search readiness" }));
    expect(await screen.findByRole("heading", { name: /Search convergence/i })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "Settings" }));
    expect(await screen.findByRole("heading", { name: "General" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "All teams" }));
    await userEvent.click(screen.getByRole("link", { name: "Team settings" }));
    const teamName = screen.getByLabelText("Name", { selector: "#team-name" });
    await userEvent.clear(teamName);
    await userEvent.type(teamName, "ab");
    await userEvent.click(screen.getByRole("button", { name: /^Save$/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at least 3");
    await userEvent.click(screen.getByRole("button", { name: /^Delete$/i }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  }, 15000);

  it("reports team editor save and delete failures", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/session")) return jsonResponse({ data: { authenticated: true } });
      if (url.endsWith("/teams") && method === "GET") return jsonResponse(page([profileA]));
      if (url.endsWith("/teams") && method === "POST") return jsonResponse({ message: "team create failed" }, 500);
      if (url.includes("/credentials") && method === "GET") return jsonResponse(page([]));
      if (url.includes("/metrics")) return jsonResponse({ data: metricsSnapshot });
      if (url.includes("/community/status")) return jsonResponse({ data: { effective_config: { enabled: false }, latest_run: null, pending_count: 0 } });
      if (url.includes("/dreaming/status")) return jsonResponse({ data: dreamStatusSnapshot });
      if (method === "PATCH") return jsonResponse({ message: "team update failed" }, 500);
      if (method === "DELETE") return jsonResponse({ message: "team delete failed" }, 500);
      return jsonResponse({ data: {} });
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    await screen.findByRole("button", { name: /Default/ });
    await userEvent.click(screen.getByRole("button", { name: "New team" }));
    await userEvent.type(screen.getByLabelText("Name", { selector: "#new-team-name" }), "Created");
    await userEvent.click(screen.getByRole("button", { name: /^Create$/i }));
    expect(await screen.findByText("team create failed")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    await userEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    await userEvent.click(screen.getByRole("button", { name: /Default/ }));
    await userEvent.click(screen.getByRole("link", { name: "Team settings" }));
    const teamName = screen.getByLabelText("Name", { selector: "#team-name" });
    await userEvent.clear(teamName);
    await userEvent.type(teamName, "ab");
    await userEvent.click(screen.getByRole("button", { name: /^Save$/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at least 3");
    await userEvent.clear(teamName);
    await userEvent.type(teamName, "Updated");
    await userEvent.type(screen.getByLabelText("Description", { selector: "#team-description" }), " updated");
    await userEvent.click(screen.getByRole("button", { name: /^Save$/i }));
    expect(await screen.findByText("team update failed")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: /^Delete$/i })).toBeEnabled(), { timeout: 5000 });
    await userEvent.click(screen.getByRole("button", { name: /^Delete$/i }));
    await userEvent.click(screen.getByRole("button", { name: "Delete team" }));
    expect(await screen.findByText("team delete failed")).toBeInTheDocument();
  }, 15000);
});
