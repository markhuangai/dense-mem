import { Component, FormEvent, lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import {
  BarChart3,
  KeyRound,
  LogOut,
  Moon,
  Network,
  RefreshCw,
  Search,
  ShieldCheck,
  Sun,
  Users,
} from "lucide-react";
import { TelemetrySnapshot, TelemetryWindowKey } from "../telemetry/types";
import {
  RotateResponse,
  SSOProvider,
  UserApi,
  UserSession,
} from "./api";
import { AuthShell, LoadingState, PortalShell, SecretBox, SectionHeading } from "../ui/components";
import { SearchPanel } from "./SearchPanel";

const TelemetryDashboard = lazy(() => import("../telemetry/TelemetryDashboard").then((module) => ({ default: module.TelemetryDashboard })));
const TeamManagementPanel = lazy(() => import("./TeamManagementPanel").then((module) => ({ default: module.TeamManagementPanel })));
const UserDreamsPanel = lazy(() => import("./DreamsPanel").then((module) => ({ default: module.UserDreamsPanel })));
const GraphPanel = lazy(() => import("./GraphPanel").then((module) => ({ default: module.GraphPanel })));

const TOKEN_STORAGE_KEY = "denseMem.userApiKey";
const THEME_STORAGE_KEY = "denseMem.userTheme";

type Theme = "light" | "dark";
type AuthMode = "none" | "api_key" | "api_key_session" | "sso";
type UserTab = "search" | "graph" | "dreams" | "usage" | "team" | "key";
type ProfilePermission = "read" | "read_write";

function sessionAuthMode(session: UserSession): AuthMode {
  if (session.auth_method === "sso") {
    return "sso";
  }
  return session.auth_method === "api_key_session" ? "api_key_session" : "api_key";
}

function canShowMyKey(session: UserSession | null): boolean {
  if (!session) {
    return false;
  }
  if (session.auth_method !== "sso") {
    return true;
  }
  return !session.can_manage_team && (session.can_create_personal_key || Boolean(session.personal_key));
}

function canShowUsage(session: UserSession | null): boolean {
  return Boolean(session?.key.scopes.includes("write"));
}

function userTelemetryTitle(session: UserSession): string {
  return session.can_manage_team ? "Team usage" : "My key usage";
}

function userTelemetryIdentity(session: UserSession): string {
  const scope = session.can_manage_team ? "team" : "self";
  return `${scope}:${session.team.id}:${session.key.id}`;
}

export function UserPortalApp() {
  const [token, setToken] = useState(() => sessionStorage.getItem(TOKEN_STORAGE_KEY) ?? "");
  const [draftToken, setDraftToken] = useState(token);
  const [authMode, setAuthMode] = useState<AuthMode>(() => token ? "api_key" : "none");
  const [rememberSession, setRememberSession] = useState(false);
  const [ssoProviders, setSSOProviders] = useState<SSOProvider[]>([]);
  const [authError, setAuthError] = useState("");
  const [theme, setTheme] = useState<Theme>(() => readTheme());

  useEffect(() => {
    if (token) {
      return;
    }
    let active = true;
    if (authMode === "api_key_session") {
      new UserApi("", "api_key_session").session()
        .then((session) => {
          if (active) {
            setAuthMode(sessionAuthMode(session));
            setAuthError("");
          }
        })
        .catch(() => {
          if (active) {
            setAuthMode("none");
          }
        });
      return () => {
        active = false;
      };
    }
    const api = new UserApi("");
    api.ssoProviders()
      .then((providers) => {
        if (active) {
          setSSOProviders(providers);
        }
      })
      .catch(() => {
        if (active) {
          setSSOProviders([]);
        }
      });
    api.session()
      .then((session) => {
        if (active) {
          setAuthMode(sessionAuthMode(session));
          setAuthError("");
        }
      })
      .catch(() => undefined);
    return () => {
      active = false;
    };
  }, [authMode, token]);

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
      setAuthError("API key is required.");
      return;
    }
    try {
      await new UserApi(nextToken).session();
      await new UserApi(nextToken).createPortalSession(rememberSession);
      let cookieSession: UserSession;
      try {
        cookieSession = await new UserApi("", "api_key_session").session();
      } catch (error) {
        await new UserApi("", "api_key_session").logoutPortalSession().catch(() => undefined);
        throw error;
      }
      sessionStorage.removeItem(TOKEN_STORAGE_KEY);
      setToken("");
      setDraftToken("");
      setAuthMode(sessionAuthMode(cookieSession));
      setAuthError("");
    } catch (error) {
      setAuthError(error instanceof Error ? error.message : "Authentication failed.");
    }
  }

  async function signOut() {
    try {
      if (authMode === "sso") {
        await new UserApi("").logoutSSO();
      } else if (authMode === "api_key_session") {
        await new UserApi("", "api_key_session").logoutPortalSession();
      }
    } catch (error) {
      setAuthError(readError(error));
      throw error;
    }
    sessionStorage.removeItem(TOKEN_STORAGE_KEY);
    setToken("");
    setDraftToken("");
    setRememberSession(false);
    setAuthMode("none");
    setAuthError("");
  }

  if (!token && authMode !== "sso" && authMode !== "api_key_session") {
    return (
      <AuthShell
        theme={theme}
        title="Dense-Mem Knowledge"
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
        <label htmlFor="user-api-key">API key</label>
        <input
          id="user-api-key"
          type="password"
          value={draftToken}
          onChange={(event) => setDraftToken(event.target.value)}
          autoComplete="current-password"
        />
        <label className="checkbox-row" htmlFor="remember-user-session">
          <input
            id="remember-user-session"
            type="checkbox"
            checked={rememberSession}
            onChange={(event) => setRememberSession(event.target.checked)}
          />
          Keep me signed in for 7 days
        </label>
        {authError && <p className="field-error" role="alert">{authError}</p>}
        <button className="primary-button" type="submit">
          <KeyRound size={17} aria-hidden="true" />
          Sign in
        </button>
        {ssoProviders.length > 0 && (
          <div className="sso-provider-list">
            {ssoProviders.map((provider) => (
              <button
                className="ghost-button"
                type="button"
                key={provider.id}
                onClick={() => {
                  window.location.href = new UserApi("").ssoStartUrl(provider.id);
                }}
              >
                <ShieldCheck size={17} aria-hidden="true" />
                {provider.name}
              </button>
            ))}
          </div>
        )}
      </AuthShell>
    );
  }

  return (
    <UserPortal
      token={token}
      authMode={authMode}
      theme={theme}
      onTokenChange={(nextToken) => {
        setToken(nextToken);
        setAuthMode(nextToken ? "api_key" : "none");
      }}
      onAuthModeChange={setAuthMode}
      onToggleTheme={toggleTheme}
      onSignOut={signOut}
    />
  );
}

