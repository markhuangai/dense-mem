import { FormEvent, ReactNode, useEffect, useMemo, useState } from "react";
import {
  BarChart3,
  Check,
  Copy,
  FileText,
  GitBranch,
  KeyRound,
  Layers3,
  LogOut,
  Moon,
  RefreshCw,
  Search,
  ShieldCheck,
  Sun,
  X,
} from "lucide-react";
import { TelemetryDashboard } from "../telemetry/TelemetryDashboard";
import { TelemetrySnapshot, TelemetryWindowKey } from "../telemetry/types";
import {
  Claim,
  Community,
  Fact,
  Fragment,
  RecallHit,
  RotateResponse,
  UserApi,
  UserSession,
} from "./api";

const TOKEN_STORAGE_KEY = "denseMem.userApiKey";
const THEME_STORAGE_KEY = "denseMem.userTheme";

type Theme = "light" | "dark";
type UserTab = "search" | "usage" | "facts" | "claims" | "fragments" | "communities" | "key";

export function UserPortalApp() {
  const [token, setToken] = useState(() => sessionStorage.getItem(TOKEN_STORAGE_KEY) ?? "");
  const [draftToken, setDraftToken] = useState(token);
  const [authError, setAuthError] = useState("");
  const [theme, setTheme] = useState<Theme>(() => readTheme());

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
      sessionStorage.setItem(TOKEN_STORAGE_KEY, nextToken);
      setToken(nextToken);
      setAuthError("");
    } catch (error) {
      setAuthError(error instanceof Error ? error.message : "Authentication failed.");
    }
  }

  function signOut() {
    sessionStorage.removeItem(TOKEN_STORAGE_KEY);
    setToken("");
    setDraftToken("");
  }

  if (!token) {
    return (
      <main className="auth-shell" data-theme={theme}>
        <form className="auth-panel" onSubmit={submitToken}>
          <div className="brand-row">
            <span className="brand-mark"><ShieldCheck size={20} aria-hidden="true" /></span>
            <h1>Dense-Mem Knowledge</h1>
            <button
              className="icon-button theme-toggle"
              type="button"
              aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
              onClick={toggleTheme}
            >
              {theme === "dark" ? <Sun size={17} aria-hidden="true" /> : <Moon size={17} aria-hidden="true" />}
            </button>
          </div>
          <label htmlFor="user-api-key">API key</label>
          <input
            id="user-api-key"
            type="password"
            value={draftToken}
            onChange={(event) => setDraftToken(event.target.value)}
            autoComplete="current-password"
          />
          {authError && <p className="field-error" role="alert">{authError}</p>}
          <button className="primary-button" type="submit">
            <KeyRound size={17} aria-hidden="true" />
            Sign in
          </button>
        </form>
      </main>
    );
  }

  return (
    <UserPortal
      token={token}
      theme={theme}
      onTokenChange={setToken}
      onToggleTheme={toggleTheme}
      onSignOut={signOut}
    />
  );
}

