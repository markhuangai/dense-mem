import { FormEvent, lazy, Suspense, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  Ban,
  BarChart3,
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
  Sun,
  Trash2,
  Users,
} from "lucide-react";
import {
  ControlApi,
  ControlIdentityProvider,
  Team,
  listControlIdentityProviders,
} from "./api";
import { TeamProfilesPanel } from "./control/TeamProfilesPanel";
import { TeamOverviewPanel, TeamWorkspaceShell } from "./control/TeamWorkspace";
import type { TeamWorkspaceTab } from "./control/TeamWorkspace";
import { TeamDreamingConfigForm } from "./teamDreamingConfig";
import { formatDate, readError, shortId } from "./control/utils";
import { AuthShell, LoadingState, PortalShell, SectionHeading } from "./ui/components";

const MetricsPanel = lazy(() => import("./control/MetricsPanel").then((module) => ({ default: module.MetricsPanel })));
const SecurityPanel = lazy(() => import("./control/SecurityPanel").then((module) => ({ default: module.SecurityPanel })));
const SSOPanel = lazy(() => import("./control/SSOPanel").then((module) => ({ default: module.SSOPanel })));
const ConfigPanel = lazy(() => import("./control/ConfigPanel").then((module) => ({ default: module.ConfigPanel })));
const ControlDreamsPanel = lazy(() => import("./control/DreamsPanel").then((module) => ({ default: module.ControlDreamsPanel })));
const LogsPanel = lazy(() => import("./control/LogsPanel").then((module) => ({ default: module.LogsPanel })));
const RecallFeedbackPanel = lazy(() => import("./control/RecallFeedbackPanel").then((module) => ({ default: module.RecallFeedbackPanel })));
const ConflictQueuePanel = lazy(() => import("./control/ConflictQueuePanel").then((module) => ({ default: module.ConflictQueuePanel })));

const TOKEN_STORAGE_KEY = "denseMem.controlToken";
const THEME_STORAGE_KEY = "denseMem.controlTheme";

type LoadState = "idle" | "loading" | "error";
type Theme = "light" | "dark";
type AuthMode = "none" | "token" | "sso";
type PortalTab = "teams" | "metrics" | "recall-feedback" | "logs" | "security" | "sso" | "config";

export function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem(TOKEN_STORAGE_KEY) ?? "");
  const [draftToken, setDraftToken] = useState(token);
  const [authMode, setAuthMode] = useState<AuthMode>(() => token ? "token" : "none");
  const [ssoProviders, setSSOProviders] = useState<ControlIdentityProvider[]>([]);
  const [authError, setAuthError] = useState("");
  const [theme, setTheme] = useState<Theme>(() => readTheme());

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

  function toggleTheme() {
    setTheme((current) => {
      const next = current === "dark" ? "light" : "dark";
      localStorage.setItem(THEME_STORAGE_KEY, next);
      return next;
    });
  }

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
        actions={(
          <button
            className="icon-button theme-toggle"
            type="button"
            aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
            onClick={toggleTheme}
          >
            {theme === "dark" ? <Sun size={17} aria-hidden="true" /> : <Moon size={17} aria-hidden="true" />}
          </button>
        )}
      >
        <label htmlFor="portal-token">Control token</label>
        <input
          id="portal-token"
          type="password"
          value={draftToken}
          onChange={(event) => setDraftToken(event.target.value)}
          autoComplete="current-password"
        />
        {authError && <p className="field-error" role="alert">{authError}</p>}
        <button className="primary-button" type="submit">
          <ShieldCheck size={17} aria-hidden="true" />
          Unlock
        </button>
        {ssoProviders.length > 0 && (
          <div className="button-row">
            {ssoProviders.map((provider) => (
              <button
                className="ghost-button"
                key={provider.id}
                type="button"
                onClick={() => window.location.assign(`/control/auth/start/${encodeURIComponent(provider.id)}`)}
              >
                <ShieldCheck size={17} aria-hidden="true" />
                Sign in with {provider.name}
              </button>
            ))}
          </div>
        )}
      </AuthShell>
    );
  }

  return <Portal api={api} theme={theme} onToggleTheme={toggleTheme} onSignOut={() => void signOut()} />;
}

