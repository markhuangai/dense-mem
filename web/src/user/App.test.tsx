import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserPortalApp } from "./App";
import { GraphNode, GraphSnapshot, RecallHit, UserKey, UserSession } from "./api";

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

const recallHits: RecallHit[] = [
  {
    tier: "canonical",
    score: 0.94,
    semantic_rank: 1,
    keyword_rank: 1,
    final_score: 0.94,
    fact: {
      fact_id: "fact-1",
      subject: "Alice",
      predicate: "works_on",
      object: "project-x",
      status: "active",
      truth_score: 0.94,
      recorded_at: "2026-05-02T12:00:00Z",
    },
  },
  {
    tier: "claim",
    score: 0.84,
    semantic_rank: 2,
    keyword_rank: 2,
    final_score: 0.84,
    claim: {
      claim_id: "claim-1",
      subject: "Alice",
      predicate: "uses",
      object: "Dense-Mem",
      modality: "assertion",
      polarity: "+",
      status: "validated",
      entailment_verdict: "entailed",
      extract_conf: 0.91,
      resolution_conf: 0.88,
      recorded_at: "2026-05-02T12:00:00Z",
    },
  },
  {
    tier: "raw",
    score: 0.74,
    semantic_rank: 3,
    keyword_rank: 3,
    final_score: 0.74,
    fragment: {
      id: "frag-1",
      fragment_id: "frag-1",
      content: "Alice is working on project-x with Dense-Mem.",
      source_type: "manual",
      source: "notes",
      labels: ["project"],
      status: "active",
      created_at: "2026-05-02T12:00:00Z",
      updated_at: "2026-05-02T12:00:00Z",
    },
  },
];

const overviewGraph: GraphSnapshot = {
  scope: "overview",
  depth: 1,
  limit: 80,
  truncated: false,
  nodes: [
    {
      key: "fact:fact-1",
      id: "fact-1",
      type: "fact",
      title: "Alice works_on project-x",
    },
    {
      key: "claim:claim-1",
      id: "claim-1",
      type: "claim",
      title: "Alice uses Dense-Mem",
    },
  ],
  edges: [
    { id: "edge-1", source: "claim:claim-1", target: "fact:fact-1", relationship: "PROMOTES_TO", directed: true },
  ],
};

const graphNodeDetails: Record<string, GraphNode> = {
  "fact:fact-1": {
    key: "fact:fact-1",
    id: "fact-1",
    type: "fact",
    title: "Alice works_on project-x",
    body: "project-x",
    status: "active",
    community_id: "community-1",
    score: 0.94,
    recorded_at: "2026-05-02T12:00:00Z",
  },
  "claim:claim-1": {
    key: "claim:claim-1",
    id: "claim-1",
    type: "claim",
    title: "Alice uses Dense-Mem",
    body: "Dense-Mem",
    status: "validated",
    community_id: "community-1",
    score: 0.88,
    recorded_at: "2026-05-02T12:00:00Z",
  },
};

const localGraph: GraphSnapshot = {
  ...overviewGraph,
  scope: "local",
  anchor: { type: "fact", id: "fact-1", key: "fact:fact-1" },
  depth: 1,
  limit: 48,
};

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.mocked(navigator.clipboard.writeText).mockClear();
});

