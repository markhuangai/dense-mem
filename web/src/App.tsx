import { Label } from "./ui/primitives/label";
import { Input } from "./ui/primitives/input";
import { Button } from "./ui/primitives/button";
import { Textarea } from "./ui/primitives/textarea";
import { FormEvent, lazy, ReactNode, Suspense, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  AlertTriangle,
  Ban,
  KeyRound,
  ListFilter,
  LogOut,
  MessageSquare,
  Moon,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Settings,
  ShieldCheck,
  Trash2,
  Users,
} from "lucide-react";
import {
  ControlApi,
  ControlIdentityProvider,
  Team,
  listControlIdentityProviders,
} from "./api";
import type { OperationLogQuery } from "./api";
import { TeamCredentialsPanel } from "./control/TeamCredentialsPanel";
import { TeamOverviewPanel, TeamWorkspaceShell } from "./control/TeamWorkspace";
import type { TeamWorkspaceTab } from "./control/TeamWorkspace";
import { readError } from "./control/utils";
import { TeamDirectory } from "./control/TeamDirectory";
import { WorkspacePicker } from "./ui/workspace-picker";
import { PortalEnvironment, portalHref, usePortalNavigation, useUnsavedChanges } from "./ui/navigation";
import { useConfirmation } from "./ui/confirmation";
import { ThemeControl, usePortalTheme } from "./ui/theme";
import type { ThemeName } from "./ui/theme";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/primitives/dialog";
import { FieldRow, AuthShell, LoadingState, PortalShell, SectionHeading } from "./ui/components";

const SecurityPanel = lazy(() => import("./control/SecurityPanel").then((module) => ({ default: module.SecurityPanel })));
const SSOPanel = lazy(() => import("./control/SSOPanel").then((module) => ({ default: module.SSOPanel })));
const ConfigPanel = lazy(() => import("./control/ConfigPanel").then((module) => ({ default: module.ConfigPanel })));
const OntologyConfigPanel = lazy(() => import("./control/ConfigPanel").then((module) => ({ default: module.OntologyConfigPanel })));
const ControlDreamsPanel = lazy(() => import("./control/DreamsPanel").then((module) => ({ default: module.ControlDreamsPanel })));
const LogsPanel = lazy(() => import("./control/LogsPanel").then((module) => ({ default: module.LogsPanel })));
const RecallFeedbackPanel = lazy(() => import("./control/RecallFeedbackPanel").then((module) => ({ default: module.RecallFeedbackPanel })));
const ConflictQueuePanel = lazy(() => import("./control/ConflictQueuePanel").then((module) => ({ default: module.ConflictQueuePanel })));
const SearchConvergencePanel = lazy(() => import("./control/SearchConvergencePanel").then((module) => ({ default: module.SearchConvergencePanel })));
const RememberAttemptsPanel = lazy(() => import("./control/RememberAttemptsPanel").then((module) => ({ default: module.RememberAttemptsPanel })));

const TOKEN_STORAGE_KEY = "denseMem.controlToken";
const THEME_STORAGE_KEY = "denseMem.controlTheme";

type LoadState = "idle" | "loading" | "error";
type Theme = ThemeName;
type AuthMode = "none" | "token" | "sso";
type PortalTab = "all-teams" | "teams" | "recall-feedback" | "search" | "logs" | "security" | "sso" | "config" | "maintenance";
type RememberLink = { teamID: string; view: "calls" | "attempts"; attemptID: string; invocationID: string };

function normalizedTeamID(value: string | null) {
  return value && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value) ? value.toLowerCase() : "";
}

function readRememberLink(params = new URLSearchParams(window.location.search)): RememberLink {
  const teamID = normalizedTeamID(params.get("team_id"));
  const view = params.get("remember_view") === "calls" || (!params.has("remember_view") && params.has("invocation_id")) ? "calls" : "attempts";
  return {
    teamID,
    view,
    attemptID: params.get("attempt_id") ?? "",
    invocationID: params.get("invocation_id") ?? "",
  };
}

export function App() { return <PortalEnvironment><ControlApp /></PortalEnvironment>; }

