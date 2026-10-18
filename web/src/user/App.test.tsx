import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserPortalApp } from "./App";
import { authorizationHeader, expectCurrentWorkspace, isRecallSequence, jsonResponse, optionValue, recallPayloadForHits, ssoSessions } from "./App.test-helpers";
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
  memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: false } },
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
    memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: false } },
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
  memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: false } },
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
  it("keeps sign-in and sign-out available without a Usage tab", async () => {
    const fetchMock = mockUserFetch(baseSession);
    render(<UserPortalApp />);

    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent("API key is required.");
    expect(fetchMock.mock.calls.some(([url, init]) => String(url) === "/ui/api/session" && init?.method === "POST")).toBe(false);

    await userEvent.click(screen.getByRole("button", { name: "Choose theme" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Dark" }));
    expect(localStorage.getItem("denseMem.userTheme")).toBe("dark");
    await userEvent.type(screen.getByLabelText("API key"), "dm_key");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await expectCurrentWorkspace("Research Team");
    expect(screen.queryByRole("link", { name: "Usage" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /sign out/i }));

    expect(await screen.findByRole("button", { name: /sign in/i })).toBeInTheDocument();
    expect(screen.queryByLabelText("Current workspace")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url, init]) => String(url) === "/ui/api/session/logout" && init?.method === "POST")).toBe(true);
  });

  it("logs in with an API key and does not call team credential list APIs", async () => {
    const fetchMock = mockUserFetch(baseSession);
    render(<UserPortalApp />);

    await userEvent.type(screen.getByLabelText(/api key/i), "dm_key");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await expectCurrentWorkspace("Research Team");
    expect(screen.getByLabelText("Knowledge navigation")).toHaveClass("primary-rail");
    expect(screen.getByLabelText("Knowledge sections")).toHaveClass("rail-tabs");
    expect(screen.getByLabelText("Current workspace")).not.toHaveTextContent("Mine");
	 expect(screen.queryByRole("button", { name: "Communities" })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/credentials"))).toBe(false);
  });

  it("disables self rotation for read-only keys", async () => {
    mockUserFetch(baseSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");
    expect(sessionStorage.getItem("denseMem.userApiKey")).toBeNull();
    await userEvent.click(screen.getByRole("link", { name: "My credentials" }));

    expect(await screen.findByRole("button", { name: /regenerate key/i })).toBeDisabled();
  });

  it("hides usage telemetry for read-only keys", async () => {
    const fetchMock = mockUserFetch(baseSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");

    expect(screen.queryByRole("link", { name: "Usage" })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith("/ui/api/telemetry"))).toBe(false);
  });

  it("filters recall results and updates the inspector selection", async () => {
    mockUserFetch(baseSession, [], { recallHits });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");
    await userEvent.type(screen.getByLabelText("Keyword"), "project");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    const resultList = await screen.findByRole("listbox", { name: "Recall result list" });
    expect(within(resultList).getAllByRole("option")).toHaveLength(3);
    expect(screen.queryByRole("button", { name: /star/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /more actions/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add to collection" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create claim" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Open details" }));
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Evidence");
    await userEvent.click(screen.getByRole("button", { name: "Close details panel" }));

    const claimResult = within(resultList).getAllByText("Alice uses: Dense-Mem")[0]?.closest("[role='option']");
    expect(claimResult).not.toBeNull();
    await userEvent.click(claimResult as HTMLElement);
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Evidence");
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Alice uses: Dense-Mem");

    await userEvent.click(screen.getByRole("button", { name: "Close details panel" }));
    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    await userEvent.click(screen.getByRole("checkbox", { name: /evidence/i }));
    expect(screen.getByText("No recall results")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: /evidence/i }));
    expect(screen.getByRole("listbox", { name: "Recall result list" })).toHaveTextContent("Alice uses: Dense-Mem");

    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Open details" }));
    await userEvent.click(screen.getByRole("tab", { name: "Recall" }));
    expect(screen.getByLabelText("Inspector")).toHaveTextContent("Final score");
    await userEvent.click(screen.getByRole("button", { name: "Close details panel" }));
    await userEvent.click(screen.getByRole("button", { name: "Sort by date" }));
    expect(screen.getByRole("button", { name: "Sort by relevance" })).toHaveTextContent("Sort: Date");
    await userEvent.click(screen.getByRole("button", { name: "Use compact density" }));
    expect(screen.getByRole("button", { name: "Use comfortable density" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByLabelText("Inspector")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Open details" }));
    expect(screen.getByLabelText("Inspector")).toBeInTheDocument();
  });

  it("opens the graph tab and loads the overview graph", async () => {
    const fetchMock = mockUserFetch(baseSession, [], { graphSnapshot: overviewGraph });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("link", { name: "Graph" }));

    expect(await screen.findByLabelText("Knowledge graph")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Graph controls" }));
    const controls = screen.getByLabelText("Graph controls");
    expect(within(controls).getByLabelText("Relationship limit")).toHaveValue(80);
    expect((await screen.findAllByText("Alice")).length).toBeGreaterThanOrEqual(1);
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Open details" }));
    expect(screen.getByLabelText("Graph totals")).toHaveTextContent("2");
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("Select a node");
    await userEvent.click(screen.getByRole("button", { name: "Close details panel" }));
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
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Graph controls" }));
    await userEvent.click(within(screen.getByLabelText("Graph controls")).getByRole("button", { name: "Refresh" }));
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
    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("link", { name: "Graph" }));
    await userEvent.click(screen.getByRole("button", { name: "Graph controls" }));
    const controls = await screen.findByLabelText("Graph controls");

    await userEvent.click(within(screen.getByTestId("force-graph")).getByRole("button", { name: "Alice" }));
    await waitFor(() => {
      expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("0.940");
    });

    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Graph controls" }));
    await userEvent.click(within(screen.getByLabelText("Graph controls")).getByRole("button", { name: "Refresh" }));

    await waitFor(() => {
      expect(fetchMock.mock.calls.filter(([url]) => String(url).startsWith("/ui/api/node-detail?type=entity&id=entity-alice"))).toHaveLength(2);
    });
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Open details" }));
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("community-2");
    expect(screen.getByLabelText("Graph inspector")).toHaveTextContent("0.670");
  });

  it("blocks graph refresh when every type filter is disabled", async () => {
    const fetchMock = mockUserFetch(baseSession, [], { graphSnapshot: overviewGraph });
    sessionStorage.setItem("denseMem.userApiKey", "dm_read");

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("link", { name: "Graph" }));
    await userEvent.click(screen.getByRole("button", { name: "Graph controls" }));
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
    await expectCurrentWorkspace("Research Team");
    await userEvent.type(screen.getByLabelText("Keyword"), "project");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await screen.findByRole("listbox", { name: "Recall result list" });
    await userEvent.click(screen.getByRole("button", { name: "Open details" }));
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
    await expectCurrentWorkspace("Research Team");
    await userEvent.type(screen.getByLabelText("Keyword"), "project");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    await userEvent.selectOptions(await screen.findByLabelText("Source"), "notes");
    expect(screen.getByRole("listbox", { name: "Recall result list" })).toHaveTextContent("Alice is working on project-x with Dense-Mem.");
    expect(screen.getByRole("listbox", { name: "Recall result list" })).not.toHaveTextContent("Alice uses: Dense-Mem");

    await userEvent.keyboard("{Escape}");
    await userEvent.clear(screen.getByLabelText("Keyword"));
    await userEvent.type(screen.getByLabelText("Keyword"), "alice");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    await userEvent.click(screen.getByRole("button", { name: "Filters" }));
    expect(await screen.findByLabelText("Source")).toHaveValue("all");
    expect(screen.getByRole("listbox", { name: "Recall result list" })).toHaveTextContent("Alice is working on project-x with Dense-Mem.");
  });

  it("omits the retired Usage tab for write members", async () => {
    const writeSession: UserSession = {
      ...baseSession,
      membership: { ...baseSession.membership, grants: ["read", "write"] },
      credential: { ...baseSession.credential!, scopes: ["read", "write"] },
    };
    const fetchMock = mockUserFetch(writeSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_write");

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");
    expect(screen.queryByRole("link", { name: "Usage" })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith("/ui/api/telemetry"))).toBe(false);
  });

  it("rotates the current write-scoped key without storing the replacement", async () => {
    const writeSession: UserSession = {
      ...baseSession,
      membership: { ...baseSession.membership, grants: ["read", "write"] },
      credential: { ...baseSession.credential!, scopes: ["read", "write"] },
    };
    const fetchMock = mockUserFetch(writeSession);
    sessionStorage.setItem("denseMem.userApiKey", "dm_old");

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("link", { name: "My credentials" }));
    await userEvent.click(await screen.findByRole("button", { name: /regenerate key/i }));
    await userEvent.click(screen.getByRole("button", { name: "Regenerate key" }));

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

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("link", { name: "Team" }));

    expect(await screen.findByLabelText("Credential name Manager")).toBeDisabled();
    expect(screen.getByRole("button", { name: /regenerate api key for credential Manager/i })).toBeDisabled();

    const teamName = screen.getByLabelText("Name", { selector: "#user-team-name" });
    await userEvent.clear(teamName);
    await userEvent.type(teamName, "Renamed Team");
    await userEvent.click(screen.getByRole("button", { name: /save team/i }));
    await expectCurrentWorkspace("Renamed Team");

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
    await userEvent.click(screen.getByRole("button", { name: "Regenerate API key" }));
    expect(await screen.findByDisplayValue("dm_member_rotated")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/ui/api/team/credentials/${memberCredential.id}/rotate`),
        expect.objectContaining({ method: "POST" }),
      );
    });

    await userEvent.click(screen.getByRole("button", { name: /delete credential Reader Updated/i }));
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining(`/ui/api/team/credentials/${memberCredential.id}`),
        expect.objectContaining({ method: "DELETE" }),
      );
    });
  });


  it("omits the retired Usage tab for managers", async () => {
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
    await expectCurrentWorkspace("Research Team");
    expect(screen.getByRole("link", { name: "Team" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Usage" })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith("/ui/api/telemetry"))).toBe(false);
  });

  it("drops a late recall response after switching authenticated teams", async () => {
    const { initial, switched, secondTeam } = ssoSessions(baseSession);
    const fetcher = mockSSOUserFetch(initial, switched);
    const original = fetcher.getMockImplementation()!;
    let release: ((value: Response) => void) | undefined;
    fetcher.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => String(input).startsWith("/ui/api/recall") ? new Promise<Response>((resolve) => { release = resolve; }) : original(input, init));
    render(<UserPortalApp />);
    await expectCurrentWorkspace(initial.team.name);
    await userEvent.type(screen.getByLabelText("Keyword"), "team-a private draft");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(release).toBeDefined());
    await userEvent.click(screen.getByRole("button", { name: "Choose team" }));
    await userEvent.click(screen.getByRole("button", { name: secondTeam.name }));
    await expectCurrentWorkspace(secondTeam.name);
    release!(jsonResponse({ data: recallPayloadForHits([{ evidence: { evidence_id: "old", context: "Old team result" } }] as RecallHit[]) }));
    await waitFor(() => expect(screen.queryByText("Old team result")).not.toBeInTheDocument());
    expect(screen.getByLabelText("Keyword")).toHaveValue("");
    expect(window.location.href).not.toContain("private");
    expect(screen.queryByRole("link", { name: "My credentials" })).not.toBeInTheDocument();
  });

  it.each(["success", "failure"] as const)("prevents overlapping SSO team switches and recovers after %s", async (outcome) => {
    const { initial, switched, secondTeam } = ssoSessions(baseSession);
    const fetchMock = mockSSOUserFetch(initial, switched);
    const original = fetchMock.getMockImplementation()!;
    let finishSwitch!: () => void;
    const pending = new Promise<void>((resolve) => { finishSwitch = resolve; });
    fetchMock.mockImplementation((input, init) => String(input) === "/ui/api/sso/team" && init?.method === "POST"
      ? pending.then(() => outcome === "success" ? original(input, init) : jsonResponse({ error: { message: "Team switch unavailable" } }, 503))
      : original(input, init));
    render(<UserPortalApp />);
    await expectCurrentWorkspace(initial.team.name);
    const picker = screen.getByRole("button", { name: "Choose team" });
    await userEvent.click(picker);
    await userEvent.click(screen.getByRole("button", { name: secondTeam.name }));
    try {
      await screen.findByText("Switching team");
      const currentTeam = within(screen.getByLabelText("Available teams")).getByRole("button", { name: new RegExp(initial.team.name) });
      await userEvent.click(currentTeam);
      expect(fetchMock.mock.calls.filter(([input, init]) => String(input) === "/ui/api/sso/team" && init?.method === "POST")).toHaveLength(1);
      expect(currentTeam).toBeDisabled();
      expect(screen.getByRole("button", { name: secondTeam.name })).toBeDisabled();
    } finally {
      finishSwitch();
    }
    await waitFor(() => expect(picker).toBeEnabled());
    await expectCurrentWorkspace(outcome === "success" ? secondTeam.name : initial.team.name);
    if (outcome === "success") {
      expect(screen.queryByRole("textbox", { name: "Find a team" })).not.toBeInTheDocument();
    } else {
      expect(await screen.findByRole("alert")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: secondTeam.name })).toBeEnabled();
    }
  });

  it("derives SSO cookie auth from the credential-free session and switches teams", async () => {
    const { initial, switched, secondTeam } = ssoSessions(baseSession);
    const fetchMock = mockSSOUserFetch(initial, switched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    expect(screen.getByRole("link", { name: "My credentials" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Team" })).not.toBeInTheDocument();
    const teamSelect = await screen.findByRole("button", { name: "Choose team" });
    expect(teamSelect).toHaveTextContent(initial.team.name);
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(screen.getByText(`https://memory.example.test/teams/${initial.team.id}/mcp`)).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");

    await userEvent.click(teamSelect);
    await userEvent.click(screen.getByRole("button", { name: secondTeam.name }));

    await expectCurrentWorkspace("Analytics Team");
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(screen.getByText(`https://memory.example.test/teams/${secondTeam.id}/mcp`)).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("link", { name: "My credentials" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Team" })).toBeInTheDocument();
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

  it("shows effective SSO and API-key capabilities independently of declared grants", async () => {
    const { initial, switched } = ssoSessions(baseSession);
    const session: UserSession = {
      ...initial,
      membership: { ...initial.membership, grants: ["read", "write"] },
      memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: false } },
      personal_credentials: [{
        ...memberCredential,
        scopes: ["read", "write"],
        memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: false } },
      }],
    };
    mockSSOUserFetch(session, switched);

    render(<UserPortalApp />);
    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("link", { name: "My credentials" }));

    expect(screen.getByText("Private: Read/write · Team-read-only")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(screen.getByText("Private memory").parentElement).toHaveTextContent("Read/write");
    expect(screen.getByText("Team memory").parentElement).toHaveTextContent("Team-read-only");
    expect(screen.getByText("Team memory").parentElement).not.toHaveTextContent("Team read/write");
    expect(screen.queryByLabelText("Memory binding")).not.toBeInTheDocument();
  });

  it("keeps the SSO portal open when logout fails", async () => {
    const { initial, switched } = ssoSessions(baseSession);
    mockSSOUserFetch(initial, switched, { logoutStatus: 500 });

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("button", { name: /sign out/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("logout failed");
    expect(screen.getByLabelText("Current workspace")).toHaveTextContent("Research Team");
    expect(screen.queryByLabelText(/api key/i)).not.toBeInTheDocument();
  });

  it("lets an SSO read/write member create and rotate their owned API key", async () => {
    const { initial, switched } = ssoSessions(baseSession);
    const membership = { ...initial.membership, grants: ["read", "write"] };
    const readWriteSession: UserSession = {
      ...initial,
      membership,
      teams: initial.teams.map((item, index) => index === 0 ? { ...item, membership } : item),
    };
    const fetchMock = mockSSOUserFetch(readWriteSession, switched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("link", { name: "My credentials" }));
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
    expect(screen.queryByLabelText("Memory binding")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /create api key/i }));
    expect((await screen.findAllByText("Second key")).length).toBeGreaterThan(0);
    expect(screen.getAllByLabelText("Credential name")).toHaveLength(1);
    const secondCredential = screen.getByRole("button", { name: /Second key/ });
    await userEvent.click(secondCredential);

    await userEvent.click(screen.getByRole("button", { name: /regenerate key/i }));
    await userEvent.click(screen.getByRole("button", { name: "Regenerate key" }));
    expect(await screen.findByDisplayValue("dm_sso_personal_rotated")).toBeInTheDocument();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/ui/api/sso/credentials/88888888-8888-4888-8888-888888888888/rotate",
        expect.objectContaining({ method: "POST", credentials: "include" }),
      );
    });
    await userEvent.click(screen.getByRole("button", { name: /revoke key/i }));
    await userEvent.click(screen.getByRole("button", { name: "Revoke key" }));
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
    const { initial, switched } = ssoSessions(baseSession);
    mockSSOUserFetch(initial, switched);

    render(<UserPortalApp />);

    await expectCurrentWorkspace("Research Team");
    await userEvent.click(screen.getByRole("link", { name: "My credentials" }));
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
    if (url === "/ui/api/session/logout" && method === "POST") {
      portalSessionCreated = false;
      return jsonResponse({ data: { status: "signed_out" } });
    }
    if (url === "/ui/api/credential/rotate" && method === "POST") {
      return jsonResponse({
        data: {
          api_key: "dm_new_plaintext",
          credential: rotatedSession.credential,
        },
      });
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
        memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: body.scopes.includes("write") } },
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