describe("UserPortalApp", () => {
  it("logs in with an API key and does not call team profile list APIs", async () => {
    const fetchMock = mockUserFetch(baseSession);
    render(<UserPortalApp />);

    await userEvent.type(screen.getByLabelText(/api key/i), "dm_key");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await expectCurrentWorkspace("Research Team");
    expect(screen.getByLabelText("Knowledge navigation")).toHaveClass("top-nav-bar");
    expect(screen.getByLabelText("Knowledge sections")).toHaveClass("top-nav-tabs");
    expect(screen.getByLabelText("Current workspace")).not.toHaveTextContent("Mine");
    expect(screen.queryByRole("button", { name: "Facts" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Claims" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Fragments" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Communities" })).not.toBeInTheDocument();
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

  it("hides usage telemetry for read-only keys", async () => {
    const fetchMock = mockUserFetch(baseSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");

    expect(screen.queryByRole("button", { name: /usage/i })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith("/ui/api/telemetry"))).toBe(false);
  });

  it("filters recall results and updates the inspector selection", async () => {
    mockUserFetch(baseSession, [], { recallHits });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.type(screen.getByLabelText("Keyword"), "project");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    const resultList = await screen.findByRole("listbox", { name: "Recall result list" });
    expect(within(resultList).getAllByRole("option")).toHaveLength(3);
    expect(screen.queryByRole("button", { name: /star/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /more actions/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add to collection" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create claim" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Fact");

    const claimResult = within(resultList).getByText("uses: Dense-Mem").closest("[role='option']");
    expect(claimResult).not.toBeNull();
    await userEvent.click(claimResult as HTMLElement);
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Claim");
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Tier claim");

    await userEvent.click(screen.getByRole("checkbox", { name: /claim/i }));
    expect(screen.getByRole("listbox", { name: "Recall result list" })).not.toHaveTextContent("uses: Dense-Mem");
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Fact");

    await userEvent.click(screen.getByRole("checkbox", { name: /fact/i }));
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Fragment");
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Alice is working on project-x with Dense-Mem.");

    await userEvent.click(screen.getByRole("tab", { name: "Recall" }));
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Final score");
    await userEvent.click(screen.getByRole("button", { name: "Sort by date" }));
    expect(screen.getByRole("button", { name: "Sort by relevance" })).toHaveTextContent("Sort: Date");
    await userEvent.click(screen.getByRole("button", { name: "Use compact density" }));
    expect(screen.getByRole("button", { name: "Use comfortable density" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: "Close details panel" }));
    expect(screen.queryByLabelText("Inspector")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Open details" }));
    expect(screen.getByLabelText("Inspector")).toBeInTheDocument();
  });

  it("opens the graph tab and loads the overview graph", async () => {
    const fetchMock = mockUserFetch(baseSession, [], { graphSnapshot: overviewGraph });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /graph/i }));

    expect(await screen.findByLabelText("Knowledge graph")).toBeInTheDocument();
    const controls = screen.getByLabelText("Graph controls");
    expect(within(controls).queryByLabelText("Limit")).not.toBeInTheDocument();
    expect((await screen.findAllByText("Alice works_on project-x")).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByLabelText("Graph totals")).toHaveTextContent("2");
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("Select a node");
	await userEvent.click(within(screen.getByTestId("sigma-graph")).getByRole("button", { name: "Alice works_on project-x" }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/ui/api/node-detail?type=fact&id=fact-1", expect.any(Object));
    });
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("community-1");
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("0.940");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
		"/ui/api/graph?scope=overview&types=entity%2Cvalue%2Cfact%2Cclaim%2Cfragment%2Cdream%2Ccommunity&depth=2&include_superseded=true",
        expect.any(Object),
      );
    });
	expect(within(controls).getByText(/all lifecycle states are included/i)).toBeInTheDocument();
	await userEvent.click(within(controls).getByRole("button", { name: "Refresh" }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
		"/ui/api/graph?scope=overview&types=entity%2Cvalue%2Cfact%2Cclaim%2Cfragment%2Cdream%2Ccommunity&depth=2&include_superseded=true",
        expect.any(Object),
      );
    });
  });

  it("keeps graph controls available without initializing a renderer for an empty team graph", async () => {
    mockUserFetch(baseSession, [], {
      graphSnapshot: { ...overviewGraph, nodes: [], edges: [] },
    });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /graph/i }));

    const controls = await screen.findByLabelText("Graph controls");
    expect(screen.getByText("No graph nodes")).toBeInTheDocument();
    expect(screen.queryByTestId("sigma-graph")).not.toBeInTheDocument();
    for (const name of ["Entity", "Value", "Fact", "Claim", "Fragment", "Dream", "Community"]) {
      expect(within(controls).getByRole("checkbox", { name })).toBeChecked();
    }
  });

  it("refreshes selected graph node details when the graph reloads", async () => {
    const refreshedDetails = {
      ...graphNodeDetails,
      "fact:fact-1": {
        ...graphNodeDetails["fact:fact-1"],
        body: "project-y",
        community_id: "community-2",
        score: 0.67,
      },
    };
    const fetchMock = mockUserFetch(baseSession, [], { graphSnapshot: [overviewGraph, overviewGraph], graphNodeDetails: [graphNodeDetails, refreshedDetails] });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /graph/i }));
    const controls = await screen.findByLabelText("Graph controls");

	await userEvent.click(within(screen.getByTestId("sigma-graph")).getByRole("button", { name: "Alice works_on project-x" }));
    await waitFor(() => {
      expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("0.940");
    });

    await userEvent.click(within(controls).getByRole("button", { name: "Refresh" }));

    await waitFor(() => {
      expect(fetchMock.mock.calls.filter(([url]) => String(url).startsWith("/ui/api/node-detail?type=fact&id=fact-1"))).toHaveLength(2);
    });
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("community-2");
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("0.670");
  });

  it("blocks graph refresh when every type filter is disabled", async () => {
    const fetchMock = mockUserFetch(baseSession, [], { graphSnapshot: overviewGraph });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /graph/i }));
    const controls = await screen.findByLabelText("Graph controls");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
		"/ui/api/graph?scope=overview&types=entity%2Cvalue%2Cfact%2Cclaim%2Cfragment%2Cdream%2Ccommunity&depth=2&include_superseded=true",
        expect.any(Object),
      );
    });
    const graphCallCount = () => fetchMock.mock.calls.filter(([url]) => String(url).startsWith("/ui/api/graph")).length;
    const beforeDisabledRefresh = graphCallCount();

	for (const name of ["Entity", "Value", "Fact", "Claim", "Fragment", "Dream", "Community"]) {
	  await userEvent.click(within(controls).getByRole("checkbox", { name }));
	}

    const refresh = within(controls).getByRole("button", { name: "Refresh" });
    expect(refresh).toBeDisabled();
    await userEvent.click(refresh);
    expect(graphCallCount()).toBe(beforeDisabledRefresh);
  });

  it("loads a local graph from the recall inspector", async () => {
    const fetchMock = mockUserFetch(baseSession, [], { recallHits, graphSnapshot: localGraph });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.type(screen.getByLabelText("Keyword"), "project");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await screen.findByRole("listbox", { name: "Recall result list" });
    await userEvent.click(screen.getByRole("tab", { name: "Graph" }));

	expect(await screen.findByTestId("sigma-graph")).toHaveTextContent("Alice works_on project-x");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
		"/ui/api/graph?scope=local&types=fact%2Cclaim%2Cfragment%2Cdream&anchor_type=fact&anchor_id=fact-1&depth=2&limit=48&include_superseded=true",
        expect.any(Object),
      );
    });
  });

  it("resets stale source filters after a new recall search", async () => {
    mockUserFetch(baseSession, [], { recallHits: [recallHits, [recallHits[0]]] });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.type(screen.getByLabelText("Keyword"), "project");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    await userEvent.selectOptions(await screen.findByLabelText("Source"), "notes");
    expect(screen.getByRole("listbox", { name: "Recall result list" })).toHaveTextContent("Alice is working on project-x with Dense-Mem.");
    expect(screen.getByRole("listbox", { name: "Recall result list" })).not.toHaveTextContent("works_on: project-x");

    await userEvent.clear(screen.getByLabelText("Keyword"));
    await userEvent.type(screen.getByLabelText("Keyword"), "alice");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    expect(await screen.findByLabelText("Source")).toHaveValue("all");
    expect(screen.getByRole("listbox", { name: "Recall result list" })).toHaveTextContent("works_on: project-x");
  });

  it("labels write-member telemetry as key usage", async () => {
    const writeSession = {
      ...baseSession,
      key: { ...baseSession.key, scopes: ["read", "write"] },
      can_rotate: true,
    };
    const fetchMock = mockUserFetch(writeSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_write");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /usage/i }));

    expect(await screen.findByLabelText("My key usage totals")).toHaveTextContent("HTTP requests");
    expect(screen.queryByLabelText("Team usage totals")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/ui/api/telemetry?window=1h", expect.any(Object));
    });
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

    expect(await screen.findByDisplayValue("dm_new_plaintext")).toHaveAccessibleName("Generated API key");
    await userEvent.click(screen.getByRole("button", { name: /copy api key/i }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("dm_new_plaintext");
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
    const createForm = screen.getByRole("button", { name: /create member profile/i }).closest("form");
    expect(createForm).not.toBeNull();
    await userEvent.click(within(createForm as HTMLElement).getByLabelText("Recall feedback"));
    await userEvent.click(screen.getByRole("button", { name: /create member profile/i }));
    expect(await screen.findByDisplayValue("dm_member_plaintext")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(`/api/v1/teams/${baseSession.team.id}/profiles`),
      expect.objectContaining({ method: "POST", body: expect.not.stringContaining("role") }),
    ));
    expect(fetchMock.mock.calls.map(([, init]) => String(init?.body ?? ""))).toContainEqual(expect.stringContaining(`"scopes":["read","write","feedback:read"]`));

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

    const memberRow = (await screen.findByDisplayValue("Reader Updated")).closest("tr");
    expect(memberRow).not.toBeNull();
    await userEvent.click(within(memberRow as HTMLElement).getByLabelText("Recall feedback"));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(`/api/v1/teams/${baseSession.team.id}/profiles/${memberProfile.id}`),
      expect.objectContaining({ method: "PATCH", body: expect.stringContaining(`"scopes":["read","feedback:read"]`) }),
    ));

    await userEvent.click(screen.getByRole("button", { name: /regenerate key for profile Reader Updated/i }));
    expect(await screen.findByDisplayValue("dm_member_rotated")).toBeInTheDocument();
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

  it("lets a manager edit team dreaming config", async () => {
    const managerSession: UserSession = {
      ...baseSession,
      team: {
        ...baseSession.team,
        config: {
          retention: "long",
          dreaming: {
            enabled: false,
            timezone: "UTC",
            max_outputs: 4,
          },
        },
      },
      key: {
        ...baseSession.key,
        name: "Manager",
        scopes: ["read", "write"],
        role: "manager",
      },
      can_rotate: true,
      can_manage_team: true,
    };
    const fetchMock = mockUserFetch(managerSession, [{ ...managerSession.key }]);
    sessionStorage.setItem("denseMem.userApiKey", "dm_manager");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /^team$/i }));

    const scheduledToggle = await screen.findByLabelText("Scheduled cycle", { selector: "input" });
    expect(scheduledToggle).not.toBeChecked();
    await userEvent.click(scheduledToggle);
    await userEvent.click(screen.getByRole("button", { name: /save dreaming/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/api/v1/teams/${baseSession.team.id}`),
        expect.objectContaining({ method: "PATCH" }),
      );
    });
    const patchCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith(`/api/v1/teams/${baseSession.team.id}`) && init?.method === "PATCH");
    const body = JSON.parse(String(patchCall?.[1]?.body));
    expect(body.config.retention).toBe("long");
    expect(body.config.dreaming).toMatchObject({
      enabled: true,
    });
    expect(body.config.dreaming.timezone).toBeUndefined();
    expect(body.config.dreaming.max_outputs).toBeUndefined();
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("labels manager telemetry as team usage", async () => {
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
    const fetchMock = mockUserFetch(managerSession, [{ ...managerSession.key }]);
    sessionStorage.setItem("denseMem.userApiKey", "dm_manager");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /usage/i }));

    expect(await screen.findByLabelText("Team usage totals")).toHaveTextContent("HTTP requests");
    expect(screen.queryByLabelText("My key usage totals")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/ui/api/telemetry?window=1h", expect.any(Object));
    });
  });

  it("uses server auth method for SSO cookie sessions and switches teams", async () => {
    const { initial, switched, secondKey } = ssoSessions();
    const fetchMock = mockSSOUserFetch(initial, switched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    expect(screen.getByRole("button", { name: /my key/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^team$/i })).not.toBeInTheDocument();
    const teamSelect = await screen.findByLabelText("Active team");
    expect(teamSelect).toHaveValue(initial.key.id);

    await userEvent.selectOptions(teamSelect, secondKey.id);

    await expectCurrentWorkspace("Analytics Team");
    expect(screen.queryByRole("button", { name: /my key/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^team$/i })).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/sso/team",
        expect.objectContaining({
          method: "POST",
          credentials: "include",
          body: JSON.stringify({ profile_id: secondKey.id }),
        }),
      );
    });
    expect(sessionStorage.getItem("denseMem.userApiKey")).toBeNull();
  });

  it("reloads usage telemetry after switching SSO teams", async () => {
    const { initial, switched, secondKey } = ssoSessions();
    const firstKey = { ...initial.key, scopes: ["read", "write"] };
    const nextKey = { ...secondKey, scopes: ["read", "write"] };
    const writeInitial: UserSession = {
      ...initial,
      key: firstKey,
      can_rotate: true,
      teams: [
        { team: initial.team, key: firstKey, can_rotate: true, can_manage_team: false },
        { team: switched.team, key: nextKey, can_rotate: true, can_manage_team: true },
      ],
    };
    const writeSwitched: UserSession = {
      ...switched,
      key: nextKey,
      can_rotate: true,
      teams: writeInitial.teams,
    };
    const fetchMock = mockSSOUserFetch(writeInitial, writeSwitched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /usage/i }));
    expect(await screen.findByLabelText("My key usage totals")).toHaveTextContent("4");

    await userEvent.selectOptions(screen.getByLabelText("Active team"), nextKey.id);

    await expectCurrentWorkspace("Analytics Team");
    await waitFor(() => {
      expect(screen.getByLabelText("Team usage totals")).toHaveTextContent("9");
    });
    expect(screen.queryByLabelText("My key usage totals")).not.toBeInTheDocument();
    await waitFor(() => {
      const telemetryCalls = fetchMock.mock.calls.filter(([url]) => String(url).startsWith("/ui/api/telemetry?window=1h"));
      expect(telemetryCalls.length).toBeGreaterThanOrEqual(2);
    });
  });

  it("keeps the SSO portal open when logout fails", async () => {
    const { initial, switched } = ssoSessions();
    mockSSOUserFetch(initial, switched, { logoutStatus: 500 });

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /sign out/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("logout failed");
    expect(screen.getByLabelText("Current workspace")).toHaveTextContent("Research Team");
    expect(screen.queryByLabelText(/api key/i)).not.toBeInTheDocument();
  });

  it("lets an SSO read/write member create and rotate their owned API key", async () => {
    const { initial, switched } = ssoSessions();
    const ssoKey = { ...initial.key, scopes: ["read", "write"] };
    const readWriteSession: UserSession = {
      ...initial,
      key: ssoKey,
      teams: initial.teams?.map((item, index) => index === 0 ? { ...item, key: ssoKey } : item),
      personal_key_max_scopes: ["read", "write"],
    };
    const fetchMock = mockSSOUserFetch(readWriteSession, switched);
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /my key/i }));
    await userEvent.click(screen.getByRole("button", { name: /create api key/i }));

    expect(await screen.findByDisplayValue("dm_sso_personal_plaintext")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/sso/key",
        expect.objectContaining({
          method: "POST",
          credentials: "include",
          body: expect.stringContaining(`"scopes":["read","write"]`),
        }),
      );
    });

    await userEvent.click(screen.getByRole("button", { name: /regenerate key/i }));
    expect(await screen.findByDisplayValue("dm_sso_personal_rotated")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/sso/key/rotate",
        expect.objectContaining({ method: "POST", credentials: "include" }),
      );
    });
  });

  it("keeps SSO read-only owned API key rotation disabled", async () => {
    const { initial, switched } = ssoSessions();
    mockSSOUserFetch(initial, switched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /my key/i }));
    await userEvent.click(screen.getByRole("button", { name: /create api key/i }));

    expect(await screen.findByDisplayValue("dm_sso_personal_plaintext")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /regenerate key/i })).toBeDisabled();
  });
});

async function expectCurrentWorkspace(teamName: string) {
  const workspace = await screen.findByLabelText("Current workspace");
  expect(workspace).toHaveTextContent(teamName);
}

function mockUserFetch(session: UserSession, profiles: UserKey[] = [], options: { recallHits?: RecallHit[] | RecallHit[][]; graphSnapshot?: GraphSnapshot | GraphSnapshot[]; graphNodeDetails?: Record<string, GraphNode> | Record<string, GraphNode>[] } = {}) {
  let currentTeam = session.team;
  let currentProfiles = profiles;
  let recallCallCount = 0;
  let graphCallCount = 0;
  let graphNodeDetailCallCount = 0;
  const rotatedSession = {
    ...session,
    key: { ...session.key, key_suffix: "new123", last_used_at: null },
  };
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url === "/ui/api/sso/providers" && method === "GET") {
      return jsonResponse({ data: [] });
    }
    if (url === "/ui/api/session" && method === "GET") {
      const auth = authorizationHeader(init);
      if (!auth) {
        return jsonResponse({ code: "AUTH_MISSING", message: "missing authorization header", details: null }, 401);
      }
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
    if (url.startsWith("/ui/api/telemetry") && method === "GET") {
      return jsonResponse({ data: telemetryForSession(session) });
    }
    if (url.startsWith("/ui/api/node-detail") && method === "GET") {
      const params = new URLSearchParams(url.split("?")[1] ?? "");
      const key = `${params.get("type")}:${params.get("id")}`;
      const details = optionValue(options.graphNodeDetails ?? graphNodeDetails, graphNodeDetailCallCount++);
      return jsonResponse({ data: details[key] ?? graphNodeDetails["fact:fact-1"] });
    }
    if (url.startsWith("/ui/api/graph") && method === "GET") {
      return jsonResponse({ data: optionValue(options.graphSnapshot ?? overviewGraph, graphCallCount++) });
    }
    if (url === `/api/v1/teams/${currentTeam.id}` && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentTeam = { ...currentTeam, name: body.name ?? currentTeam.name, description: body.description ?? currentTeam.description, config: body.config ?? currentTeam.config };
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
      const updated = { ...current, name: body.name ?? current.name, scopes: body.scopes ?? current.scopes };
      currentProfiles = currentProfiles.map((profile) => (profile.id === updated.id ? updated : profile));
      return jsonResponse({ data: updated });
    }
    if (url.includes(`/api/v1/teams/${currentTeam.id}/profiles/`) && method === "DELETE") {
      currentProfiles = currentProfiles.filter((profile) => !url.endsWith(`/profiles/${profile.id}`));
      return jsonResponse({ data: { status: "deleted" } });
    }
    if (url.startsWith("/api/v1/recall")) {
      const configuredHits = options.recallHits ?? [];
      const hits = isRecallSequence(configuredHits)
        ? configuredHits[Math.min(recallCallCount, configuredHits.length - 1)] ?? []
        : configuredHits;
      recallCallCount += 1;
      return jsonResponse({ data: hits });
    }
    return jsonResponse({ message: "not found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function optionValue<T>(value: T | T[], index: number): T {
  return Array.isArray(value) ? value[Math.min(index, value.length - 1)] : value;
}

function isRecallSequence(value: RecallHit[] | RecallHit[][]): value is RecallHit[][] {
  return Array.isArray(value[0]);
}

function telemetryForSession(session: UserSession) {
  const teamScope = session.can_manage_team;
  return {
    available: true,
    window: {
      key: "1h",
      from: "2026-05-02T12:00:00Z",
      to: "2026-05-02T13:00:00Z",
      step_seconds: 60,
      retention_days: 30,
    },
    scope: teamScope
      ? { type: "team", team_id: session.team.id }
      : { type: "self", team_id: session.team.id, profile_id: session.key.id },
    cards: [{ id: "http_requests", label: "HTTP requests", unit: "requests", value: teamScope ? 9 : 4 }],
    series: [],
  };
}

function ssoSessions() {
  const secondTeam = {
    ...baseSession.team,
    id: "55555555-5555-4555-8555-555555555555",
    name: "Analytics Team",
  };
  const secondKey: UserKey = {
    ...baseSession.key,
    id: "66666666-6666-4666-8666-666666666666",
    team_id: secondTeam.id,
    name: "Analytics SSO",
  };
  const initial: UserSession = {
    ...baseSession,
    auth_method: "sso",
    can_create_personal_key: true,
    personal_key_max_scopes: ["read"],
    teams: [
      { team: baseSession.team, key: baseSession.key, can_rotate: false, can_manage_team: false },
      { team: secondTeam, key: secondKey, can_rotate: false, can_manage_team: true },
    ],
  };
  const switched: UserSession = {
    ...initial,
    team: secondTeam,
    key: secondKey,
    can_rotate: false,
    can_manage_team: true,
    can_create_personal_key: false,
    personal_key_max_scopes: [],
  };
  return { initial, switched, secondKey };
}

function mockSSOUserFetch(initial: UserSession, switched: UserSession, options: { logoutStatus?: number } = {}) {
  let current = initial;
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url === "/ui/api/sso/providers" && method === "GET") {
      return jsonResponse({ data: [] });
    }
    if (url === "/ui/api/session" && method === "GET") {
      return jsonResponse({ data: current });
    }
    if (url === "/ui/api/sso/team" && method === "POST") {
      current = switched;
      return jsonResponse({ data: current });
    }
    if (url.startsWith("/ui/api/telemetry") && method === "GET") {
      return jsonResponse({ data: telemetryForSession(current) });
    }
    if (url === "/ui/api/sso/key" && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const created: UserKey = {
        ...current.key,
        id: "77777777-7777-4777-8777-777777777777",
        name: body.name,
        key_suffix: "own123",
        scopes: body.scopes,
        role: "member",
      };
      current = {
        ...current,
        personal_key: created,
        can_create_personal_key: false,
        can_rotate_personal_key: created.scopes.includes("write") && (current.personal_key_max_scopes ?? []).includes("write"),
      };
      return jsonResponse({ data: { api_key: "dm_sso_personal_plaintext", key: created } }, 201);
    }
    if (url === "/ui/api/sso/key/rotate" && method === "POST") {
      const rotated = { ...(current.personal_key ?? current.key), key_suffix: "rot321" };
      current = { ...current, personal_key: rotated };
      return jsonResponse({ data: { api_key: "dm_sso_personal_rotated", key: rotated } });
    }
    if (url === "/ui/api/sso/logout" && method === "POST") {
      if (options.logoutStatus) {
        return jsonResponse({ message: "logout failed" }, options.logoutStatus);
      }
      return jsonResponse({ data: { status: "signed_out" } });
    }
    if (url.startsWith("/api/v1/recall")) {
      return jsonResponse({ data: [] });
    }
    return jsonResponse({ message: "not found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function authorizationHeader(init?: RequestInit): string {
  const headers = init?.headers;
  if (!headers) {
    return "";
  }
  if (headers instanceof Headers) {
    return headers.get("Authorization") ?? "";
  }
  if (Array.isArray(headers)) {
    return headers.find(([name]) => name.toLowerCase() === "authorization")?.[1] ?? "";
  }
  return (headers as Record<string, string>).Authorization ?? (headers as Record<string, string>).authorization ?? "";
}

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