function UserPortal({
  token,
  authMode,
  theme,
  onTokenChange,
  onAuthModeChange,
  onToggleTheme,
  onSignOut,
}: {
  token: string;
  authMode: AuthMode;
  theme: Theme;
  onTokenChange: (token: string) => void;
  onAuthModeChange: (mode: AuthMode) => void;
  onToggleTheme: () => void;
  onSignOut: () => Promise<void>;
}) {
  const api = useMemo(() => new UserApi(token, authMode === "none" ? "anonymous" : authMode), [token, authMode]);
  const [session, setSession] = useState<UserSession | null>(null);
  const [activeTab, setActiveTab] = useState<UserTab>("search");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [switchingTeam, setSwitchingTeam] = useState(false);
  const switchRequestId = useRef(0);

  async function loadSession() {
    setLoading(true);
    setError("");
    try {
      const next = await api.session();
      setSession(next);
      onAuthModeChange(sessionAuthMode(next));
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  async function switchSSOTeam(profileId: string) {
    const requestId = switchRequestId.current + 1;
    switchRequestId.current = requestId;
    setSwitchingTeam(true);
    setError("");
    try {
      const next = await api.switchSSOTeam(profileId);
      if (switchRequestId.current === requestId) {
        setSession(next);
        onAuthModeChange(sessionAuthMode(next));
      }
    } catch (err) {
      if (switchRequestId.current === requestId) {
        setError(readError(err));
      }
    } finally {
      if (switchRequestId.current === requestId) {
        setSwitchingTeam(false);
      }
    }
  }

  useEffect(() => {
    void loadSession();
  }, [api]);

  useEffect(() => {
    if (session && !session.can_manage_team && activeTab === "team") {
      setActiveTab("search");
    }
  }, [activeTab, session]);

  useEffect(() => {
    if (!canShowMyKey(session) && activeTab === "key") {
      setActiveTab("search");
    }
  }, [activeTab, session]);

  useEffect(() => {
    if (!canShowUsage(session) && activeTab === "usage") {
      setActiveTab("search");
    }
  }, [activeTab, session]);

  const navItems = [
    { id: "search", label: "Recall", icon: <Search size={17} aria-hidden="true" />, active: activeTab === "search", onClick: () => setActiveTab("search") },
    { id: "graph", label: "Graph", icon: <Network size={17} aria-hidden="true" />, active: activeTab === "graph", onClick: () => setActiveTab("graph") },
    { id: "dreams", label: "Dreams", icon: <Moon size={17} aria-hidden="true" />, active: activeTab === "dreams", onClick: () => setActiveTab("dreams") },
    ...(canShowUsage(session) ? [
      { id: "usage", label: "Usage", icon: <BarChart3 size={17} aria-hidden="true" />, active: activeTab === "usage", onClick: () => setActiveTab("usage") },
    ] : []),
    ...(session?.can_manage_team ? [
      { id: "team", label: "Team", icon: <Users size={17} aria-hidden="true" />, active: activeTab === "team", onClick: () => setActiveTab("team") },
    ] : []),
    ...(canShowMyKey(session) ? [
      { id: "key", label: "My key", icon: <KeyRound size={17} aria-hidden="true" />, active: activeTab === "key", onClick: () => setActiveTab("key") },
    ] : []),
  ];
  const ssoTeamOptions = session?.teams ?? [];

  return (
    <PortalShell
      theme={theme}
      title="Knowledge"
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
          <button className="icon-button" type="button" aria-label="Refresh session" onClick={() => void loadSession()}>
            <RefreshCw size={18} aria-hidden="true" />
          </button>
          <button
            className="ghost-button"
            type="button"
            onClick={() => void onSignOut().catch((err) => setError(readError(err)))}
          >
            <LogOut size={17} aria-hidden="true" />
            Sign out
          </button>
        </>
      )}
      navLabel="Knowledge navigation"
      navItemsLabel="Knowledge sections"
      navItems={navItems}
      navPlacement="top"
      contextBar={session && (
        <UserContextBar
          session={session}
          authMode={authMode}
          switchingTeam={switchingTeam}
          ssoTeamOptions={ssoTeamOptions}
          onSwitchTeam={switchSSOTeam}
        />
      )}
      detailLabel="Knowledge details"
      error={error}
    >
      <LazyPanelErrorBoundary key={activeTab}>
        <Suspense fallback={<LazyPanelFallback />}>
          {activeTab === "search" && <SearchPanel api={api} />}
          {activeTab === "graph" && <GraphPanel api={api} />}
          {activeTab === "dreams" && <UserDreamsPanel api={api} />}
          {activeTab === "usage" && session && canShowUsage(session) && (
            <UserTelemetryPanel key={userTelemetryIdentity(session)} api={api} session={session} />
          )}
          {activeTab === "team" && session?.can_manage_team && (
            <TeamManagementPanel
              api={api}
              session={session}
              onTeamUpdated={(team) => setSession((current) => current ? { ...current, team } : current)}
            />
          )}
          {activeTab === "key" && session && (
            <KeyPanel
              api={api}
              session={session}
              onRotated={(rotated) => {
                if (authMode === "api_key") {
                  sessionStorage.setItem(TOKEN_STORAGE_KEY, rotated.api_key);
                  onTokenChange(rotated.api_key);
                }
                setSession((current) => current ? {
                  ...current,
                  key: rotated.key,
                  can_rotate: rotated.key.scopes.includes("write"),
                  can_manage_team: rotated.key.role === "manager",
                } : current);
              }}
              onSSOKeyChanged={(key) => {
                setSession((current) => current ? {
                  ...current,
                  personal_key: key,
                  can_create_personal_key: false,
                  can_rotate_personal_key: key.scopes.includes("write") && (current.personal_key_max_scopes ?? []).includes("write"),
                } : current);
              }}
            />
          )}
        </Suspense>
      </LazyPanelErrorBoundary>
    </PortalShell>
  );
}

