import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ControlApi, DirectoryConnector, DirectoryPreview, SSOProvider, Team } from "./api";
import { App } from "./App";
import { requestBytes, requestJson } from "./http";
import { TeamDreamingConfigForm } from "./teamDreamingConfig";
import { buildOperationLogsPath, buildRememberAttemptDiagnosticPath, buildRememberAttemptDiagnosticsPath } from "./control-observability-api";
import { getEvidenceConflict, listEvidenceConflicts, resolveEvidenceConflict } from "./evidence-conflict-api";
import { ConflictQueuePanel } from "./control/ConflictQueuePanel";
import { SecurityPanel } from "./control/SecurityPanel";
import { DirectoryAutomationPanel } from "./control/DirectoryAutomationPanel";
import { SSOPanel } from "./control/SSOPanel";
import { RecallFeedbackPanel } from "./control/RecallFeedbackPanel";
import { UserDreamsPanel } from "./user/DreamsPanel";
import { ControlDreamsPanel } from "./control/DreamsPanel";
import { ConfigPanel } from "./control/ConfigPanel";
import { TeamManagementPanel } from "./user/TeamManagementPanel";
import { GraphSnapshot, RecallHit, UserApi, UserSession } from "./user/api";
import { GraphPanel, ResultGraphPreview } from "./user/GraphPanel";
import { SearchPanel } from "./user/SearchPanel";
import { InfoTooltip, PortalShell, SecretBox, writeClipboardText } from "./ui/components";
import { DreamEvidenceSummary, MetricLabel, runOutcome, runStatusClass } from "./ui/dreams";

vi.mock("react-force-graph-2d", async () => {
  const React = await import("react");
  return { default: React.forwardRef((props: {
    graphData?: { nodes?: Array<{ id?: string; key?: string; title?: string; type?: string }> ; links?: Array<{ source?: unknown; target?: unknown; relationship?: string }> };
    onNodeClick?: (node: { key?: string }) => void;
    nodeCanvasObject?: (node: { key?: string; id?: string; title?: string; type?: string; x?: number; y?: number }, canvas: CanvasRenderingContext2D, scale: number) => void;
    nodePointerAreaPaint?: (node: { key?: string; id?: string; title?: string; type?: string; x?: number; y?: number }, color: string, canvas: CanvasRenderingContext2D) => void;
    linkLabel?: (link: { relationship?: string }) => string;
    linkColor?: (link: { relationship?: string }) => string;
    linkWidth?: (link: { source?: unknown; target?: unknown }) => number;
    linkDirectionalArrowColor?: (link: { relationship?: string }) => string;
  }, ref) => {
    React.useImperativeHandle(ref, () => ({ d3Force: () => ({ distance: () => undefined }), d3ReheatSimulation: () => undefined, zoomToFit: () => undefined }));
    const canvas = { beginPath: () => undefined, arc: () => undefined, fill: () => undefined, stroke: () => undefined, fillText: () => undefined, setLineDash: () => undefined } as unknown as CanvasRenderingContext2D;
    const nodes = props.graphData?.nodes ?? [];
    const links = props.graphData?.links ?? [];
    nodes.forEach((node) => {
      props.nodeCanvasObject?.({ ...node, x: 4, y: 8 }, canvas, 1);
      props.nodeCanvasObject?.({ ...node, x: 4, y: 8 }, canvas, 2);
      props.nodePointerAreaPaint?.({ ...node, x: 4, y: 8 }, "#fff", canvas);
    });
    links.forEach((link) => {
      props.linkLabel?.(link);
      props.linkColor?.(link);
      props.linkWidth?.(link);
      props.linkDirectionalArrowColor?.(link);
    });
    return <div data-testid="force-graph">{nodes.map((node) => <button key={node.id ?? node.key} type="button" onClick={() => props.onNodeClick?.(node)}>{node.title ?? node.id}</button>)}</div>;
  }) };
});

const team: Team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Smoke Team",
  description: "",
  metadata: null,
  config: null,
  created_at: "2026-09-01T00:00:00Z",
  updated_at: "2026-09-01T00:00:00Z",
};

const provider: SSOProvider = {
  id: "22222222-2222-4222-8222-222222222222",
  name: "Smoke OIDC",
  kind: "generic_oidc",
  issuer_url: "https://issuer.example.test",
  tenant_id: "",
  identity_claim: "sub",
  client_id: "client",
  client_secret_env: "CLIENT_SECRET",
  scopes: ["openid"],
  group_claims: ["groups"],
  groups_endpoint: "",
  groups_scopes: [],
  protected_resource: { enabled: false, audiences: [], jwks_source: "discovery", jwks_uri: "", algorithms: ["RS256"], scope_claim: "scope", scope_mappings: [], team_claim: "" },
  enabled: true,
  created_at: "2026-09-01T00:00:00Z",
  updated_at: "2026-09-01T00:00:00Z",
};

beforeEach(() => {
  vi.restoreAllMocks();
  vi.stubGlobal("confirm", vi.fn(() => true));
});