function UserPortal({
  token,
  theme,
  onTokenChange,
  onToggleTheme,
  onSignOut,
}: {
  token: string;
  theme: Theme;
  onTokenChange: (token: string) => void;
  onToggleTheme: () => void;
  onSignOut: () => void;
}) {
  const api = useMemo(() => new UserApi(token), [token]);
  const [session, setSession] = useState<UserSession | null>(null);
  const [activeTab, setActiveTab] = useState<UserTab>("search");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  async function loadSession() {
    setLoading(true);
    setError("");
    try {
      setSession(await api.session());
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadSession();
  }, [api]);

  return (
    <main className="app-shell" data-theme={theme}>
      <header className="topbar">
        <div className="brand-row">
          <span className="brand-mark"><ShieldCheck size={20} aria-hidden="true" /></span>
          <h1>Dense-Mem Knowledge</h1>
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
          <button className="icon-button" type="button" aria-label="Refresh session" onClick={() => void loadSession()}>
            <RefreshCw size={18} aria-hidden="true" />
          </button>
          <button className="ghost-button" type="button" onClick={onSignOut}>
            <LogOut size={17} aria-hidden="true" />
            Sign out
          </button>
        </div>
      </header>

      {error && <div className="banner error" role="alert">{error}</div>}

      <section className="workspace">
        <aside className="control-sidebar" aria-label="Knowledge navigation">
          <nav className="portal-tabs" aria-label="Knowledge sections">
            <TabButton active={activeTab === "search"} icon={<Search size={17} aria-hidden="true" />} label="Recall" onClick={() => setActiveTab("search")} />
            <TabButton active={activeTab === "usage"} icon={<BarChart3 size={17} aria-hidden="true" />} label="Usage" onClick={() => setActiveTab("usage")} />
            <TabButton active={activeTab === "facts"} icon={<ShieldCheck size={17} aria-hidden="true" />} label="Facts" onClick={() => setActiveTab("facts")} />
            <TabButton active={activeTab === "claims"} icon={<GitBranch size={17} aria-hidden="true" />} label="Claims" onClick={() => setActiveTab("claims")} />
            <TabButton active={activeTab === "fragments"} icon={<FileText size={17} aria-hidden="true" />} label="Fragments" onClick={() => setActiveTab("fragments")} />
            <TabButton active={activeTab === "communities"} icon={<Layers3 size={17} aria-hidden="true" />} label="Communities" onClick={() => setActiveTab("communities")} />
            <TabButton active={activeTab === "key"} icon={<KeyRound size={17} aria-hidden="true" />} label="My key" onClick={() => setActiveTab("key")} />
          </nav>
          <div className="section-heading">
            <div>
              <h2>{session?.team.name ?? (loading ? "Loading" : "Team")}</h2>
              {session && <p className="section-subtitle">{shortId(session.team.id)}</p>}
            </div>
          </div>
          {session && (
            <div className="key-summary">
              <span>{session.key.name}</span>
              <code>{displayKeySuffix(session.key.key_suffix)}</code>
            </div>
          )}
        </aside>

        <section className="detail-pane" aria-label="Knowledge details">
          {activeTab === "search" && <SearchPanel api={api} />}
          {activeTab === "usage" && <UserTelemetryPanel api={api} />}
          {activeTab === "facts" && <FactsPanel api={api} />}
          {activeTab === "claims" && <ClaimsPanel api={api} />}
          {activeTab === "fragments" && <FragmentsPanel api={api} />}
          {activeTab === "communities" && <CommunitiesPanel api={api} />}
          {activeTab === "key" && session && (
            <KeyPanel
              api={api}
              session={session}
              onRotated={(rotated) => {
                sessionStorage.setItem(TOKEN_STORAGE_KEY, rotated.api_key);
                onTokenChange(rotated.api_key);
                setSession((current) => current ? { ...current, key: rotated.key, can_rotate: rotated.key.scopes.includes("write") } : current);
              }}
            />
          )}
        </section>
      </section>
    </main>
  );
}

function TabButton({
  active,
  icon,
  label,
  onClick,
}: {
  active: boolean;
  icon: ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      className={active ? "tab-button active" : "tab-button"}
      type="button"
      aria-current={active ? "page" : undefined}
      onClick={onClick}
    >
      {icon}
      {label}
    </button>
  );
}

function UserTelemetryPanel({ api }: { api: UserApi }) {
  const [snapshot, setSnapshot] = useState<TelemetrySnapshot | null>(null);
  const [windowKey, setWindowKey] = useState<TelemetryWindowKey>("1h");
  const [scope, setScope] = useState<"self" | "team">("self");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function loadTelemetry(nextWindow = windowKey, nextScope = scope) {
    setLoading(true);
    setError("");
    try {
      setSnapshot(await api.telemetry({ window: nextWindow, scope: nextScope }));
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadTelemetry();
  }, [windowKey, scope]);

  return (
    <section className="surface">
      <TelemetryDashboard
        title="Usage"
        snapshot={snapshot}
        windowKey={windowKey}
        loading={loading}
        error={error}
        onWindowChange={setWindowKey}
        onRefresh={() => void loadTelemetry()}
        controls={(
          <>
            <label htmlFor="user-telemetry-scope">Scope</label>
            <select id="user-telemetry-scope" value={scope} onChange={(event) => setScope(event.target.value as "self" | "team")}>
              <option value="self">My profile</option>
              <option value="team">Team</option>
            </select>
          </>
        )}
      />
    </section>
  );
}