function UserContextBar({
  session,
  authMode,
  switchingTeam,
  ssoTeamOptions,
  onSwitchTeam,
}: {
  session: UserSession;
  authMode: AuthMode;
  switchingTeam: boolean;
  ssoTeamOptions: UserSession["teams"];
  onSwitchTeam: (profileId: string) => Promise<void>;
}) {
  return (
    <div className="session-context compact" aria-label="Current workspace">
      <span className="context-label">Team</span>
      {authMode === "sso" ? (
        <select
          aria-label="Active team"
          value={session.key.id}
          disabled={switchingTeam || (ssoTeamOptions?.length ?? 0) <= 1}
          onChange={(event) => void onSwitchTeam(event.target.value)}
        >
          {(ssoTeamOptions ?? []).map((item) => (
            <option value={item.key.id} key={item.key.id}>{item.team.name}</option>
          ))}
        </select>
      ) : (
        <strong className="team-select-chip">{session.team.name}</strong>
      )}
    </div>
  );
}

type LazyPanelErrorBoundaryProps = {
  children: ReactNode;
};

type LazyPanelErrorBoundaryState = {
  hasError: boolean;
};

class LazyPanelErrorBoundary extends Component<LazyPanelErrorBoundaryProps, LazyPanelErrorBoundaryState> {
  state: LazyPanelErrorBoundaryState = { hasError: false };

