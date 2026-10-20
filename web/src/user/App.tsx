import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { Input } from "../ui/primitives/input";
import { Checkbox } from "../ui/primitives/checkbox";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Component, FormEvent, lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import {
  Check,
  Copy,
  KeyRound,
  LogOut,
  Network,
  RefreshCw,
  Search,
  ShieldCheck,
  Trash2,
  Users,
} from "lucide-react";
import {
  RotateResponse,
  SSOProvider,
  UserApi,
  UserCredential,
  UserSession,
} from "./api";
import { FieldRow, AuthShell, LoadingState, PortalShell, SecretBox, SectionHeading, writeClipboardText } from "../ui/components";
import { SearchPanel } from "./SearchPanel";
import { MemoryAccessSummary, memoryPermissionLabel } from "../credentialPermissions";
import { PortalEnvironment, portalHref, usePortalNavigation, useUnsavedChanges } from "../ui/navigation";
import { ThemeControl, usePortalTheme } from "../ui/theme";
import type { ThemeName } from "../ui/theme";
import { WorkspacePicker } from "../ui/workspace-picker";
import { useConfirmation } from "../ui/confirmation";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "../ui/primitives/dialog";

const TeamManagementPanel = lazy(() => import("./TeamManagementPanel").then((module) => ({ default: module.TeamManagementPanel })));
const GraphPanel = lazy(() => import("./GraphPanel").then((module) => ({ default: module.GraphPanel })));
const OntologyPanel = lazy(() => import("./OntologyPanel").then((module) => ({ default: module.OntologyPanel })));

const TOKEN_STORAGE_KEY = "denseMem.userApiKey";
const THEME_STORAGE_KEY = "denseMem.userTheme";

type Theme = ThemeName;
type AuthMode = "none" | "api_key" | "api_key_session" | "sso";
type UserTab = "search" | "graph" | "team" | "credential" | "ontology";
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

export function UserPortalApp() { return <PortalEnvironment><UserAuth /></PortalEnvironment>; }