function ControlApp() {
  const [token, setToken] = useState(() => sessionStorage.getItem(TOKEN_STORAGE_KEY) ?? "");
  const [draftToken, setDraftToken] = useState(token);
  const [authMode, setAuthMode] = useState<AuthMode>(() => token ? "token" : "none");
  const [ssoProviders, setSSOProviders] = useState<ControlIdentityProvider[]>([]);
  const [authError, setAuthError] = useState("");
  const { theme, preference, setPreference } = usePortalTheme(THEME_STORAGE_KEY);
  const { transition } = usePortalNavigation();
  const themeMenu = <ThemeControl preference={preference} onChange={setPreference} />;

  const api = useMemo(() => {
    if (authMode === "token" && token) {
      return new ControlApi(token);
    }
    if (authMode === "sso") {
      return new ControlApi();
    }
    return null;
  }, [authMode, token]);

  useEffect(() => {
    let cancelled = false;
    void listControlIdentityProviders()
      .then((providers) => {
        if (!cancelled) {
          setSSOProviders(providers);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSSOProviders([]);
        }
      });
    if (!token) {
      void new ControlApi().session()
        .then(() => {
          if (!cancelled) {
            setAuthMode("sso");
          }
        })
        .catch(() => undefined);
    }
    return () => {
      cancelled = true;
    };
  }, [token]);

  async function submitToken(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextToken = draftToken.trim();
    if (!nextToken) {
      setAuthError("Token is required.");
      return;
    }
    const nextApi = new ControlApi(nextToken);
    try {
      await nextApi.session();
      sessionStorage.setItem(TOKEN_STORAGE_KEY, nextToken);
      setToken(nextToken);
      setAuthMode("token");
      setAuthError("");
    } catch (error) {
      setAuthError(error instanceof Error ? error.message : "Authentication failed.");
    }
  }

  async function signOut() {
    if (authMode === "sso") {
      try {
        await new ControlApi().logoutControlSSO();
      } catch {
        setAuthError("Control SSO sign-out could not be confirmed. Clear the browser session before signing in again.");
      }
    }
    sessionStorage.removeItem(TOKEN_STORAGE_KEY);
    setToken("");
    setDraftToken("");
    setAuthMode("none");
  }

  if (!api) {
    return (
      <AuthShell
        theme={theme}
        title="Dense-Mem Control"
        icon={<span className="brand-initials" aria-hidden="true">DM</span>}
        onSubmit={submitToken}
        actions={themeMenu}
      >
        <Label htmlFor="portal-token">Control token</Label>
        <Input
          id="portal-token"
          type="password"
          value={draftToken}
          onChange={(event) => setDraftToken(event.target.value)}
          autoComplete="current-password"
        />
        {authError && <p className="field-error" role="alert">{authError}</p>}
        <Button variant="default" className="primary-button" type="submit">
          <ShieldCheck size={17} aria-hidden="true" />
          Unlock
        </Button>
        {ssoProviders.length > 0 && (
          <div className="button-row">
            {ssoProviders.map((provider) => (
              <Button variant="outline"
                className="ghost-button"
                key={provider.id}
                type="button"
                onClick={() => window.location.assign(`/control/auth/start/${encodeURIComponent(provider.id)}`)}
              >
                <ShieldCheck size={17} aria-hidden="true" />
                Sign in with {provider.name}
              </Button>
            ))}
          </div>
        )}
      </AuthShell>
    );
  }

  return <Portal api={api} theme={theme} themeMenu={themeMenu} onSignOut={() => void transition(signOut)} />;
}

