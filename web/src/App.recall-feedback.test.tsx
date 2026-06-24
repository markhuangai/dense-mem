import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { App } from "./App";
import { GeneralConfig, RecallFeedbackConfig, RecallFeedbackEvent, Team, TeamProfile } from "./api";

const team: Team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Default",
  description: "",
  metadata: null,
  config: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

const profile: TeamProfile = {
  id: "22222222-2222-4222-8222-222222222222",
  team_id: team.id,
  name: "default profile",
  key_suffix: "abc123",
  scopes: ["read", "write"],
  role: "manager",
  rate_limit: 120,
  last_used_at: "2026-05-02T13:00:00Z",
  expires_at: null,
  created_at: "2026-04-30T12:00:00Z",
};

const generalConfig: GeneralConfig = {
  update_time: "2026-06-16T09:00:00Z",
  items: [
    { key: "APP_TIMEZONE", value: "Local", effective_value: "Local", updated_at: "2026-06-16T09:00:00Z" },
  ],
  effective: {
    timezone: "Local",
  },
};

const recallFeedbackConfig: RecallFeedbackConfig = {
  update_time: "2026-06-23T12:00:00Z",
  items: [
    { key: "RECALL_FEEDBACK_ENABLED", value: "true", effective_value: "true", updated_at: "2026-06-23T12:00:00Z" },
    { key: "RECALL_FEEDBACK_RETENTION_DAYS", value: "30", effective_value: "30", updated_at: "2026-06-23T12:00:00Z" },
  ],
  effective: {
    enabled: true,
    retention_days: 30,
  },
};

const recallFeedbackEvents: RecallFeedbackEvent[] = [
  {
    recall_id: "rec_1234567890",
    created_at: "2026-06-23T10:00:00Z",
    updated_at: "2026-06-23T10:01:00Z",
    feedback_at: "2026-06-23T10:01:00Z",
    team_id: team.id,
    profile_id: profile.id,
    key_id: profile.id,
    auth_method: "api_key",
    tool_name: "recall_memory",
    query: "Why was recall bad?",
    tool_args: {
      input: { query: "Why was recall bad?", limit: 5 },
      effective: { query: "Why was recall bad?", limit: 5, include_evidence: false, use_communities: false },
    },
    result_refs: [
      {
        type: "fragment",
        id: "fragment-1",
        rank: 1,
        tier: "2",
        final_score: 0.74,
        status_at_recall: "active",
      },
    ],
    result_count: 1,
    snapshot_state: "captured",
    used: true,
    answer_supported: false,
    quality: "low",
    missing_context: true,
    irrelevant: false,
    resolved_results: [
      {
        type: "fragment",
        id: "fragment-1",
        rank: 1,
        resolution_status: "found",
        current_status: "retracted",
        current: { content: "The old fragment has been retracted.", status: "retracted" },
        ref: {
          type: "fragment",
          id: "fragment-1",
          rank: 1,
          tier: "2",
          status_at_recall: "active",
        },
      },
    ],
  },
  {
    recall_id: "rec_pending",
    created_at: "2026-06-23T10:02:00Z",
    updated_at: "2026-06-23T10:02:00Z",
    feedback_at: null,
    team_id: team.id,
    profile_id: profile.id,
    key_id: profile.id,
    auth_method: "api_key",
    tool_name: "recall_memory",
    query: "Pending recall waiting",
    tool_args: {},
    result_refs: [],
    result_count: 0,
    snapshot_state: "captured",
    quality: "",
  },
];

beforeEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
});

it("shows recall feedback query, params, result ids, and resolved state", async () => {
  const fetchMock = mockPortalFetch();
  sessionStorage.setItem("denseMem.controlToken", "secret");

  render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  await userEvent.click(screen.getByRole("button", { name: /^feedback$/i }));

  expect(await screen.findByText("Why was recall bad?")).toBeInTheDocument();
  expect(screen.queryByText("Pending recall waiting")).not.toBeInTheDocument();
  expect(screen.getByText("missing context")).toBeInTheDocument();

  await userEvent.click(screen.getByLabelText("Include pending"));
  expect(await screen.findByText("Pending recall waiting")).toBeInTheDocument();
  await waitFor(() => {
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("include_pending=true"),
      expect.objectContaining({ method: "GET" }),
    );
  });

  await userEvent.click(screen.getByRole("button", { name: /view recall feedback rec_1234567890/i }));
  expect(await screen.findByText("fragment-1")).toBeInTheDocument();
  expect(screen.getByText("The old fragment has been retracted.")).toBeInTheDocument();
  expect(screen.getByLabelText(/Raw recall feedback rec_1234567890/i)).toHaveTextContent('"tool_args"');

  await userEvent.selectOptions(screen.getByLabelText("Quality"), "low");
  await waitFor(() => {
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/recall-feedback-events?limit=100&offset=0&quality=low"),
      expect.objectContaining({ method: "GET" }),
    );
  });
});

