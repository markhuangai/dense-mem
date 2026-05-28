import { FormEvent, useEffect, useMemo, useState } from "react";
import {
  Ban,
  Check,
  Copy,
  KeyRound,
  LogOut,
  Pencil,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";
import {
  ApiError,
  TeamProfile,
  ControlApi,
  CreatedTeamProfile,
  Team,
  SecurityBan,
  SecuritySettings,
} from "./api";

const TOKEN_STORAGE_KEY = "denseMem.controlToken";

type LoadState = "idle" | "loading" | "error";

export function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem(TOKEN_STORAGE_KEY) ?? "");
  const [draftToken, setDraftToken] = useState(token);
  const [authError, setAuthError] = useState("");

  const api = useMemo(() => (token ? new ControlApi(token) : null), [token]);

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
      <main className="auth-shell">
        <form className="auth-panel" onSubmit={submitToken}>
          <div className="brand-row">
            <span className="brand-mark"><ShieldCheck size={20} aria-hidden="true" /></span>
            <h1>Dense-Mem Control</h1>
          </div>
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
        </form>
      </main>
    );
  }

  return <Portal api={api} onSignOut={clearToken} />;
}

function Portal({ api, onSignOut }: { api: ControlApi; onSignOut: () => void }) {
  const [teams, setTeams] = useState<Team[]>([]);
  const [selectedTeamId, setSelectedTeamId] = useState("");
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

  return (
    <main className="app-shell">
      <header className="topbar">
        <div className="brand-row">
          <span className="brand-mark"><ShieldCheck size={20} aria-hidden="true" /></span>
          <h1>Dense-Mem Control</h1>
        </div>
        <div className="topbar-actions">
          <button className="icon-button" type="button" aria-label="Refresh teams" onClick={() => void loadTeams()}>
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
        <aside className="team-pane" aria-label="Teams">
          <div className="section-heading">
            <h2>Teams</h2>
            <span>{teams.length}</span>
          </div>
          <TeamCreateForm api={api} onCreated={(team) => void loadTeams(team.id)} />
          <TeamTable
            teams={teams}
            selectedTeamId={selectedTeamId}
            loading={loadState === "loading"}
            onSelect={setSelectedTeamId}
          />
        </aside>

        <section className="detail-pane" aria-label="Team details">
          {selectedTeam ? (
            <>
              <TeamEditor
                api={api}
                team={selectedTeam}
                onUpdated={(team) => {
                  setTeams((current) => current.map((item) => (item.id === team.id ? team : item)));
                }}
                onDeleted={() => void loadTeams()}
              />
              <TeamProfilesPanel api={api} team={selectedTeam} />
            </>
          ) : (
            <div className="empty-state">{loadState === "loading" ? "Loading" : "No teams"}</div>
          )}
          <SecurityPanel api={api} />
        </section>
      </section>
    </main>
  );
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

function TeamTable({
  teams,
  selectedTeamId,
  loading,
  onSelect,
}: {
  teams: Team[];
  selectedTeamId: string;
  loading: boolean;
  onSelect: (teamId: string) => void;
}) {
  if (loading && teams.length === 0) {
    return <div className="table-placeholder">Loading</div>;
  }

  return (
    <div className="table-wrap">
      <table className="data-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {teams.map((team) => (
            <tr
              key={team.id}
              className={team.id === selectedTeamId ? "selected" : ""}
              onClick={() => onSelect(team.id)}
            >
              <td>
                <button className="row-button" type="button" onClick={() => onSelect(team.id)}>
                  {team.name}
                </button>
              </td>
              <td>{formatDate(team.updated_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
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
    <section className="surface">
      <div className="section-heading">
        <h2>{team.name}</h2>
        <span>{shortId(team.id)}</span>
      </div>
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
    </section>
  );
}

function TeamProfilesPanel({ api, team }: { api: ControlApi; team: Team }) {
  const [keys, setKeys] = useState<TeamProfile[]>([]);
  const [createdKey, setCreatedKey] = useState<CreatedTeamProfile | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [deletingKeyId, setDeletingKeyId] = useState("");

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
    <section className="surface">
      <div className="section-heading">
        <h2>Profiles</h2>
        <span>{keys.length}</span>
      </div>
      {createdKey && <CreatedKeyNotice createdKey={createdKey} onDismiss={() => setCreatedKey(null)} />}
      {error && <div className="banner error" role="alert">{error}</div>}
      <TeamProfileCreateForm api={api} team={team} onCreated={(value) => {
        setCreatedKey(value);
        void loadKeys();
      }} />
      {loading && <div className="table-placeholder">Loading</div>}
      {!loading && (
        <TeamProfileTable
          keys={keys}
          deletingKeyId={deletingKeyId}
          onDelete={(keyId) => void deleteKey(keyId)}
        />
      )}
    </section>
  );
}

function TeamProfileTable({
  keys,
  deletingKeyId,
  onDelete,
}: {
  keys: TeamProfile[];
  deletingKeyId: string;
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
            <th>Created</th>
            <th>Last used</th>
            <th className="actions-cell">Delete</th>
          </tr>
        </thead>
        <tbody>
          {keys.map((key) => {
            const display = displayKeySuffix(key);
            return (
              <tr key={key.id}>
                <td>{key.name}</td>
                <td><code>{display}</code></td>
                <td>{formatDate(key.created_at)}</td>
                <td>{key.last_used_at ? formatDate(key.last_used_at) : "Never"}</td>
                <td className="actions-cell">
                  <button
                    className="icon-button danger"
                    type="button"
                    aria-label={`Delete profile ${key.name}`}
                    disabled={deletingKeyId === key.id}
                    onClick={() => onDelete(key.id)}
                  >
                    <Trash2 size={16} aria-hidden="true" />
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function TeamProfileCreateForm({
  api,
  team,
  onCreated,
}: {
  api: ControlApi;
  team: Team;
  onCreated: (created: CreatedTeamProfile) => void;
}) {
  const [name, setName] = useState("default profile");
  const [rateLimit, setRateLimit] = useState("120");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

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
      <label htmlFor="rate-limit">Rate limit</label>
      <input id="rate-limit" inputMode="numeric" value={rateLimit} onChange={(event) => setRateLimit(event.target.value)} />
      {error && <p className="field-error span" role="alert">{error}</p>}
      <button className="primary-button span" type="submit" disabled={busy}>
        <KeyRound size={16} aria-hidden="true" />
        Create profile
      </button>
    </form>
  );
}

function CreatedKeyNotice({ createdKey, onDismiss }: { createdKey: CreatedTeamProfile; onDismiss: () => void }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    await navigator.clipboard?.writeText(createdKey.api_key);
    setCopied(true);
  }

  return (
    <div className="secret-box" role="status">
      <div>
        <code>{createdKey.api_key}</code>
      </div>
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

function SecurityPanel({ api }: { api: ControlApi }) {
  const [settings, setSettings] = useState<SecuritySettings | null>(null);
  const [bans, setBans] = useState<SecurityBan[]>([]);
  const [includeExpired, setIncludeExpired] = useState(false);
  const [ip, setIp] = useState("");
  const [reason, setReason] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function loadSecurity(nextIncludeExpired = includeExpired) {
    setBusy(true);
    setError("");
    try {
      const [nextSettings, page] = await Promise.all([
        api.getSecuritySettings(),
        api.listSecurityBans(nextIncludeExpired),
      ]);
      setSettings(nextSettings);
      setBans(page.data);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void loadSecurity();
  }, []);

  async function saveSettings(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!settings) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      setSettings(await api.updateSecuritySettings(settings));
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function createBan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!ip.trim()) {
      setError("IP address is required.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.createSecurityBan({
        ip: ip.trim(),
        reason: reason.trim() || "manual ban",
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
      });
      setIp("");
      setReason("");
      setExpiresAt("");
      await loadSecurity();
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  async function deleteBan(ipAddress: string) {
    if (!window.confirm(`Remove IP ban for ${ipAddress}?`)) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.deleteSecurityBan(ipAddress);
      await loadSecurity();
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  function updateNumber(field: keyof Pick<SecuritySettings, "failure_threshold" | "failure_window_seconds" | "ban_duration_seconds">, value: string) {
    const parsed = Number.parseInt(value, 10);
    setSettings((current) => current ? { ...current, [field]: Number.isFinite(parsed) ? parsed : 0 } : current);
  }

  return (
    <section className="surface">
      <div className="section-heading">
        <h2>Security</h2>
        <button className="icon-button" type="button" aria-label="Refresh security" onClick={() => void loadSecurity()} disabled={busy}>
          <RefreshCw size={16} aria-hidden="true" />
        </button>
      </div>
      {error && <div className="banner error" role="alert">{error}</div>}
      {settings ? (
        <form className="security-grid" onSubmit={saveSettings}>
          <label className="check-row span" htmlFor="security-enabled">
            <input
              id="security-enabled"
              type="checkbox"
              checked={settings.enabled}
              onChange={(event) => setSettings({ ...settings, enabled: event.target.checked })}
            />
            IP ban protection
          </label>
          <label htmlFor="failure-threshold">Failures</label>
          <input id="failure-threshold" inputMode="numeric" value={settings.failure_threshold} onChange={(event) => updateNumber("failure_threshold", event.target.value)} />
          <label htmlFor="failure-window">Window seconds</label>
          <input id="failure-window" inputMode="numeric" value={settings.failure_window_seconds} onChange={(event) => updateNumber("failure_window_seconds", event.target.value)} />
          <label htmlFor="ban-duration">Ban seconds</label>
          <input id="ban-duration" inputMode="numeric" value={settings.ban_duration_seconds} onChange={(event) => updateNumber("ban_duration_seconds", event.target.value)} />
          <button className="primary-button span" type="submit" disabled={busy}>
            <ShieldCheck size={16} aria-hidden="true" />
            Save security
          </button>
        </form>
      ) : (
        <div className="table-placeholder">Loading</div>
      )}

      <form className="security-grid security-ban-form" onSubmit={createBan}>
        <label htmlFor="ban-ip">IP address</label>
        <input id="ban-ip" value={ip} onChange={(event) => setIp(event.target.value)} />
        <label htmlFor="ban-reason">Reason</label>
        <input id="ban-reason" value={reason} onChange={(event) => setReason(event.target.value)} />
        <label htmlFor="ban-expires">Expires</label>
        <input id="ban-expires" type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} />
        <button className="danger-button span" type="submit" disabled={busy}>
          <Ban size={16} aria-hidden="true" />
          Add ban
        </button>
      </form>

      <label className="check-row include-row" htmlFor="include-expired">
        <input
          id="include-expired"
          type="checkbox"
          checked={includeExpired}
          onChange={(event) => {
            setIncludeExpired(event.target.checked);
            void loadSecurity(event.target.checked);
          }}
        />
        Include expired
      </label>
      <SecurityBanTable bans={bans} busy={busy} onDelete={(ipAddress) => void deleteBan(ipAddress)} />
    </section>
  );
}

function SecurityBanTable({
  bans,
  busy,
  onDelete,
}: {
  bans: SecurityBan[];
  busy: boolean;
  onDelete: (ip: string) => void;
}) {
  if (bans.length === 0) {
    return <div className="table-placeholder">No IP bans</div>;
  }

  return (
    <div className="table-wrap">
      <table className="data-table security-table">
        <thead>
          <tr>
            <th>IP</th>
            <th>Source</th>
            <th>Failures</th>
            <th>Expires</th>
            <th className="actions-cell">Delete</th>
          </tr>
        </thead>
        <tbody>
          {bans.map((ban) => (
            <tr key={ban.ip}>
              <td><code>{ban.ip}</code></td>
              <td>{ban.source}</td>
              <td>{ban.failure_count}</td>
              <td>{ban.expires_at ? formatDate(ban.expires_at) : "Never"}</td>
              <td className="actions-cell">
                <button
                  className="icon-button danger"
                  type="button"
                  aria-label={`Delete IP ban ${ban.ip}`}
                  disabled={busy}
                  onClick={() => onDelete(ban.ip)}
                >
                  <Trash2 size={16} aria-hidden="true" />
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function readError(error: unknown): string {
  if (error instanceof ApiError || error instanceof Error) {
    return error.message;
  }
  return "Request failed.";
}

function formatDate(value: string): string {
  if (!value) {
    return "";
  }
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
}

function displayKeySuffix(key: TeamProfile): string {
  const suffix = key.key_suffix?.trim();
  return suffix ? `******${suffix}` : "Unavailable";
}

function shortId(value: string): string {
  return value.slice(0, 8);
}
