import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserPortalApp } from "./App";
import { authorizationHeader, expectCurrentWorkspace, isRecallSequence, jsonResponse, optionValue, recallPayloadForHits } from "./App.test-helpers";
import { GraphNode, GraphSnapshot, RecallHit, UserCredential, UserSession } from "./api";

vi.mock("react-force-graph-2d", async () => {
  const React = await import("react");
  return {
    default: React.forwardRef(function MockForceGraph(props: {
      graphData?: { nodes?: Array<{ key: string; title: string }> };
      onNodeClick?: (node: { key: string; title: string }) => void;
    }, ref) {
      React.useImperativeHandle(ref, () => ({
        d3Force: () => ({ distance: () => undefined }),
        d3ReheatSimulation: () => undefined,
        zoomToFit: () => undefined,
      }));
      const nodes = props.graphData?.nodes ?? [];
      return (
        <div data-testid="force-graph">
          {nodes.map((node) => (
            <button key={node.key} type="button" onClick={() => props.onNodeClick?.(node)}>
              {node.title}
            </button>
          ))}
        </div>
      );
    }),
  };
});

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

const memberCredential: UserCredential = {
  id: "33333333-3333-4333-8333-333333333333",
  team_id: baseSession.team.id,
  name: "Reader credential",
  key_suffix: "def456",
  scopes: ["read"],
  role: "member",
  rate_limit: 120,
  last_used_at: null,
  expires_at: null,
  created_at: "2026-05-01T12:00:00Z",
  memory_binding: "shared_only",
  memory_space_kind: "team_shared",
};

const recallHits: RecallHit[] = [
  {
    score: 0.94,
    semantic_rank: 1,
    keyword_rank: 1,
    final_score: 0.94,
    evidence: {
      evidence_id: "evidence-1",
      context: "Alice is working on project-x with Dense-Mem.",
      source: "notes",
      source_type: "manual",
      created_at: "2026-05-02T12:00:00Z",
    },
    relationships: [{
      relationship_id: "relationship-1",
      subject: { entity_id: "entity-alice", name: "Alice" },
      predicate: "works_on",
      object: { value_id: "value-project-x", value: "project-x" },
      search_state: "current",
    }],
  },
  {
    score: 0.84,
    semantic_rank: 2,
    keyword_rank: 2,
    final_score: 0.84,
    evidence: {
      evidence_id: "evidence-2",
      context: "Alice uses: Dense-Mem",
      source: "knowledge graph",
      created_at: "2026-05-02T12:00:00Z",
    },
  },
  {
    score: 0.74,
    semantic_rank: 3,
    keyword_rank: 3,
    final_score: 0.74,
    evidence: {
      evidence_id: "evidence-3",
      context: "Project-x uses Dense-Mem.",
      source: "notes",
      source_type: "manual",
      created_at: "2026-05-02T12:00:00Z",
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
      key: "entity:entity-alice",
      id: "entity-alice",
      type: "entity",
      title: "Alice",
    },
    {
      key: "value:value-project-x",
      id: "value-project-x",
      type: "value",
      title: "project-x",
    },
  ],
  edges: [
    { id: "edge-1", source: "entity:entity-alice", target: "value:value-project-x", relationship: "works_on", directed: true },
  ],
};

const graphNodeDetails: Record<string, GraphNode> = {
  "entity:entity-alice": {
    key: "entity:entity-alice",
    id: "entity-alice",
    type: "entity",
    title: "Alice",
    body: "Person",
    status: "active",
    community_id: "community-1",
    score: 0.94,
    recorded_at: "2026-05-02T12:00:00Z",
  },
  "value:value-project-x": {
    key: "value:value-project-x",
    id: "value-project-x",
    type: "value",
    title: "project-x",
    body: "project-x",
    status: "active",
    community_id: "community-1",
    score: 0.88,
    recorded_at: "2026-05-02T12:00:00Z",
  },
};