describe("coverage contracts", () => {
  it("covers bounded request and path conversion branches", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }))
      .mockResolvedValueOnce(new Response("not-json", { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ message: "bad request" }), { status: 400, statusText: "Bad" }))
      .mockResolvedValueOnce(new Response(new Uint8Array([1, 2, 3]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestJson("/ok", { token: "secret", method: "POST", body: { value: 1 } })).resolves.toEqual({ ok: true });
    await expect(requestJson("/invalid-json")).rejects.toMatchObject({ status: 200 });
    await expect(requestJson("/bad")).rejects.toMatchObject({ status: 400, message: "bad request" });
    await expect(requestBytes("/bytes")).resolves.toEqual(new Uint8Array([1, 2, 3]));
    expect(buildOperationLogsPath({ limit: 10, offset: 2, severity: "ERROR", sort: "severity", direction: "asc", event: "x", team_id: "team", reference_type: "kind", reference_id: "ref", from: "a", to: "b" })).toContain("limit=10");
    expect(buildRememberAttemptDiagnosticsPath({ team_id: "team", outcome: "failed", limit: 10, offset: 2 })).toContain("outcome=failed");
    expect(buildRememberAttemptDiagnosticPath("team/a", "attempt/b")).toContain("team%2Fa");
    const request = vi.fn().mockResolvedValue({});
    await listEvidenceConflicts(request, "team/a", { status: "open", limit: 10, cursor: "next" });
    await getEvidenceConflict(request, "team/a", "conflict/b", 25, "cursor");
    await resolveEvidenceConflict(request, "team/a", "conflict/b", { expected_version: 1, decision: "dismiss", reason: "done" });
    expect(request).toHaveBeenCalledTimes(3);
  });

  it("covers the ControlApi request surface and encoded filters", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ data: {}, pagination: { limit: 25, offset: 0, total: 0 } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const api = new ControlApi("token", "/control/api");
    await api.session(); await api.listTeams(); await api.createTeam({ name: "x", description: "" }); await api.updateTeam("team/a", { name: "x", description: "" }); await api.deleteTeam("team/a");
    await api.listTeamCredentials("team/a"); await api.createTeamCredential("team/a", { name: "x", rate_limit: 1 }); await api.updateTeamCredential("team/a", "key/a", { name: "x" }); await api.rotateTeamCredential("team/a", "key/a", { name: "x", rate_limit: 1 }); await api.deleteTeamCredential("team/a", "key/a");
    await api.getSecuritySettings(); await api.updateSecuritySettings({ enabled: true, failure_threshold: 1, failure_window_seconds: 1, ban_duration_seconds: 0, updated_at: "" }); await api.listSecurityBans(true); await api.createSecurityBan({ ip: "127.0.0.1", reason: "smoke test" }); await api.deleteSecurityBan("127.0.0.1");
    await api.getMetrics({ window_minutes: 60, team_id: "team/a" }); await api.getConflictQueue("team/a", { status: "open", limit: 5, cursor: "c" }); await api.listEvidenceConflicts("team/a", { status: "open", limit: 5, cursor: "c" }); await api.getEvidenceConflict("team/a", "conflict/a", 5, "cursor"); await api.resolveEvidenceConflict("team/a", "conflict/a", { expected_version: 1, decision: "resolve", reason: "ok" }); await api.getSearchConvergence(); await api.getTelemetry({ window: "1h", scope: "team", team_id: "team/a", profile_id: "profile/a" });
    await api.listSSOProviders(); await api.createSSOProvider({ name: "x", kind: "generic_oidc", issuer_url: "https://issuer", tenant_id: "", identity_claim: "sub", client_id: "id", client_secret_env: "secret", scopes: [], group_claims: [], groups_endpoint: "", groups_scopes: [], protected_resource: provider.protected_resource, enabled: true }); await api.updateSSOProvider("provider/a", {} as never); await api.deleteSSOProvider("provider/a"); await api.listSSOGroupMappings("provider/a"); await api.createSSOGroupMapping("provider/a", {} as never); await api.updateSSOGroupMapping("provider/a", "mapping/a", {} as never); await api.deleteSSOGroupMapping("provider/a", "mapping/a");
    await api.listDirectoryConnectors(); await api.getDirectoryConnector("provider/a"); await api.createDirectoryConnector("provider/a", { group_pattern: "", role_entitlements: {}, max_auto_teams: 1 }); await api.updateDirectoryConnector("connector/a", { group_pattern: "", role_entitlements: {}, max_auto_teams: 1 }); await api.rotateDirectoryCredentials("connector/a"); await api.previewDirectoryConnector("connector/a"); await api.setDirectoryConnectorStatus("connector/a", "observe", ""); await api.adoptDirectoryGroupTeam("connector/a", "group/a", "team/a"); await api.listControlAdminGroups("provider/a"); await api.createControlAdminGroup("provider/a", { group_id: "g", group_name: "G", enabled: true }); await api.deleteControlAdminGroup("provider/a", "group/a");
    await api.getGeneralConfig(); await api.updateGeneralConfig({ items: [] }); await api.getSSOConfig(); await api.updateSSOConfig({ items: [] }); await api.getDreamingConfig(); await api.updateDreamingConfig({ items: [] }); await api.getCommunityDetectionConfig(); await api.updateCommunityDetectionConfig({ items: [] }); await api.getOperationLogConfig(); await api.updateOperationLogConfig({ items: [] }); await api.getRecallFeedbackConfig(); await api.updateRecallFeedbackConfig({ items: [] }); await api.getTelemetryPricingConfig(); await api.updateTelemetryPricingConfig({ items: [] });
    await api.listOperationLogs({ limit: 5, offset: 1, severity: "ERROR", event: "x" }); await api.listRememberAttemptDiagnostics({ limit: 5, offset: 1, outcome: "failed" }); await api.getRememberAttemptDiagnostic("team/a", "attempt/a"); await api.listRecallFeedbackEvents({ limit: 5, offset: 1, team_id: "team/a", profile_id: "profile/a", quality: "low", include_pending: true, missing_context: false, irrelevant: true, from: "2026-09-01T00:00:00Z", to: "2026-09-02T00:00:00Z" }); await api.getRecallFeedbackEvent("recall/a"); await api.getTeamDreamingStatus("team/a"); await api.getTeamCommunityStatus("team/a"); await api.listTeamDreamingRuns("team/a", 5); await api.listTeamDreams("team/a", { limit: 5, status: "proposed", cursor: "c" });
    expect(fetchMock.mock.calls.length).toBeGreaterThan(50);
  });

  it("covers UserApi authentication modes, recall normalization, and request builders", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const data = url.includes("/recall?") ? { results: [], related_communities: [], related_relationships: [], discovery_paths: [], related_hypotheses: [] } : {};
      return new Response(JSON.stringify({ data, pagination: { limit: 25, offset: 0, total: 0 } }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchMock);
    const api = new UserApi("token");
    await api.session(); await api.ssoProviders(); expect(api.ssoStartUrl("provider/a")).toContain("provider%2Fa"); await api.switchSSOTeam("team/a"); await api.logoutSSO(); await api.createPortalSession(true); await api.logoutPortalSession(); await api.createSSOCredential({ name: "x", rate_limit: 1 }); await api.listSSOCredentials(); await api.getSSOCredential("key/a"); await api.rotateSSOCredential("key/a"); await api.revokeSSOCredential("key/a", "idem"); await api.rotateCredential(); await api.updateTeam({ name: "x", description: "" }); await api.listTeamCredentials(); await api.createTeamCredential({ name: "x", rate_limit: 1 }); await api.updateTeamCredential("key/a", { name: "x" }); await api.rotateTeamCredential("key/a", { name: "x", rate_limit: 1 }); await api.deleteTeamCredential("key/a"); await api.recall("query", 5); await api.graph({ scope: "local", q: "q", types: ["entity"], anchorType: "entity", anchorId: "id", depth: 2, limit: 5 }); await api.nodeDetail("entity", "id"); await api.dreamingStatus(); await api.listDreamingRuns(5); await api.listDreams({ limit: 5, status: "proposed", cursor: "c", sort: "created_at", direction: "asc" });
    const anonymous = new UserApi("", "api_key_session");
    await anonymous.createPortalSession(false);
    expect(fetchMock).toHaveBeenCalled();
  });

  it("rejects malformed recall payloads", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: { evidences: [] } }), { status: 200 })));
    await expect(new UserApi("token").recall("malformed")).rejects.toMatchObject({ status: 500 });
  });

  it("covers the team dreaming form overrides and save/error paths", async () => {
    const save = vi.fn(async () => undefined);
    const user = userEvent.setup();
    render(<TeamDreamingConfigForm config={{ dreaming: { enabled: "true", stale: true } }} effective={{ enabled: true, force_enabled: false, start_time_local: "03:00", timezone: "UTC", max_outputs: 5, team_enabled: true, source: "global_force" }} onSave={save} />);
    expect(screen.getByText("Global force")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear Scheduled cycle override" }));
    await user.click(screen.getByRole("button", { name: "Save dreaming" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith({ dreaming: { stale: true } }));
    expect(screen.getByRole("status")).toHaveTextContent("Saved");

    save.mockRejectedValueOnce(new Error("save failed"));
    await user.click(screen.getByRole("button", { name: "Save dreaming" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("save failed");
  });

  it("covers shared UI fallbacks and dream presentation branches", async () => {
    expect(runOutcome({})).toBe("-");
    expect(runOutcome({ outcome_summary: { provider_failed: 1 } })).toBe("Provider call failed");
    expect(runOutcome({ lane: "evidence_discovery", evidence_targets: 0, outcome_summary: {} })).toBe("No eligible evidence target");
    expect(runOutcome({ lane: "evidence_discovery", status: "completed", evidence_targets: 1, outcome_summary: { created_hypotheses: 0 } })).toBe("Provider returned no supported relationship");
    expect(runOutcome({ lane: "evidence_discovery", status: "completed", evidence_targets: 1, outcome_summary: { created_hypotheses: 1 } })).toBe("Evidence discovery stored");
    expect(runOutcome({ lane: "evidence_discovery", evidence_targets: 2, evaluated_evidence_targets: 1, outcome_summary: { provider_proposals: 1 } })).toBe("1 of 4 evidence target passes evaluated");
    expect(runOutcome({ attempted_paths: 0, outcome_summary: { blocked_targets: 1 } })).toBe("1 target already exist");
    expect(runOutcome({ attempted_paths: 0, outcome_summary: { blocked_targets: 2 } })).toBe("2 targets already exist");
    expect(runOutcome({ attempted_paths: 0, outcome_summary: { previously_assessed_paths: 1 } })).toBe("All paths already assessed");
    expect(runOutcome({ attempted_paths: 1, outcome_summary: { policy_rejections: 1 } })).toBe("1 blocked during persistence");
    expect(runOutcome({ attempted_paths: 1, outcome_summary: { provider_proposals: 0 } })).toBe("Provider returned no supported relationship");
    expect(runOutcome({ attempted_paths: 1, outcome_summary: { provider_proposals: 1 } })).toBe("Provider result stored");
    expect(runStatusClass("failed")).toBe("status-pill error");
    expect(runStatusClass("skipped")).toBe("status-pill warning");
    expect(runStatusClass("completed")).toBe("status-pill");

    const input = document.createElement("input");
    const execCommand = vi.fn(() => true);
    Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
    const clipboard = navigator.clipboard;
    vi.spyOn(clipboard, "writeText").mockRejectedValueOnce(new Error("clipboard denied"));
    await expect(writeClipboardText("secret", input)).resolves.toBe(true);
    expect(execCommand).toHaveBeenCalledWith("copy");
    vi.spyOn(clipboard, "writeText").mockRejectedValueOnce(new Error("clipboard denied"));
    execCommand.mockImplementationOnce(() => { throw new Error("copy failed"); });
    await expect(writeClipboardText("secret", input)).resolves.toBe(false);
    vi.spyOn(clipboard, "writeText").mockRejectedValueOnce(new Error("clipboard denied"));
    await expect(writeClipboardText("secret", null)).resolves.toBe(false);

    const user = userEvent.setup();
    render(<>
      <SecretBox value=" secret " valueLabel="Secret" copyLabel="Copy secret" dismissLabel="Dismiss secret" onDismiss={vi.fn()} />
      <InfoTooltip label="Meaning">Details</InfoTooltip>
      <DreamEvidenceSummary dream={{ hypothesis: "No evidence" }} />
      <DreamEvidenceSummary dream={{ hypothesis: "Evidence", derivations: [{ premise_position: 1, relationship_id: "r", relationship_version: 1, quote: "quote", authority: "owner" }], evidence_derivations: [{ evidence_id: "e", source_group_key: "g", span_start: 0, span_end: 5, quote: "excerpt", authority: "owner" }] }} />
      <MetricLabel label="Metric" detail="Meaning" />
      <PortalShell theme="dark" title="Portal" icon={<span>icon</span>} topbarActions={<span>actions</span>} navLabel="Navigation" navItemsLabel="Sections" navPlacement="top" contextBar={<span>context</span>} resourceRail={<span>resources</span>} detailLabel="Details" error="portal error" navItems={[{ id: "one", label: "One", icon: <span>one</span>, active: true, onClick: vi.fn() }, { id: "two", label: "Two", icon: <span>two</span>, active: false, disabled: true, onClick: vi.fn() }]}><span>content</span></PortalShell>
    </>);
    expect(screen.getByRole("alert")).toHaveTextContent("portal error");
    expect(screen.getAllByRole("tooltip").some((tooltip) => tooltip.textContent === "Details")).toBe(true);
    expect(screen.getByText("No cited excerpts")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy secret" }));
    expect(await screen.findByRole("button", { name: "Copy secret" })).toBeInTheDocument();
  });

  it("covers security loading, filtering, creation, refresh, and deletion", async () => {
    const api = {
      getSecuritySettings: vi.fn(async () => ({ enabled: true, failure_threshold: 3, failure_window_seconds: 60, ban_duration_seconds: 0, updated_at: "2026-09-01T00:00:00Z" })),
      listSecurityBans: vi.fn(async () => ({ data: [{ ip: "192.0.2.1", reason: "manual", source: "manual", failure_count: 2, banned_at: "2026-09-01T00:00:00Z", expires_at: null, last_failed_at: null, metadata: {}, revoked_at: null }], pagination: { limit: 25, offset: 0, total: 1 } })),
      createSecurityBan: vi.fn(async () => ({})),
      deleteSecurityBan: vi.fn(async () => ({ status: "ok" })),
    } as unknown as ControlApi;
    const user = userEvent.setup();
    render(<SecurityPanel api={api} />);
    expect(await screen.findByText("192.0.2.1")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Add ban" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("IP address is required");
    await user.type(screen.getByLabelText("IP address"), "198.51.100.4");
    await user.type(screen.getByLabelText("Reason"), "test");
    await user.click(screen.getByRole("button", { name: "Add ban" }));
    await waitFor(() => expect(api.createSecurityBan).toHaveBeenCalled());
    await user.click(screen.getByLabelText("Include expired"));
    await waitFor(() => expect(api.listSecurityBans).toHaveBeenLastCalledWith(true));
    await user.click(screen.getByRole("button", { name: "Clear IP ban and reset strikes for 192.0.2.1" }));
    await waitFor(() => expect(api.deleteSecurityBan).toHaveBeenCalledWith("192.0.2.1"));
  });

  it("covers directory connector creation, preview activation, credentials, and admin groups", async () => {
    const connector: DirectoryConnector = {
      id: "connector-1", provider_id: provider.id, status: "disabled", group_pattern: "^group$",
      role_entitlements: { Member: { role: "member", scopes: ["read"] } }, max_auto_teams: 10,
      credential_version: 1, scim_path: "/scim/connector-1", last_activation_at: null,
      created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z",
    };
    const preview: DirectoryPreview = {
      version: "preview-1",
      candidates: [{ group_id: "group-1", external_id: "external-1", display_name: "Member group", team_id: team.id, team_name: team.name, entitlement: { role: "member", scopes: ["read"] }, binding_origin: "directory_created" }],
      issues: [{ kind: "warning", detail: "one issue", active: true }],
    };
    const createdCredential = { connector_id: connector.id, credential_version: 1, bearer_token: "bearer", oauth_client_id: "oauth-id", oauth_client_secret: "oauth-secret" };
    let current = { ...connector };
    const api = {
      getDirectoryConnector: vi.fn(async () => { throw new ApiError(404, "not found"); }),
      listControlAdminGroups: vi.fn(async () => []),
      createDirectoryConnector: vi.fn(async () => ({ connector: current, credential: createdCredential })),
      updateDirectoryConnector: vi.fn(async () => current),
      previewDirectoryConnector: vi.fn(async () => preview),
      setDirectoryConnectorStatus: vi.fn(async (_id: string, status: DirectoryConnector["status"]) => { current = { ...current, status }; return current; }),
      rotateDirectoryCredentials: vi.fn(async () => ({ ...createdCredential, credential_version: 2 })),
      createControlAdminGroup: vi.fn(async (_id: string, input: { group_id: string; group_name: string; enabled: boolean }) => ({ id: "admin-1", provider_id: provider.id, ...input, retired_at: null, created_at: "", updated_at: "" })),
      deleteControlAdminGroup: vi.fn(async () => ({ status: "ok" })),
      adoptDirectoryGroupTeam: vi.fn(async () => ({ status: "ok" })),
    } as unknown as ControlApi;
    const user = userEvent.setup();
    render(<DirectoryAutomationPanel api={api} provider={provider} teams={[team]} />);
    expect(await screen.findByRole("button", { name: "Create connector" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create connector" }));
    expect(await screen.findByText(/Connector created disabled/)).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "Dismiss directory credentials" })[0]);
    await user.type(screen.getByLabelText("Entra group ID"), "admin-group");
    await user.type(screen.getByLabelText("Display name"), "Admins");
    await user.click(screen.getByRole("button", { name: "Add control admin group" }));
    expect(await screen.findByText("Control-admin group added.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Start observe" }));
    await user.click(await screen.findByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Member group")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Activate preview" }));
    await waitFor(() => expect(api.setDirectoryConnectorStatus).toHaveBeenCalledWith(connector.id, "active", "preview-1"));
    await user.click(screen.getByRole("button", { name: "Rotate credentials" }));
    expect(await screen.findByText(/Directory credentials rotated/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Adopt team/ }));
    await waitFor(() => expect(api.adoptDirectoryGroupTeam).toHaveBeenCalledWith(connector.id, "group-1", team.id));
    await user.click(screen.getByRole("button", { name: /Remove Admins from control access/ }));
    await waitFor(() => expect(api.deleteControlAdminGroup).toHaveBeenCalledWith(provider.id, "admin-1"));
  });

  it("covers directory validation, disabled actions, and operation errors", async () => {
    const connector: DirectoryConnector = {
      id: "connector-errors", provider_id: provider.id, status: "observe", group_pattern: "^group$",
      role_entitlements: { Member: { role: "member", scopes: ["read"] } }, max_auto_teams: 2,
      credential_version: 1, scim_path: "/scim/connector-errors", last_activation_at: null,
      created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z",
    };
    const admin = { id: "admin-1", provider_id: provider.id, group_id: "admins", group_name: "Admins", enabled: false, retired_at: null, created_at: "", updated_at: "" };
    const api = {
      getDirectoryConnector: vi.fn(async () => connector), listControlAdminGroups: vi.fn(async () => [admin]),
      updateDirectoryConnector: vi.fn(async () => { throw new Error("policy failed"); }),
      previewDirectoryConnector: vi.fn(async () => { throw new Error("preview failed"); }),
      rotateDirectoryCredentials: vi.fn(async () => { throw new Error("rotation failed"); }),
      setDirectoryConnectorStatus: vi.fn(), deleteControlAdminGroup: vi.fn(),
    } as unknown as ControlApi;
    const user = userEvent.setup();
    render(<DirectoryAutomationPanel api={api} provider={provider} teams={[]} />);
    expect(await screen.findByText("Admins")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("preview failed");
    vi.mocked(window.confirm).mockReturnValue(false);
    await user.click(screen.getByRole("button", { name: "Disable" }));
    await user.click(screen.getByRole("button", { name: "Rotate credentials" }));
    expect(api.setDirectoryConnectorStatus).not.toHaveBeenCalled();
    expect(api.rotateDirectoryCredentials).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Add control admin group" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("group ID is required");
    await user.clear(screen.getByLabelText("Role entitlements (JSON)"));
    await user.type(screen.getByLabelText("Role entitlements (JSON)"), "not-json");
    await user.click(screen.getByRole("button", { name: "Save directory policy" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("valid JSON");
    await user.clear(screen.getByLabelText("Role entitlements (JSON)"));
    fireEvent.change(screen.getByLabelText("Role entitlements (JSON)"), { target: { value: "{}" } });
    await user.clear(screen.getByLabelText("Maximum auto-created teams"));
    await user.type(screen.getByLabelText("Maximum auto-created teams"), "no");
    await user.click(screen.getByRole("button", { name: "Save directory policy" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("must be an integer");
    await user.clear(screen.getByLabelText("Maximum auto-created teams"));
    await user.type(screen.getByLabelText("Maximum auto-created teams"), "1");
    await user.click(screen.getByRole("button", { name: "Save directory policy" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("policy failed");
  });

  it("covers team management validation and member credential failures", async () => {
    const member = { id: "member-key", team_id: team.id, name: "Member key", key_suffix: "xyz", scopes: ["read", "write"], role: "member", rate_limit: 60, last_used_at: null, expires_at: null, created_at: "2026-09-01T00:00:00Z", memory_binding: "shared_only", memory_space_kind: "team_shared" };
    const session: UserSession = { mcp_public_base_url: "", team: { id: team.id, name: team.name, description: "desc", created_at: team.created_at, updated_at: team.updated_at }, membership: { team_id: team.id, name: "Manager", grants: ["read", "write"], role: "manager" }, credential: { ...member, id: "manager-key", name: "Manager key", role: "manager" }, teams: [], personal_credentials: [] };
    const api = {
      listTeamCredentials: vi.fn(async () => ({ data: [member], pagination: { limit: 25, offset: 0, total: 1 } })),
      updateTeam: vi.fn(async () => { throw new Error("team update failed"); }),
      createTeamCredential: vi.fn(async () => { throw new Error("credential create failed"); }),
      updateTeamCredential: vi.fn(async () => { throw new Error("credential update failed"); }),
      rotateTeamCredential: vi.fn(async () => { throw new Error("credential rotate failed"); }),
      deleteTeamCredential: vi.fn(async () => { throw new Error("credential delete failed"); }),
    } as unknown as UserApi;
    const user = userEvent.setup();
    render(<TeamManagementPanel api={api} session={session} onTeamUpdated={vi.fn()} />);
    expect(await screen.findByDisplayValue("Member key")).toBeInTheDocument();
    const teamName = screen.getByLabelText("Name", { selector: "#user-team-name" });
    await user.clear(teamName);
    await user.type(teamName, "ab");
    await user.click(screen.getByRole("button", { name: "Save team" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at least 3");
    await user.clear(teamName);
    await user.type(teamName, "Updated");
    await user.click(screen.getByRole("button", { name: "Save team" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("team update failed");

    const createForm = screen.getByRole("button", { name: "Create member credential" }).closest("form") as HTMLElement;
    const createName = within(createForm).getByLabelText("Credential name");
    await user.clear(createName);
    await user.click(within(createForm).getByRole("button", { name: "Create member credential" }));
    expect(await within(createForm).findByRole("alert")).toHaveTextContent("Credential name is required");
    await user.type(createName, "new");
    await user.clear(within(createForm).getByLabelText("Rate limit"));
    await user.type(within(createForm).getByLabelText("Rate limit"), "0");
    await user.click(within(createForm).getByRole("button", { name: "Create member credential" }));
    expect(await within(createForm).findByRole("alert")).toHaveTextContent("Rate limit must be greater");
    await user.clear(within(createForm).getByLabelText("Rate limit"));
    await user.type(within(createForm).getByLabelText("Rate limit"), "10");
    await user.click(within(createForm).getByRole("button", { name: "Create member credential" }));
    expect(await within(createForm).findByRole("alert")).toHaveTextContent("credential create failed");

    const row = screen.getByDisplayValue("Member key").closest("tr") as HTMLElement;
    const rowName = within(row).getByLabelText("Credential name Member key");
    await user.clear(rowName);
    await user.type(rowName, "Renamed");
    await user.click(within(row).getByRole("button", { name: "Save credential Member key" }));
    expect(await screen.findByText("credential update failed")).toBeInTheDocument();
    await user.click(within(row).getByLabelText("Recall feedback"));
    expect(await screen.findByText("credential update failed")).toBeInTheDocument();
    vi.mocked(window.confirm).mockReturnValue(false);
    await user.click(within(row).getByRole("button", { name: /Regenerate API key/ }));
    await user.click(within(row).getByRole("button", { name: /Delete credential/ }));
    vi.mocked(window.confirm).mockReturnValue(true);
    await user.click(within(row).getByRole("button", { name: /Regenerate API key/ }));
    expect(await screen.findByText("credential rotate failed")).toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: /Delete credential/ }));
    expect(await screen.findByText("credential delete failed")).toBeInTheDocument();
  });

  it("covers conflict queue pagination, evidence review, and bounded errors", async () => {
    const position = { position_id: "position-1", position_key: "subject|uses|object", disposition: "open", supporter_count: 2, supporters_truncated: true, supporters: [{ profile_id: "profile-1", profile_name: "Owner", strongest_authority: "owner", accepted_at: "2026-09-01T00:00:00Z" }] };
    const queue = { summary: { open_count: 1, overdue_count: 1, active_lease_count: 1, expired_lease_count: 0, failed_assessment_count_24h: 1, lww_resolution_count_24h: 0, pending_derived_task_count: 1, failed_derived_task_count: 0, oldest_open_age_seconds: 90, oldest_overdue_age_seconds: 172800, collected_at: "2026-09-01T00:00:00Z" }, items: [{ conflict_id: "conflict-1", version: 2, status: "overdue", question: "Which value?", question_truncated: true, predicate_key: "uses", predicate_key_truncated: true, positions_truncated: true, review_due_at: "2020-01-01T00:00:00Z", next_review_at: "2020-01-01T00:00:00Z", created_at: "2026-08-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", attempt_count: 2, lease_state: "active", lease_until: "2026-09-01T01:00:00Z", last_failure_class: "provider", positions: [position] }], next_cursor: "cursor-2" };
    const evidencePosition = { position_id: "ep-1", evidence_id: "e1", occurrence_id: "o1", quote: "Quoted evidence", span_start: 0, span_end: 10, authority: "owner", submitted: true, created_at: "2026-09-01T00:00:00Z" };
    const evidence = { team_id: team.id, conflict_id: "e-conflict", space_id: "space", space_generation: 1, kind: "evidence_conflict", status: "open", version: 1, created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", positions: [evidencePosition], events: [{ event_id: "event-1", conflict_id: "e-conflict", ordinal: 1, action: "opened", status_after: "open", case_version: 1, actor_kind: "system", reason: "initial", citation_snapshot: [evidencePosition], created_at: "2026-09-01T00:00:00Z" }] };
    const detail = { conflict: evidence, next_event_cursor: "event-cursor" };
    const older = { conflict: { ...evidence, events: [] }, next_event_cursor: null };
    const api = {
      getConflictQueue: vi.fn(async () => queue),
      getTelemetry: vi.fn(async () => ({ available: true, current_cards: [{ id: "conflict_queue_collection_success", label: "collector", unit: "state", value: 0, available: true }] })),
      listEvidenceConflicts: vi.fn(async () => ({ items: [evidence], next_cursor: "evidence-cursor" })),
      getEvidenceConflict: vi.fn(async (_team: string, _id: string, _limit?: number, cursor?: string) => cursor ? older : detail),
      resolveEvidenceConflict: vi.fn(async () => ({ conflict: evidence })),
    } as unknown as ControlApi;
    const user = userEvent.setup();
    const view = render(<ConflictQueuePanel api={api} team={team} />);
    expect(await screen.findByText("Which value?")).toBeInTheDocument();
    expect(screen.getByText(/Queue collector is degraded/)).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Show"), "overdue");
    await user.selectOptions(screen.getByLabelText("Rows"), "50");
    await user.click(screen.getByRole("button", { name: "Next" }));
    await user.click(screen.getByRole("button", { name: "Previous" }));
    await user.click(screen.getByRole("button", { name: "Refresh conflict queue" }));
    await user.click(screen.getByRole("tab", { name: "Evidence" }));
    await user.click(await screen.findByRole("button", { name: /1 cited positions/ }));
    await user.click(screen.getByText(/Review history/));
    await user.click(screen.getByRole("button", { name: "Load older history" }));
    await waitFor(() => expect(api.getEvidenceConflict).toHaveBeenLastCalledWith(team.id, "e-conflict", 50, "event-cursor"));
    await user.type(screen.getByLabelText("Review reason"), "reviewed");
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    await waitFor(() => expect(api.resolveEvidenceConflict).toHaveBeenCalledWith(team.id, "e-conflict", expect.objectContaining({ decision: "resolve", reason: "reviewed" })));
    await user.selectOptions(screen.getByLabelText("Show"), "resolved");

    view.unmount();
    const notFoundApi = { getConflictQueue: vi.fn(async () => { throw new ApiError(404, "not found"); }), getTelemetry: vi.fn(async () => ({ available: true, current_cards: [] })) } as unknown as ControlApi;
    render(<ConflictQueuePanel api={notFoundApi} team={team} />);
    expect(await screen.findByText("Team queue not found.")).toBeInTheDocument();
  });

  it("covers control auth discovery, theme switching, and token validation", async () => {
    sessionStorage.clear();
    localStorage.clear();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/control/auth/providers") {
        return new Response(JSON.stringify({ data: [{ id: provider.id, name: provider.name, kind: provider.kind }] }), { status: 200 });
      }
      return new Response(JSON.stringify({ message: "invalid control token" }), { status: 401, statusText: "Unauthorized" });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<App />);
    expect(await screen.findByRole("button", { name: "Sign in with Smoke OIDC" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));
    expect(localStorage.getItem("denseMem.controlTheme")).toBe("dark");
    await user.click(screen.getByRole("button", { name: "Unlock" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Token is required");
    await user.type(screen.getByLabelText("Control token"), "bad");
    await user.click(screen.getByRole("button", { name: "Unlock" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("invalid control token");
  });

  it("covers SSO provider and group mapping CRUD controls", async () => {
    const mapping = { id: "mapping-1", provider_id: provider.id, team_id: team.id, team_name: team.name, group_id: "group-1", group_name: "Group", scopes: ["read"], role: "member", enabled: false, origin: "manual", retired_at: null, created_at: "", updated_at: "" };
    const api = {
      listSSOProviders: vi.fn(async () => [provider]), listSSOGroupMappings: vi.fn(async () => [mapping]),
      createSSOProvider: vi.fn(async (input) => ({ ...provider, ...input })), updateSSOProvider: vi.fn(async (_id, input) => ({ ...provider, ...input })), deleteSSOProvider: vi.fn(async () => ({ status: "ok" })),
      createSSOGroupMapping: vi.fn(async () => mapping), deleteSSOGroupMapping: vi.fn(async () => ({ status: "ok" })),
      getDirectoryConnector: vi.fn(async () => { throw new ApiError(404, "not found"); }), listControlAdminGroups: vi.fn(async () => []),
    } as unknown as ControlApi;
    const user = userEvent.setup();
    render(<SSOPanel api={api} teams={[team]} />);
    expect(await screen.findByText("Smoke OIDC")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "New" }));
    await user.type(screen.getByLabelText("Name"), "New provider");
    await user.selectOptions(screen.getByLabelText("Kind"), "pingone");
    await user.selectOptions(screen.getByLabelText("JWKS source"), "static");
    await user.click(screen.getByLabelText("Accept OAuth JWTs on MCP"));
    await user.click(screen.getByRole("button", { name: "Save provider" }));
    await waitFor(() => expect(api.createSSOProvider).toHaveBeenCalled());
    await user.click(screen.getByRole("button", { name: "Edit Smoke OIDC" }));
    await user.selectOptions(screen.getByLabelText("Role"), "manager");
    await user.click(screen.getByLabelText("Recall feedback access"));
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(api.createSSOGroupMapping).toHaveBeenCalled());
    await user.click(screen.getByRole("button", { name: "Delete group mapping" }));
    await waitFor(() => expect(api.deleteSSOGroupMapping).toHaveBeenCalledWith(provider.id, mapping.id));
    await user.click(screen.getByRole("button", { name: "Refresh SSO providers" }));
    await user.click(screen.getByRole("button", { name: "Delete Smoke OIDC" }));
    await waitFor(() => expect(api.deleteSSOProvider).toHaveBeenCalledWith(provider.id));
  });

  it("covers SSO load and save failures with provider form defaults", async () => {
    const api = {
      listSSOProviders: vi.fn(async () => [provider]),
      listSSOGroupMappings: vi.fn(async () => { throw new Error("mapping load failed"); }),
      createSSOProvider: vi.fn(async () => { throw new Error("provider save failed"); }),
      updateSSOProvider: vi.fn(), deleteSSOProvider: vi.fn(),
      getDirectoryConnector: vi.fn(async () => { throw new ApiError(404, "not found"); }),
      listControlAdminGroups: vi.fn(async () => []),
    } as unknown as ControlApi;
    const user = userEvent.setup();
    render(<SSOPanel api={api} teams={[team]} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("mapping load failed");
    await user.click(screen.getByRole("button", { name: "New" }));
    await user.selectOptions(screen.getByLabelText("Kind"), "azure_ad");
    expect(screen.getByLabelText("Stable identity claim")).toHaveValue("oid");
    await user.click(screen.getByLabelText("Enabled"));
    await user.click(screen.getByRole("button", { name: "Save provider" }));
    expect(await screen.findByText("provider save failed")).toBeInTheDocument();
    vi.mocked(window.confirm).mockReturnValue(false);
    await user.click(screen.getByRole("button", { name: "Delete Smoke OIDC" }));
    expect(api.deleteSSOProvider).not.toHaveBeenCalled();
  });

  it("covers graph validation, node detail, controls, and request errors", async () => {
    const snapshot: GraphSnapshot = { scope: "overview", depth: 2, limit: 80, truncated: true, nodes: [{ key: "entity:e1", id: "e1", type: "entity", title: "Entity" }, { key: "value:v1", id: "v1", type: "value", title: "Value" }], edges: ["PROMOTES_TO", "SUPPORTED_BY", "CONTRADICTS", "SUPERSEDED_BY", "OVERLAYS", "ALIGNS_WITH", "DREAMS_FROM", "uses"].map((relationship, index) => ({ id: `edge-${index}`, source: "entity:e1", target: "value:v1", relationship, directed: true })) };
    const api = { graph: vi.fn(async () => snapshot), nodeDetail: vi.fn(async (_type: string, id: string) => ({ key: `${id}-detail`, id, type: "entity", title: `${id} detail`, body: "body" })) } as unknown as UserApi;
    const user = userEvent.setup();
    render(<GraphPanel api={api} />);
    expect(await screen.findByText("Entity")).toBeInTheDocument();
    await user.click(within(screen.getByTestId("force-graph")).getByRole("button", { name: "Entity" }));
    await user.click(screen.getByLabelText("Value"));
    await user.click(screen.getByLabelText("Entity"));
    await user.click(screen.getByLabelText("Value"));
    await user.click(screen.getByRole("button", { name: "Local" }));
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Anchor ID is required");
    await user.type(screen.getByLabelText("Anchor ID"), "e1");
    fireEvent.change(screen.getByLabelText("Node size"), { target: { value: "9" } });
    fireEvent.change(screen.getByLabelText("Link distance"), { target: { value: "180" } });
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    await user.click(screen.getByLabelText("Entity"));
    await waitFor(() => expect(api.nodeDetail).toHaveBeenCalled());
    await user.clear(screen.getByLabelText("Relationship limit"));
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("positive integer");
  });

  it("covers graph preview empty, success, and request error states", async () => {
    const snapshot: GraphSnapshot = { scope: "local", depth: 2, limit: 48, truncated: false, anchor: { key: "entity:e1", id: "e1", type: "entity" }, nodes: [{ key: "entity:e1", id: "e1", type: "entity", title: "Anchor" }], edges: [] };
    const api = { graph: vi.fn(async () => snapshot) } as unknown as UserApi;
    const view = render(<ResultGraphPreview api={api} anchor={null} />);
    expect(screen.getByText("No graph anchor")).toBeInTheDocument();
    view.rerender(<ResultGraphPreview api={api} anchor={{ type: "entity", id: "e1" }} />);
    expect(await screen.findByText("1 nodes")).toBeInTheDocument();
    expect(api.graph).toHaveBeenCalledWith(expect.objectContaining({ scope: "local", anchorId: "e1" }));
    const failing = { graph: vi.fn(async () => { throw new Error("graph unavailable"); }) } as unknown as UserApi;
    view.rerender(<ResultGraphPreview api={failing} anchor={{ type: "entity", id: "e2" }} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("graph unavailable");
  });

  it("covers search filters, inspector tabs, keyboard selection, and recall failure", async () => {
    const evidence = { evidence_id: "e1", context: "Evidence context", source_type: "manual", created_at: "2026-08-01T00:00:00Z", status: "active", rank: 1 };
    const relationship = { relationship_id: "r1", tier: "verified", search_state: "current", subject: { name: "Subject", entity_id: "e1" }, predicate: "uses", object: { name: "Postgres", entity_id: "e2" }, valid_from: "2026-08-02T00:00:00Z", polarity: "+" };
    const provisional = { ...relationship, relationship_id: "r2", tier: "candidate", search_state: "pending", valid_from: "2026-08-03T00:00:00Z" };
    const disputed = { ...relationship, relationship_id: "r3", search_state: "failed", valid_from: "2026-08-04T00:00:00Z" };
    const community = { community_id: "c1", logical_community_id: "logical-c1", rank: 1, summary: "Community summary", entity_count: 2, relationship_count: 1, relationships_truncated: true, top_entities: [{ name: "Subject" }], top_predicates: ["uses"], relationships: [relationship] };
    const hits: RecallHit[] = [{ evidence, evidences: [evidence], tier: "evidence", final_score: 0.9 }, { relationship, relationships: [relationship], tier: "verified", final_score: 0.8 }, { relationship: provisional, relationships: [provisional], tier: "candidate", final_score: 0.75 }, { relationship: disputed, relationships: [disputed], tier: "verified", final_score: 0.7 }, { community, tier: "community", final_score: 0.6 }];
    const recall = vi.fn(async () => hits);
    const api = { recall, graph: vi.fn(async () => ({ scope: "local", depth: 2, limit: 48, truncated: false, nodes: [], edges: [] })), nodeDetail: vi.fn() } as unknown as UserApi;
    const user = userEvent.setup();
    render(<SearchPanel api={api} />);
    await user.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Query is required");
    await user.type(screen.getByLabelText("Keyword"), "knowledge");
    await user.click(screen.getByRole("button", { name: "Search" }));
    expect((await screen.findAllByText("Evidence context")).length).toBeGreaterThan(0);
    const filters = screen.getByLabelText("Knowledge filters");
    const typeFilters = within(filters).getByRole("group", { name: "Type" });
    await user.click(within(typeFilters).getAllByRole("checkbox")[2]);
    await user.click(screen.getByLabelText("Date range"));
    await user.selectOptions(screen.getByLabelText("Date range"), "custom");
    await user.type(screen.getByLabelText("Start date"), "2026-01-01");
    await user.type(screen.getByLabelText("End date"), "2026-12-31");
    await user.click(screen.getByRole("button", { name: /Sort by date/ }));
    await user.click(screen.getByRole("button", { name: "Use compact density" }));
    const list = screen.getByRole("listbox", { name: "Recall result list" });
    const option = within(list).getAllByRole("option")[0];
    option.focus();
    fireEvent.keyDown(option, { key: "Enter" });
    await user.click(screen.getByRole("tab", { name: "Lineage" }));
    await user.click(screen.getByRole("tab", { name: "Recall" }));
    await user.click(screen.getByRole("tab", { name: "Graph" }));
    await user.click(screen.getByRole("button", { name: "Close details panel" }));
    await user.click(screen.getByRole("button", { name: "Open details" }));
    const statusFilters = within(filters).getByRole("group", { name: "Status" });
    await user.click(within(statusFilters).getAllByRole("checkbox")[1]);
    await user.click(within(statusFilters).getAllByRole("checkbox")[2]);
    await user.selectOptions(screen.getByLabelText("Source"), "manual");
    await user.click(screen.getByRole("button", { name: "Clear all" }));
    await user.selectOptions(screen.getByLabelText("Date range"), "custom");
    await user.clear(screen.getByLabelText("Start date"));
    await user.type(screen.getByLabelText("Start date"), "2027-01-01");
    expect(screen.getByText("No recall results")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear all" }));
    recall.mockRejectedValueOnce(new Error("recall failed"));
    await user.clear(screen.getByLabelText("Keyword"));
    await user.type(screen.getByLabelText("Keyword"), "failed");
    await user.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("recall failed");
  });

  it("covers user dreams sorting, pagination, empty and error states", async () => {
    const dream = { dream_id: "dream-1", team_id: team.id, hypothesis: "A hypothesis", what_if: "", possible_outcome: "", rationale: "Because", likelihood: 0.5, confidence: 0.8, status: "rejected", lane: "evidence_discovery", source_evidence_ids: ["e1"], cycle_run_id: "run-12345678", created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-02T00:00:00Z" };
    const run = { run_id: "run-1", team_id: team.id, run_date: "2026-09-01", started_at: "2026-09-01T00:00:00Z", completed_at: "2026-09-01T00:01:00Z", input_relationships: 1, attempted_paths: 1, provider_proposals: 0, created_dreams: 0, rejected_dreams: 1, status: "failed", lane: "evidence_discovery", evidence_targets: 2, evaluated_evidence_targets: 1, outcome_summary: { provider_failed: 1 } };
    const graphRun = { ...run, run_id: "run-graph", lane: "graph", input_relationships: 0, attempted_paths: 0, provider_proposals: 1, outcome_summary: { blocked_targets: 1 } };
    const listDreams = vi.fn(async () => ({ items: [dream], next_cursor: "next" }));
    const api = { dreamingStatus: vi.fn(async () => ({ effective_config: { enabled: true, force_enabled: true, start_time_local: "03:00", timezone: "UTC", max_outputs: 5, team_enabled: true, source: "global_force" }, latest_run: run, pending_count: 2 })), listDreamingRuns: vi.fn(async () => [run, graphRun]), listDreams } as unknown as UserApi;
    const user = userEvent.setup();
    render(<UserDreamsPanel api={api} />);
    expect(await screen.findByText("A hypothesis")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Sort"), "created_at");
    await user.selectOptions(screen.getByLabelText("Direction"), "asc");
    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(api.listDreams).toHaveBeenCalledTimes(4));
    await user.click(screen.getByRole("button", { name: "Previous" }));
    await user.selectOptions(screen.getByLabelText("Rows"), "10");
    await user.selectOptions(screen.getByLabelText("Status"), "rejected");
    await user.click(screen.getByRole("button", { name: "Refresh dreams" }));
    expect(screen.getByText("Evidence discovery")).toBeInTheDocument();
    listDreams.mockResolvedValueOnce({ items: [], next_cursor: "" });
    await user.selectOptions(screen.getByLabelText("Status"), "submitted");
    expect(await screen.findByText("No dreams")).toBeInTheDocument();
    const failingApi = { dreamingStatus: vi.fn(async () => { throw new Error("dreams unavailable"); }), listDreamingRuns: vi.fn(async () => []), listDreams: vi.fn(async () => ({ items: [], next_cursor: "" })) } as unknown as UserApi;
    const view = render(<UserDreamsPanel api={failingApi} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("dreams unavailable");
    view.unmount();
  });

  it("covers control dreams empty and error states", async () => {
    const emptyApi = { getTeamDreamingStatus: vi.fn(async () => ({ effective_config: { enabled: false, force_enabled: false, start_time_local: "03:00", timezone: "UTC", max_outputs: 5, team_enabled: false, source: "team" }, latest_run: null, pending_count: 0 })), listTeamDreamingRuns: vi.fn(async () => []), listTeamDreams: vi.fn(async () => ({ items: [], next_cursor: "" })) } as unknown as ControlApi;
    const view = render(<ControlDreamsPanel api={emptyApi} team={team} />);
    expect(await screen.findByText("No dreams")).toBeInTheDocument();
    expect(screen.getByText("No runs")).toBeInTheDocument();
    view.unmount();
    const statusDream = (status: string) => ({ dream_id: `dream-${status}`, team_id: team.id, hypothesis: status, what_if: "", possible_outcome: "", rationale: "", likelihood: 0.1, confidence: 0.1, status, created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z" });
    const statusApi = { getTeamDreamingStatus: vi.fn(async () => ({ effective_config: { enabled: true, force_enabled: false, start_time_local: "03:00", timezone: "UTC", max_outputs: 5, team_enabled: true, source: "global" }, latest_run: null, pending_count: 0 })), listTeamDreamingRuns: vi.fn(async () => []), listTeamDreams: vi.fn(async () => ({ items: [statusDream("reinforced"), statusDream("submitted"), statusDream("stale"), statusDream("proposed")], next_cursor: "" })) } as unknown as ControlApi;
    render(<ControlDreamsPanel api={statusApi} team={team} />);
    expect(await screen.findByText("reinforced")).toBeInTheDocument();
    expect(screen.getByText("submitted", { selector: "strong" })).toBeInTheDocument();
    const failingApi = { getTeamDreamingStatus: vi.fn(async () => { throw new Error("control dreams failed"); }), listTeamDreamingRuns: vi.fn(async () => []), listTeamDreams: vi.fn(async () => ({ items: [], next_cursor: "" })) } as unknown as ControlApi;
    render(<ControlDreamsPanel api={failingApi} team={team} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("control dreams failed");
  });

  it("covers runtime config load and save failures", async () => {
    const api = { getGeneralConfig: vi.fn(async () => ({ update_time: "2026-09-01T00:00:00Z", items: [{ key: "APP_TIMEZONE", value: "UTC", effective_value: "UTC", updated_at: "" }] })), updateGeneralConfig: vi.fn(async () => { throw new Error("config save failed"); }) } as unknown as ControlApi;
    const user = userEvent.setup();
    render(<ConfigPanel api={api} />);
    await user.selectOptions(await screen.findByLabelText("Timezone"), "America/New_York");
    await user.click(screen.getByRole("button", { name: "Save config" }));
    expect(await screen.findByText("config save failed")).toBeInTheDocument();
    const failingApi = { getGeneralConfig: vi.fn(async () => { throw new Error("config load failed"); }) } as unknown as ControlApi;
    render(<ConfigPanel api={failingApi} />);
    expect(await screen.findByText("config load failed")).toBeInTheDocument();
  });

  it("covers recall feedback filters and detail resolution", async () => {
    const event = { recall_id: "rec-1", created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T00:00:00Z", feedback_at: "2026-09-01T00:01:00Z", team_id: team.id, profile_id: "profile-1", key_id: "key-1", auth_method: "api_key", tool_name: "recall_memory", query: "why", tool_args: {}, result_refs: [{ type: "evidence", id: "e1", rank: 1, tier: "evidence", status_at_recall: "active" }], result_count: 1, snapshot_state: "captured", used: true, answer_supported: true, quality: "medium", missing_context: false, irrelevant: true, feedback_comment: "comment", irrelevant_result_refs: [{ type: "evidence", id: "e1", rank: 1 }], resolved_results: [{ type: "evidence", id: "e1", rank: 1, resolution_status: "found", current_status: "retracted", current: { content: "current" }, ref: { type: "evidence", id: "e1", rank: 1, tier: "evidence", status_at_recall: "active" } }] };
    const pendingEvent = { ...event, recall_id: "rec-2", profile_id: "", key_id: "", auth_method: "", query: "", snapshot_state: "", result_refs: [], resolved_results: [], used: undefined, answer_supported: undefined, missing_context: undefined, irrelevant: undefined, quality: undefined, feedback_comment: "", irrelevant_result_refs: [] };
    const edgeEvent = { ...pendingEvent, recall_id: "rec-3", result_refs: [{ type: "evidence", id: "missing", rank: 1, status_at_recall: "active" }, { type: "relationship", id: "triple", rank: 2, status_at_recall: "active" }, { type: "value", id: "empty", rank: 3, status_at_recall: "active" }], resolved_results: [
      { type: "evidence", id: "missing", rank: 1, resolution_status: "missing", ref: { type: "evidence", id: "missing", rank: 1, status_at_recall: "active" } },
      { type: "relationship", id: "triple", rank: 2, resolution_status: "found", current: { subject: "Subject", predicate: "uses", object: "Value" }, ref: { type: "relationship", id: "triple", rank: 2, status_at_recall: "active" } },
      { type: "value", id: "empty", rank: 3, resolution_status: "found", current: {}, ref: { type: "value", id: "empty", rank: 3, status_at_recall: "active" } },
    ] };
    const api = { listRecallFeedbackEvents: vi.fn(async () => ({ data: [event, pendingEvent, edgeEvent], pagination: { limit: 1, offset: 0, total: 3 } })), getRecallFeedbackEvent: vi.fn(async (id: string) => id === "rec-2" ? pendingEvent : id === "rec-3" ? edgeEvent : event) } as unknown as ControlApi;
    const user = userEvent.setup();
    const view = render(<RecallFeedbackPanel api={api} teams={[team]} />);
    expect(await screen.findByText("why")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Quality"), "low");
    await user.selectOptions(screen.getByLabelText("Missing context"), "true");
    await user.click(screen.getByLabelText("Include pending"));
    await user.selectOptions(screen.getByLabelText("Irrelevant"), "false");
    await user.clear(screen.getByLabelText("From"));
    await user.type(screen.getByLabelText("From"), "2026-09-01T00:00");
    await user.clear(screen.getByLabelText("To"));
    await user.type(screen.getByLabelText("To"), "2026-09-02T00:00");
    await user.click(screen.getByRole("button", { name: /View recall feedback rec-1/ }));
    expect(await screen.findByText("comment")).toBeInTheDocument();
    expect(screen.getByText("current")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Hide recall feedback rec-1/ }));
    await user.click(screen.getByRole("button", { name: /View recall feedback rec-2/ }));
    expect(await screen.findByText("No result refs")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Hide recall feedback rec-2/ }));
    await user.click(screen.getByRole("button", { name: /View recall feedback rec-3/ }));
    expect(await screen.findByText("Missing from graph")).toBeInTheDocument();
    expect(screen.getByText("Subject uses Value")).toBeInTheDocument();
    expect(screen.getByText("Resolved")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Hide recall feedback rec-3/ }));
    await user.selectOptions(screen.getByLabelText("Rows"), "50");
    await user.click(screen.getByRole("button", { name: "Next" }));
    await user.click(screen.getByRole("button", { name: "Previous" }));
    await waitFor(() => expect(api.listRecallFeedbackEvents).toHaveBeenCalled());

    view.unmount();
    const failingApi = { listRecallFeedbackEvents: vi.fn(async () => { throw new Error("feedback unavailable"); }), getRecallFeedbackEvent: vi.fn() } as unknown as ControlApi;
    render(<RecallFeedbackPanel api={failingApi} teams={[]} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("feedback unavailable");
  });
});
