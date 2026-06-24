import { FormEvent, lazy, Suspense, useEffect, useMemo, useState } from "react";
import {
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
  CreatedTeamProfile,
  ProfileRole,
  Team,
  TeamProfile,
} from "./api";
import { TeamOverviewPanel, TeamWorkspaceShell } from "./control/TeamWorkspace";
import type { TeamWorkspaceTab } from "./control/TeamWorkspace";
import { TeamDreamingConfigForm } from "./teamDreamingConfig";
import { displayKeySuffix, formatDate, profilePermissionLabel, profileRoleLabel, readError, shortId } from "./control/utils";
import { AuthShell, PortalShell, SecretBox, SectionHeading } from "./ui/components";

const MetricsPanel = lazy(() => import("./control/MetricsPanel").then((module) => ({ default: module.MetricsPanel })));
const SecurityPanel = lazy(() => import("./control/SecurityPanel").then((module) => ({ default: module.SecurityPanel })));
const SSOPanel = lazy(() => import("./control/SSOPanel").then((module) => ({ default: module.SSOPanel })));
const ConfigPanel = lazy(() => import("./control/ConfigPanel").then((module) => ({ default: module.ConfigPanel })));
const ControlDreamsPanel = lazy(() => import("./control/DreamsPanel").then((module) => ({ default: module.ControlDreamsPanel })));
const LogsPanel = lazy(() => import("./control/LogsPanel").then((module) => ({ default: module.LogsPanel })));
const RecallFeedbackPanel = lazy(() => import("./control/RecallFeedbackPanel").then((module) => ({ default: module.RecallFeedbackPanel })));

const TOKEN_STORAGE_KEY = "denseMem.controlToken";
const THEME_STORAGE_KEY = "denseMem.controlTheme";

type LoadState = "idle" | "loading" | "error";
type Theme = "light" | "dark";
type PortalTab = "teams" | "metrics" | "recall-feedback" | "logs" | "security" | "sso" | "config";
type ProfilePermission = "read" | "read_write";