const localGraph: GraphSnapshot = {
  ...overviewGraph,
  scope: "local",
  anchor: { type: "entity", id: "entity-alice", key: "entity:entity-alice" },
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
  it("logs in with an API key and does not call team credential list APIs", async () => {
    const fetchMock = mockUserFetch(baseSession);
    render(<UserPortalApp />);

    await userEvent.type(screen.getByLabelText(/api key/i), "dm_key");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await expectCurrentWorkspace("Research Team");
    expect(screen.getByLabelText("Knowledge navigation")).toHaveClass("top-nav-bar");
    expect(screen.getByLabelText("Knowledge sections")).toHaveClass("top-nav-tabs");
    expect(screen.getByLabelText("Current workspace")).not.toHaveTextContent("Mine");
	 expect(screen.queryByRole("button", { name: "Communities" })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/credentials"))).toBe(false);
  });

  it("disables self rotation for read-only keys", async () => {
    mockUserFetch(baseSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    expect(sessionStorage.getItem("denseMem.userApiKey")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /my credential/i }));

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
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Evidence");

    const claimResult = within(resultList).getAllByText("Alice uses: Dense-Mem")[0]?.closest("[role='option']");
    expect(claimResult).not.toBeNull();
    await userEvent.click(claimResult as HTMLElement);
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Evidence");
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Alice uses: Dense-Mem");

    await userEvent.click(screen.getByRole("checkbox", { name: /evidence/i }));
    expect(screen.getByText("No recall results")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: /evidence/i }));
    expect(screen.getByRole("listbox", { name: "Recall result list" })).toHaveTextContent("Alice uses: Dense-Mem");

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
    expect(within(controls).getByLabelText("Relationship limit")).toHaveValue(80);
    expect((await screen.findAllByText("Alice")).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByLabelText("Graph totals")).toHaveTextContent("2");
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("Select a node");
    await userEvent.click(within(screen.getByTestId("force-graph")).getByRole("button", { name: "Alice" }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/ui/api/node-detail?type=entity&id=entity-alice", expect.any(Object));
    });
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("community-1");
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("0.940");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/graph?scope=overview&types=entity%2Cvalue&depth=2&limit=80",
        expect.any(Object),
      );
    });
    await userEvent.click(within(controls).getByRole("button", { name: "Refresh" }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/graph?scope=overview&types=entity%2Cvalue&depth=2&limit=80",
        expect.any(Object),
      );
    });
  });

  it("refreshes selected graph node details when the graph reloads", async () => {
    const refreshedDetails = {
      ...graphNodeDetails,
      "entity:entity-alice": {
        ...graphNodeDetails["entity:entity-alice"],
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

    await userEvent.click(within(screen.getByTestId("force-graph")).getByRole("button", { name: "Alice" }));
    await waitFor(() => {
      expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("0.940");
    });

    await userEvent.click(within(controls).getByRole("button", { name: "Refresh" }));

    await waitFor(() => {
      expect(fetchMock.mock.calls.filter(([url]) => String(url).startsWith("/ui/api/node-detail?type=entity&id=entity-alice"))).toHaveLength(2);
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
        "/ui/api/graph?scope=overview&types=entity%2Cvalue&depth=2&limit=80",
        expect.any(Object),
      );
    });
    const graphCallCount = () => fetchMock.mock.calls.filter(([url]) => String(url).startsWith("/ui/api/graph")).length;
    const beforeDisabledRefresh = graphCallCount();

    for (const label of ["Entity", "Value"]) {
      await userEvent.click(within(controls).getByRole("checkbox", { name: label }));
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

    expect(await screen.findByTestId("force-graph")).toHaveTextContent("Alice");
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/graph?scope=local&types=entity%2Cvalue&anchor_type=entity&anchor_id=entity-alice&depth=2&limit=48",
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
    expect(screen.getByRole("listbox", { name: "Recall result list" })).not.toHaveTextContent("Alice uses: Dense-Mem");

    await userEvent.clear(screen.getByLabelText("Keyword"));
    await userEvent.type(screen.getByLabelText("Keyword"), "alice");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    expect(await screen.findByLabelText("Source")).toHaveValue("all");
    expect(screen.getByRole("listbox", { name: "Recall result list" })).toHaveTextContent("Alice is working on project-x with Dense-Mem.");
  });

  it("labels write-member telemetry as credential usage", async () => {
    const writeSession: UserSession = {
      ...baseSession,
      membership: { ...baseSession.membership, grants: ["read", "write"] },
      credential: { ...baseSession.credential!, scopes: ["read", "write"] },
    };
    const fetchMock = mockUserFetch(writeSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_write");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /usage/i }));

    expect(await screen.findByLabelText("My credential usage totals")).toHaveTextContent("HTTP requests");
    expect(screen.queryByLabelText("Team usage totals")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/ui/api/telemetry?window=1h", expect.any(Object));
    });
  });

  it("rotates the current write-scoped key without storing the replacement", async () => {
    const writeSession: UserSession = {
      ...baseSession,
      membership: { ...baseSession.membership, grants: ["read", "write"] },
      credential: { ...baseSession.credential!, scopes: ["read", "write"] },
    };
    const fetchMock = mockUserFetch(writeSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_old");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /my credential/i }));
    await userEvent.click(await screen.findByRole("button", { name: /regenerate key/i }));

    expect(await screen.findByDisplayValue("dm_new_plaintext")).toHaveAccessibleName("Generated API key");
    await userEvent.click(screen.getByRole("button", { name: /copy api key/i }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("dm_new_plaintext");
    expect(sessionStorage.getItem("denseMem.userApiKey")).toBeNull();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/ui/api/credential/rotate", expect.objectContaining({ method: "POST" }));
    });
  });

  it("lets a manager update the team and manage member credentials", async () => {
    const managerSession: UserSession = {
      ...baseSession,
      membership: {
        ...baseSession.membership,
        name: "Manager",
        grants: ["read", "write"],
        role: "manager",
      },
      credential: {
        ...baseSession.credential!,
        name: "Manager",
        scopes: ["read", "write"],
        role: "manager",
      },
    };
    const managerCredential: UserCredential = { ...managerSession.credential! };
    const fetchMock = mockUserFetch(managerSession, [managerCredential, memberCredential]);
    sessionStorage.setItem("denseMem.userApiKey", "dm_manager");
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /^team$/i }));

    expect(await screen.findByLabelText("Credential name Manager")).toBeDisabled();
    expect(screen.getByRole("button", { name: /regenerate api key for credential Manager/i })).toBeDisabled();

    const teamName = screen.getByLabelText("Name", { selector: "#user-team-name" });
    await userEvent.clear(teamName);
    await userEvent.type(teamName, "Renamed Team");
    await userEvent.click(screen.getByRole("button", { name: /save team/i }));
    expect(await screen.findByText("Renamed Team")).toBeInTheDocument();

    const newCredentialName = screen.getByLabelText("Credential name", { selector: "#managed-credential-name" });
    await userEvent.clear(newCredentialName);
    await userEvent.type(newCredentialName, "Writer");
    const createForm = screen.getByRole("button", { name: /create member credential/i }).closest("form");
    expect(createForm).not.toBeNull();
    await userEvent.click(within(createForm as HTMLElement).getByLabelText("Recall feedback"));
    await userEvent.click(screen.getByRole("button", { name: /create member credential/i }));
    expect(await screen.findByDisplayValue("dm_member_plaintext")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/ui/api/team/credentials"),
      expect.objectContaining({ method: "POST", body: expect.not.stringContaining("role") }),
    ));
    expect(fetchMock.mock.calls.map(([, init]) => String(init?.body ?? ""))).toContainEqual(expect.stringContaining(`"scopes":["read","write","feedback:read"]`));

    const memberName = await screen.findByLabelText("Credential name Reader credential");
    await userEvent.clear(memberName);
    await userEvent.type(memberName, "Reader Updated");
    await userEvent.click(screen.getByRole("button", { name: /save credential Reader credential/i }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/ui/api/team/credentials/${memberCredential.id}`),
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
      expect.stringContaining(`/ui/api/team/credentials/${memberCredential.id}`),
      expect.objectContaining({ method: "PATCH", body: expect.stringContaining(`"scopes":["read","feedback:read"]`) }),
    ));

    await userEvent.click(screen.getByRole("button", { name: /regenerate api key for credential Reader Updated/i }));
    expect(await screen.findByDisplayValue("dm_member_rotated")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/ui/api/team/credentials/${memberCredential.id}/rotate`),
        expect.objectContaining({ method: "POST" }),
      );
    });

    await userEvent.click(screen.getByRole("button", { name: /delete credential Reader Updated/i }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/ui/api/team/credentials/${memberCredential.id}`),
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
      membership: {
        ...baseSession.membership,
        name: "Manager",
        grants: ["read", "write"],
        role: "manager",
      },
      credential: {
        ...baseSession.credential!,
        name: "Manager",
        scopes: ["read", "write"],
        role: "manager",
      },
    };
    const fetchMock = mockUserFetch(managerSession, [{ ...managerSession.credential! }]);
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
        expect.stringContaining("/ui/api/team"),
        expect.objectContaining({ method: "PATCH" }),
      );
    });
    const patchCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith("/ui/api/team") && init?.method === "PATCH");
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
      membership: {
        ...baseSession.membership,
        name: "Manager",
        grants: ["read", "write"],
        role: "manager",
      },
      credential: {
        ...baseSession.credential!,
        name: "Manager",
        scopes: ["read", "write"],
        role: "manager",
      },
    };
    const fetchMock = mockUserFetch(managerSession, [{ ...managerSession.credential! }]);
    sessionStorage.setItem("denseMem.userApiKey", "dm_manager");

    render(<UserPortalApp />);
    await screen.findByText("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /usage/i }));

    expect(await screen.findByLabelText("Team usage totals")).toHaveTextContent("HTTP requests");
    expect(screen.queryByLabelText("My credential usage totals")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/ui/api/telemetry?window=1h", expect.any(Object));
    });
  });

  it("derives SSO cookie auth from the credential-free session and switches teams", async () => {
    const { initial, switched, secondTeam } = ssoSessions();
    const fetchMock = mockSSOUserFetch(initial, switched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    expect(screen.getByRole("button", { name: /my credential/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^team$/i })).not.toBeInTheDocument();
    const teamSelect = await screen.findByLabelText("Active team");
    expect(teamSelect).toHaveValue(initial.team.id);
    expect(screen.getByText(`https://memory.example.test/teams/${initial.team.id}/mcp`)).toBeInTheDocument();

    await userEvent.selectOptions(teamSelect, secondTeam.id);

    await expectCurrentWorkspace("Analytics Team");
    expect(screen.getByText(`https://memory.example.test/teams/${secondTeam.id}/mcp`)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /my credential/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^team$/i })).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/sso/team",
        expect.objectContaining({
          method: "POST",
          credentials: "include",
          body: JSON.stringify({ team_id: secondTeam.id }),
        }),
      );
    });
    expect(sessionStorage.getItem("denseMem.userApiKey")).toBeNull();
  });

  it("reloads usage telemetry after switching SSO teams", async () => {
    const { initial, switched, secondTeam } = ssoSessions();
    const firstMembership = { ...initial.membership, grants: ["read", "write"] };
    const writeInitial: UserSession = {
      ...initial,
      membership: firstMembership,
      teams: [
        { team: initial.team, membership: firstMembership },
        { team: switched.team, membership: switched.membership },
      ],
    };
    const writeSwitched: UserSession = {
      ...switched,
      teams: writeInitial.teams,
    };
    const fetchMock = mockSSOUserFetch(writeInitial, writeSwitched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /usage/i }));
    expect(await screen.findByLabelText("My credential usage totals")).toHaveTextContent("4");

    await userEvent.selectOptions(screen.getByLabelText("Active team"), secondTeam.id);

    await expectCurrentWorkspace("Analytics Team");
    await waitFor(() => {
      expect(screen.getByLabelText("Team usage totals")).toHaveTextContent("9");
    });
    expect(screen.queryByLabelText("My credential usage totals")).not.toBeInTheDocument();
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
    const membership = { ...initial.membership, grants: ["read", "write"] };
    const readWriteSession: UserSession = {
      ...initial,
      membership,
      teams: initial.teams.map((item, index) => index === 0 ? { ...item, membership } : item),
    };
    const fetchMock = mockSSOUserFetch(readWriteSession, switched);
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /my credential/i }));
    await userEvent.click(screen.getByRole("button", { name: /create api key/i }));

    expect(await screen.findByDisplayValue("dm_sso_personal_plaintext")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/sso/credentials",
        expect.objectContaining({
          method: "POST",
          credentials: "include",
          body: expect.stringContaining(`"scopes":["read","write"]`),
        }),
      );
    });

    const secondName = screen.getByLabelText("Credential name", { selector: "#sso-personal-credential-name" });
    await userEvent.type(secondName, "Second key");
    await userEvent.selectOptions(screen.getByLabelText("Memory binding", { selector: "#sso-personal-credential-binding" }), "credential_private");
    await userEvent.click(screen.getByRole("button", { name: /create api key/i }));
    expect((await screen.findAllByText("Second key")).length).toBeGreaterThan(0);
    expect(screen.getAllByLabelText("Credential name")).toHaveLength(1);
    const secondCredential = screen.getByRole("button", { name: /Second key/ });
    await userEvent.click(secondCredential);

    await userEvent.click(screen.getByRole("button", { name: /regenerate key/i }));
    expect(await screen.findByDisplayValue("dm_sso_personal_rotated")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/sso/credentials/88888888-8888-4888-8888-888888888888/rotate",
        expect.objectContaining({ method: "POST", credentials: "include" }),
      );
    });
    await userEvent.click(screen.getByRole("button", { name: /revoke key/i }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/sso/credentials/88888888-8888-4888-8888-888888888888",
        expect.objectContaining({ method: "DELETE", credentials: "include", body: JSON.stringify({ acknowledge_irreversible: true }),
          headers: expect.objectContaining({ "Idempotency-Key": expect.stringMatching(/^sso-credential-delete-/) }),
        }),
      );
    });
    expect(screen.queryByText("Second key")).not.toBeInTheDocument();
  });

  it("keeps SSO read-only owned API key rotation disabled", async () => {
    const { initial, switched } = ssoSessions();
    mockSSOUserFetch(initial, switched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /my credential/i }));
    await userEvent.click(screen.getByRole("button", { name: /create api key/i }));

    expect(await screen.findByDisplayValue("dm_sso_personal_plaintext")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /regenerate key/i })).toBeDisabled();
  });
});
function mockUserFetch(session: UserSession, credentials: UserCredential[] = [], options: { recallHits?: RecallHit[] | RecallHit[][]; graphSnapshot?: GraphSnapshot | GraphSnapshot[]; graphNodeDetails?: Record<string, GraphNode> | Record<string, GraphNode>[] } = {}) {
  let currentTeam = session.team;
  let currentCredentials = credentials;
  let recallCallCount = 0;
  let graphCallCount = 0;
  let graphNodeDetailCallCount = 0;
  let portalSessionCreated = false;
  const rotatedSession = {
    ...session,
    credential: session.credential ? { ...session.credential, key_suffix: "new123", last_used_at: null } : null,
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
        return portalSessionCreated
          ? jsonResponse({ data: { ...session, team: currentTeam } })
          : jsonResponse({ code: "AUTH_MISSING", message: "missing authorization header", details: null }, 401);
      }
      const selectedSession = auth.includes("dm_new_plaintext") ? rotatedSession : session;
      return jsonResponse({ data: { ...selectedSession, team: currentTeam } });
    }
    if (url === "/ui/api/session" && method === "POST") {
      portalSessionCreated = true;
      return jsonResponse({ data: { status: "signed_in" } });
    }
    if (url === "/ui/api/credential/rotate" && method === "POST") {
      return jsonResponse({
        data: {
          api_key: "dm_new_plaintext",
          credential: rotatedSession.credential,
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
      return jsonResponse({ data: details[key] ?? graphNodeDetails["entity:entity-alice"] });
    }
    if (url.startsWith("/ui/api/graph") && method === "GET") {
      return jsonResponse({ data: optionValue(options.graphSnapshot ?? overviewGraph, graphCallCount++) });
    }
    if (url === "/ui/api/team" && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      currentTeam = { ...currentTeam, name: body.name ?? currentTeam.name, description: body.description ?? currentTeam.description, config: body.config ?? currentTeam.config };
      return jsonResponse({ data: currentTeam });
    }
    if (url === "/ui/api/team/credentials" && method === "GET") {
      return jsonResponse({ data: currentCredentials, pagination: { limit: 20, offset: 0, total: currentCredentials.length } });
    }
    if (url === "/ui/api/team/credentials" && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const created: UserCredential = {
        ...memberCredential,
        id: "44444444-4444-4444-8444-444444444444",
        name: body.name,
        scopes: body.scopes,
        role: "member",
        key_suffix: "new456",
      };
      currentCredentials = [created, ...currentCredentials];
      return jsonResponse({ data: { api_key: "dm_member_plaintext", credential: created } }, 201);
    }
    if (url.includes("/ui/api/team/credentials/") && url.endsWith("/rotate") && method === "POST") {
      const rotated = { ...(currentCredentials.find((credential) => url.includes(credential.id)) ?? memberCredential), key_suffix: "rot789" };
      currentCredentials = currentCredentials.map((credential) => (credential.id === rotated.id ? rotated : credential));
      return jsonResponse({ data: { api_key: "dm_member_rotated", credential: rotated } });
    }
    if (url.includes("/ui/api/team/credentials/") && method === "PATCH") {
      const body = JSON.parse(String(init?.body));
      const current = currentCredentials.find((credential) => url.endsWith(`/credentials/${credential.id}`)) ?? memberCredential;
      const updated = { ...current, name: body.name ?? current.name, scopes: body.scopes ?? current.scopes };
      currentCredentials = currentCredentials.map((credential) => (credential.id === updated.id ? updated : credential));
      return jsonResponse({ data: updated });
    }
    if (url.includes("/ui/api/team/credentials/") && method === "DELETE") {
      currentCredentials = currentCredentials.filter((credential) => !url.endsWith(`/credentials/${credential.id}`));
      return jsonResponse({ data: { status: "deleted" } });
    }
    if (url.startsWith("/ui/api/recall")) {
      const configuredHits = options.recallHits ?? [];
      const hits = isRecallSequence(configuredHits)
        ? configuredHits[Math.min(recallCallCount, configuredHits.length - 1)] ?? []
        : configuredHits;
      recallCallCount += 1;
      return jsonResponse({ data: recallPayloadForHits(hits) });
    }
    return jsonResponse({ message: "not found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function telemetryForSession(session: UserSession) {
  const teamScope = session.membership.role === "manager";
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
      : { type: "self", team_id: session.team.id, profile_id: session.credential?.id ?? "sso-owner" },
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
  const firstMembership = { ...baseSession.membership };
  const secondMembership = {
    ...baseSession.membership,
    team_id: secondTeam.id,
    name: "Analytics SSO",
    grants: ["read", "write"],
    role: "manager" as const,
  };
  const initial: UserSession = {
    ...baseSession,
    credential: null,
    membership: firstMembership,
    personal_credentials: [],
    teams: [
      { team: baseSession.team, membership: firstMembership },
      { team: secondTeam, membership: secondMembership },
    ],
  };
  const switched: UserSession = {
    ...initial,
    team: secondTeam,
    membership: secondMembership,
  };
  return { initial, switched, secondTeam, secondMembership };
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
    if (url === "/ui/api/sso/credentials" && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const createdID = current.personal_credentials.length === 0
        ? "77777777-7777-4777-8777-777777777777"
        : "88888888-8888-4888-8888-888888888888";
      const created: UserCredential = {
        ...memberCredential,
        team_id: current.team.id,
        id: createdID,
        name: body.name,
        key_suffix: "own123",
        scopes: body.scopes,
        role: "member",
        memory_binding: body.memory_binding ?? "profile_private",
        memory_space_kind: body.memory_binding === "credential_private" ? "credential_private" : body.memory_binding === "shared_only" ? "team_shared" : "profile_private",
      };
      current = {
        ...current,
        personal_credentials: [...current.personal_credentials, created],
      };
      return jsonResponse({ data: { api_key: "dm_sso_personal_plaintext", credential: created } }, 201);
    }
    if (url.includes("/ui/api/sso/credentials/") && url.endsWith("/rotate") && method === "POST") {
      const credentialID = url.split("/").at(-2);
      const rotated = { ...(current.personal_credentials.find((credential) => credential.id === credentialID) ?? memberCredential), team_id: current.team.id, key_suffix: "rot321" };
      current = { ...current, personal_credentials: current.personal_credentials.map((credential) => credential.id === rotated.id ? rotated : credential) };
      return jsonResponse({ data: { api_key: "dm_sso_personal_rotated", credential: rotated } });
    }
    if (url.includes("/ui/api/sso/credentials/") && method === "DELETE") {
      const credentialID = url.split("/").at(-1);
      current = { ...current, personal_credentials: current.personal_credentials.filter((credential) => credential.id !== credentialID) };
      return jsonResponse({ data: { status: "revoked" } });
    }
    if (url === "/ui/api/sso/logout" && method === "POST") {
      if (options.logoutStatus) {
        return jsonResponse({ message: "logout failed" }, options.logoutStatus);
      }
      return jsonResponse({ data: { status: "signed_out" } });
    }
    if (url.startsWith("/ui/api/recall")) {
      return jsonResponse({ data: [] });
    }
    return jsonResponse({ message: "not found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}
