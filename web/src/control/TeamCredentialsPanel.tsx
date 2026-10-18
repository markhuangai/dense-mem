import { useConfirmation } from "../ui/confirmation";
import { useUnsavedChanges } from "../ui/navigation";
import { Table, TableHeader, TableRow, TableHead, TableBody, TableCell } from "../ui/primitives/table";
import { Input } from "../ui/primitives/input";
import { Button } from "../ui/primitives/button";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { FormEvent, useEffect, useState } from "react";
import { KeyRound, Pencil, RefreshCw, Trash2 } from "lucide-react";
import { ControlApi, CreatedCredential, Credential, CredentialRole, Team } from "../api";
import { CredentialPermissionCheckboxes, MemoryAccessSummary, normalizeCredentialScopes } from "../credentialPermissions";
import { FieldRow, LoadingState, SecretBox, SectionHeading } from "../ui/components";
import { credentialRoleLabel, displayKeySuffix, formatDate, readError } from "./utils";

export function TeamCredentialsPanel({ api, team, embedded = false }: { api: ControlApi; team: Team; embedded?: boolean }) {
  const confirm = useConfirmation();
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [createdCredential, setCreatedCredential] = useState<CreatedCredential | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [deletingCredentialId, setDeletingCredentialId] = useState("");
  const [savingCredentialId, setSavingCredentialId] = useState("");
  const [updatingRoleCredentialId, setUpdatingRoleCredentialId] = useState("");
  const [updatingScopesCredentialId, setUpdatingScopesCredentialId] = useState("");
  const [rotatingCredentialId, setRotatingCredentialId] = useState("");

  async function loadCredentials() {
    setLoading(true);
    setError("");
    try {
      const page = await api.listTeamCredentials(team.id);
      setCredentials(page.data);
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    setCreatedCredential(null);
    void loadCredentials();
  }, [team.id]);

  async function updateCredentialName(credentialId: string, name: string) {
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Credential name is required.");
      return;
    }
    setSavingCredentialId(credentialId);
    setError("");
    try {
      const updated = await api.updateTeamCredential(team.id, credentialId, { name: trimmedName });
      setCredentials((current) => current.map((item) => (item.id === credentialId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setSavingCredentialId("");
    }
  }

  async function updateCredentialRole(credentialId: string, role: CredentialRole) {
    setUpdatingRoleCredentialId(credentialId);
    setError("");
    try {
      const updated = await api.updateTeamCredential(team.id, credentialId, { role });
      setCredentials((current) => current.map((item) => (item.id === credentialId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setUpdatingRoleCredentialId("");
    }
  }

  async function updateCredentialScopes(credentialId: string, scopes: string[]) {
    setUpdatingScopesCredentialId(credentialId);
    setError("");
    try {
      const updated = await api.updateTeamCredential(team.id, credentialId, { scopes });
      setCredentials((current) => current.map((item) => (item.id === credentialId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setUpdatingScopesCredentialId("");
    }
  }

  async function rotateCredential(credentialId: string) {
    const credential = credentials.find((item) => item.id === credentialId);
    if (!credential) {
      return;
    }
    if (!await confirm({ title: "Regenerate API key?", description: `Regenerate the API key for credential "${credential.name}"? The current key will stop working.`, confirmLabel: "Regenerate API key", destructive: true })) {
      return;
    }
    setRotatingCredentialId(credentialId);
    setError("");
    try {
      const rotated = await api.rotateTeamCredential(team.id, credentialId, {
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
    const label = credential ? credential.name : "this credential";
    if (!await confirm({ title: "Delete?", description: `Delete credential "${label}"?`, confirmLabel: "Delete", destructive: true })) {
      return;
    }
    setDeletingCredentialId(credentialId);
    setError("");
    try {
      await api.deleteTeamCredential(team.id, credentialId);
      await loadCredentials();
    } catch (err) {
      setError(readError(err));
    } finally {
      setDeletingCredentialId("");
    }
  }

  return (
    <section className={embedded ? "team-embedded-panel" : "surface"}>
      <SectionHeading title="Credentials" meta={credentials.length} />
      {createdCredential && <CreatedCredentialNotice createdCredential={createdCredential} onDismiss={() => setCreatedCredential(null)} />}
      {error && <div className="banner error" role="alert">{error}</div>}
      <CredentialCreateForm
        api={api}
        team={team}
        defaultRole={credentials.length === 0 ? "manager" : "member"}
        disabled={loading}
        onCreated={(value) => {
          setCreatedCredential(value);
          void loadCredentials();
        }}
      />
      {loading && <LoadingState label="Loading credentials" />}
      <div className="min-w-0" aria-busy={loading} inert={loading} hidden={loading && credentials.length === 0}>
        <CredentialTable
          credentials={credentials}
          savingCredentialId={savingCredentialId}
          updatingRoleCredentialId={updatingRoleCredentialId}
          updatingScopesCredentialId={updatingScopesCredentialId}
          rotatingCredentialId={rotatingCredentialId}
          deletingCredentialId={deletingCredentialId}
          onRename={(credentialId, name) => void updateCredentialName(credentialId, name)}
          onRoleChange={(credentialId, role) => void updateCredentialRole(credentialId, role)}
          onScopesChange={(credentialId, scopes) => void updateCredentialScopes(credentialId, scopes)}
          onRotate={(credentialId) => void rotateCredential(credentialId)}
          onDelete={(credentialId) => void deleteCredential(credentialId)}
        />
      </div>
    </section>
  );
}

function CredentialTable({
  credentials,
  savingCredentialId,
  updatingRoleCredentialId,
  updatingScopesCredentialId,
  rotatingCredentialId,
  deletingCredentialId,
  onRename,
  onRoleChange,
  onScopesChange,
  onRotate,
  onDelete,
}: {
  credentials: Credential[];
  savingCredentialId: string;
  updatingRoleCredentialId: string;
  updatingScopesCredentialId: string;
  rotatingCredentialId: string;
  deletingCredentialId: string;
  onRename: (credentialId: string, name: string) => void;
  onRoleChange: (credentialId: string, role: CredentialRole) => void;
  onScopesChange: (credentialId: string, scopes: string[]) => void;
  onRotate: (credentialId: string) => void;
  onDelete: (credentialId: string) => void;
}) {
  if (credentials.length === 0) {
    return <div className="table-placeholder">No credentials</div>;
  }

  return (
    <div className="table-wrap">
      <Table className="data-table key-table">
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Key</TableHead>
            <TableHead>Permission</TableHead>
            <TableHead>Role</TableHead>
            <TableHead>Created</TableHead>
            <TableHead>Last used</TableHead>
            <TableHead className="actions-cell">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {credentials.map((credential) => (
            <CredentialRow
              key={credential.id}
              credential={credential}
              saving={savingCredentialId === credential.id}
              updatingRole={updatingRoleCredentialId === credential.id}
              updatingScopes={updatingScopesCredentialId === credential.id}
              rotating={rotatingCredentialId === credential.id}
              deleting={deletingCredentialId === credential.id}
              onRename={onRename}
              onRoleChange={onRoleChange}
              onScopesChange={onScopesChange}
              onRotate={onRotate}
              onDelete={onDelete}
            />
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function CredentialRow({
  credential,
  saving,
  updatingRole,
  updatingScopes,
  rotating,
  deleting,
  onRename,
  onRoleChange,
  onScopesChange,
  onRotate,
  onDelete,
}: {
  credential: Credential;
  saving: boolean;
  updatingRole: boolean;
  updatingScopes: boolean;
  rotating: boolean;
  deleting: boolean;
  onRename: (credentialId: string, name: string) => void;
  onRoleChange: (credentialId: string, role: CredentialRole) => void;
  onScopesChange: (credentialId: string, scopes: string[]) => void;
  onRotate: (credentialId: string) => void;
  onDelete: (credentialId: string) => void;
}) {
  const [draftName, setDraftName] = useState(credential.name);
  useUnsavedChanges(draftName !== credential.name);
  const trimmedDraft = draftName.trim();
  const unchanged = trimmedDraft === credential.name;
  const busy = saving || updatingRole || updatingScopes || rotating || deleting;

  useEffect(() => {
    setDraftName(credential.name);
  }, [credential.id, credential.name]);

  return (
    <TableRow>
      <TableCell>
        <div className="credential-name-cell">
          <Input
            aria-label={`Credential name ${credential.name}`}
            value={draftName}
            onChange={(event) => setDraftName(event.target.value)}
          />
          <Button variant="ghost" size="icon"
            className="icon-button"
            type="button"
            aria-label={`Save credential ${credential.name}`}
            title="Save credential"
            disabled={busy || unchanged || trimmedDraft.length === 0}
            onClick={() => onRename(credential.id, draftName)}
          >
            <Pencil size={16} aria-hidden="true" />
          </Button>
        </div>
      </TableCell>
      <TableCell><code>{displayKeySuffix(credential)}</code></TableCell>
      <TableCell>
        <CredentialPermissionCheckboxes
          scopes={credential.scopes}
          forceWrite={credential.role === "manager"}
          disabled={busy}
          ariaLabel={`Permissions for ${credential.name}`}
          className="compact"
          onChange={(scopes) => onScopesChange(credential.id, normalizeCredentialScopes(scopes, { forceWrite: credential.role === "manager" }))}
        />
        <MemoryAccessSummary access={credential.memory_access} />
      </TableCell>
      <TableCell>
        <NativeSelect
          aria-label={`Credential role ${credential.name}`}
          value={credential.role}
          disabled={busy}
          onChange={(event) => onRoleChange(credential.id, event.target.value as CredentialRole)}
        >
          <NativeSelectOption value="manager">{credentialRoleLabel("manager")}</NativeSelectOption>
          <NativeSelectOption value="member">{credentialRoleLabel("member")}</NativeSelectOption>
        </NativeSelect>
      </TableCell>
      <TableCell>{formatDate(credential.created_at)}</TableCell>
      <TableCell>{credential.last_used_at ? formatDate(credential.last_used_at) : "Never"}</TableCell>
      <TableCell className="actions-cell">
        <div className="table-actions">
          <Button variant="ghost" size="icon"
            className="icon-button"
            type="button"
            aria-label={`Regenerate API key for credential ${credential.name}`}
            title="Regenerate key"
            disabled={busy}
            onClick={() => onRotate(credential.id)}
          >
            <RefreshCw size={16} aria-hidden="true" />
          </Button>
          <Button variant="ghost" size="icon"
            className="icon-button danger"
            type="button"
            aria-label={`Delete credential ${credential.name}`}
            title="Delete credential"
            disabled={busy}
            onClick={() => onDelete(credential.id)}
          >
            <Trash2 size={16} aria-hidden="true" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

function CredentialCreateForm({
  api,
  team,
  defaultRole,
  disabled,
  onCreated,
}: {
  api: ControlApi;
  team: Team;
  defaultRole: CredentialRole;
  disabled: boolean;
  onCreated: (created: CreatedCredential) => void;
}) {
  const [name, setName] = useState("default credential");
  const [role, setRole] = useState<CredentialRole>(defaultRole);
  const [scopes, setScopes] = useState<string[]>(["read", "write"]);
  const [rateLimit, setRateLimit] = useState("120");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useUnsavedChanges(name !== "default credential" || role !== defaultRole || rateLimit !== "120" || scopes.join(",") !== "read,write");

  useEffect(() => {
    setRole(defaultRole);
  }, [team.id, defaultRole]);

  useEffect(() => {
    setScopes((current) => normalizeCredentialScopes(current, { forceWrite: role === "manager" }));
  }, [role]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (name.trim().length < 1) {
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
      const created = await api.createTeamCredential(team.id, {
        name: name.trim(),
        scopes: normalizeCredentialScopes(scopes, { forceWrite: role === "manager" }),
        role,
        rate_limit: parsedRateLimit,
      });
      setName("default credential"); setRole(defaultRole); setScopes(["read", "write"]); setRateLimit("120");
      onCreated(created);
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="key-form" onSubmit={submit}>
      <p className="span">Each key has its own private memory, including team-read-only keys. The calling agent chooses private or team shared for each Remember submission.</p>
      <FieldRow label="Credential name" htmlFor="team-credential-name">
        <Input id="team-credential-name" value={name} onChange={(event) => setName(event.target.value)} />
      </FieldRow>
      <FieldRow label="Role" htmlFor="team-credential-role">
        <NativeSelect
          id="team-credential-role"
          value={role}
          onChange={(event) => setRole(event.target.value as CredentialRole)}
        >
          <NativeSelectOption value="manager">{credentialRoleLabel("manager")}</NativeSelectOption>
          <NativeSelectOption value="member">{credentialRoleLabel("member")}</NativeSelectOption>
        </NativeSelect>
      </FieldRow>
      <FieldRow label="Permission">
        <CredentialPermissionCheckboxes
          scopes={scopes}
          forceWrite={role === "manager"}
          disabled={busy || disabled}
          ariaLabel="New credential permissions"
          onChange={(nextScopes) => setScopes(normalizeCredentialScopes(nextScopes, { forceWrite: role === "manager" }))}
        />
      </FieldRow>
      <FieldRow label="Rate limit" htmlFor="rate-limit">
        <Input id="rate-limit" inputMode="numeric" value={rateLimit} onChange={(event) => setRateLimit(event.target.value)} />
      </FieldRow>
      {error && <p className="field-error span" role="alert">{error}</p>}
      <Button variant="default" className="primary-button span" type="submit" disabled={busy || disabled}>
        <KeyRound size={16} aria-hidden="true" />
        Create credential
      </Button>
    </form>
  );
}

function CreatedCredentialNotice({ createdCredential, onDismiss }: { createdCredential: CreatedCredential; onDismiss: () => void }) {
  return (
    <SecretBox
      value={createdCredential.api_key}
      valueLabel="Generated API key"
      copyLabel="Copy API key"
      dismissLabel="Dismiss API key"
      onDismiss={onDismiss}
    />
  );
}
