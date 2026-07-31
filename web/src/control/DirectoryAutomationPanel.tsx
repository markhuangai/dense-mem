import { FormEvent, useEffect, useRef, useState } from "react";
import { Check, Eye, KeyRound, Play, Power, RefreshCw, Trash2 } from "lucide-react";
import {
  ApiError,
  ControlAdminGroup,
  ControlApi,
  DirectoryConnector,
  DirectoryConnectorInput,
  DirectoryCredential,
  DirectoryPreview,
  SSOProvider,
  Team,
} from "../api";
import { SecretBox, SectionHeading } from "../ui/components";
import { readError } from "./utils";

const defaultGroupPattern = "^gAD7485(?P<team>.+?)(?P<role>Readonly|Member|Manager)Permission$";
const defaultRoleEntitlements = {
  Readonly: { role: "member", scopes: ["read"] },
  Member: { role: "member", scopes: ["read", "write"] },
  Manager: { role: "manager", scopes: ["read", "write", "feedback:read"] },
};

type ConnectorDraft = {
  group_pattern: string;
  role_entitlements: string;
  max_auto_teams: string;
};

export function DirectoryAutomationPanel({ api, provider, teams }: { api: ControlApi; provider: SSOProvider; teams: Team[] }) {
  const [connector, setConnector] = useState<DirectoryConnector | null>(null);
  const [draft, setDraft] = useState<ConnectorDraft>(() => emptyConnectorDraft());
  const [preview, setPreview] = useState<DirectoryPreview | null>(null);
  const [credential, setCredential] = useState<DirectoryCredential | null>(null);
  const [adminGroups, setAdminGroups] = useState<ControlAdminGroup[]>([]);
  const [adminGroupID, setAdminGroupID] = useState("");
  const [adminGroupName, setAdminGroupName] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [loading, setLoading] = useState(false);
  const requestID = useRef(0);

  useEffect(() => {
    const activeRequest = requestID.current + 1;
    requestID.current = activeRequest;
    setLoading(true);
    setError("");
    setMessage("");
    setPreview(null);
    setCredential(null);

    async function load() {
      try {
        const [nextConnector, nextAdminGroups] = await Promise.all([
          loadConnector(api, provider.id),
          api.listControlAdminGroups(provider.id),
        ]);
        if (requestID.current !== activeRequest) {
          return;
        }
        setConnector(nextConnector);
        setDraft(nextConnector ? connectorToDraft(nextConnector) : emptyConnectorDraft());
        setAdminGroups(nextAdminGroups);
      } catch (loadError) {
        if (requestID.current === activeRequest) {
          setError(readError(loadError));
        }
      } finally {
        if (requestID.current === activeRequest) {
          setLoading(false);
        }
      }
    }

    void load();
  }, [api, provider.id]);

  async function saveConnector(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    let input: DirectoryConnectorInput;
    try {
      input = connectorInputFromDraft(draft);
    } catch (parseError) {
      setError(readError(parseError));
      return;
    }
    setLoading(true);
    setError("");
    setMessage("");
    try {
      if (connector) {
        const saved = await api.updateDirectoryConnector(connector.id, input);
        setConnector(saved);
        setDraft(connectorToDraft(saved));
        setPreview(null);
        setMessage("Directory policy saved.");
      } else {
        const created = await api.createDirectoryConnector(provider.id, input);
        setConnector(created.connector);
        setDraft(connectorToDraft(created.connector));
        setCredential(created.credential);
        setMessage("Connector created disabled. Copy one credential method before dismissing it.");
      }
    } catch (saveError) {
      setError(readError(saveError));
    } finally {
      setLoading(false);
    }
  }

  async function refreshPreview() {
    if (!connector) {
      return;
    }
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const nextPreview = await api.previewDirectoryConnector(connector.id);
      setPreview(nextPreview);
      setMessage("Preview refreshed. Activation requires this exact preview version.");
    } catch (previewError) {
      setError(readError(previewError));
    } finally {
      setLoading(false);
    }
  }

  async function setStatus(status: DirectoryConnector["status"]) {
    if (!connector) {
      return;
    }
    if (status === "active" && !preview) {
      setError("Refresh the directory preview before activating the connector.");
      return;
    }
    if (status === "disabled" && !window.confirm("Disable directory automation? Existing directory-created teams remain unchanged until a later reconciliation.")) {
      return;
    }
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const updated = await api.setDirectoryConnectorStatus(connector.id, status, status === "active" ? preview?.version : "");
      setConnector(updated);
      setDraft(connectorToDraft(updated));
      setMessage(status === "active" ? "Directory automation is active." : status === "observe" ? "Directory automation is observing SCIM changes." : "Directory automation is disabled.");
    } catch (statusError) {
      setError(readError(statusError));
    } finally {
      setLoading(false);
    }
  }

  async function rotateCredentials() {
    if (!connector || !window.confirm("Rotate the directory credentials? Existing bearer and OAuth credentials stop working immediately.")) {
      return;
    }
    setLoading(true);
    setError("");
    setMessage("");
    try {
      setCredential(await api.rotateDirectoryCredentials(connector.id));
      setMessage("Directory credentials rotated. Copy a replacement before dismissing it.");
    } catch (rotateError) {
      setError(readError(rotateError));
    } finally {
      setLoading(false);
    }
  }

  async function addAdminGroup(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!adminGroupID.trim()) {
      setError("Control admin group ID is required.");
      return;
    }
    setLoading(true);
    setError("");
    setMessage("");
    try {
      const created = await api.createControlAdminGroup(provider.id, {
        group_id: adminGroupID,
        group_name: adminGroupName,
        enabled: true,
      });
      setAdminGroups((current) => [...current, created]);
      setAdminGroupID("");
      setAdminGroupName("");
      setMessage("Control-admin group added.");
    } catch (createError) {
      setError(readError(createError));
    } finally {
      setLoading(false);
    }
  }

  async function retireAdminGroup(group: ControlAdminGroup) {
    if (!window.confirm(`Remove ${group.group_name || group.group_id} from control access?`)) {
      return;
    }
    setLoading(true);
    setError("");
    setMessage("");
    try {
      await api.deleteControlAdminGroup(provider.id, group.id);
      setAdminGroups((current) => current.filter((item) => item.id !== group.id));
      setMessage("Control-admin group removed.");
    } catch (deleteError) {
      setError(readError(deleteError));
    } finally {
      setLoading(false);
    }
  }

  async function adoptCandidate(candidate: DirectoryPreview["candidates"][number]) {
    if (!connector) {
      return;
    }
    const team = teams.find((item) => item.name === candidate.team_name);
    if (!team) {
      setError("The directory-created team is not available in this control session.");
      return;
    }
    if (!window.confirm(`Adopt ${team.name}? It remains available but future directory removal will not archive it.`)) {
      return;
    }
    setLoading(true);
    setError("");
    setMessage("");
    try {
      await api.adoptDirectoryGroupTeam(connector.id, candidate.group_id, team.id);
      setMessage(`${team.name} is now an adopted team.`);
      await refreshPreview();
    } catch (adoptError) {
      setError(readError(adoptError));
      setLoading(false);
    }
  }

  return (
    <>
      <section className="surface">
        <SectionHeading
          title="Directory automation"
          subtitle="One connector per provider. SCIM observes first, then makes teams and memberships authoritative after activation."
          actions={connector ? <span className={`status-pill ${connector.status === "active" ? "neutral" : "warning"}`}>{connector.status}</span> : undefined}
        />
        {error && <div className="banner error" role="alert">{error}</div>}
        {message && <div className="banner neutral">{message}</div>}
        <form className="edit-grid" onSubmit={saveConnector}>
          <label htmlFor="directory-group-pattern">Group pattern</label>
          <input id="directory-group-pattern" value={draft.group_pattern} onChange={(event) => setDraft((current) => ({ ...current, group_pattern: event.target.value }))} spellCheck={false} />
          <label htmlFor="directory-role-entitlements">Role entitlements (JSON)</label>
          <textarea id="directory-role-entitlements" value={draft.role_entitlements} onChange={(event) => setDraft((current) => ({ ...current, role_entitlements: event.target.value }))} spellCheck={false} />
          <label htmlFor="directory-max-auto-teams">Maximum auto-created teams</label>
          <input id="directory-max-auto-teams" type="number" min="1" max="1000" value={draft.max_auto_teams} onChange={(event) => setDraft((current) => ({ ...current, max_auto_teams: event.target.value }))} />
          <div className="button-row span">
            <button className="primary-button" type="submit" disabled={loading}>
              <Check size={16} aria-hidden="true" />
              {connector ? "Save directory policy" : "Create connector"}
            </button>
            {connector && (
              <>
                <button className="ghost-button" type="button" disabled={loading || connector.status === "disabled"} onClick={() => void refreshPreview()}>
                  <Eye size={16} aria-hidden="true" />
                  Preview
                </button>
                {connector.status === "disabled" && (
                  <button className="ghost-button" type="button" disabled={loading} onClick={() => void setStatus("observe")}>
                    <Play size={16} aria-hidden="true" />
                    Start observe
                  </button>
                )}
                {connector.status === "observe" && (
                  <button className="primary-button" type="button" disabled={loading || !preview} onClick={() => void setStatus("active")}>
                    <Power size={16} aria-hidden="true" />
                    Activate preview
                  </button>
                )}
                {connector.status !== "disabled" && (
                  <button className="danger-button" type="button" disabled={loading} onClick={() => void setStatus("disabled")}>
                    <Power size={16} aria-hidden="true" />
                    Disable
                  </button>
                )}
                <button className="ghost-button" type="button" disabled={loading} onClick={() => void rotateCredentials()}>
                  <KeyRound size={16} aria-hidden="true" />
                  Rotate credentials
                </button>
              </>
            )}
          </div>
        </form>
        {connector && (
          <div className="banner neutral">
            SCIM base path: <code>{connector.scim_path}</code>. Set the HTTPS SCIM ingress URL in Config before provisioning from Entra.
          </div>
        )}
        {credential && <DirectoryCredentialNotice credential={credential} onDismiss={() => setCredential(null)} />}
      </section>

      {preview && connector && (
        <section className="surface">
          <SectionHeading title="Directory preview" subtitle={`Version ${preview.version}`} actions={<button className="icon-button" type="button" aria-label="Refresh directory preview" onClick={() => void refreshPreview()}><RefreshCw size={16} aria-hidden="true" /></button>} />
          <DirectoryPreviewTable candidates={preview.candidates} teams={teams} onAdopt={(candidate) => void adoptCandidate(candidate)} />
          {preview.issues.length > 0 && (
            <div className="table-wrap">
              <table className="data-table">
                <thead><tr><th>Issue</th><th>Detail</th><th>Status</th></tr></thead>
                <tbody>{preview.issues.map((issue) => <tr key={`${issue.kind}-${issue.detail}`}><td>{issue.kind}</td><td>{issue.detail}</td><td>{issue.active ? "active" : "resolved"}</td></tr>)}</tbody>
              </table>
            </div>
          )}
        </section>
      )}

      <section className="surface">
        <SectionHeading title="Control SSO administrators" subtitle="Members of any enabled group can use this provider to enter the control portal; the break-glass token remains available." meta={adminGroups.length} />
        <form className="inline-form" onSubmit={addAdminGroup}>
          <label htmlFor="control-admin-group-id">Entra group ID</label>
          <input id="control-admin-group-id" value={adminGroupID} onChange={(event) => setAdminGroupID(event.target.value)} />
          <label htmlFor="control-admin-group-name">Display name</label>
          <input id="control-admin-group-name" value={adminGroupName} onChange={(event) => setAdminGroupName(event.target.value)} />
          <div className="button-row">
            <button className="primary-button compact" type="submit" disabled={loading}>
              <Check size={16} aria-hidden="true" />
              Add control admin group
            </button>
          </div>
        </form>
        {adminGroups.length === 0 ? (
          <div className="table-placeholder">No control-admin groups configured</div>
        ) : (
          <div className="table-wrap">
            <table className="data-table">
              <thead><tr><th>Name</th><th>Group ID</th><th>Status</th><th className="actions-cell">Actions</th></tr></thead>
              <tbody>{adminGroups.map((group) => (
                <tr key={group.id}>
                  <td>{group.group_name || "Unnamed group"}</td>
                  <td><code>{group.group_id}</code></td>
                  <td><span className={group.enabled ? "status-pill neutral" : "status-pill warning"}>{group.enabled ? "enabled" : "disabled"}</span></td>
                  <td className="actions-cell"><button className="icon-button danger-icon" type="button" aria-label={`Remove ${group.group_name || group.group_id} from control access`} disabled={loading} onClick={() => void retireAdminGroup(group)}><Trash2 size={16} aria-hidden="true" /></button></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        )}
      </section>
    </>
  );
}

function emptyConnectorDraft(): ConnectorDraft {
  return {
    group_pattern: defaultGroupPattern,
    role_entitlements: JSON.stringify(defaultRoleEntitlements, null, 2),
    max_auto_teams: "100",
  };
}

function connectorToDraft(connector: DirectoryConnector): ConnectorDraft {
  return {
    group_pattern: connector.group_pattern,
    role_entitlements: JSON.stringify(connector.role_entitlements, null, 2),
    max_auto_teams: String(connector.max_auto_teams),
  };
}

function connectorInputFromDraft(draft: ConnectorDraft): DirectoryConnectorInput {
  const maxAutoTeams = Number.parseInt(draft.max_auto_teams, 10);
  if (!Number.isInteger(maxAutoTeams)) {
    throw new Error("Maximum auto-created teams must be an integer.");
  }
  let roleEntitlements: DirectoryConnectorInput["role_entitlements"];
  try {
    roleEntitlements = JSON.parse(draft.role_entitlements) as DirectoryConnectorInput["role_entitlements"];
  } catch {
    throw new Error("Role entitlements must be valid JSON.");
  }
  if (!roleEntitlements || Array.isArray(roleEntitlements) || typeof roleEntitlements !== "object") {
    throw new Error("Role entitlements must be a JSON object.");
  }
  return {
    group_pattern: draft.group_pattern,
    role_entitlements: roleEntitlements,
    max_auto_teams: maxAutoTeams,
  };
}

async function loadConnector(api: ControlApi, providerID: string): Promise<DirectoryConnector | null> {
  try {
    return await api.getDirectoryConnector(providerID);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      return null;
    }
    throw error;
  }
}

function DirectoryCredentialNotice({ credential, onDismiss }: { credential: DirectoryCredential; onDismiss: () => void }) {
  return (
    <div className="banner warning">
      <p>Copy one authentication method now. Dense-Mem stores only hashes and will not show these values again.</p>
      <label>Bearer token</label>
      <SecretBox value={credential.bearer_token} valueLabel="Directory bearer token" copyLabel="Copy directory bearer token" dismissLabel="Dismiss directory credentials" onDismiss={onDismiss} />
      <label>OAuth client ID</label>
      <SecretBox value={credential.oauth_client_id} valueLabel="Directory OAuth client ID" copyLabel="Copy directory OAuth client ID" dismissLabel="Dismiss directory credentials" onDismiss={onDismiss} />
      <label>OAuth client secret</label>
      <SecretBox value={credential.oauth_client_secret} valueLabel="Directory OAuth client secret" copyLabel="Copy directory OAuth client secret" dismissLabel="Dismiss directory credentials" onDismiss={onDismiss} />
    </div>
  );
}

function DirectoryPreviewTable({ candidates, teams, onAdopt }: { candidates: DirectoryPreview["candidates"]; teams: Team[]; onAdopt: (candidate: DirectoryPreview["candidates"][number]) => void }) {
  if (candidates.length === 0) {
    return <div className="table-placeholder">No groups match the directory policy</div>;
  }
  return (
    <div className="table-wrap">
      <table className="data-table">
        <thead><tr><th>Group</th><th>Team</th><th>Access</th><th>Binding</th><th className="actions-cell">Actions</th></tr></thead>
        <tbody>{candidates.map((candidate) => {
          const team = teams.find((item) => item.name === candidate.team_name);
          const canAdopt = candidate.binding_origin === "directory_created" && Boolean(team);
          return (
            <tr key={candidate.group_id}>
              <td>{candidate.display_name}</td>
              <td>{candidate.team_name}</td>
              <td>{candidate.entitlement.role}: {candidate.entitlement.scopes.join(", ")}</td>
              <td>{candidate.binding_origin}</td>
              <td className="actions-cell">{canAdopt && <button className="ghost-button compact" type="button" onClick={() => onAdopt(candidate)}>Adopt team</button>}</td>
            </tr>
          );
        })}</tbody>
      </table>
    </div>
  );
}
