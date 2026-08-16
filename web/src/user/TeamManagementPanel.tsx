import { FormEvent, useEffect, useState } from "react";
import { Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { CreatedCredential, UserApi, UserCredential, UserSession, UserTeam } from "./api";
import { LoadingState, SecretBox, SectionHeading } from "../ui/components";
import { TeamDreamingConfigForm } from "../teamDreamingConfig";
import { CredentialPermissionCheckboxes, normalizeCredentialScopes } from "../credentialPermissions";

export function TeamManagementPanel({
  api,
  session,
  onTeamUpdated,
}: {
  api: UserApi;
  session: UserSession;
  onTeamUpdated: (team: UserTeam) => void;
}) {
  const [teamName, setTeamName] = useState(session.team.name);
  const [teamDescription, setTeamDescription] = useState(session.team.description ?? "");
  const [credentials, setCredentials] = useState<UserCredential[]>([]);
  const [createdCredential, setCreatedCredential] = useState<CreatedCredential | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [teamBusy, setTeamBusy] = useState(false);
  const [savingCredentialId, setSavingCredentialId] = useState("");
  const [savingScopesCredentialId, setSavingScopesCredentialId] = useState("");
  const [rotatingCredentialId, setRotatingCredentialId] = useState("");
  const [deletingCredentialId, setDeletingCredentialId] = useState("");

  async function loadCredentials() {
    setLoading(true);
    setError("");
    try {
      const page = await api.listTeamCredentials();
      setCredentials(page.data);
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    setTeamName(session.team.name);
    setTeamDescription(session.team.description ?? "");
  }, [session.team.id, session.team.name, session.team.description]);

  useEffect(() => {
    setCreatedCredential(null);
    void loadCredentials();
  }, [api, session.team.id]);

  async function saveTeam(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmedName = teamName.trim();
    if (trimmedName.length < 3) {
      setError("Team name must be at least 3 characters.");
      return;
    }
    setTeamBusy(true);
    setError("");
    try {
      onTeamUpdated(await api.updateTeam({ name: trimmedName, description: teamDescription.trim() }));
    } catch (err) {
      setError(readError(err));
    } finally {
      setTeamBusy(false);
    }
  }

  async function updateCredentialName(credentialId: string, name: string) {
    const credential = credentials.find((item) => item.id === credentialId);
    if (credential?.role !== "member") {
      return;
    }
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Credential name is required.");
      return;
    }
    setSavingCredentialId(credentialId);
    setError("");
    try {
      const updated = await api.updateTeamCredential(credentialId, { name: trimmedName });
      setCredentials((current) => current.map((item) => (item.id === credentialId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setSavingCredentialId("");
    }
  }

  async function updateCredentialScopes(credentialId: string, scopes: string[]) {
    const credential = credentials.find((item) => item.id === credentialId);
    if (credential?.role !== "member") {
      return;
    }
    setSavingScopesCredentialId(credentialId);
    setError("");
    try {
      const updated = await api.updateTeamCredential(credentialId, { scopes });
      setCredentials((current) => current.map((item) => (item.id === credentialId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setSavingScopesCredentialId("");
    }
  }

  async function rotateCredential(credentialId: string) {
    const credential = credentials.find((item) => item.id === credentialId);
    if (!credential || credential.role !== "member") {
      return;
    }
    if (!window.confirm(`Regenerate the API key for credential "${credential.name}"? The current key will stop working.`)) {
      return;
    }
    setRotatingCredentialId(credentialId);
    setError("");
    try {
      const rotated = await api.rotateTeamCredential(credentialId, {
        name: credential.name,
        rate_limit: credential.rate_limit,
        expires_at: credential.expires_at ?? undefined,
      });
      setCreatedCredential(rotated);
      await loadCredentials();
    } catch (err) {
      setError(readError(err));
    } finally {
      setRotatingCredentialId("");
    }
  }

  async function deleteCredential(credentialId: string) {
    const credential = credentials.find((item) => item.id === credentialId);
    if (!credential || credential.role !== "member") {
      return;
    }
    if (!window.confirm(`Delete credential "${credential.name}"?`)) {
      return;
    }
    setDeletingCredentialId(credentialId);
    setError("");
    try {
      await api.deleteTeamCredential(credentialId);
      await loadCredentials();
    } catch (err) {
      setError(readError(err));
    } finally {
      setDeletingCredentialId("");
    }
  }

  return (
    <section className="surface team-management-surface">
      <div className="surface-section">
        <SectionHeading title="Team" meta={credentialRoleLabel(session.membership.role)} />
        {createdCredential && <CreatedCredentialNotice apiKey={createdCredential.api_key} onDismiss={() => setCreatedCredential(null)} />}
        {error && <div className="banner error" role="alert">{error}</div>}
        <form className="edit-grid" onSubmit={saveTeam}>
          <label htmlFor="user-team-name">Name</label>
          <input id="user-team-name" value={teamName} onChange={(event) => setTeamName(event.target.value)} />
          <label htmlFor="user-team-description">Description</label>
          <textarea id="user-team-description" value={teamDescription} onChange={(event) => setTeamDescription(event.target.value)} />
          <div className="button-row span">
            <button className="primary-button" type="submit" disabled={teamBusy}>
              <Pencil size={16} aria-hidden="true" />
              Save team
            </button>
          </div>
        </form>
      </div>

      <div className="surface-section">
        <TeamDreamingConfigForm
          key={session.team.id}
          config={session.team.config}
          effective={session.team.dreaming_effective}
          disabled={teamBusy}
          onSave={async (config) => {
            onTeamUpdated(await api.updateTeam({ name: session.team.name, description: session.team.description ?? "", config }));
          }}
        />
      </div>

      <div className="surface-section">
        <SectionHeading
          title="Credentials"
          actions={(
            <div className="button-row">
              <span>{credentials.length}</span>
              <button className="icon-button" type="button" aria-label="Refresh credentials" onClick={() => void loadCredentials()}>
                <RefreshCw size={16} aria-hidden="true" />
              </button>
            </div>
          )}
        />
        <ManagedCredentialCreateForm
          disabled={loading}
          onCreate={async (input) => {
            const created = await api.createTeamCredential(input);
            setCreatedCredential(created);
            await loadCredentials();
          }}
        />
        {loading && <LoadingState label="Loading credentials" />}
        {!loading && (
          <ManagedCredentialTable
            credentials={credentials}
            savingCredentialId={savingCredentialId}
            savingScopesCredentialId={savingScopesCredentialId}
            rotatingCredentialId={rotatingCredentialId}
            deletingCredentialId={deletingCredentialId}
            onRename={(credentialId, name) => void updateCredentialName(credentialId, name)}
            onScopesChange={(credentialId, scopes) => void updateCredentialScopes(credentialId, scopes)}
            onRotate={(credentialId) => void rotateCredential(credentialId)}
            onDelete={(credentialId) => void deleteCredential(credentialId)}
          />
        )}
      </div>
    </section>
  );
}

function ManagedCredentialCreateForm({
  disabled,
  onCreate,
}: {
  disabled: boolean;
  onCreate: (input: { name: string; scopes: string[]; rate_limit: number }) => Promise<void>;
}) {
  const [name, setName] = useState("member credential");
  const [scopes, setScopes] = useState<string[]>(["read", "write"]);
  const [rateLimit, setRateLimit] = useState("120");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Credential name is required.");
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
      await onCreate({
        name: trimmedName,
        scopes: normalizeCredentialScopes(scopes),
        rate_limit: parsedRateLimit,
      });
      setName("member credential");
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="key-form" onSubmit={submit}>
      <label htmlFor="managed-credential-name">Credential name</label>
      <input id="managed-credential-name" value={name} onChange={(event) => setName(event.target.value)} />
      <label>Permission</label>
      <CredentialPermissionCheckboxes
        scopes={scopes}
        disabled={busy || disabled}
        ariaLabel="New member credential permissions"
        onChange={(nextScopes) => setScopes(normalizeCredentialScopes(nextScopes))}
      />
      <label htmlFor="managed-credential-rate-limit">Rate limit</label>
      <input id="managed-credential-rate-limit" inputMode="numeric" value={rateLimit} onChange={(event) => setRateLimit(event.target.value)} />
      {error && <p className="field-error span" role="alert">{error}</p>}
      <button className="primary-button span" type="submit" disabled={busy || disabled}>
        <Plus size={16} aria-hidden="true" />
        Create member credential
      </button>
    </form>
  );
}

function ManagedCredentialTable({
  credentials,
  savingCredentialId,
  savingScopesCredentialId,
  rotatingCredentialId,
  deletingCredentialId,
  onRename,
  onScopesChange,
  onRotate,
  onDelete,
}: {
  credentials: UserCredential[];
  savingCredentialId: string;
  savingScopesCredentialId: string;
  rotatingCredentialId: string;
  deletingCredentialId: string;
  onRename: (credentialId: string, name: string) => void;
  onScopesChange: (credentialId: string, scopes: string[]) => void;
  onRotate: (credentialId: string) => void;
  onDelete: (credentialId: string) => void;
}) {
  if (credentials.length === 0) {
    return <div className="table-placeholder">No credentials</div>;
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
            <th>Last used</th>
            <th className="actions-cell">Actions</th>
          </tr>
        </thead>
        <tbody>
          {credentials.map((credential) => (
            <ManagedCredentialRow
              key={credential.id}
              credential={credential}
              saving={savingCredentialId === credential.id}
              savingScopes={savingScopesCredentialId === credential.id}
              rotating={rotatingCredentialId === credential.id}
              deleting={deletingCredentialId === credential.id}
              onRename={onRename}
              onScopesChange={onScopesChange}
              onRotate={onRotate}
              onDelete={onDelete}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ManagedCredentialRow({
  credential,
  saving,
  savingScopes,
  rotating,
  deleting,
  onRename,
  onScopesChange,
  onRotate,
  onDelete,
}: {
  credential: UserCredential;
  saving: boolean;
  savingScopes: boolean;
  rotating: boolean;
  deleting: boolean;
  onRename: (credentialId: string, name: string) => void;
  onScopesChange: (credentialId: string, scopes: string[]) => void;
  onRotate: (credentialId: string) => void;
  onDelete: (credentialId: string) => void;
}) {
  const [draftName, setDraftName] = useState(credential.name);
  const trimmedDraft = draftName.trim();
  const isMember = credential.role === "member";
  const busy = saving || savingScopes || rotating || deleting;
  const unchanged = trimmedDraft === credential.name;

  useEffect(() => {
    setDraftName(credential.name);
  }, [credential.id, credential.name]);

  return (
    <tr>
      <td>
        <div className="credential-name-cell">
          <input
            aria-label={`Credential name ${credential.name}`}
            value={draftName}
            disabled={!isMember}
            onChange={(event) => setDraftName(event.target.value)}
          />
          <button
            className="icon-button"
            type="button"
            aria-label={`Save credential ${credential.name}`}
            title={isMember ? "Save credential" : "Managed from control portal"}
            disabled={!isMember || busy || unchanged || trimmedDraft.length === 0}
            onClick={() => onRename(credential.id, draftName)}
          >
            <Pencil size={16} aria-hidden="true" />
          </button>
        </div>
      </td>
      <td><code>{displayKeySuffix(credential.key_suffix)}</code></td>
      <td>
        <CredentialPermissionCheckboxes
          scopes={credential.scopes}
          forceWrite={credential.role === "manager"}
          disabled={!isMember || busy}
          ariaLabel={`Permissions for ${credential.name}`}
          className="compact"
          onChange={(scopes) => onScopesChange(credential.id, normalizeCredentialScopes(scopes))}
        />
      </td>
      <td>{credentialRoleLabel(credential.role)}</td>
      <td>{credential.last_used_at ? formatDate(credential.last_used_at) : "Never"}</td>
      <td className="actions-cell">
        <div className="table-actions">
          <button
            className="icon-button"
            type="button"
            aria-label={`Regenerate API key for credential ${credential.name}`}
            title={isMember ? "Regenerate key" : "Managed from control portal"}
            disabled={!isMember || busy}
            onClick={() => onRotate(credential.id)}
          >
            <RefreshCw size={16} aria-hidden="true" />
          </button>
          <button
            className="icon-button danger"
            type="button"
            aria-label={`Delete credential ${credential.name}`}
            title={isMember ? "Delete credential" : "Managed from control portal"}
            disabled={!isMember || busy}
            onClick={() => onDelete(credential.id)}
          >
            <Trash2 size={16} aria-hidden="true" />
          </button>
        </div>
      </td>
    </tr>
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
