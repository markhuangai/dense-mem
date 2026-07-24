import { FormEvent, lazy, ReactNode, Suspense, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  ArrowRight,
  Ban,
  BarChart3,
  CheckCircle2,
  Database,
  KeyRound,
  ListFilter,
  LogOut,
  MessageSquare,
  Moon,
  PauseCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  ServerCog,
  Settings,
  ShieldCheck,
  Sun,
  Trash2,
  Users,
} from "lucide-react";
import {
  ControlApi,
  ControlSession,
  MigrationStatus,
  Team,
} from "./api";
import { TeamProfilesPanel } from "./control/TeamProfilesPanel";
import { TeamOverviewPanel, TeamWorkspaceShell } from "./control/TeamWorkspace";
import type { TeamWorkspaceTab } from "./control/TeamWorkspace";
import { TeamDreamingConfigForm } from "./teamDreamingConfig";
import { formatDate, readError, shortId, startSerialPolling } from "./control/utils";
import { AuthShell, LoadingState, PortalShell, SectionHeading } from "./ui/components";
import { MigrationRepairCard } from "./MigrationRepairCard";

const MetricsPanel = lazy(() => import("./control/MetricsPanel").then((module) => ({ default: module.MetricsPanel })));
const SecurityPanel = lazy(() => import("./control/SecurityPanel").then((module) => ({ default: module.SecurityPanel })));
const SSOPanel = lazy(() => import("./control/SSOPanel").then((module) => ({ default: module.SSOPanel })));
const ConfigPanel = lazy(() => import("./control/ConfigPanel").then((module) => ({ default: module.ConfigPanel })));
const ControlDreamsPanel = lazy(() => import("./control/DreamsPanel").then((module) => ({ default: module.ControlDreamsPanel })));
const LogsPanel = lazy(() => import("./control/LogsPanel").then((module) => ({ default: module.LogsPanel })));
const RecallFeedbackPanel = lazy(() => import("./control/RecallFeedbackPanel").then((module) => ({ default: module.RecallFeedbackPanel })));

const TOKEN_STORAGE_KEY = "denseMem.controlToken";
const THEME_STORAGE_KEY = "denseMem.controlTheme";
const CURRENT_MIGRATION_CONTRACT = "dense-mem.v2.1.migration-control.v3";

type LoadState = "idle" | "loading" | "error";
type Theme = "light" | "dark";
type PortalTab = "teams" | "metrics" | "recall-feedback" | "logs" | "security" | "sso" | "config";

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
  const [session, setSession] = useState<ControlSession | null>(null);
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

  async function loadPortal() {
    setLoadState("loading");
    setError("");
    try {
      const nextSession = normalizeControlSession(await api.session());
      setSession(nextSession);
      if (nextSession.portal_mode === "normal") {
        await loadTeams();
      } else {
        setLoadState("idle");
      }
    } catch (err) {
      setLoadState("error");
      setError(readError(err));
    }
  }

  useEffect(() => {
    void loadPortal();
  }, []);

  if (!session) {
    return (
      <MaintenanceFrame theme={theme} onToggleTheme={onToggleTheme} onSignOut={onSignOut}>
        {error ? <div className="banner error" role="alert">{error}</div> : <LoadingState label="Loading control session" />}
      </MaintenanceFrame>
    );
  }

  if (session.portal_mode !== "normal") {
    return (
      <MigrationPortal
        api={api}
        session={session}
        theme={theme}
        onToggleTheme={onToggleTheme}
        onSignOut={onSignOut}
      />
    );
  }

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

function normalizeControlSession(session: ControlSession): ControlSession {
  return {
    ...session,
    portal_mode: session.portal_mode ?? "normal",
    legacy_config_present: session.legacy_config_present ?? false,
  };
}

