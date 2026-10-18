import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { App } from "./App";
import { mockPortalFetch, profileA } from "./App.test-helpers";

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  sessionStorage.setItem("denseMem.controlToken", "secret");
  vi.restoreAllMocks();
});

it("keeps the Remember outcome and page when inspecting and clearing a selected attempt", async () => {
  const attempts = Array.from({ length: 52 }, (_, index) => {
    const summary = {
      team_id: profileA.id, team_name: profileA.name, owner_profile_id: "owner-1", attempt_id: `attempt-${index}`,
      contract_version: "dense-mem.v2.6", submission_kind: "remember", outcome: index === 51 ? "failed" : "completed",
      evidence_count: 0, relationship_count: 0, document_count: 0, assessor_turns: 0,
      duration_ms: 1, created_at: "2026-08-18T01:00:00Z",
    };
    return { summary, detail: {
      ...summary,
      public_result: {
        contract_version: "dense-mem.v2.6", submission_id: summary.attempt_id, submission_kind: "remember",
        processing_state: "completed", search_state: "current", correlation_id: "corr",
        evidence: [], relationship_results: [], errors: [],
      },
      events: [], diagnostics: { original_request: null, provider_exchanges: [], caller_response: null },
    } };
  });
  const fetchMock = mockPortalFetch({ teams: [profileA], keys: [], rememberAttempts: attempts });
  render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  await userEvent.click(screen.getByRole("link", { name: "Remember history" }));
  await userEvent.selectOptions(await screen.findByLabelText("Remember attempt outcome"), "completed");
  await waitFor(() => expect(screen.getByRole("button", { name: "Next" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Next" }));
  await userEvent.click(await screen.findByRole("button", { name: "Inspect Remember attempt attempt-50" }));
  expect(await screen.findByRole("region", { name: "Remember attempt details" })).toHaveTextContent("attempt-50");
  expect(screen.getByLabelText("Remember attempt outcome")).toHaveValue("completed");
  expect(screen.getByText("51-51 of 51")).toBeInTheDocument();
  expect(new URLSearchParams(window.location.search).get("attempt_id")).toBe("attempt-50");
  const listQueries = () => fetchMock.mock.calls.map(([url]) => new URL(String(url), location.origin)).filter((url) => url.pathname.endsWith("/remember-attempts"));
  expect(listQueries().at(-1)?.searchParams.get("offset")).toBe("50");

  await userEvent.selectOptions(screen.getByLabelText("Remember attempt outcome"), "failed");
  await screen.findByRole("button", { name: "Inspect Remember attempt attempt-51" });
  expect(screen.getByLabelText("Remember attempt outcome")).toHaveValue("failed");
  expect(screen.getByText("1-1 of 1")).toBeInTheDocument();
  expect(new URLSearchParams(window.location.search).get("attempt_id")).toBeNull();
  expect(listQueries().at(-1)?.searchParams.get("outcome")).toBe("failed");
});

it("preserves global Logs scope across team navigation, refresh and history", async () => {
  const fetchMock = mockPortalFetch({ teams: [profileA], keys: [] });
  const logQueries = () => fetchMock.mock.calls.map(([url]) => new URL(String(url), location.origin)).filter((url) => url.pathname === "/control/api/logs");
  let view = render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  await userEvent.click(screen.getByRole("link", { name: "Credentials" }));
  await userEvent.click(screen.getByRole("link", { name: "Logs" }));
  await waitFor(() => expect(logQueries().length).toBeGreaterThan(0));
  expect(logQueries().at(-1)?.searchParams.has("team_id")).toBe(false);
  expect(new URLSearchParams(location.search).get("logs_scope")).toBe("all");

  view.unmount();
  view = render(<App />);
  await waitFor(() => expect(logQueries().length).toBeGreaterThan(1));
  expect(logQueries().at(-1)?.searchParams.has("team_id")).toBe(false);
  await userEvent.click(screen.getByRole("link", { name: "Credentials" }));
  act(() => window.history.back());
  await screen.findByRole("heading", { name: "Operation Logs" });
  expect(logQueries().at(-1)?.searchParams.has("team_id")).toBe(false);
  act(() => window.history.forward());
  await screen.findByRole("heading", { name: "Credentials" });

  view.unmount();
  window.history.replaceState(null, "", `/?section=logs&team_id=${profileA.id}`);
  render(<App />);
  await waitFor(() => expect(logQueries().at(-1)?.searchParams.get("team_id")).toBe(profileA.id));
});

it("keeps unsaved runtime settings while changing theme and cancelling navigation", async () => {
  const fetchMock = mockPortalFetch({ teams: [profileA], keys: [] });
  render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  await userEvent.click(screen.getByRole("link", { name: "Settings" }));
  await userEvent.click(await screen.findByRole("tab", { name: "SSO runtime" }));
  await userEvent.type(await screen.findByLabelText("Public base URL"), "https://unsaved.example.com");
  const loads = () => fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/config/sso")).length;
  const count = loads();
  await userEvent.click(screen.getByRole("button", { name: "Choose theme" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Dark" }));
  expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  expect(screen.getByLabelText("Public base URL")).toHaveValue("https://unsaved.example.com");
  expect(loads()).toBe(count);
  await userEvent.click(screen.getByRole("link", { name: "Logs" }));
  await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
  expect(screen.getByLabelText("Public base URL")).toHaveValue("https://unsaved.example.com");
});

it("restores collapsed navigation and closes the mobile sheet after a destination is selected", async () => {
  mockPortalFetch({ teams: [profileA], keys: [] });
  const user = userEvent.setup();
  const view = render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  await user.click(screen.getByRole("button", { name: "Collapse navigation" }));
  expect(localStorage.getItem("denseMem.controlSidebar")).toBe("collapsed");
  expect(screen.getByRole("button", { name: "Expand navigation" })).toHaveAttribute("aria-expanded", "false");
  view.unmount();

  render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  expect(screen.getByRole("button", { name: "Expand navigation" })).toHaveAttribute("aria-expanded", "false");
  await user.click(screen.getByRole("button", { name: "Expand navigation" }));
  expect(localStorage.getItem("denseMem.controlSidebar")).toBe("expanded");
  await user.click(screen.getByRole("button", { name: "Open navigation" }));
  const navigation = await screen.findByRole("dialog", { name: "Control navigation" });
  await user.click(within(navigation).getByRole("link", { name: "Logs" }));
  await screen.findByRole("heading", { name: "Operation Logs" });
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(new URLSearchParams(location.search).get("section")).toBe("logs");
});

it("keeps the current team's edits when a filtered workspace switch is cancelled", async () => {
  const secondTeam = { ...profileA, id: "22222222-2222-4222-8222-222222222222", name: "Research" };
  mockPortalFetch({ teams: [profileA, secondTeam], keys: [] });
  const user = userEvent.setup();
  render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  await user.click(screen.getByRole("link", { name: "Team settings" }));
  await user.type(await screen.findByLabelText("Name", { exact: true }), " edited");
  await user.click(screen.getByRole("button", { name: "Choose team" }));
  await user.type(screen.getByRole("textbox", { name: "Find a team" }), "unmatched");
  expect(screen.getByText("No matching loaded teams.")).toBeInTheDocument();
  await user.clear(screen.getByRole("textbox", { name: "Find a team" }));
  await user.type(screen.getByRole("textbox", { name: "Find a team" }), "research");
  await user.click(screen.getByRole("button", { name: "Research" }));
  await user.click(await screen.findByRole("button", { name: "Cancel" }));
  expect(screen.getByLabelText("Name", { exact: true })).toHaveValue("Default edited");
  expect(screen.getByLabelText("Current workspace")).toHaveTextContent("Default");
  await user.click(screen.getByRole("button", { name: "Choose team" }));
  await user.click(screen.getByRole("button", { name: "Research" }));
  await user.click(await screen.findByRole("button", { name: "Discard changes" }));
  await waitFor(() => expect(screen.getByLabelText("Current workspace")).toHaveTextContent("Research"));
  expect(screen.getByLabelText("Name", { exact: true })).toHaveValue("Research");
  expect(new URLSearchParams(location.search).get("team_id")).toBe(secondTeam.id);
  expect(screen.queryByRole("textbox", { name: "Find a team" })).not.toBeInTheDocument();
});

it("loads the next team page from the workspace picker before selecting an unloaded team", async () => {
  const research = { ...profileA, id: "22222222-2222-4222-8222-222222222222", name: "Research" };
  const teams = [profileA, ...Array.from({ length: 19 }, (_, index) => ({
    ...profileA, id: `00000000-0000-4000-8000-${String(index + 1).padStart(12, "0")}`, name: `Team ${index + 1}`,
  })), research];
  const fetchMock = mockPortalFetch({ teams, keys: [] });
  const user = userEvent.setup();
  render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  await user.click(screen.getByRole("button", { name: "Choose team" }));
  await user.type(screen.getByRole("textbox", { name: "Find a team" }), "Research");
  expect(screen.getByText("No matching loaded teams.")).toBeInTheDocument();
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Load more teams" }));
  await user.click(await screen.findByRole("button", { name: "Research" }));
  await waitFor(() => expect(screen.getByLabelText("Current workspace")).toHaveTextContent("Research"));
  expect(new URLSearchParams(location.search).get("team_id")).toBe(research.id);
  expect(fetchMock.mock.calls.some(([url]) => {
    const parsed = new URL(String(url), location.origin);
    return parsed.pathname === "/control/api/teams" && parsed.searchParams.get("offset") === "20";
  })).toBe(true);
});
