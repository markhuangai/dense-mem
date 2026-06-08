import { FormEvent, useEffect, useState } from "react";
import { Check, Copy, Pencil, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { CreatedTeamProfile, UserApi, UserKey, UserSession, UserTeam } from "./api";
import { SectionHeading } from "../ui/components";

type ProfilePermission = "read" | "read_write";

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
  const [profiles, setProfiles] = useState<UserKey[]>([]);
  const [createdKey, setCreatedKey] = useState<CreatedTeamProfile | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [teamBusy, setTeamBusy] = useState(false);
  const [savingProfileId, setSavingProfileId] = useState("");
  const [rotatingProfileId, setRotatingProfileId] = useState("");
  const [deletingProfileId, setDeletingProfileId] = useState("");

  async function loadProfiles() {
    setLoading(true);
    setError("");
    try {
      const page = await api.listTeamProfiles(session.team.id);
      setProfiles(page.data);
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
    setCreatedKey(null);
    void loadProfiles();
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
      onTeamUpdated(await api.updateTeam(session.team.id, { name: trimmedName, description: teamDescription.trim() }));
    } catch (err) {
      setError(readError(err));
    } finally {
      setTeamBusy(false);
    }
  }

  async function updateProfileName(profileId: string, name: string) {
    const profile = profiles.find((item) => item.id === profileId);
    if (profile?.role !== "member") {
      return;
    }
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Profile name is required.");
      return;
    }
    setSavingProfileId(profileId);
    setError("");
    try {
      const updated = await api.updateTeamProfile(session.team.id, profileId, { name: trimmedName });
      setProfiles((current) => current.map((item) => (item.id === profileId ? updated : item)));
    } catch (err) {
      setError(readError(err));
    } finally {
      setSavingProfileId("");
    }
  }

  async function rotateProfile(profileId: string) {
    const profile = profiles.find((item) => item.id === profileId);
    if (!profile || profile.role !== "member") {
      return;
    }
    if (!window.confirm(`Regenerate key for profile "${profile.name}"? The current key will stop working.`)) {
      return;
    }
    setRotatingProfileId(profileId);
    setError("");
    try {
      const rotated = await api.rotateTeamProfile(session.team.id, profileId, {
        name: profile.name,
        rate_limit: profile.rate_limit,
        expires_at: profile.expires_at ?? undefined,
      });
      setCreatedKey(rotated);
      await loadProfiles();
    } catch (err) {
      setError(readError(err));
    } finally {
      setRotatingProfileId("");
    }
  }

  async function deleteProfile(profileId: string) {
    const profile = profiles.find((item) => item.id === profileId);
    if (!profile || profile.role !== "member") {
      return;
    }
    if (!window.confirm(`Delete profile "${profile.name}"?`)) {
      return;
    }
    setDeletingProfileId(profileId);
    setError("");
    try {
      await api.deleteTeamProfile(session.team.id, profileId);
      await loadProfiles();
    } catch (err) {
      setError(readError(err));
    } finally {
      setDeletingProfileId("");
    }
  }

  return (
    <section className="surface team-management-surface">
      <div className="surface-section">
        <SectionHeading title="Team" meta={profileRoleLabel(session.key.role)} />
        {createdKey && <CreatedKeyNotice apiKey={createdKey.api_key} onDismiss={() => setCreatedKey(null)} />}
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
        <SectionHeading
          title="Profiles"
          actions={(
            <div className="button-row">
              <span>{profiles.length}</span>
              <button className="icon-button" type="button" aria-label="Refresh profiles" onClick={() => void loadProfiles()}>
                <RefreshCw size={16} aria-hidden="true" />
              </button>
            </div>
          )}
        />
        <ManagedProfileCreateForm
          disabled={loading}
          onCreate={async (input) => {
            const created = await api.createTeamProfile(session.team.id, input);
            setCreatedKey(created);
            await loadProfiles();
          }}
        />
        {loading && <div className="table-placeholder">Loading</div>}
        {!loading && (
          <ManagedProfileTable
            profiles={profiles}
            savingProfileId={savingProfileId}
            rotatingProfileId={rotatingProfileId}
            deletingProfileId={deletingProfileId}
            onRename={(profileId, name) => void updateProfileName(profileId, name)}
            onRotate={(profileId) => void rotateProfile(profileId)}
            onDelete={(profileId) => void deleteProfile(profileId)}
          />
        )}
      </div>
    </section>
  );
}