function MigrationPortal({
  api,
  session,
  theme,
  onToggleTheme,
  onSignOut,
}: {
  api: ControlApi;
  session: ControlSession;
  theme: Theme;
  onToggleTheme: () => void;
  onSignOut: () => void;
}) {
  const [status, setStatus] = useState<MigrationStatus | null>(null);
  const [backupsConfirmed, setBackupsConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [connection, setConnection] = useState<"connected" | "reconnecting">("connected");
  const [error, setError] = useState("");

  async function loadStatus() {
    try {
      const next = await api.getMigrationStatus();
      setStatus(next);
      setConnection("connected");
      setError("");
    } catch (err) {
      setConnection("reconnecting");
      setError(readError(err));
    }
  }

  useEffect(() => {
    return startSerialPolling(loadStatus, 2000);
  }, [api]);

  async function start() {
    if (session.portal_mode !== "migration") {
      return;
    }
    setBusy(true);
    setError("");
    try {
      let next = status;
      if (!next) {
        next = await api.getMigrationStatus();
      }
      const currentPreflight = next?.run?.preflight_approved &&
        next.run.migration_contract_version === CURRENT_MIGRATION_CONTRACT &&
        hasBackupConfirmation(next.run.preflight_checks);
      if (!currentPreflight) {
        next = await api.approveMigrationPreflight({
          backups_confirmed: backupsConfirmed,
          reason: "operator confirmed external PostgreSQL and Neo4j backups",
        });
        setStatus(next);
      }
      if (next.state === "ready") {
        next = await api.startMigration("operator started guided migration");
      }
      setStatus(next);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function pause() {
    setBusy(true);
    setError("");
    try {
      setStatus(await api.pauseMigration("operator paused guided migration"));
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function resume() {
    setBusy(true);
    setError("");
    try {
      setStatus(await api.resumeMigration("operator resumed guided migration"));
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  const run = status?.run;
  const total = run?.total_items ?? 0;
  const completed = run?.completed_items ?? 0;
  const progress = total > 0 ? Math.min(100, Math.round((completed / total) * 100)) : (status?.state === "cut_over" ? 100 : 0);
  const repair = status?.repair;
  const hardBlockingExclusions = repair?.hard_blocking_exclusions ?? repair?.blocking_exclusions ?? 0;
  const repairBlocked = ((repair?.blocked_items ?? 0) > 0) || hardBlockingExclusions > 0;
  const contractCurrent = run?.migration_contract_version === CURRENT_MIGRATION_CONTRACT;
  const backupConfirmationCurrent = !!run?.preflight_approved && contractCurrent && hasBackupConfirmation(run.preflight_checks);
  useEffect(() => {
    if (backupConfirmationCurrent) {
      setBackupsConfirmed(false);
    }
  }, [backupConfirmationCurrent]);
  const confirmationRenewalState = !!run && !backupConfirmationCurrent && (
    status?.state === "running" ||
    status?.state === "paused_retryable" ||
    (status?.state === "failed" && run.retryable) ||
    status?.state === "verifying" ||
    status?.state === "ready_to_cutover"
  );
  const canStart = session.portal_mode === "migration" &&
    (status?.state === "required" || status?.state === "ready" || confirmationRenewalState) &&
    (backupConfirmationCurrent || backupsConfirmed);
  const canPause = session.portal_mode === "migration" && status?.state === "running";
  const canResume = session.portal_mode === "migration" &&
    backupConfirmationCurrent &&
    (status?.state === "paused_retryable" || (status?.state === "failed" && run?.retryable)) &&
    !repairBlocked;
  const cleanup = session.portal_mode === "cleanup";

  return (
    <MaintenanceFrame theme={theme} onToggleTheme={onToggleTheme} onSignOut={onSignOut}>
      <section className="maintenance-hero" aria-label="V2 migration">
        <div>
          <p className="eyebrow">Authority migration</p>
          <h2>{cleanup ? "PostgreSQL V2 is active" : "Legacy migration is required"}</h2>
          <p>
            {cleanup
              ? "The MCP service is running on PostgreSQL V2. Finish environment cleanup, then recreate the deployment to restore the normal control portal."
              : "The data plane is closed until the legacy Neo4j corpus is migrated and the V2 compatibility marker is committed."}
          </p>
        </div>
        <div className="migration-state-badge" data-state={status?.state ?? "loading"}>
          {status?.state ? stateLabel(status.state) : "Loading"}
        </div>
      </section>

      {error && <div className="banner error" role="alert">{error}</div>}
      {connection === "reconnecting" && (
        <div className="banner warning" role="status">
          Reconnecting to the control portal. If cutover just completed, the server is restarting into PostgreSQL V2.
        </div>
      )}

      <div className="maintenance-grid">
        <section className="maintenance-panel authority-panel">
          <SectionHeading title="Authority handoff" subtitle={status?.readiness_message} />
          <div className="authority-rail" aria-label="Authority handoff phases">
            <AuthorityStep label="Neo4j source" active={!cleanup && status?.state !== "cut_over"} done={cleanup || status?.state === "cut_over"} />
            <ArrowRight size={18} aria-hidden="true" />
            <AuthorityStep label="PostgreSQL V2" active={status?.state === "running" || status?.state === "ready_to_cutover"} done={cleanup || status?.state === "cut_over"} />
            <ArrowRight size={18} aria-hidden="true" />
            <AuthorityStep label="Active service" active={cleanup || status?.state === "cut_over"} done={cleanup} />
          </div>
          <div className="migration-progress" aria-label="Migration progress">
            <div>
              <strong>{progress}%</strong>
              <span>{total > 0 ? `${completed} of ${total} items complete` : "Waiting for corpus scan"}</span>
            </div>
            <div className="progress-track">
              <span style={{ width: `${progress}%` }} />
            </div>
          </div>
          {status?.recent_errors?.map((item) => (
            <p className="field-error" role="alert" key={item}>{item}</p>
          ))}
          {repair && <MigrationRepairCard repair={repair} repairBlocked={repairBlocked} claimEpoch={run?.claim_epoch} />}
        </section>

        {cleanup ? (
          <CleanupPanel />
        ) : (
          <section className="maintenance-panel">
            <SectionHeading
              title="Start migration"
              subtitle="Confirm that both recovery artifacts exist before the migration starts."
            />
            <form className="migration-preflight" onSubmit={(event) => { event.preventDefault(); void start(); }}>
              <div className="backup-confirmation-card" aria-label="Before you migrate">
                <div>
                  <h3>Before you migrate</h3>
                  <p>Confirm that recoverable copies exist for both databases.</p>
                </div>
                <div className="backup-confirmation-scope" aria-label="Databases covered by this confirmation">
                  <span><Database size={15} aria-hidden="true" /> PostgreSQL</span>
                  <span><Database size={15} aria-hidden="true" /> Neo4j</span>
                </div>
              </div>
              <label className="check-row">
                <input
                  type="checkbox"
                  checked={backupsConfirmed || backupConfirmationCurrent}
                  onChange={(event) => setBackupsConfirmed(event.target.checked)}
                  disabled={busy || backupConfirmationCurrent}
                />
                I confirm that I have backed up both the PostgreSQL and Neo4j databases.
              </label>
              <p className="confirmation-note">Dense-Mem does not create, inspect, or restore these backups.</p>
              <div className="button-row">
                <button className="primary-button" type="submit" disabled={busy || !canStart}>
                  <Play size={16} aria-hidden="true" />
                  {backupConfirmationCurrent ? "Start migration" : "Confirm and start migration"}
                </button>
                <button className="ghost-button" type="button" disabled={busy || !canPause} onClick={() => void pause()}>
                  <PauseCircle size={16} aria-hidden="true" />
                  Pause
                </button>
                <button className="ghost-button" type="button" disabled={busy || !canResume} onClick={() => void resume()}>
                  <RotateCcw size={16} aria-hidden="true" />
                  {repair?.required ? "Repair and resume" : "Resume"}
                </button>
              </div>
            </form>
          </section>
        )}

        <section className="maintenance-panel gates-panel">
          <SectionHeading title="Cutover gates" subtitle="The supervisor commits the marker only after every hard gate passes." />
          {status?.gate_results?.length ? (
            <div className="gate-list">
              {status.gate_results.map((gate) => (
                <div className="gate-row" key={gate.gate_name}>
                  {gate.outcome === "pass" ? <CheckCircle2 size={16} aria-hidden="true" /> : <AlertTriangle size={16} aria-hidden="true" />}
                  <span>{gate.gate_name.replaceAll("_", " ")}</span>
                  <small>{gate.message ?? gate.outcome}</small>
                </div>
              ))}
            </div>
          ) : (
            <div className="table-placeholder compact">Gate results appear after corpus processing finishes.</div>
          )}
        </section>
      </div>
    </MaintenanceFrame>
  );
}

function MaintenanceFrame({
  theme,
  onToggleTheme,
  onSignOut,
  children,
}: {
  theme: Theme;
  onToggleTheme: () => void;
  onSignOut: () => void;
  children: ReactNode;
}) {
  return (
    <main className="app-shell maintenance-shell" data-theme={theme}>
      <header className="topbar maintenance-topbar">
        <div className="brand-row">
          <span className="brand-mark"><ServerCog size={18} aria-hidden="true" /></span>
          <h1>Dense-Mem Control</h1>
        </div>
        <div className="topbar-actions">
          <button
            className="icon-button"
            type="button"
            aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
            title={theme === "dark" ? "Light theme" : "Dark theme"}
            onClick={onToggleTheme}
          >
            {theme === "dark" ? <Sun size={17} aria-hidden="true" /> : <Moon size={17} aria-hidden="true" />}
          </button>
          <button className="ghost-button" type="button" onClick={onSignOut}>
            <LogOut size={17} aria-hidden="true" />
            Sign out
          </button>
        </div>
      </header>
      <div className="maintenance-content">
        {children}
      </div>
    </main>
  );
}

function AuthorityStep({ label, active, done }: { label: string; active: boolean; done: boolean }) {
  return (
    <div className={active ? "authority-step active" : done ? "authority-step done" : "authority-step"}>
      {done ? <CheckCircle2 size={16} aria-hidden="true" /> : <Database size={16} aria-hidden="true" />}
      <span>{label}</span>
    </div>
  );
}

function CleanupPanel() {
  const steps = [
    "Remove NEO4J_URI, NEO4J_USER, NEO4J_PASSWORD, and NEO4J_DATABASE from the production runtime or secret store.",
    "Remove the legacy Neo4j service or block application network access to it.",
    "Recreate the deployment so the process starts without any NEO4J_* settings.",
    "Return to the private control portal; the normal administration UI will appear after cleanup.",
  ];
  return (
    <section className="maintenance-panel cleanup-panel">
      <SectionHeading title="Manual cleanup" subtitle="The application does not edit environment variables." />
      <ol>
        {steps.map((step) => (
          <li key={step}>{step}</li>
        ))}
      </ol>
    </section>
  );
}

function stateLabel(state: string) {
  switch (state) {
    case "required":
      return "Needs backup confirmation";
    case "ready":
      return "Ready to start";
    case "running":
      return "Migrating";
    case "paused_retryable":
      return "Paused";
    case "ready_to_cutover":
      return "Verifying";
    case "cut_over":
      return "Cut over";
    case "failed":
      return "Needs attention";
    default:
      return state.replaceAll("_", " ");
  }
}

function hasBackupConfirmation(checks?: Record<string, unknown>) {
  if (!checks) {
    return false;
  }
  return checks.operator_backup_confirmation === true &&
    checks.postgres_backup_confirmed === true &&
    checks.neo4j_backup_confirmed === true &&
    checks.confirmation_scope === "operator" &&
    checks.backup_verification === "not_performed";
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