function Portal({
  api,
  theme,
  onToggleTheme,
  onSignOut,
}: {
  api: ControlApi;
  theme: Theme;
  onToggleTheme: () => void;
  onSignOut: () => void;
}) {
  const [teams, setTeams] = useState<Team[]>([]);
  const [selectedTeamId, setSelectedTeamId] = useState("");
  const [activeTab, setActiveTab] = useState<PortalTab>("teams");
  const [teamWorkspaceTab, setTeamWorkspaceTab] = useState<TeamWorkspaceTab>("overview");
  const [creatingTeam, setCreatingTeam] = useState(false);
  const [loadState, setLoadState] = useState<LoadState>("idle");
  const [error, setError] = useState("");

  async function loadTeams(nextSelectedId?: string) {
    setLoadState("loading");
    setError("");
    try {
      const page = await api.listTeams();
      setTeams(page.data);
      const selected = nextSelectedId || selectedTeamId;
      if (selected && page.data.some((team) => team.id === selected)) {
        setSelectedTeamId(selected);
      } else {
        setSelectedTeamId(page.data[0]?.id ?? "");
      }
      setLoadState("idle");
    } catch (err) {
      setLoadState("error");
      setError(readError(err));
    }
  }

  useEffect(() => {
    void loadTeams();
  }, []);

  const selectedTeam = teams.find((team) => team.id === selectedTeamId) ?? null;
  const teamScopedTab = activeTab === "teams";

  function openTeamWorkspace(nextTab: TeamWorkspaceTab) {
    setCreatingTeam(false);
    setTeamWorkspaceTab(nextTab);
    setActiveTab("teams");
  }

  return (
    <PortalShell
      theme={theme}
      title="Dense-Mem Control"
      icon={<span className="brand-initials" aria-hidden="true">DM</span>}
      topbarActions={(
        <>
          <button
            className="icon-button"
            type="button"
            aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
            title={theme === "dark" ? "Light theme" : "Dark theme"}
            onClick={onToggleTheme}
          >
            {theme === "dark" ? <Sun size={17} aria-hidden="true" /> : <Moon size={17} aria-hidden="true" />}
          </button>
          <button className="icon-button" type="button" aria-label="Refresh teams" onClick={() => void loadTeams()}>
            <RefreshCw size={18} aria-hidden="true" />
          </button>
          <button className="ghost-button" type="button" onClick={onSignOut}>
            <LogOut size={17} aria-hidden="true" />
            Sign out
          </button>
        </>
      )}
      navLabel="Control navigation"
      navItemsLabel="Control sections"
      navItems={[
        {
          id: "teams",
          label: "Teams",
          icon: <Users size={17} aria-hidden="true" />,
          active: activeTab === "teams" && (teamWorkspaceTab === "overview" || teamWorkspaceTab === "settings"),
          onClick: () => openTeamWorkspace("overview"),
        },
        {
          id: "metrics",
          label: "Metrics",
          icon: <BarChart3 size={17} aria-hidden="true" />,
          active: activeTab === "metrics",
          onClick: () => setActiveTab("metrics"),
        },
        {
          id: "recall-feedback",
          label: "Feedback",
          icon: <MessageSquare size={17} aria-hidden="true" />,
          active: activeTab === "recall-feedback",
          onClick: () => setActiveTab("recall-feedback"),
        },
        {
          id: "dreams",
          label: "Dreams",
          icon: <Moon size={17} aria-hidden="true" />,
          active: activeTab === "teams" && teamWorkspaceTab === "dreams",
          disabled: !selectedTeam,
          onClick: () => openTeamWorkspace("dreams"),
        },
        {
          id: "conflicts",
          label: "Conflicts",
          icon: <AlertTriangle size={17} aria-hidden="true" />,
          active: activeTab === "teams" && teamWorkspaceTab === "conflicts",
          disabled: !selectedTeam,
          onClick: () => openTeamWorkspace("conflicts"),
        },
        {
          id: "logs",
          label: "Logs",
          icon: <ListFilter size={17} aria-hidden="true" />,
          active: activeTab === "logs",
          onClick: () => setActiveTab("logs"),
        },
        {
          id: "profiles",
          label: "Profiles",
          icon: <KeyRound size={17} aria-hidden="true" />,
          active: activeTab === "teams" && teamWorkspaceTab === "profiles",
          disabled: !selectedTeam,
          onClick: () => openTeamWorkspace("profiles"),
        },
        {
          id: "security",
          label: "IP Bans",
          icon: <Ban size={17} aria-hidden="true" />,
          active: activeTab === "security",
          onClick: () => setActiveTab("security"),
        },
        {
          id: "sso",
          label: "SSO",
          icon: <ShieldCheck size={17} aria-hidden="true" />,
          active: activeTab === "sso",
          onClick: () => setActiveTab("sso"),
        },
        {
          id: "config",
          label: "Config",
          icon: <Settings size={17} aria-hidden="true" />,
          active: activeTab === "config",
          onClick: () => setActiveTab("config"),
        },
      ]}
      resourceRailLabel="Teams"
      resourceRail={teamScopedTab ? (
        <TeamResourceRail
          teams={teams}
          selectedTeamId={selectedTeamId}
          loading={loadState === "loading"}
          onCreate={() => {
            setActiveTab("teams");
            setTeamWorkspaceTab("overview");
            setCreatingTeam(true);
            window.requestAnimationFrame(() => document.getElementById("new-team-name")?.focus());
          }}
          onSelect={(teamId) => {
            setSelectedTeamId(teamId);
          }}
        />
      ) : undefined}
      detailLabel="Control details"
      error={error}
    >
      <Suspense fallback={<LazyPanelFallback />}>
        {teamScopedTab && (
          <>
            {creatingTeam && (
              <section className="surface">
                <SectionHeading
                  title="Create team"
                  actions={(
                    <button className="text-button" type="button" onClick={() => setCreatingTeam(false)}>
                      Cancel
                    </button>
                  )}
                />
                <TeamCreateForm
                  api={api}
                  onCreated={(team) => {
                    setCreatingTeam(false);
                    void loadTeams(team.id);
                  }}
                />
              </section>
            )}
            {selectedTeam ? (
              <TeamWorkspace
                api={api}
                team={selectedTeam}
                activeTab={teamWorkspaceTab}
                onSelectTab={openTeamWorkspace}
                onOpenMetrics={() => setActiveTab("metrics")}
                onUpdated={(team) => {
                  setTeams((current) => current.map((item) => (item.id === team.id ? team : item)));
                }}
                onDeleted={() => void loadTeams()}
              />
            ) : (
              loadState === "loading" ? <LoadingState label="Loading teams" /> : <div className="empty-state">No teams</div>
            )}
          </>
        )}
        {activeTab === "metrics" && <MetricsPanel api={api} teams={teams} />}
        {activeTab === "recall-feedback" && <RecallFeedbackPanel api={api} teams={teams} />}
        {activeTab === "logs" && <LogsPanel api={api} teams={teams} />}
        {activeTab === "security" && <SecurityPanel api={api} />}
        {activeTab === "sso" && <SSOPanel api={api} teams={teams} />}
        {activeTab === "config" && <ConfigPanel api={api} />}
      </Suspense>
    </PortalShell>
  );
}