  static getDerivedStateFromError(): LazyPanelErrorBoundaryState {
    return { hasError: true };
  }

  render() {
    if (this.state.hasError) {
      return (
        <section className="surface">
          <SectionHeading title="Panel unavailable" />
          <p className="field-error" role="alert">This panel could not load.</p>
          <button className="ghost-button" type="button" onClick={() => window.location.reload()}>
            <RefreshCw size={17} aria-hidden="true" />
            Reload
          </button>
        </section>
      );
    }
    return this.props.children;
  }
}

function LazyPanelFallback() {
  return <LoadingState label="Loading panel" />;
}

function UserTelemetryPanel({ api, session }: { api: UserApi; session: UserSession }) {
  const [snapshot, setSnapshot] = useState<TelemetrySnapshot | null>(null);
  const [windowKey, setWindowKey] = useState<TelemetryWindowKey>("1h");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function loadTelemetry(nextWindow = windowKey) {
    setLoading(true);
    setError("");
    try {
      setSnapshot(await api.telemetry({ window: nextWindow }));
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadTelemetry();
  }, [windowKey]);

  return (
    <section className="surface">
      <TelemetryDashboard
        title={userTelemetryTitle(session)}
        snapshot={snapshot}
        windowKey={windowKey}
        loading={loading}
        error={error}
        onWindowChange={setWindowKey}
        onRefresh={() => void loadTelemetry()}
      />
    </section>
  );
}