it("edits recall feedback retention config", async () => {
  const fetchMock = mockPortalFetch();
  sessionStorage.setItem("denseMem.controlToken", "secret");

  render(<App />);
  await screen.findByRole("button", { name: /Default/ });
  await userEvent.click(screen.getByRole("button", { name: /^Config$/i }));
  await userEvent.click(within(await screen.findByRole("tablist", { name: /config sections/i })).getByRole("tab", { name: /^recall$/i }));

  expect(await screen.findByRole("heading", { name: "Recall Feedback" })).toBeInTheDocument();
  await userEvent.clear(screen.getByLabelText("Investigation retention days"));
  await userEvent.type(screen.getByLabelText("Investigation retention days"), "45");
  await userEvent.click(screen.getByRole("button", { name: /save config/i }));

  await waitFor(() => {
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/config/recall-feedback"),
      expect.objectContaining({
        method: "PATCH",
        body: expect.stringContaining(`"RECALL_FEEDBACK_RETENTION_DAYS"`),
      }),
    );
  });
  expect(await screen.findByText("Saved")).toBeInTheDocument();
});

function mockPortalFetch() {
  let currentRecallConfig = structuredClone(recallFeedbackConfig);
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const parsedUrl = new URL(url, "http://localhost");

    if (url.endsWith("/session")) {
      return jsonResponse({ data: { authenticated: true } });
    }
    if (url.endsWith("/config/general") && method === "GET") {
      return jsonResponse({ data: generalConfig });
    }
    if (url.endsWith("/config/recall-feedback") && method === "GET") {
      return jsonResponse({ data: currentRecallConfig });
    }
    if (url.endsWith("/config/recall-feedback") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentRecallConfig = {
        ...currentRecallConfig,
        update_time: "2026-06-23T12:01:00Z",
        items: currentRecallConfig.items.map((item) => {
          const update = body.items.find((candidate: { key: string }) => candidate.key === item.key);
          return update ? { ...item, value: update.value, updated_at: "2026-06-23T12:01:00Z" } : item;
        }),
        effective: {
          ...currentRecallConfig.effective,
          retention_days: Number(body.items.find((item: { key: string }) => item.key === "RECALL_FEEDBACK_RETENTION_DAYS")?.value ?? "30"),
        },
      };
      return jsonResponse({ data: currentRecallConfig });
    }
    if (parsedUrl.pathname.endsWith("/control/api/recall-feedback-events/rec_1234567890") && method === "GET") {
      return jsonResponse({ data: recallFeedbackEvents[0] });
    }
    if (parsedUrl.pathname.endsWith("/control/api/recall-feedback-events") && method === "GET") {
      const limit = Number(parsedUrl.searchParams.get("limit") ?? "100");
      const offset = Number(parsedUrl.searchParams.get("offset") ?? "0");
      const quality = parsedUrl.searchParams.get("quality") ?? "";
      const includePending = parsedUrl.searchParams.get("include_pending") === "true";
      const filtered = recallFeedbackEvents.filter((event) => {
        if (quality) {
          return event.quality === quality;
        }
        return includePending || event.quality;
      });
      return jsonResponse({
        data: filtered.slice(offset, offset + limit),
        pagination: { limit, offset, total: filtered.length },
      });
    }
    if (url.endsWith("/teams") && method === "GET") {
      return jsonResponse(page([team]));
    }
    if (url.includes("/profiles") && method === "GET") {
      return jsonResponse(page([profile]));
    }
    return jsonResponse({ message: `unhandled ${method} ${url}` }, 500);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function page<T>(data: T[]) {
  return { data, pagination: { limit: 100, offset: 0, total: data.length } };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}