function LazyPanelFallback() {
  return <LoadingState label="Loading panel" />;
}
function readTheme(): Theme {
  return localStorage.getItem(THEME_STORAGE_KEY) === "dark" ? "dark" : "light";
}
function TeamCreateForm({ api, onCreated }: { api: ControlApi; onCreated: (team: Team) => void }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

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
      onCreated(team);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="inline-form" onSubmit={submit}>
      <label htmlFor="new-team-name">Name</label>
      <input id="new-team-name" value={name} onChange={(event) => setName(event.target.value)} />
      <label htmlFor="new-team-description">Description</label>
      <input id="new-team-description" value={description} onChange={(event) => setDescription(event.target.value)} />
      {error && <p className="field-error" role="alert">{error}</p>}
      <button className="primary-button compact" type="submit" disabled={busy}>
        <Plus size={16} aria-hidden="true" />
        Create
      </button>
    </form>
  );
}

function TeamResourceRail({
  teams,
  selectedTeamId,
  loading,
  onCreate,
  onSelect,
}: {
  teams: Team[];
  selectedTeamId: string;
  loading: boolean;
  onCreate: () => void;
  onSelect: (teamId: string) => void;
}) {
  const [query, setQuery] = useState("");
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const visibleTeams = normalizedQuery
    ? teams.filter((team) => (
      team.name.toLocaleLowerCase().includes(normalizedQuery)
        || team.id.toLocaleLowerCase().includes(normalizedQuery)
    ))
    : teams;

  const listContent = (() => {
    if (loading && teams.length === 0) {
      return <LoadingState label="Loading teams" compact />;
    }

    if (teams.length === 0) {
      return <div className="table-placeholder compact">No teams</div>;
    }

    if (visibleTeams.length === 0) {
      return <div className="table-placeholder compact">No matching teams</div>;
    }

    return (
      <div className="team-list">
        {visibleTeams.map((team) => (
          <button
            key={team.id}
            className={team.id === selectedTeamId ? "team-list-item selected" : "team-list-item"}
            type="button"
            onClick={() => onSelect(team.id)}
          >
            <span className="team-list-primary">
              <span className="status-dot" aria-hidden="true" />
              <span>{team.name}</span>
            </span>
            <small>{formatDate(team.updated_at)}</small>
          </button>
        ))}
      </div>
    );
  })();

  return (
    <div className="resource-panel team-resource-panel">
      <SectionHeading
        title="Teams"
        meta={teams.length}
        actions={(
          <button className="primary-button compact" type="button" onClick={onCreate}>
            <Plus size={16} aria-hidden="true" />
            New Team
          </button>
        )}
      />
      <label className="resource-search">
        <Search size={16} aria-hidden="true" />
        <span className="sr-only">Search teams</span>
        <input
          aria-label="Search teams"
          placeholder="Search teams..."
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </label>
      {listContent}
    </div>
  );
}