export function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem(TOKEN_STORAGE_KEY) ?? "");
  const [draftToken, setDraftToken] = useState(token);
  const [authError, setAuthError] = useState("");
  const [theme, setTheme] = useState<Theme>(() => readTheme());

  const api = useMemo(() => (token ? new ControlApi(token) : null), [token]);

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
      setAuthError("");
    } catch (error) {
      setAuthError(error instanceof Error ? error.message : "Authentication failed.");
    }
  }

  function clearToken() {
    sessionStorage.removeItem(TOKEN_STORAGE_KEY);
    setToken("");
    setDraftToken("");
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
      </AuthShell>
    );
  }

  return <Portal api={api} theme={theme} onToggleTheme={toggleTheme} onSignOut={clearToken} />;
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
                onUpdated={(team) => {
                  setTeams((current) => current.map((item) => (item.id === team.id ? team : item)));
                }}
                onDeleted={() => void loadTeams()}
              />
            ) : (
              <div className="empty-state">{loadState === "loading" ? "Loading" : "No teams"}</div>
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
  return <div className="table-placeholder">Loading</div>;
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
      return <div className="table-placeholder compact">Loading</div>;
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
  onUpdated,
  onDeleted,
}: {
  api: ControlApi;
  team: Team;
  activeTab: TeamWorkspaceTab;
  onSelectTab: (tab: TeamWorkspaceTab) => void;
  onUpdated: (team: Team) => void;
  onDeleted: () => void;
}) {
  return (
    <TeamWorkspaceShell team={team} activeTab={activeTab} onSelectTab={onSelectTab}>
      {activeTab === "overview" && <TeamOverviewPanel api={api} team={team} onOpenSettings={() => onSelectTab("settings")} />}
      {activeTab === "profiles" && <TeamProfilesPanel api={api} team={team} embedded />}
      {activeTab === "dreams" && (
        <Suspense fallback={<div className="team-embedded-panel"><div className="table-placeholder">Loading</div></div>}>
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
    if (!window.confirm(`Delete team "${team.name}"? This cannot be undone.`)) {
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

function TeamProfilesPanel({ api, team, embedded = false }: { api: ControlApi; team: Team; embedded?: boolean }) {
  const [keys, setKeys] = useState<TeamProfile[]>([]);
  const [createdKey, setCreatedKey] = useState<CreatedTeamProfile | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [deletingKeyId, setDeletingKeyId] = useState("");
  const [savingKeyId, setSavingKeyId] = useState("");
  const [updatingRoleKeyId, setUpdatingRoleKeyId] = useState("");
  const [rotatingKeyId, setRotatingKeyId] = useState("");

  async function loadKeys() {
    setLoading(true);
    setError("");
    try {
      const page = await api.listTeamProfiles(team.id);
      setKeys(page.data);
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    setCreatedKey(null);
    void loadKeys();
  }, [team.id]);

  async function updateKeyName(keyId: string, name: string) {
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Profile name is required.");
      return;
    }
    setSavingKeyId(keyId);
    setError("");
    try {
      const updated = await api.updateTeamProfile(team.id, keyId, { name: trimmedName });
      setKeys((current) => current.map((item) => (item.id === keyId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setSavingKeyId("");
    }
  }

  async function updateKeyRole(keyId: string, role: ProfileRole) {
    setUpdatingRoleKeyId(keyId);
    setError("");
    try {
      const updated = await api.updateTeamProfile(team.id, keyId, { role });
      setKeys((current) => current.map((item) => (item.id === keyId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setUpdatingRoleKeyId("");
    }
  }

  async function rotateKey(keyId: string) {
    const key = keys.find((item) => item.id === keyId);
    if (!key) {
      return;
    }
    if (!window.confirm(`Regenerate key for profile "${key.name}"? The current key will stop working.`)) {
      return;
    }
    setRotatingKeyId(keyId);
    setError("");
    try {
      const rotated = await api.regenerateTeamProfileKey(team.id, keyId, {
        name: key.name,
        rate_limit: key.rate_limit,
        expires_at: key.expires_at ?? undefined,
      });
      setCreatedKey(rotated);
      await loadKeys();
    } catch (err) {
      setError(readError(err));
    } finally {
      setRotatingKeyId("");
    }
  }

  async function deleteKey(keyId: string) {
    const key = keys.find((item) => item.id === keyId);
    const label = key ? key.name : "this profile";
    if (!window.confirm(`Delete profile "${label}"?`)) {
      return;
    }
    setDeletingKeyId(keyId);
    setError("");
    try {
      await api.deleteTeamProfile(team.id, keyId);
      await loadKeys();
    } catch (err) {
      setError(readError(err));
    } finally {
      setDeletingKeyId("");
    }
  }

  return (
    <section className={embedded ? "team-embedded-panel" : "surface"}>
      <SectionHeading title="Profiles" meta={keys.length} />
      {createdKey && <CreatedKeyNotice createdKey={createdKey} onDismiss={() => setCreatedKey(null)} />}
      {error && <div className="banner error" role="alert">{error}</div>}
      <TeamProfileCreateForm
        api={api}
        team={team}
        defaultRole={keys.length === 0 ? "manager" : "member"}
        disabled={loading}
        onCreated={(value) => {
          setCreatedKey(value);
          void loadKeys();
        }}
      />
      {loading && <div className="table-placeholder">Loading</div>}
      {!loading && (
        <TeamProfileTable
          keys={keys}
          savingKeyId={savingKeyId}
          updatingRoleKeyId={updatingRoleKeyId}
          rotatingKeyId={rotatingKeyId}
          deletingKeyId={deletingKeyId}
          onRename={(keyId, name) => void updateKeyName(keyId, name)}
          onRoleChange={(keyId, role) => void updateKeyRole(keyId, role)}
          onRotate={(keyId) => void rotateKey(keyId)}
          onDelete={(keyId) => void deleteKey(keyId)}
        />
      )}
    </section>
  );
}

function TeamProfileTable({
  keys,
  savingKeyId,
  updatingRoleKeyId,
  rotatingKeyId,
  deletingKeyId,
  onRename,
  onRoleChange,
  onRotate,
  onDelete,
}: {
  keys: TeamProfile[];
  savingKeyId: string;
  updatingRoleKeyId: string;
  rotatingKeyId: string;
  deletingKeyId: string;
  onRename: (keyId: string, name: string) => void;
  onRoleChange: (keyId: string, role: ProfileRole) => void;
  onRotate: (keyId: string) => void;
  onDelete: (keyId: string) => void;
}) {
  if (keys.length === 0) {
    return <div className="table-placeholder">No profiles</div>;
  }

  return (
    <div className="table-wrap">
      <table className="data-table key-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Key</th>
            <th>Permission</th>
            <th>Role</th>
            <th>Created</th>
            <th>Last used</th>
            <th className="actions-cell">Actions</th>
          </tr>
        </thead>
        <tbody>
          {keys.map((key) => (
            <TeamProfileRow
              key={key.id}
              profile={key}
              saving={savingKeyId === key.id}
              updatingRole={updatingRoleKeyId === key.id}
              rotating={rotatingKeyId === key.id}
              deleting={deletingKeyId === key.id}
              onRename={onRename}
              onRoleChange={onRoleChange}
              onRotate={onRotate}
              onDelete={onDelete}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TeamProfileRow({
  profile,
  saving,
  updatingRole,
  rotating,
  deleting,
  onRename,
  onRoleChange,
  onRotate,
  onDelete,
}: {
  profile: TeamProfile;
  saving: boolean;
  updatingRole: boolean;
  rotating: boolean;
  deleting: boolean;
  onRename: (keyId: string, name: string) => void;
  onRoleChange: (keyId: string, role: ProfileRole) => void;
  onRotate: (keyId: string) => void;
  onDelete: (keyId: string) => void;
}) {
  const [draftName, setDraftName] = useState(profile.name);
  const trimmedDraft = draftName.trim();
  const unchanged = trimmedDraft === profile.name;
  const busy = saving || updatingRole || rotating || deleting;

  useEffect(() => {
    setDraftName(profile.name);
  }, [profile.id, profile.name]);

  return (
    <tr>
      <td>
        <div className="profile-name-cell">
          <input
            aria-label={`Profile name ${profile.name}`}
            value={draftName}
            onChange={(event) => setDraftName(event.target.value)}
          />
          <button
            className="icon-button"
            type="button"
            aria-label={`Save profile ${profile.name}`}
            title="Save profile"
            disabled={busy || unchanged || trimmedDraft.length === 0}
            onClick={() => onRename(profile.id, draftName)}
          >
            <Pencil size={16} aria-hidden="true" />
          </button>
        </div>
      </td>
      <td><code>{displayKeySuffix(profile)}</code></td>
      <td>{profilePermissionLabel(profile.scopes)}</td>
      <td>
        <select
          aria-label={`Profile role ${profile.name}`}
          value={profile.role}
          disabled={busy}
          onChange={(event) => onRoleChange(profile.id, event.target.value as ProfileRole)}
        >
          <option value="manager">{profileRoleLabel("manager")}</option>
          <option value="member">{profileRoleLabel("member")}</option>
        </select>
      </td>
      <td>{formatDate(profile.created_at)}</td>
      <td>{profile.last_used_at ? formatDate(profile.last_used_at) : "Never"}</td>
      <td className="actions-cell">
        <div className="table-actions">
          <button
            className="icon-button"
            type="button"
            aria-label={`Regenerate key for profile ${profile.name}`}
            title="Regenerate key"
            disabled={busy}
            onClick={() => onRotate(profile.id)}
          >
            <RefreshCw size={16} aria-hidden="true" />
          </button>
          <button
            className="icon-button danger"
            type="button"
            aria-label={`Delete profile ${profile.name}`}
            title="Delete profile"
            disabled={busy}
            onClick={() => onDelete(profile.id)}
          >
            <Trash2 size={16} aria-hidden="true" />
          </button>
        </div>
      </td>
    </tr>
  );
}

function TeamProfileCreateForm({
  api,
  team,
  defaultRole,
  disabled,
  onCreated,
}: {
  api: ControlApi;
  team: Team;
  defaultRole: ProfileRole;
  disabled: boolean;
  onCreated: (created: CreatedTeamProfile) => void;
}) {
  const [name, setName] = useState("default profile");
  const [permission, setPermission] = useState<ProfilePermission>("read_write");
  const [role, setRole] = useState<ProfileRole>(defaultRole);
  const [rateLimit, setRateLimit] = useState("120");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setRole(defaultRole);
  }, [team.id, defaultRole]);

  useEffect(() => {
    if (role === "manager") {
      setPermission("read_write");
    }
  }, [role]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (name.trim().length < 1) {
      setError("Profile name is required.");
      return;
    }
    const parsedRateLimit = Number.parseInt(rateLimit, 10);
    if (!Number.isFinite(parsedRateLimit) || parsedRateLimit <= 0) {
      setError("Rate limit must be greater than zero.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const created = await api.createTeamProfile(team.id, {
        name: name.trim(),
        scopes: role === "manager" || permission === "read_write" ? ["read", "write"] : ["read"],
        role,
        rate_limit: parsedRateLimit,
      });
      onCreated(created);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="key-form" onSubmit={submit}>
      <label htmlFor="team-profile-name">Profile name</label>
      <input id="team-profile-name" value={name} onChange={(event) => setName(event.target.value)} />
      <label htmlFor="team-profile-role">Role</label>
      <select
        id="team-profile-role"
        value={role}
        onChange={(event) => setRole(event.target.value as ProfileRole)}
      >
        <option value="manager">{profileRoleLabel("manager")}</option>
        <option value="member">{profileRoleLabel("member")}</option>
      </select>
      {role === "member" && (
        <>
          <label htmlFor="team-profile-permission">Permission</label>
          <select
            id="team-profile-permission"
            value={permission}
            onChange={(event) => setPermission(event.target.value as ProfilePermission)}
          >
            <option value="read_write">Read/write</option>
            <option value="read">Read only</option>
          </select>
        </>
      )}
      <label htmlFor="rate-limit">Rate limit</label>
      <input id="rate-limit" inputMode="numeric" value={rateLimit} onChange={(event) => setRateLimit(event.target.value)} />
      {error && <p className="field-error span" role="alert">{error}</p>}
      <button className="primary-button span" type="submit" disabled={busy || disabled}>
        <KeyRound size={16} aria-hidden="true" />
        Create profile
      </button>
    </form>
  );
}

function CreatedKeyNotice({ createdKey, onDismiss }: { createdKey: CreatedTeamProfile; onDismiss: () => void }) {
  return (
    <SecretBox
      value={createdKey.api_key}
      valueLabel="Generated API key"
      copyLabel="Copy API key"
      dismissLabel="Dismiss API key"
      onDismiss={onDismiss}
    />
  );
}
