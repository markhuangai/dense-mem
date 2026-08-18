import { Component, FormEvent, lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import {
  BarChart3,
  Check,
  Copy,
  KeyRound,
  LogOut,
  Moon,
  Network,
  RefreshCw,
  Search,
  ShieldCheck,
  Sun,
  Trash2,
  Users,
} from "lucide-react";
import { TelemetrySnapshot, TelemetryWindowKey } from "../telemetry/types";
import { useVisiblePolling } from "../telemetry/useVisiblePolling";
import {
  RotateResponse,
  PrivateMemoryOperation,
  SSOProvider,
  UserApi,
  UserCredential,
  UserSession,
} from "./api";
import { AuthShell, LoadingState, PortalShell, SecretBox, SectionHeading, writeClipboardText } from "../ui/components";
import { SearchPanel } from "./SearchPanel";

const TelemetryDashboard = lazy(() => import("../telemetry/TelemetryDashboard").then((module) => ({ default: module.TelemetryDashboard })));
const TeamManagementPanel = lazy(() => import("./TeamManagementPanel").then((module) => ({ default: module.TeamManagementPanel })));
const UserDreamsPanel = lazy(() => import("./DreamsPanel").then((module) => ({ default: module.UserDreamsPanel })));
const GraphPanel = lazy(() => import("./GraphPanel").then((module) => ({ default: module.GraphPanel })));

const TOKEN_STORAGE_KEY = "denseMem.userApiKey";
const THEME_STORAGE_KEY = "denseMem.userTheme";

type Theme = "light" | "dark";
type AuthMode = "none" | "api_key" | "api_key_session" | "sso";
type UserTab = "search" | "graph" | "dreams" | "usage" | "team" | "credential";
type CredentialPermission = "read" | "read_write";

function sessionAuthMode(session: UserSession, token: string): AuthMode {
  if (!session.credential) {
    return "sso";
  }
  return token ? "api_key" : "api_key_session";
}

function canManageTeam(session: UserSession | null): boolean {
  return session?.membership.role === "manager";
}

function canShowMyCredential(session: UserSession | null): boolean {
  if (!session) {
    return false;
  }
  if (session.credential) {
    return true;
  }
  return !canManageTeam(session) && session.membership.grants.includes("read");
}

function canShowUsage(session: UserSession | null): boolean {
  return Boolean(session?.membership.grants.includes("write"));
}

function userTelemetryTitle(session: UserSession): string {
  return canManageTeam(session) ? "Team usage" : "My credential usage";
}

function userTelemetryIdentity(session: UserSession): string {
  const scope = canManageTeam(session) ? "team" : "self";
  return `${scope}:${session.team.id}:${session.credential?.id ?? "sso"}`;
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
    sessionStorage.removeItem(TOKEN_STORAGE_KEY);
  }, []);

  useEffect(() => {
    if (token) {
      return;
    }
    let active = true;
    if (authMode === "api_key_session") {
      new UserApi("", "api_key_session").session()
        .then((session) => {
          if (active) {
            setAuthMode(sessionAuthMode(session, ""));
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
          setAuthMode(sessionAuthMode(session, ""));
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
      setAuthMode(sessionAuthMode(cookieSession, ""));
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
      onAuthModeChange(sessionAuthMode(next, token));
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  async function switchSSOTeam(teamId: string) {
    const requestId = switchRequestId.current + 1;
    switchRequestId.current = requestId;
    setSwitchingTeam(true);
    setError("");
    try {
      const next = await api.switchSSOTeam(teamId);
      if (switchRequestId.current === requestId) {
        setSession(next);
        onAuthModeChange(sessionAuthMode(next, token));
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
    if (session && !canManageTeam(session) && activeTab === "team") {
      setActiveTab("search");
    }
  }, [activeTab, session]);

  useEffect(() => {
    if (!canShowMyCredential(session) && activeTab === "credential") {
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
    ...(canManageTeam(session) ? [
      { id: "team", label: "Team", icon: <Users size={17} aria-hidden="true" />, active: activeTab === "team", onClick: () => setActiveTab("team") },
    ] : []),
    ...(canShowMyCredential(session) ? [
      { id: "credential", label: "My credential", icon: <KeyRound size={17} aria-hidden="true" />, active: activeTab === "credential", onClick: () => setActiveTab("credential") },
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
          {activeTab === "team" && session && canManageTeam(session) && (
            <TeamManagementPanel
              api={api}
              session={session}
              onTeamUpdated={(team) => setSession((current) => current ? { ...current, team } : current)}
            />
          )}
          {activeTab === "credential" && session && (
            <CredentialPanel
              api={api}
              session={session}
              onRotated={(rotated) => {
                if (authMode === "api_key") {
                  onTokenChange(rotated.api_key);
                }
                setSession((current) => current ? {
                  ...current,
                  credential: rotated.credential,
                } : current);
              }}
              onSSOCredentialsChanged={(credentials) => {
                setSession((current) => current ? {
                  ...current,
                  personal_credentials: credentials,
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
  onSwitchTeam: (teamId: string) => Promise<void>;
}) {
  const [copied, setCopied] = useState(false);
  const configuredBase = session.mcp_public_base_url.trim().replace(/\/+$/, "");
  const browserBase = window.location.origin.replace(/\/+$/, "");
  const mcpURL = `${configuredBase || browserBase}/teams/${session.team.id}/mcp`;

  useEffect(() => {
    setCopied(false);
  }, [mcpURL]);

  async function copyMCPURL() {
    if (await writeClipboardText(mcpURL, null)) {
      setCopied(true);
    }
  }

  return (
    <div className="session-context compact mcp-context" aria-label="Current workspace">
      <div className="mcp-context-team">
        <span className="context-label">Team</span>
        {authMode === "sso" ? (
          <select
            aria-label="Active team"
            value={session.team.id}
            disabled={switchingTeam || (ssoTeamOptions?.length ?? 0) <= 1}
            onChange={(event) => void onSwitchTeam(event.target.value)}
          >
            {(ssoTeamOptions ?? []).map((item) => (
              <option value={item.team.id} key={item.team.id}>{item.team.name}</option>
            ))}
          </select>
        ) : (
          <strong className="team-select-chip">{session.team.name}</strong>
        )}
      </div>
      <div className="mcp-context-value">
        <span className="context-label">Team ID</span>
        <code>{session.team.id}</code>
      </div>
      <div className="mcp-context-value mcp-context-endpoint">
        <span className="context-label">MCP URL</span>
        <code>{mcpURL}</code>
        <button className="icon-button compact" type="button" aria-label="Copy MCP URL" onClick={() => void copyMCPURL()}>
          {copied ? <Check size={15} aria-hidden="true" /> : <Copy size={15} aria-hidden="true" />}
        </button>
      </div>
      {!configuredBase && (
        <small className="mcp-origin-fallback">Using this browser origin because MCP_PUBLIC_BASE_URL is not configured.</small>
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
  const requestRef = useRef(0);

  async function loadTelemetry(nextWindow = windowKey, signal?: AbortSignal) {
    const requestID = ++requestRef.current;
    setLoading(true);
    setError("");
    try {
      setSnapshot(await api.telemetry({ window: nextWindow }, signal));
    } catch (err) {
      if (!isAbortError(err)) {
        setError(readError(err));
      }
    } finally {
      if (requestRef.current === requestID) {
        setLoading(false);
      }
    }
  }

  const refreshTelemetry = useVisiblePolling(
    (signal) => loadTelemetry(windowKey, signal),
    [api, windowKey],
  );

  return (
    <section className="surface">
      <TelemetryDashboard
        title={userTelemetryTitle(session)}
        snapshot={snapshot}
        windowKey={windowKey}
        loading={loading}
        error={error}
        onWindowChange={setWindowKey}
        onRefresh={() => void refreshTelemetry()}
      />
    </section>
  );
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function CredentialPanel({
  api,
  session,
  onRotated,
  onSSOCredentialsChanged,
}: {
  api: UserApi;
  session: UserSession;
  onRotated: (rotated: RotateResponse) => void;
  onSSOCredentialsChanged: (credentials: UserCredential[]) => void;
}) {
  const [createdAPIKey, setCreatedAPIKey] = useState("");
  const [name, setName] = useState("");
  const [permission, setPermission] = useState<CredentialPermission>(() => (session.membership.grants.includes("write") ? "read_write" : "read"));
  const [rateLimit, setRateLimit] = useState("120");
  const [expiresAt, setExpiresAt] = useState("");
  const [memoryBinding, setMemoryBinding] = useState<"shared_only" | "profile_private" | "credential_private">("profile_private");
  const [selectedCredentialID, setSelectedCredentialID] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [erasureMessage, setErasureMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const isSSO = session.credential === null;
  const personalCredentials = isSSO ? (session.personal_credentials ?? []) : [];
  const selectedPersonalCredential = personalCredentials.find((credential) => credential.id === selectedCredentialID) ?? personalCredentials[0] ?? null;
  const personalCredential = isSSO ? selectedPersonalCredential : session.credential;
  const maxScopes = session.membership.grants;
  const canCreateWrite = maxScopes.includes("write");
  const canRotate = Boolean(personalCredential?.scopes.includes("write") && maxScopes.includes("write"));
  const canRevoke = Boolean(personalCredential);

  useEffect(() => {
    if (!isSSO) {
      setSelectedCredentialID(null);
      return;
    }
    if (selectedCredentialID && personalCredentials.some((credential) => credential.id === selectedCredentialID)) {
      return;
    }
    setSelectedCredentialID(personalCredentials[0]?.id ?? null);
  }, [isSSO, personalCredentials, selectedCredentialID]);

  useEffect(() => {
    if (!canCreateWrite) {
      setPermission("read");
    }
  }, [canCreateWrite]);

  async function createSSOCredential(event: FormEvent<HTMLFormElement>) {
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
      const created = await api.createSSOCredential({
        name: trimmedName,
        scopes: permission === "read_write" && canCreateWrite ? ["read", "write"] : ["read"],
        rate_limit: parsedRateLimit,
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
        memory_binding: memoryBinding,
      });
      setCreatedAPIKey(created.api_key);
      const nextCredentials = [...personalCredentials, created.credential];
      setSelectedCredentialID(created.credential.id);
      onSSOCredentialsChanged(nextCredentials);
      setName("");
      setExpiresAt("");
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
      const rotated = isSSO && personalCredential
        ? await api.rotateSSOCredential(personalCredential.id)
        : await api.rotateCredential();
      setCreatedAPIKey(rotated.api_key);
      if (isSSO) {
        onSSOCredentialsChanged(personalCredentials.map((credential) => credential.id === rotated.credential.id ? rotated.credential : credential));
      } else {
        onRotated(rotated);
      }
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function deleteCredential() {
    if (!personalCredential || !window.confirm(`Permanently delete ${personalCredential.name}? The key will stop working immediately. Credential-private memory is physically erased; profile-private and team-shared memory are preserved.`)) {
      return;
    }
    setBusy(true);
    setError("");
    setErasureMessage("");
    try {
      const operation = await api.deleteSSOCredential(personalCredential.id, privateMemoryIdempotencyKey("delete-credential", personalCredential.id));
      const nextCredentials = personalCredentials.filter((credential) => credential.id !== personalCredential.id);
      setSelectedCredentialID(nextCredentials[0]?.id ?? null);
      onSSOCredentialsChanged(nextCredentials);
      setCreatedAPIKey("");
      void pollPrivateMemoryErasure(api, operation, setErasureMessage, "Credential deletion");
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function erasePrivateMemory() {
    const profilePrivate = isSSO;
    const target = profilePrivate ? "profile-private" : "credential-private";
    if (!window.confirm(`Permanently erase your ${target} memory for this team? The credential remains active, but erased content cannot be recovered without an authorized backup restore.`)) {
      return;
    }
    setBusy(true);
    setError("");
    setErasureMessage("");
    try {
      const operation = profilePrivate
        ? await api.eraseSSOPrivateMemory(privateMemoryIdempotencyKey("erase-profile-private", session.team.id))
        : await api.eraseCredentialPrivateMemory(privateMemoryIdempotencyKey("erase-credential-private", personalCredential?.id ?? session.team.id));
      void pollPrivateMemoryErasure(api, operation, setErasureMessage, `${profilePrivate ? "Profile-private" : "Credential-private"} erasure`);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="surface">
      <SectionHeading title="My credentials" meta={personalCredential?.scopes.includes("write") || permission === "read_write" ? "write" : "read"} />
      {createdAPIKey && <CreatedCredentialNotice apiKey={createdAPIKey} onDismiss={() => setCreatedAPIKey("")} />}
      {error && <div className="banner error" role="alert">{error}</div>}
      {erasureMessage && <div className="banner neutral" role="status">{erasureMessage}</div>}
      {isSSO && personalCredentials.length > 0 && (
        <div className="credential-list" aria-label="Owned API credentials">
          <h3>Owned API keys</h3>
          <div className="credential-list-items">
            {personalCredentials.map((credential) => (
              <button
                className={`credential-list-item${credential.id === personalCredential?.id ? " selected" : ""}`}
                type="button"
                key={credential.id}
                aria-pressed={credential.id === personalCredential?.id}
                onClick={() => setSelectedCredentialID(credential.id)}
              >
                <span>{credential.name}</span>
                <small>{displayKeySuffix(credential.key_suffix)} · {credential.memory_binding}</small>
              </button>
            ))}
          </div>
        </div>
      )}
      {personalCredential && (
        <>
          <dl className="key-detail-grid">
            <div><dt>Credential</dt><dd>{personalCredential.name}</dd></div>
            <div><dt>Key</dt><dd><code>{displayKeySuffix(personalCredential.key_suffix)}</code></dd></div>
            <div><dt>Role</dt><dd>{credentialRoleLabel(personalCredential.role)}</dd></div>
            <div><dt>Scopes</dt><dd>{personalCredential.scopes.join(", ") || "none"}</dd></div>
            <div><dt>Rate limit</dt><dd>{personalCredential.rate_limit}</dd></div>
            <div><dt>Memory binding</dt><dd>{personalCredential.memory_binding}</dd></div>
            <div><dt>Memory space</dt><dd>{personalCredential.memory_space_kind}</dd></div>
            <div><dt>Created</dt><dd>{formatDate(personalCredential.created_at)}</dd></div>
            <div><dt>Last used</dt><dd>{personalCredential.last_used_at ? formatDate(personalCredential.last_used_at) : "Never"}</dd></div>
            <div><dt>Expires</dt><dd>{personalCredential.expires_at ? formatDate(personalCredential.expires_at) : "Never"}</dd></div>
          </dl>
          <div className="button-row">
            <button className="primary-button" type="button" disabled={!canRotate || busy} onClick={() => void rotate()}>
              <RefreshCw size={16} aria-hidden="true" />
              Regenerate key
            </button>
            {isSSO && (
              <button className="danger-button" type="button" disabled={!canRevoke || busy} onClick={() => void deleteCredential()}>
                <Trash2 size={16} aria-hidden="true" />
                Permanently delete key
              </button>
            )}
          </div>
        </>
      )}
      {isSSO && (
        <form className={`key-form${personalCredential ? " credential-create-form" : ""}`} onSubmit={createSSOCredential}>
          {personalCredential && <h3>Create another API key</h3>}
          <label htmlFor="sso-personal-credential-name">Credential name</label>
          <input id="sso-personal-credential-name" value={name} onChange={(event) => setName(event.target.value)} />
          <label htmlFor="sso-personal-credential-permission">Permission</label>
          <select
            id="sso-personal-credential-permission"
            value={permission}
            disabled={!canCreateWrite}
            onChange={(event) => setPermission(event.target.value as CredentialPermission)}
          >
            {canCreateWrite && <option value="read_write">Read/write</option>}
            <option value="read">Read only</option>
          </select>
          <label htmlFor="sso-personal-credential-rate-limit">Rate limit</label>
          <input id="sso-personal-credential-rate-limit" inputMode="numeric" value={rateLimit} onChange={(event) => setRateLimit(event.target.value)} />
          <label htmlFor="sso-personal-credential-binding">Memory binding</label>
          <select id="sso-personal-credential-binding" value={memoryBinding} onChange={(event) => setMemoryBinding(event.target.value as typeof memoryBinding)}>
            <option value="profile_private">Profile-private (shared by your SSO identity)</option>
            <option value="credential_private">Credential-private (isolated to this key)</option>
            <option value="shared_only">Team-shared only</option>
          </select>
          <label htmlFor="sso-personal-credential-expires">Expires (optional)</label>
          <input id="sso-personal-credential-expires" type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} />
          <button className="primary-button span" type="submit" disabled={busy || !session.membership.grants.includes("read")}>
            <KeyRound size={16} aria-hidden="true" />
            Create API key
          </button>
        </form>
      )}
      {(isSSO || personalCredential?.memory_space_kind === "credential_private") && (
        <div className="private-memory-danger-zone">
          <div>
            <strong>{isSSO ? "Profile-private memory" : "Credential-private memory"}</strong>
            <span>{isSSO ? "Erases private memory shared by your SSO identity in this team. Keys and team-shared memory remain." : "Erases private memory isolated to this credential. The credential remains active."}</span>
          </div>
          <button className="danger-button" type="button" disabled={busy} onClick={() => void erasePrivateMemory()}>
            <Trash2 size={16} aria-hidden="true" />
            Erase private memory
          </button>
        </div>
      )}
    </section>
  );
}

async function pollPrivateMemoryErasure(
  api: UserApi,
  initial: PrivateMemoryOperation,
  onMessage: (message: string) => void,
  label: string,
) {
  let operation = initial;
  onMessage(privateMemoryOperationMessage(label, operation));
  for (let attempt = 0; operation.status !== "completed" && attempt < 40; attempt += 1) {
    await new Promise((resolve) => window.setTimeout(resolve, 500));
    try {
      operation = await api.getPrivateMemoryErasure(operation.operation_id);
      onMessage(privateMemoryOperationMessage(label, operation));
    } catch (error) {
      onMessage(`${label} was accepted, but status polling failed: ${readError(error)}`);
      return;
    }
  }
}

function privateMemoryOperationMessage(label: string, operation: PrivateMemoryOperation): string {
  if (operation.status === "completed") {
    return `${label} completed.`;
  }
  return `${label} ${operation.status}. Operation ${operation.operation_id.slice(0, 8)}.`;
}

function privateMemoryIdempotencyKey(action: string, target: string): string {
  return `${action}:${target}:${crypto.randomUUID()}`;
}

function CreatedCredentialNotice({ apiKey, onDismiss }: { apiKey: string; onDismiss: () => void }) {
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

function credentialRoleLabel(role: UserCredential["role"] | null | undefined): string {
  return role === "manager" ? "Manager" : "Member";
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}