function UserAuth() {
  const [token, setToken] = useState(() => sessionStorage.getItem(TOKEN_STORAGE_KEY) ?? "");
  const [draftToken, setDraftToken] = useState(token);
  const [authMode, setAuthMode] = useState<AuthMode>(() => token ? "api_key" : "none");
  const [rememberSession, setRememberSession] = useState(false);
  const [ssoProviders, setSSOProviders] = useState<SSOProvider[]>([]);
  const [authError, setAuthError] = useState("");
  const { theme, preference, setPreference } = usePortalTheme(THEME_STORAGE_KEY);
  const themeMenu = <ThemeControl preference={preference} onChange={setPreference} />;

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
        actions={themeMenu}
      >
        <Label htmlFor="user-api-key">API key</Label>
        <Input
          id="user-api-key"
          type="password"
          value={draftToken}
          onChange={(event) => setDraftToken(event.target.value)}
          autoComplete="current-password"
        />
        <Label className="checkbox-row" htmlFor="remember-user-session">
          <Checkbox
            id="remember-user-session"
            checked={rememberSession}
            onCheckedChange={(event) => setRememberSession((event === true))}
          />
          Keep me signed in for 7 days
        </Label>
        {authError && <p className="field-error" role="alert">{authError}</p>}
        <Button variant="default" className="primary-button" type="submit">
          <KeyRound size={17} aria-hidden="true" />
          Sign in
        </Button>
        {ssoProviders.length > 0 && (
          <div className="sso-provider-list">
            {ssoProviders.map((provider) => (
              <Button variant="outline"
                className="ghost-button"
                type="button"
                key={provider.id}
                onClick={() => {
                  window.location.href = new UserApi("").ssoStartUrl(provider.id);
                }}
              >
                <ShieldCheck size={17} aria-hidden="true" />
                {provider.name}
              </Button>
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
      themeMenu={themeMenu}
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
  themeMenu,
  onSignOut,
}: {
  token: string;
  authMode: AuthMode;
  theme: Theme;
  onTokenChange: (token: string) => void;
  onAuthModeChange: (mode: AuthMode) => void;
  themeMenu: ReactNode;
  onSignOut: () => Promise<void>;
}) {
  const api = useMemo(() => new UserApi(token, authMode === "none" ? "anonymous" : authMode), [token, authMode]);
  const [session, setSession] = useState<UserSession | null>(null);
  const { parameters, navigate, transition } = usePortalNavigation();
  const requestedTab = parameters.get("section");
  const allowedTabs: UserTab[] = ["search", "graph", ...(canManageTeam(session) ? ["ontology", "team"] as const : []), ...(canShowMyCredential(session) ? ["credential"] as const : [])];
  const activeTab: UserTab = allowedTabs.includes(requestedTab as UserTab) ? requestedTab as UserTab : "search";
  function setActiveTab(section: UserTab) { return navigate({ section, tab: null, team_id: null, team_tab: null, remember_view: null, attempt_id: null, invocation_id: null }); }
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [switchingTeam, setSwitchingTeam] = useState(false);
  const [teamEditorResetVersion, setTeamEditorResetVersion] = useState(0);
  const switchRequestId = useRef(0);

  async function loadSession() {
    const requestId = ++switchRequestId.current;
    setLoading(true);
    setError("");
    try {
      const next = await api.session();
      if (requestId !== switchRequestId.current) return;
      setSession(next);
      onAuthModeChange(sessionAuthMode(next, token));
    } catch (err) {
      if (requestId === switchRequestId.current) setError(readError(err));
    } finally {
      if (requestId === switchRequestId.current) setLoading(false);
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
        return true;
      }
      return false;
    } catch (err) {
      if (switchRequestId.current === requestId) {
        setError(readError(err));
      }
      return false;
    } finally {
      if (switchRequestId.current === requestId) {
        setSwitchingTeam(false);
      }
    }
  }

  useEffect(() => {
    void loadSession();
    return () => { switchRequestId.current += 1; };
  }, [api]);

  const navItems = [
    { id: "search", label: "Search memory", group: "Explore", icon: <Search size={17} aria-hidden="true" />, active: activeTab === "search", href: portalHref({ section: "search", tab: null }), disabled: !session || switchingTeam, onClick: () => setActiveTab("search") },
    { id: "graph", label: "Graph", group: "Explore", icon: <Network size={17} aria-hidden="true" />, active: activeTab === "graph", href: portalHref({ section: "graph", tab: null }), disabled: !session || switchingTeam, onClick: () => setActiveTab("graph") },
    ...(canManageTeam(session) ? [
      { id: "ontology", label: "Ontology", group: "Manage", icon: <Network size={17} aria-hidden="true" />, active: activeTab === "ontology", href: portalHref({ section: "ontology", tab: null }), disabled: !session || switchingTeam, onClick: () => setActiveTab("ontology") },
      { id: "team", label: "Team", group: "Manage", icon: <Users size={17} aria-hidden="true" />, active: activeTab === "team", href: portalHref({ section: "team", tab: null }), disabled: !session || switchingTeam, onClick: () => setActiveTab("team") },
    ] : []),
    ...(canShowMyCredential(session) ? [
      { id: "credential", label: "My credentials", group: "Account", icon: <KeyRound size={17} aria-hidden="true" />, active: activeTab === "credential", href: portalHref({ section: "credential", tab: null }), disabled: !session || switchingTeam, onClick: () => setActiveTab("credential") },
    ] : []),
  ];
  const ssoTeamOptions = session?.teams ?? [];

  return (
    <PortalShell
      theme={theme}
      title="Knowledge"
      icon={<span className="brand-initials" aria-hidden="true">DM</span>}
      topbarActions={<>{session && <UserContextBar session={session} />}{themeMenu}
        <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label="Refresh session" disabled={switchingTeam || loading} onClick={() => void transition(() => { setTeamEditorResetVersion((version) => version + 1); return loadSession(); })}><RefreshCw size={17} aria-hidden="true" /></Button>
        <Button variant="ghost" className="ghost-button" type="button" aria-label="Sign out" onClick={() => void transition(async () => { try { await onSignOut(); return true; } catch (err) { setError(readError(err)); return false; } })}><LogOut size={17} aria-hidden="true" /><span className="sign-out-label">Sign out</span></Button>
      </>}
      navLabel="Knowledge navigation"
      navItemsLabel="Knowledge sections"
      navItems={navItems}
      workspacePicker={session && <WorkspacePicker options={authMode === "sso" ? ssoTeamOptions.map((item) => item.team) : [session.team]} selectedID={session.team.id} disabled={authMode !== "sso" || ssoTeamOptions.length <= 1} loading={switchingTeam} onSelect={async (id) => { const changed = await transition(() => switchSSOTeam(id)); if (changed) await navigate({ section: "search", tab: null }); return changed; }} />}
      contextBar={<span className="current-team" aria-label="Current workspace">{session?.team.name ?? "Knowledge"}</span>}
      detailLabel="Knowledge details"
      error={error || (session && requestedTab && !allowedTabs.includes(requestedTab as UserTab) ? "This section is unavailable for your current access." : "")}
    >
      {(!session || switchingTeam) ? <LoadingState label={switchingTeam ? "Switching team" : loading ? "Loading workspace" : "Workspace unavailable. Refresh your session to retry."} /> : <LazyPanelErrorBoundary key={session.team.id + ":" + activeTab}>
        <Suspense fallback={<LazyPanelFallback />}>
          {activeTab === "search" && <SearchPanel api={api} />}
          {activeTab === "graph" && <GraphPanel api={api} />}
          {activeTab === "ontology" && session && canManageTeam(session) && <OntologyPanel key={session.team.id} api={api} session={session} />}
          {activeTab === "team" && session && canManageTeam(session) && (
            <TeamManagementPanel
              api={api}
              session={session}
              resetVersion={teamEditorResetVersion}
              refreshing={loading}
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
      </LazyPanelErrorBoundary>}
    </PortalShell>
  );
}

function UserContextBar({ session }: { session: UserSession }) {
  const [copied, setCopied] = useState(false);
  const copyFallbackRef = useRef<HTMLInputElement>(null);
  const configuredBase = session.mcp_public_base_url.trim().replace(/\/+$/, "");
  const browserBase = window.location.origin.replace(/\/+$/, "");
  const mcpURL = (configuredBase || browserBase) + "/teams/" + session.team.id + "/mcp";
  useEffect(() => { setCopied(false); }, [mcpURL]);
  useEffect(() => {
    if (!copied) return;
    const timeout = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(timeout);
  }, [copied]);
  return <Dialog><DialogTrigger asChild><Button variant="outline" size="sm"><Network size={16} aria-hidden="true" /><span>Connect</span></Button></DialogTrigger>
    <DialogContent><DialogHeader><DialogTitle>Connect to Dense-Mem</DialogTitle><DialogDescription>Use this team-scoped MCP endpoint in your client.</DialogDescription></DialogHeader>
      <div className="connection-details">
        <div><h3>Team</h3><strong>{session.team.name}</strong><code aria-label="Team ID">{session.team.id}</code></div>
        <div><h3>MCP URL</h3><code aria-label="MCP URL">{mcpURL}</code><Input ref={copyFallbackRef} className="sr-only" value={mcpURL} readOnly tabIndex={-1} aria-hidden="true" />
          <Button variant="outline" size="sm" className="mt-3" aria-label={copied ? "MCP URL copied" : "Copy MCP URL"} onClick={async () => setCopied(await writeClipboardText(mcpURL, copyFallbackRef.current))}>{copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}{copied ? "Copied" : "Copy MCP URL"}</Button>
          {!configuredBase && <p className="form-meta">Using this browser origin because MCP_PUBLIC_BASE_URL is not configured.</p>}
        </div>
        <div><h3>Your memory access</h3><MemoryAccessSummary access={session.memory_access} /></div>
      </div>
    </DialogContent>
  </Dialog>;
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
          <Button variant="outline" className="ghost-button" type="button" onClick={() => window.location.reload()}>
            <RefreshCw size={17} aria-hidden="true" />
            Reload
          </Button>
        </section>
      );
    }
    return this.props.children;
  }
}

function LazyPanelFallback() {
  return <LoadingState label="Loading panel" />;
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
  const [selectedCredentialID, setSelectedCredentialID] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const confirm = useConfirmation();
  const isSSO = session.credential === null;
  const personalCredentials = isSSO ? (session.personal_credentials ?? []) : [];
  const selectedPersonalCredential = personalCredentials.find((credential) => credential.id === selectedCredentialID) ?? personalCredentials[0] ?? null;
  const personalCredential = isSSO ? selectedPersonalCredential : session.credential;
  const maxScopes = session.membership.grants;
  const canCreateWrite = maxScopes.includes("write");
  const canRotate = Boolean(personalCredential?.scopes.includes("write") && maxScopes.includes("write"));
  const canRevoke = Boolean(personalCredential);
  const clearDirty = useUnsavedChanges(isSSO && Boolean(name || expiresAt || rateLimit !== "120" || permission !== (canCreateWrite ? "read_write" : "read")));

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
      });
      setCreatedAPIKey(created.api_key);
      const nextCredentials = [...personalCredentials, created.credential];
      setSelectedCredentialID(created.credential.id);
      onSSOCredentialsChanged(nextCredentials);
      setName("");
      setExpiresAt("");
      setRateLimit("120");
      setPermission(canCreateWrite ? "read_write" : "read");
      clearDirty();
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function rotate() {
    if (!await confirm({ title: "Regenerate API key?", description: "The current key will stop working. Copy the replacement key before closing it.", confirmLabel: "Regenerate key", destructive: true })) {
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

  async function revoke() {
    if (!personalCredential || !await confirm({ title: "Revoke API key?", description: "Revoke " + personalCredential.name + "? This key will stop working.", confirmLabel: "Revoke key", destructive: true })) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.revokeSSOCredential(personalCredential.id);
      const nextCredentials = personalCredentials.filter((credential) => credential.id !== personalCredential.id);
      setSelectedCredentialID(nextCredentials[0]?.id ?? null);
      onSSOCredentialsChanged(nextCredentials);
      setCreatedAPIKey("");
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="surface">
      <SectionHeading title="My credentials" meta={memoryPermissionLabel((personalCredential?.memory_access ?? session.memory_access).team_shared, true)} />
      <MemoryAccessSummary access={session.memory_access} />
      <p>Each API key has separate private memory. Your SSO session uses its own private space. Choose private or team shared in each Remember request.</p>
      {createdAPIKey && <CreatedCredentialNotice apiKey={createdAPIKey} onDismiss={() => setCreatedAPIKey("")} />}
      {error && <div className="banner error" role="alert">{error}</div>}
      {isSSO && personalCredentials.length > 0 && (
        <div className="credential-list" aria-label="Owned API credentials">
          <h3>Owned API keys</h3>
          <div className="credential-list-items">
            {personalCredentials.map((credential) => (
              <Button variant="ghost"
                className={`credential-list-item${credential.id === personalCredential?.id ? " selected" : ""}`}
                type="button"
                key={credential.id}
                aria-pressed={credential.id === personalCredential?.id}
                onClick={() => setSelectedCredentialID(credential.id)}
              >
                <span>{credential.name}</span>
                <small>{displayKeySuffix(credential.key_suffix)}</small>
              </Button>
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
            <div><dt>Private memory</dt><dd>{memoryPermissionLabel(personalCredential.memory_access.private)}</dd></div>
            <div><dt>Team memory</dt><dd>{memoryPermissionLabel(personalCredential.memory_access.team_shared, true)}</dd></div>
            <div><dt>Created</dt><dd>{formatDate(personalCredential.created_at)}</dd></div>
            <div><dt>Last used</dt><dd>{personalCredential.last_used_at ? formatDate(personalCredential.last_used_at) : "Never"}</dd></div>
            <div><dt>Expires</dt><dd>{personalCredential.expires_at ? formatDate(personalCredential.expires_at) : "Never"}</dd></div>
          </dl>
          <div className="button-row">
            <Button variant="default" className="primary-button" type="button" disabled={!canRotate || busy} onClick={() => void rotate()}>
              <RefreshCw size={16} aria-hidden="true" />
              Regenerate key
            </Button>
            {isSSO && (
              <Button variant="outline" className="ghost-button" type="button" disabled={!canRevoke || busy} onClick={() => void revoke()}>
                <Trash2 size={16} aria-hidden="true" />
                Revoke key
              </Button>
            )}
          </div>
        </>
      )}
      {isSSO && (
        <form className={`key-form${personalCredential ? " credential-create-form" : ""}`} onSubmit={createSSOCredential}>
          {personalCredential && <h3>Create another API key</h3>}
          <FieldRow label="Credential name" htmlFor="sso-personal-credential-name">
            <Input id="sso-personal-credential-name" value={name} onChange={(event) => setName(event.target.value)} />
          </FieldRow>
          <FieldRow label="Permission" htmlFor="sso-personal-credential-permission">
            <NativeSelect
              id="sso-personal-credential-permission"
              value={permission}
              disabled={!canCreateWrite}
              onChange={(event) => setPermission(event.target.value as CredentialPermission)}
            >
              {canCreateWrite && <NativeSelectOption value="read_write">Team read/write</NativeSelectOption>}
              <NativeSelectOption value="read">Team-read-only</NativeSelectOption>
            </NativeSelect>
          </FieldRow>
          <FieldRow label="Rate limit" htmlFor="sso-personal-credential-rate-limit">
            <Input id="sso-personal-credential-rate-limit" inputMode="numeric" value={rateLimit} onChange={(event) => setRateLimit(event.target.value)} />
          </FieldRow>
          <FieldRow label="Expires (optional)" htmlFor="sso-personal-credential-expires">
            <Input id="sso-personal-credential-expires" type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} />
          </FieldRow>
          <Button variant="default" className="primary-button span" type="submit" disabled={busy || !session.membership.grants.includes("read")}>
            <KeyRound size={16} aria-hidden="true" />
            Create API key
          </Button>
        </form>
      )}
    </section>
  );
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