function TeamWorkspace({
  api,
  team,
  activeTab,
  onSelectTab,
  onOpenMetrics,
  onUpdated,
  onDeleted,
}: {
  api: ControlApi;
  team: Team;
  activeTab: TeamWorkspaceTab;
  onSelectTab: (tab: TeamWorkspaceTab) => void;
  onOpenMetrics: () => void;
  onUpdated: (team: Team) => void;
  onDeleted: () => void;
}) {
  return (
    <TeamWorkspaceShell team={team} activeTab={activeTab} onSelectTab={onSelectTab}>
      {activeTab === "overview" && <TeamOverviewPanel api={api} team={team} onOpenMetrics={onOpenMetrics} />}
      {activeTab === "profiles" && <TeamProfilesPanel api={api} team={team} embedded />}
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
}: {
  api: ControlApi;
  team: Team;
  onUpdated: (team: Team) => void;
  onDeleted: () => void;
}) {
  const [name, setName] = useState(team.name);
  const [description, setDescription] = useState(team.description ?? "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setName(team.name);
    setDescription(team.description ?? "");
    setError("");
  }, [team.id, team.name, team.description]);

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
    if (!window.confirm(`Delete team "${team.name}"? This disables access and hides the team. Knowledge and audit history are preserved.`)) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.deleteTeam(team.id);
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
        <label htmlFor="team-name">Name</label>
        <input id="team-name" value={name} onChange={(event) => setName(event.target.value)} />
        <label htmlFor="team-description">Description</label>
        <textarea id="team-description" value={description} onChange={(event) => setDescription(event.target.value)} />
        {error && <p className="field-error span" role="alert">{error}</p>}
        <div className="button-row span">
          <button className="primary-button" type="submit" disabled={busy}>
            <Pencil size={16} aria-hidden="true" />
            Save
          </button>
          <button className="danger-button" type="button" disabled={busy} onClick={remove}>
            <Trash2 size={16} aria-hidden="true" />
            Delete
          </button>
        </div>
      </form>
      <div className="surface-section team-dreaming-section">
        <TeamDreamingConfigForm
          key={team.id}
          config={team.config}
          effective={team.dreaming_effective}
          disabled={busy}
          onSave={async (config) => {
            onUpdated(await api.updateTeam(team.id, { name: team.name, description: team.description ?? "", config }));
          }}
        />
      </div>
    </section>
  );
}