function Portal({ api, theme, themeMenu, onSignOut }: { api: ControlApi; theme: Theme; themeMenu: ReactNode; onSignOut: () => void }) {
  const { parameters, entryIndex, navigate, transition } = usePortalNavigation();
  const sections: PortalTab[] = ["all-teams", "teams", "recall-feedback", "search", "logs", "security", "sso", "config", "maintenance"];
  const requestedSection = parameters.get("section");
  const legacyRemember = parameters.has("remember_view") || parameters.has("attempt_id") || parameters.has("invocation_id");
  const activeTab: PortalTab = sections.includes(requestedSection as PortalTab) ? requestedSection as PortalTab : legacyRemember ? "teams" : "all-teams";
  const workspaceTabs: TeamWorkspaceTab[] = ["overview", "credentials", "remember-attempts", "conflicts", "dreams", "settings"];
  const requestedWorkspace = parameters.get("team_tab");
  const teamWorkspaceTab = workspaceTabs.includes(requestedWorkspace as TeamWorkspaceTab) ? requestedWorkspace as TeamWorkspaceTab : legacyRemember ? "remember-attempts" : "overview";
  const [teams, setTeams] = useState<Team[]>([]);
  const [totalTeams, setTotalTeams] = useState(0);
  const [nextOffset, setNextOffset] = useState(0);
  const linkedTeamID = normalizedTeamID(parameters.get("team_id"));
  const selectedTeamId = parameters.has("team_id") ? linkedTeamID : teams[0]?.id ?? "";
  const selectedTeam = teams.find((team) => team.id === selectedTeamId) ?? null;
  const [creatingTeam, setCreatingTeam] = useState(false);
  const [loadState, setLoadState] = useState<LoadState>("loading");
  const [error, setError] = useState("");
  const [teamEditorResetVersion, setTeamEditorResetVersion] = useState(0);
  const [logsResetVersion, setLogsResetVersion] = useState(0);
  const logsScope = parameters.get("logs_scope");
  const logsCorrelationID = parameters.get("correlation_id");
  const logsInvocationID = parameters.get("invocation_id");
  const logsQuery = useMemo<OperationLogQuery>(() => ({ team_id: logsScope === "all" ? undefined : linkedTeamID || undefined, correlation_id: logsCorrelationID || undefined, invocation_id: logsInvocationID || undefined }), [logsScope, linkedTeamID, logsCorrelationID, logsInvocationID]);
  const loadRequest = useRef(0);
  const teamIDs = useRef(new Set<string>());
  teamIDs.current = new Set(teams.map((team) => team.id));

  async function loadTeams() {
    const request = ++loadRequest.current;
    setLoadState("loading"); setError("");
    try {
      const page = await api.listTeams();
      let visible = page.data;
      const linkedID = linkedTeamID;
      if (linkedID && /^[0-9a-f-]{36}$/i.test(linkedID) && !visible.some((team) => team.id === linkedID)) {
        for (let offset = page.pagination.offset + page.data.length; offset < page.pagination.total; offset += 100) {
          const next = await api.listTeams(100, offset);
          const linked = next.data.find((team) => team.id === linkedID);
          if (linked) { visible = [...visible, linked]; break; }
          if (!next.data.length) break;
        }
      }
      if (request !== loadRequest.current) return;
      setTeams(visible); setTotalTeams(page.pagination.total); setNextOffset(page.pagination.offset + page.data.length); setLoadState("idle");
    } catch (err) {
      if (request === loadRequest.current) { setLoadState("error"); setError(readError(err)); }
    }
  }

  async function loadMoreTeams() {
    if (loadState === "loading") return;
    const request = ++loadRequest.current;
    setLoadState("loading");
    try {
      const page = await api.listTeams(100, nextOffset);
      if (request !== loadRequest.current) return;
      setTeams((current) => [...new Map([...current, ...page.data].map((team) => [team.id, team])).values()]);
      setNextOffset(page.pagination.offset + page.data.length); setTotalTeams(page.pagination.total); setLoadState("idle");
    } catch (err) {
      if (request === loadRequest.current) { setLoadState("error"); setError(readError(err)); }
    }
  }

  useEffect(() => {
    if (!teamIDs.current.size || (linkedTeamID && !teamIDs.current.has(linkedTeamID))) void loadTeams();
    else setLoadState("idle");
    return () => { loadRequest.current += 1; };
  }, [api, linkedTeamID]);

  async function openSection(section: PortalTab) {
    const accepted = await navigate({ section, logs_scope: section === "logs" ? "all" : null, team_tab: null, tab: null, remember_view: null, attempt_id: null, invocation_id: null, correlation_id: null });
    if (accepted && section === "logs") setLogsResetVersion((version) => version + 1);
    return accepted;
  }
  function openTeamWorkspace(tab: TeamWorkspaceTab, teamID = selectedTeamId) {
    return navigate({ section: "teams", logs_scope: null, team_id: teamID, team_tab: tab, tab: null, remember_view: tab === "remember-attempts" ? "attempts" : null, attempt_id: null, invocation_id: null, correlation_id: null });
  }

  const navItems = [
    { id: "all-teams", label: "All teams", group: "Workspace", icon: <Users size={17} aria-hidden="true" />, active: activeTab === "all-teams", href: portalHref({ section: "all-teams", team_tab: null, correlation_id: null, invocation_id: null }), onClick: () => openSection("all-teams") },
    ...([
      ["overview", "Overview", <Activity size={17} aria-hidden="true" />],
      ["credentials", "Credentials", <KeyRound size={17} aria-hidden="true" />],
      ["remember-attempts", "Remember history", <Activity size={17} aria-hidden="true" />],
      ["conflicts", "Conflicts", <AlertTriangle size={17} aria-hidden="true" />],
      ["dreams", "Dream history", <Moon size={17} aria-hidden="true" />],
      ["settings", "Team settings", <Settings size={17} aria-hidden="true" />],
    ] as const).map(([id, label, icon]) => ({ id, label, icon, group: "Team", active: activeTab === "teams" && teamWorkspaceTab === id, disabled: !selectedTeam, href: portalHref({ section: "teams", team_id: selectedTeamId, team_tab: id, correlation_id: null, invocation_id: null }), onClick: () => openTeamWorkspace(id) })),
    ...([
      ["search", "Search readiness", <Search size={17} aria-hidden="true" />],
      ["recall-feedback", "Recall feedback", <MessageSquare size={17} aria-hidden="true" />],
      ["logs", "Logs", <ListFilter size={17} aria-hidden="true" />],
      ["maintenance", "Ontology maintenance", <Activity size={17} aria-hidden="true" />],
      ["sso", "Identity & access", <ShieldCheck size={17} aria-hidden="true" />],
      ["security", "IP bans", <Ban size={17} aria-hidden="true" />],
      ["config", "Settings", <Settings size={17} aria-hidden="true" />],
    ] as const).map(([id, label, icon]) => ({ id, label, icon, group: ["sso", "security", "config"].includes(id) ? "Administration" : "Operations", active: activeTab === id, href: portalHref({ section: id, team_tab: null, logs_scope: id === "logs" ? "all" : null, correlation_id: null, invocation_id: null }), onClick: () => openSection(id) })),
  ];
  const rememberLink = readRememberLink(parameters);
  const unavailableTeam = activeTab === "teams" && !selectedTeam && loadState !== "loading";
  return <PortalShell theme={theme} title="Dense-Mem Control" icon={<span className="brand-initials" aria-hidden="true">DM</span>}
    navLabel="Control navigation" navItemsLabel="Control sections" navItems={navItems}
    workspacePicker={<WorkspacePicker options={teams} selectedID={selectedTeamId} loading={loadState === "loading" && !teams.length} onSelect={(id) => openTeamWorkspace(activeTab === "teams" ? teamWorkspaceTab : "overview", id)} onLoadMore={nextOffset < totalTeams ? () => void loadMoreTeams() : undefined} />}
    contextBar={<span className="current-team" aria-label="Current workspace">{selectedTeam?.name ?? "Service"}</span>}
    topbarActions={<>{themeMenu}<Button variant="ghost" size="icon" className="icon-button" type="button" aria-label="Refresh teams" disabled={loadState === "loading"} onClick={() => void transition(() => { setTeamEditorResetVersion((version) => version + 1); return loadTeams(); })}><RefreshCw size={17} aria-hidden="true" /></Button><Button variant="outline" className="ghost-button" type="button" aria-label="Sign out" onClick={onSignOut}><LogOut size={17} aria-hidden="true" /><span className="sign-out-label">Sign out</span></Button></>}
    detailLabel="Control details" error={error || (unavailableTeam ? "Linked team is unavailable or you do not have access." : requestedSection && !sections.includes(requestedSection as PortalTab) ? "This section is unavailable. Choose a destination from navigation." : "")}>
    <Suspense fallback={<LazyPanelFallback />}>
      {activeTab === "all-teams" && <TeamDirectory teams={teams} loading={loadState === "loading"} total={totalTeams} onCreate={() => setCreatingTeam(true)} onSelect={(id) => void openTeamWorkspace("overview", id)} onLoadMore={() => void loadMoreTeams()} />}
      {activeTab === "teams" && (selectedTeam ? <TeamWorkspace key={selectedTeam.id + ":" + teamWorkspaceTab + ":" + entryIndex} api={api} team={selectedTeam} activeTab={teamWorkspaceTab} resetVersion={teamEditorResetVersion} refreshing={loadState === "loading"}
        onOpenLogs={(query) => { void navigate({ section: "logs", logs_scope: query?.team_id ? "team" : "all", team_id: query?.team_id ?? selectedTeamId, team_tab: null, tab: null, remember_view: null, attempt_id: null, correlation_id: query?.correlation_id ?? null, invocation_id: query?.invocation_id ?? null }); }}
        onUpdated={(team) => setTeams((current) => current.map((item) => item.id === team.id ? team : item))}
        onDeleted={() => { setTeams((current) => current.filter((team) => team.id !== selectedTeam.id)); void navigate({ section: "all-teams", team_id: null, team_tab: null }); void loadTeams(); }}
        rememberLink={rememberLink.teamID === selectedTeam.id ? rememberLink : undefined}
        onRememberSelection={(view, id) => void navigate({ remember_view: view, attempt_id: view === "attempts" ? id : null, invocation_id: view === "calls" ? id : null }, true)}
      /> : loadState === "loading" ? <LoadingState label="Loading teams" /> : <div className="empty-state">Choose an available team from All teams.</div>)}
      {activeTab === "search" && <SearchConvergencePanel api={api} />}
      {activeTab === "recall-feedback" && <RecallFeedbackPanel api={api} teams={teams} />}
      {activeTab === "logs" && <LogsPanel key={entryIndex + ":" + logsResetVersion} api={api} teams={teams} initialQuery={logsQuery} />}
      {activeTab === "security" && <SecurityPanel api={api} />}
      {activeTab === "sso" && <SSOPanel api={api} teams={teams} />}
      {activeTab === "config" && <ConfigPanel api={api} tab={parameters.get("tab") ?? undefined} onTabChange={(tab) => void navigate({ tab })} />}
      {activeTab === "maintenance" && <OntologyConfigPanel api={api} />}
    </Suspense>
    <Dialog open={creatingTeam} onOpenChange={(open) => { if (open) setCreatingTeam(true); else void transition(() => setCreatingTeam(false)); }}>
      <DialogContent><DialogHeader><DialogTitle>Create team</DialogTitle><DialogDescription>Give the team a name and an optional description.</DialogDescription></DialogHeader><TeamCreateForm api={api} onCreated={(team) => { setCreatingTeam(false); setTeams((current) => [...current, team]); setTotalTeams((current) => current + 1); void openTeamWorkspace("overview", team.id); }} /></DialogContent>
    </Dialog>
  </PortalShell>;
}