function SearchPanel({ api }: { api: UserApi }) {
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<RecallHit[]>([]);
  const [communities, setCommunities] = useState<Community[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = query.trim();
    if (!trimmed) {
      setError("Query is required.");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const [nextHits, nextCommunities] = await Promise.all([
        api.recall(trimmed, 10),
        api.listCommunities(20).catch(() => ({ items: [] as Community[] })),
      ]);
      setHits(nextHits);
      setCommunities(nextCommunities.items);
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  const entities = deriveEntities(hits, communities);

  return (
    <section className="surface">
      <div className="section-heading">
        <h2>Recall</h2>
        <span>{hits.length}</span>
      </div>
      <form className="search-form" onSubmit={submit}>
        <label htmlFor="recall-query">Keyword</label>
        <input id="recall-query" value={query} onChange={(event) => setQuery(event.target.value)} />
        <button className="primary-button" type="submit" disabled={loading}>
          <Search size={16} aria-hidden="true" />
          Search
        </button>
      </form>
      {error && <div className="banner error" role="alert">{error}</div>}
      {entities.length > 0 && (
        <div className="entity-strip" aria-label="Related entities">
          {entities.map((entity) => <span className="status-pill neutral" key={entity}>{entity}</span>)}
        </div>
      )}
      {loading && <div className="table-placeholder">Loading</div>}
      {!loading && <RecallResults hits={hits} />}
    </section>
  );
}

function RecallResults({ hits }: { hits: RecallHit[] }) {
  if (hits.length === 0) {
    return <div className="table-placeholder">No recall results</div>;
  }
  return (
    <div className="knowledge-list">
      {hits.map((hit, index) => {
        const item = hit.fact ?? hit.claim ?? hit.fragment;
        return (
          <article className="knowledge-item" key={recallKey(hit, index)}>
            <div className="knowledge-item-head">
              <span className="status-pill neutral">{tierLabel(hit)}</span>
              <small>{scoreLabel(hit.score ?? hit.final_score)}</small>
            </div>
            <h3>{itemTitle(item)}</h3>
            <p>{itemBody(item)}</p>
          </article>
        );
      })}
    </div>
  );
}

function FactsPanel({ api }: { api: UserApi }) {
  const { data, loading, error, reload } = useKnowledge(() => api.listFacts(20), [api]);
  return (
    <section className="surface">
      <PanelHeading title="Facts" count={data?.items.length ?? 0} onRefresh={reload} />
      {error && <div className="banner error" role="alert">{error}</div>}
      {loading && <div className="table-placeholder">Loading</div>}
      {!loading && <FactList items={data?.items ?? []} />}
    </section>
  );
}

function ClaimsPanel({ api }: { api: UserApi }) {
  const { data, loading, error, reload } = useKnowledge(() => api.listClaims(20), [api]);
  return (
    <section className="surface">
      <PanelHeading title="Claims" count={data?.items.length ?? 0} onRefresh={reload} />
      {error && <div className="banner error" role="alert">{error}</div>}
      {loading && <div className="table-placeholder">Loading</div>}
      {!loading && <ClaimList items={data?.items ?? []} />}
    </section>
  );
}

function FragmentsPanel({ api }: { api: UserApi }) {
  const { data, loading, error, reload } = useKnowledge(() => api.listFragments(20), [api]);
  return (
    <section className="surface">
      <PanelHeading title="Fragments" count={data?.items.length ?? 0} onRefresh={reload} />
      {error && <div className="banner error" role="alert">{error}</div>}
      {loading && <div className="table-placeholder">Loading</div>}
      {!loading && <FragmentList items={data?.items ?? []} />}
    </section>
  );
}

function CommunitiesPanel({ api }: { api: UserApi }) {
  const { data, loading, error, reload } = useKnowledge(() => api.listCommunities(20), [api]);
  return (
    <section className="surface">
      <PanelHeading title="Communities" count={data?.items.length ?? 0} onRefresh={reload} />
      {error && <div className="banner error" role="alert">{error}</div>}
      {loading && <div className="table-placeholder">Loading</div>}
      {!loading && <CommunityList items={data?.items ?? []} />}
    </section>
  );
}

function KeyPanel({
  api,
  session,
  onRotated,
}: {
  api: UserApi;
  session: UserSession;
  onRotated: (rotated: RotateResponse) => void;
}) {
  const [createdKey, setCreatedKey] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function rotate() {
    if (!window.confirm("Regenerate this API key? The current key will stop working.")) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      const rotated = await api.rotateKey();
      setCreatedKey(rotated.api_key);
      onRotated(rotated);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="surface">
      <div className="section-heading">
        <h2>My key</h2>
        <span>{session.can_rotate ? "write" : "read"}</span>
      </div>
      {createdKey && <CreatedKeyNotice apiKey={createdKey} onDismiss={() => setCreatedKey("")} />}
      {error && <div className="banner error" role="alert">{error}</div>}
      <dl className="key-detail-grid">
        <div><dt>Profile</dt><dd>{session.key.name}</dd></div>
        <div><dt>Key</dt><dd><code>{displayKeySuffix(session.key.key_suffix)}</code></dd></div>
        <div><dt>Scopes</dt><dd>{session.key.scopes.join(", ") || "none"}</dd></div>
        <div><dt>Rate limit</dt><dd>{session.key.rate_limit}</dd></div>
        <div><dt>Created</dt><dd>{formatDate(session.key.created_at)}</dd></div>
        <div><dt>Last used</dt><dd>{session.key.last_used_at ? formatDate(session.key.last_used_at) : "Never"}</dd></div>
        <div><dt>Expires</dt><dd>{session.key.expires_at ? formatDate(session.key.expires_at) : "Never"}</dd></div>
      </dl>
      <div className="button-row">
        <button className="primary-button" type="button" disabled={!session.can_rotate || busy} onClick={() => void rotate()}>
          <RefreshCw size={16} aria-hidden="true" />
          Regenerate key
        </button>
      </div>
    </section>
  );
}

function PanelHeading({ title, count, onRefresh }: { title: string; count: number; onRefresh: () => void }) {
  return (
    <div className="section-heading">
      <h2>{title}</h2>
      <div className="button-row">
        <span>{count}</span>
        <button className="icon-button" type="button" aria-label={`Refresh ${title}`} onClick={onRefresh}>
          <RefreshCw size={16} aria-hidden="true" />
        </button>
      </div>
    </div>
  );
}

function FactList({ items }: { items: Fact[] }) {
  if (items.length === 0) {
    return <div className="table-placeholder">No facts</div>;
  }
  return (
    <div className="knowledge-list">
      {items.map((fact) => (
        <article className="knowledge-item" key={fact.fact_id}>
          <div className="knowledge-item-head">
            <span className="status-pill neutral">{fact.status}</span>
            <small>{scoreLabel(fact.truth_score)}</small>
          </div>
          <h3>{fact.subject}</h3>
          <p>{fact.predicate}: {fact.object}</p>
        </article>
      ))}
    </div>
  );
}

function ClaimList({ items }: { items: Claim[] }) {
  if (items.length === 0) {
    return <div className="table-placeholder">No claims</div>;
  }
  return (
    <div className="knowledge-list">
      {items.map((claim) => (
        <article className="knowledge-item" key={claim.claim_id}>
          <div className="knowledge-item-head">
            <span className="status-pill neutral">{claim.status}</span>
            <small>{claim.entailment_verdict || claim.modality}</small>
          </div>
          <h3>{claim.subject}</h3>
          <p>{claim.predicate}: {claim.object}</p>
        </article>
      ))}
    </div>
  );
}

function FragmentList({ items }: { items: Fragment[] }) {
  if (items.length === 0) {
    return <div className="table-placeholder">No fragments</div>;
  }
  return (
    <div className="knowledge-list">
      {items.map((fragment) => (
        <article className="knowledge-item" key={fragment.fragment_id || fragment.id}>
          <div className="knowledge-item-head">
            <span className="status-pill neutral">{fragment.source_type || "fragment"}</span>
            <small>{fragment.status || "active"}</small>
          </div>
          <h3>{fragment.source || shortId(fragment.fragment_id || fragment.id)}</h3>
          <p>{fragment.content}</p>
        </article>
      ))}
    </div>
  );
}

function CommunityList({ items }: { items: Community[] }) {
  if (items.length === 0) {
    return <div className="table-placeholder">No communities</div>;
  }
  return (
    <div className="knowledge-list">
      {items.map((community) => (
        <article className="knowledge-item" key={community.community_id}>
          <div className="knowledge-item-head">
            <span className="status-pill neutral">Level {community.level}</span>
            <small>{community.member_count} members</small>
          </div>
          <h3>{community.top_entities?.slice(0, 3).join(", ") || shortId(community.community_id)}</h3>
          <p>{community.summary}</p>
        </article>
      ))}
    </div>
  );
}

function CreatedKeyNotice({ apiKey, onDismiss }: { apiKey: string; onDismiss: () => void }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    await navigator.clipboard?.writeText(apiKey);
    setCopied(true);
  }

  return (
    <div className="secret-box" role="status">
      <div><code>{apiKey}</code></div>
      <div className="secret-actions">
        <button className="icon-button" type="button" aria-label="Copy API key" onClick={() => void copy()}>
          {copied ? <Check size={17} aria-hidden="true" /> : <Copy size={17} aria-hidden="true" />}
        </button>
        <button className="icon-button" type="button" aria-label="Dismiss API key" onClick={onDismiss}>
          <X size={17} aria-hidden="true" />
        </button>
      </div>
    </div>
  );
}

function useKnowledge<T>(load: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  async function reload() {
    setLoading(true);
    setError("");
    try {
      setData(await load());
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void reload();
  }, deps);

  return { data, loading, error, reload };
}

function deriveEntities(hits: RecallHit[], communities: Community[]): string[] {
  const values = new Set<string>();
  for (const hit of hits) {
    const source = hit.fact ?? hit.claim;
    if (source) {
      addEntity(values, source.subject);
      addEntity(values, source.object);
      addEntity(values, source.predicate);
    }
  }
  for (const community of communities) {
    for (const entity of community.top_entities ?? []) {
      addEntity(values, entity);
    }
  }
  return Array.from(values).slice(0, 20);
}

function addEntity(values: Set<string>, value: string | undefined) {
  const trimmed = value?.trim();
  if (trimmed) {
    values.add(trimmed);
  }
}

function itemTitle(item: Fact | Claim | Fragment | undefined): string {
  if (!item) {
    return "Result";
  }
  if ("content" in item) {
    return item.source || shortId(item.fragment_id || item.id);
  }
  return item.subject;
}

function itemBody(item: Fact | Claim | Fragment | undefined): string {
  if (!item) {
    return "";
  }
  if ("content" in item) {
    return item.content;
  }
  return `${item.predicate}: ${item.object}`;
}

function tierLabel(hit: RecallHit): string {
  if (hit.fact) {
    return "Fact";
  }
  if (hit.claim) {
    return "Claim";
  }
  return "Fragment";
}

function recallKey(hit: RecallHit, index: number): string {
  return hit.fact?.fact_id ?? hit.claim?.claim_id ?? hit.fragment?.fragment_id ?? String(index);
}

function scoreLabel(value: number | undefined): string {
  if (value === undefined || Number.isNaN(value)) {
    return "";
  }
  return value.toFixed(value >= 1 ? 0 : 3);
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

function shortId(id: string): string {
  return id.length <= 8 ? id : id.slice(0, 8);
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}