function KeyPanel({
  api,
  session,
  onRotated,
  onSSOKeyChanged,
}: {
  api: UserApi;
  session: UserSession;
  onRotated: (rotated: RotateResponse) => void;
  onSSOKeyChanged: (key: UserSession["key"]) => void;
}) {
  const [createdKey, setCreatedKey] = useState("");
  const [name, setName] = useState("");
  const [permission, setPermission] = useState<ProfilePermission>(() => (session.personal_key_max_scopes?.includes("write") ? "read_write" : "read"));
  const [rateLimit, setRateLimit] = useState("120");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const isSSO = session.auth_method === "sso";
  const personalKey = isSSO ? session.personal_key : session.key;
  const maxScopes = session.personal_key_max_scopes ?? ["read"];
  const canCreateWrite = maxScopes.includes("write");
  const canRotate = isSSO ? session.can_rotate_personal_key : session.can_rotate;

  useEffect(() => {
    if (!canCreateWrite) {
      setPermission("read");
    }
  }, [canCreateWrite]);

  async function createSSOKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmedName = name.trim();
    const parsedRateLimit = Number.parseInt(rateLimit, 10);
    if (!Number.isFinite(parsedRateLimit) || parsedRateLimit <= 0) {
      setError("Rate limit must be greater than zero.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const created = await api.createSSOKey({
        name: trimmedName,
        scopes: permission === "read_write" && canCreateWrite ? ["read", "write"] : ["read"],
        rate_limit: parsedRateLimit,
      });
      setCreatedKey(created.api_key);
      onSSOKeyChanged(created.key);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function rotate() {
    if (!window.confirm("Regenerate this API key? The current key will stop working.")) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      const rotated = isSSO ? await api.rotateSSOKey() : await api.rotateKey();
      setCreatedKey(rotated.api_key);
      if (isSSO) {
        onSSOKeyChanged(rotated.key);
      } else {
        onRotated(rotated);
      }
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="surface">
      <SectionHeading title="My key" meta={personalKey?.scopes.includes("write") || permission === "read_write" ? "write" : "read"} />
      {createdKey && <CreatedKeyNotice apiKey={createdKey} onDismiss={() => setCreatedKey("")} />}
      {error && <div className="banner error" role="alert">{error}</div>}
      {personalKey ? (
        <>
          <dl className="key-detail-grid">
            <div><dt>Profile</dt><dd>{personalKey.name}</dd></div>
            <div><dt>Key</dt><dd><code>{displayKeySuffix(personalKey.key_suffix)}</code></dd></div>
            <div><dt>Role</dt><dd>{profileRoleLabel(personalKey.role)}</dd></div>
            <div><dt>Scopes</dt><dd>{personalKey.scopes.join(", ") || "none"}</dd></div>
            <div><dt>Rate limit</dt><dd>{personalKey.rate_limit}</dd></div>
            <div><dt>Created</dt><dd>{formatDate(personalKey.created_at)}</dd></div>
            <div><dt>Last used</dt><dd>{personalKey.last_used_at ? formatDate(personalKey.last_used_at) : "Never"}</dd></div>
            <div><dt>Expires</dt><dd>{personalKey.expires_at ? formatDate(personalKey.expires_at) : "Never"}</dd></div>
          </dl>
          <div className="button-row">
            <button className="primary-button" type="button" disabled={!canRotate || busy} onClick={() => void rotate()}>
              <RefreshCw size={16} aria-hidden="true" />
              Regenerate key
            </button>
          </div>
        </>
      ) : (
        <form className="key-form" onSubmit={createSSOKey}>
          <label htmlFor="sso-personal-key-name">Profile name</label>
          <input id="sso-personal-key-name" value={name} onChange={(event) => setName(event.target.value)} />
          <label htmlFor="sso-personal-key-permission">Permission</label>
          <select
            id="sso-personal-key-permission"
            value={permission}
            disabled={!canCreateWrite}
            onChange={(event) => setPermission(event.target.value as ProfilePermission)}
          >
            {canCreateWrite && <option value="read_write">Read/write</option>}
            <option value="read">Read only</option>
          </select>
          <label htmlFor="sso-personal-key-rate-limit">Rate limit</label>
          <input id="sso-personal-key-rate-limit" inputMode="numeric" value={rateLimit} onChange={(event) => setRateLimit(event.target.value)} />
          <button className="primary-button span" type="submit" disabled={busy || !session.can_create_personal_key}>
            <KeyRound size={16} aria-hidden="true" />
            Create API key
          </button>
        </form>
      )}
    </section>
  );
}

function CreatedKeyNotice({ apiKey, onDismiss }: { apiKey: string; onDismiss: () => void }) {
  return (
    <SecretBox
      value={apiKey}
      valueLabel="Generated API key"
      copyLabel="Copy API key"
      dismissLabel="Dismiss API key"
      onDismiss={onDismiss}
    />
  );
}

function readTheme(): Theme {
  return localStorage.getItem(THEME_STORAGE_KEY) === "dark" ? "dark" : "light";
}

function readError(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed.";
}

function displayKeySuffix(suffix: string | null): string {
  return suffix ? `******${suffix}` : "Unavailable";
}

function profileRoleLabel(role: UserSession["key"]["role"] | null | undefined): string {
  return role === "manager" ? "Manager" : "Member";
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}