function LazyPanelFallback() {
  return <LoadingState label="Loading panel" />;
}
function TeamCreateForm({ api, onCreated }: { api: ControlApi; onCreated: (team: Team) => void }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const clearDirty = useUnsavedChanges(Boolean(name || description));

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (name.trim().length < 3) {
      setError("Name must be at least 3 characters.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const team = await api.createTeam({ name: name.trim(), description: description.trim() });
      setName("");
      setDescription("");
      clearDirty();
      onCreated(team);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="inline-form" onSubmit={submit}>
      <FieldRow label="Name" htmlFor="new-team-name">
        <Input id="new-team-name" value={name} onChange={(event) => setName(event.target.value)} />
      </FieldRow>
      <FieldRow label="Description" htmlFor="new-team-description">
        <Input id="new-team-description" value={description} onChange={(event) => setDescription(event.target.value)} />
      </FieldRow>
      {error && <p className="field-error" role="alert">{error}</p>}
      <Button variant="default" className="primary-button compact" type="submit" disabled={busy}>
        <Plus size={16} aria-hidden="true" />
        Create
      </Button>
    </form>
  );
}

function TeamWorkspace({
  api,
  team,
  activeTab,
  onOpenLogs,
  onUpdated,
  onDeleted,
  rememberLink,
  onRememberSelection,
  resetVersion,
  refreshing,
}: {
  api: ControlApi;
  team: Team;
  activeTab: TeamWorkspaceTab;
  onOpenLogs: (query?: OperationLogQuery) => void;
  onUpdated: (team: Team) => void;
  onDeleted: () => void;
  rememberLink?: RememberLink;
  onRememberSelection: (view: "calls" | "attempts", id: string) => void;
  resetVersion: number;
  refreshing: boolean;
}) {
  // Selection replaces the current URL; only a new history entry reinitializes the list.
  const initialRememberLink = useRef(rememberLink).current;
  return (
    <TeamWorkspaceShell team={team} activeTab={activeTab}>
      {activeTab === "overview" && <TeamOverviewPanel api={api} team={team} />}
      {activeTab === "credentials" && <TeamCredentialsPanel api={api} team={team} embedded />}
      {activeTab === "remember-attempts" && (
        <Suspense fallback={<div className="team-embedded-panel"><LoadingState label="Loading Remember attempts" /></div>}>
          <RememberAttemptsPanel
            api={api} team={team} onOpenLogs={onOpenLogs}
            initialView={initialRememberLink?.view ?? "attempts"}
            initialAttemptID={initialRememberLink?.attemptID}
            initialInvocationID={initialRememberLink?.invocationID}
            onSelectionChange={onRememberSelection}
          />
        </Suspense>
      )}
      {activeTab === "conflicts" && (
        <Suspense fallback={<div className="team-embedded-panel"><LoadingState label="Loading conflict queue" /></div>}>
          <ConflictQueuePanel api={api} team={team} />
        </Suspense>
      )}
        {activeTab === "dreams" && (
        <Suspense fallback={<div className="team-embedded-panel"><LoadingState label="Loading dreams" /></div>}>
          <ControlDreamsPanel api={api} team={team} embedded />
        </Suspense>
      )}
      {activeTab === "settings" && (
        <TeamEditor
          api={api}
          team={team}
          resetVersion={resetVersion}
          refreshing={refreshing}
          onUpdated={onUpdated}
          onDeleted={onDeleted}
        />
      )}
    </TeamWorkspaceShell>
  );
}

function TeamEditor({
  api,
  team,
  onUpdated,
  onDeleted,
  resetVersion,
  refreshing,
}: {
  api: ControlApi;
  team: Team;
  onUpdated: (team: Team) => void;
  onDeleted: () => void;
  resetVersion: number;
  refreshing: boolean;
}) {
  const [name, setName] = useState(team.name);
  const [description, setDescription] = useState(team.description ?? "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setName(team.name);
    setDescription(team.description ?? "");
    setError("");
  }, [team.id, team.name, team.description, resetVersion]);

  const clearDirty = useUnsavedChanges(name !== team.name || description !== (team.description ?? ""));
  const confirm = useConfirmation();

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (name.trim().length < 3) {
      setError("Name must be at least 3 characters.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      onUpdated(await api.updateTeam(team.id, { name: name.trim(), description: description.trim() }));
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!await confirm({ title: "Delete team?", confirmLabel: "Delete team", destructive: true, description: `Delete team "${team.name}"? This disables access and hides the team. Knowledge and audit history are preserved.` })) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.deleteTeam(team.id);
      clearDirty();
      onDeleted();
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="team-detail-surface">
      <SectionHeading title="Team settings" />
      <form className="edit-grid" onSubmit={save}>
        <FieldRow label="Name" htmlFor="team-name">
          <Input id="team-name" value={name} disabled={busy || refreshing} onChange={(event) => setName(event.target.value)} />
        </FieldRow>
        <FieldRow label="Description" htmlFor="team-description">
          <Textarea id="team-description" value={description} disabled={busy || refreshing} onChange={(event) => setDescription(event.target.value)} />
        </FieldRow>
        {error && <p className="field-error span" role="alert">{error}</p>}
        <div className="button-row span">
          <Button variant="default" className="primary-button" type="submit" disabled={busy || refreshing}>
            <Pencil size={16} aria-hidden="true" />
            Save
          </Button>
          <Button variant="destructive" className="danger-button" type="button" disabled={busy || refreshing} onClick={remove}>
            <Trash2 size={16} aria-hidden="true" />
            Delete
          </Button>
        </div>
      </form>
    </section>
  );
}