function ManagedProfileCreateForm({
  disabled,
  onCreate,
}: {
  disabled: boolean;
  onCreate: (input: { name: string; scopes: string[]; rate_limit: number }) => Promise<void>;
}) {
  const [name, setName] = useState("member profile");
  const [permission, setPermission] = useState<ProfilePermission>("read_write");
  const [rateLimit, setRateLimit] = useState("120");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmedName = name.trim();
    if (!trimmedName) {
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
      await onCreate({
        name: trimmedName,
        scopes: permission === "read" ? ["read"] : ["read", "write"],
        rate_limit: parsedRateLimit,
      });
      setName("member profile");
    } catch (err) {
      setError(readError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="key-form" onSubmit={submit}>
      <label htmlFor="managed-profile-name">Profile name</label>
      <input id="managed-profile-name" value={name} onChange={(event) => setName(event.target.value)} />
      <label htmlFor="managed-profile-permission">Permission</label>
      <select
        id="managed-profile-permission"
        value={permission}
        onChange={(event) => setPermission(event.target.value as ProfilePermission)}
      >
        <option value="read_write">Read/write</option>
        <option value="read">Read only</option>
      </select>
      <label htmlFor="managed-profile-rate-limit">Rate limit</label>
      <input id="managed-profile-rate-limit" inputMode="numeric" value={rateLimit} onChange={(event) => setRateLimit(event.target.value)} />
      {error && <p className="field-error span" role="alert">{error}</p>}
      <button className="primary-button span" type="submit" disabled={busy || disabled}>
        <Plus size={16} aria-hidden="true" />
        Create member profile
      </button>
    </form>
  );
}

function ManagedProfileTable({
  profiles,
  savingProfileId,
  rotatingProfileId,
  deletingProfileId,
  onRename,
  onRotate,
  onDelete,
}: {
  profiles: UserKey[];
  savingProfileId: string;
  rotatingProfileId: string;
  deletingProfileId: string;
  onRename: (profileId: string, name: string) => void;
  onRotate: (profileId: string) => void;
  onDelete: (profileId: string) => void;
}) {
  if (profiles.length === 0) {
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
            <th>Last used</th>
            <th className="actions-cell">Actions</th>
          </tr>
        </thead>
        <tbody>
          {profiles.map((profile) => (
            <ManagedProfileRow
              key={profile.id}
              profile={profile}
              saving={savingProfileId === profile.id}
              rotating={rotatingProfileId === profile.id}
              deleting={deletingProfileId === profile.id}
              onRename={onRename}
              onRotate={onRotate}
              onDelete={onDelete}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ManagedProfileRow({
  profile,
  saving,
  rotating,
  deleting,
  onRename,
  onRotate,
  onDelete,
}: {
  profile: UserKey;
  saving: boolean;
  rotating: boolean;
  deleting: boolean;
  onRename: (profileId: string, name: string) => void;
  onRotate: (profileId: string) => void;
  onDelete: (profileId: string) => void;
}) {
  const [draftName, setDraftName] = useState(profile.name);
  const trimmedDraft = draftName.trim();
  const isMember = profile.role === "member";
  const busy = saving || rotating || deleting;
  const unchanged = trimmedDraft === profile.name;

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
            disabled={!isMember}
            onChange={(event) => setDraftName(event.target.value)}
          />
          <button
            className="icon-button"
            type="button"
            aria-label={`Save profile ${profile.name}`}
            title={isMember ? "Save profile" : "Managed from control portal"}
            disabled={!isMember || busy || unchanged || trimmedDraft.length === 0}
            onClick={() => onRename(profile.id, draftName)}
          >
            <Pencil size={16} aria-hidden="true" />
          </button>
        </div>
      </td>
      <td><code>{displayKeySuffix(profile.key_suffix)}</code></td>
      <td>{profilePermissionLabel(profile.scopes)}</td>
      <td>{profileRoleLabel(profile.role)}</td>
      <td>{profile.last_used_at ? formatDate(profile.last_used_at) : "Never"}</td>
      <td className="actions-cell">
        <div className="table-actions">
          <button
            className="icon-button"
            type="button"
            aria-label={`Regenerate key for profile ${profile.name}`}
            title={isMember ? "Regenerate key" : "Managed from control portal"}
            disabled={!isMember || busy}
            onClick={() => onRotate(profile.id)}
          >
            <RefreshCw size={16} aria-hidden="true" />
          </button>
          <button
            className="icon-button danger"
            type="button"
            aria-label={`Delete profile ${profile.name}`}
            title={isMember ? "Delete profile" : "Managed from control portal"}
            disabled={!isMember || busy}
            onClick={() => onDelete(profile.id)}
          >
            <Trash2 size={16} aria-hidden="true" />
          </button>
        </div>
      </td>
    </tr>
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

function readError(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed.";
}

function displayKeySuffix(suffix: string | null): string {
  return suffix ? `******${suffix}` : "Unavailable";
}

function profilePermissionLabel(scopes: string[] | null | undefined): string {
  return scopes?.includes("write") ? "Read/write" : "Read only";
}

function profileRoleLabel(role: UserKey["role"] | null | undefined): string {
  return role === "manager" ? "Manager" : "Member";
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}
