import { FormEvent, useEffect, useState } from "react";
import { KeyRound, Pencil, RefreshCw, Trash2 } from "lucide-react";
import { ControlApi, CreatedTeamProfile, ProfileRole, Team, TeamProfile } from "../api";
import { normalizeProfileScopes, ProfilePermissionCheckboxes } from "../profilePermissions";
import { LoadingState, SecretBox, SectionHeading } from "../ui/components";
import { displayKeySuffix, formatDate, profileRoleLabel, readError } from "./utils";

export function TeamProfilesPanel({ api, team, embedded = false }: { api: ControlApi; team: Team; embedded?: boolean }) {
  const [keys, setKeys] = useState<TeamProfile[]>([]);
  const [createdKey, setCreatedKey] = useState<CreatedTeamProfile | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [deletingKeyId, setDeletingKeyId] = useState("");
  const [savingKeyId, setSavingKeyId] = useState("");
  const [updatingRoleKeyId, setUpdatingRoleKeyId] = useState("");
  const [updatingScopesKeyId, setUpdatingScopesKeyId] = useState("");
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

  async function updateKeyScopes(keyId: string, scopes: string[]) {
    setUpdatingScopesKeyId(keyId);
    setError("");
    try {
      const updated = await api.updateTeamProfile(team.id, keyId, { scopes });
      setKeys((current) => current.map((item) => (item.id === keyId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setUpdatingScopesKeyId("");
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
      {loading && <LoadingState label="Loading profiles" />}
      {!loading && (
        <TeamProfileTable
          keys={keys}
          savingKeyId={savingKeyId}
          updatingRoleKeyId={updatingRoleKeyId}
          updatingScopesKeyId={updatingScopesKeyId}
          rotatingKeyId={rotatingKeyId}
          deletingKeyId={deletingKeyId}
          onRename={(keyId, name) => void updateKeyName(keyId, name)}
          onRoleChange={(keyId, role) => void updateKeyRole(keyId, role)}
          onScopesChange={(keyId, scopes) => void updateKeyScopes(keyId, scopes)}
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
  updatingScopesKeyId,
  rotatingKeyId,
  deletingKeyId,
  onRename,
  onRoleChange,
  onScopesChange,
  onRotate,
  onDelete,
}: {
  keys: TeamProfile[];
  savingKeyId: string;
  updatingRoleKeyId: string;
  updatingScopesKeyId: string;
  rotatingKeyId: string;
  deletingKeyId: string;
  onRename: (keyId: string, name: string) => void;
  onRoleChange: (keyId: string, role: ProfileRole) => void;
  onScopesChange: (keyId: string, scopes: string[]) => void;
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
              updatingScopes={updatingScopesKeyId === key.id}
              rotating={rotatingKeyId === key.id}
              deleting={deletingKeyId === key.id}
              onRename={onRename}
              onRoleChange={onRoleChange}
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

function TeamProfileRow({
  profile,
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
  profile: TeamProfile;
  saving: boolean;
  updatingRole: boolean;
  updatingScopes: boolean;
  rotating: boolean;
  deleting: boolean;
  onRename: (keyId: string, name: string) => void;
  onRoleChange: (keyId: string, role: ProfileRole) => void;
  onScopesChange: (keyId: string, scopes: string[]) => void;
  onRotate: (keyId: string) => void;
  onDelete: (keyId: string) => void;
}) {
  const [draftName, setDraftName] = useState(profile.name);
  const trimmedDraft = draftName.trim();
  const unchanged = trimmedDraft === profile.name;
  const busy = saving || updatingRole || updatingScopes || rotating || deleting;

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
      <td>
        <ProfilePermissionCheckboxes
          scopes={profile.scopes}
          forceWrite={profile.role === "manager"}
          disabled={busy}
          ariaLabel={`Permissions for ${profile.name}`}
          className="compact"
          onChange={(scopes) => onScopesChange(profile.id, normalizeProfileScopes(scopes, { forceWrite: profile.role === "manager" }))}
        />
      </td>
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
  const [role, setRole] = useState<ProfileRole>(defaultRole);
  const [scopes, setScopes] = useState<string[]>(["read", "write"]);
  const [rateLimit, setRateLimit] = useState("120");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setRole(defaultRole);
  }, [team.id, defaultRole]);

  useEffect(() => {
    setScopes((current) => normalizeProfileScopes(current, { forceWrite: role === "manager" }));
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
        scopes: normalizeProfileScopes(scopes, { forceWrite: role === "manager" }),
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
      <label>Permission</label>
      <ProfilePermissionCheckboxes
        scopes={scopes}
        forceWrite={role === "manager"}
        disabled={busy || disabled}
        ariaLabel="New profile permissions"
        onChange={(nextScopes) => setScopes(normalizeProfileScopes(nextScopes, { forceWrite: role === "manager" }))}
      />
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
