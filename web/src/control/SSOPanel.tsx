import { FormEvent, useEffect, useRef, useState } from "react";
import { Check, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import {
  ControlApi,
  MembershipRole,
  SSOGroupMapping,
  SSOGroupMappingInput,
  SSOProvider,
  SSOProviderInput,
  Team,
} from "../api";
import { SectionHeading } from "../ui/components";
import { DirectoryAutomationPanel } from "./DirectoryAutomationPanel";
import { membershipGrantLabel, membershipRoleLabel, readError, shortId } from "./utils";

export function SSOPanel({ api, teams }: { api: ControlApi; teams: Team[] }) {
  const [providers, setProviders] = useState<SSOProvider[]>([]);
  const [selectedProviderId, setSelectedProviderId] = useState("");
  const [mappings, setMappings] = useState<SSOGroupMapping[]>([]);
  const [providerDraft, setProviderDraft] = useState<SSOProviderInput>(() => emptyProviderInput());
  const [mappingDraft, setMappingDraft] = useState<SSOGroupMappingInput>(() => emptyMappingInput(teams[0]?.id ?? ""));
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const selectedProviderIdRef = useRef("");
  const mappingsRequestId = useRef(0);
  const selectedProvider = providers.find((provider) => provider.id === selectedProviderId) ?? null;

  function selectProvider(providerId: string) {
    selectedProviderIdRef.current = providerId;
    mappingsRequestId.current += 1;
    setMappings([]);
    setSelectedProviderId(providerId);
  }

  async function loadProviders(nextSelectedId?: string) {
    setLoading(true);
    setError("");
    try {
      const items = await api.listSSOProviders();
      setProviders(items);
      const selected = nextSelectedId || selectedProviderIdRef.current || items[0]?.id || "";
      selectProvider(items.some((item) => item.id === selected) ? selected : items[0]?.id ?? "");
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  async function loadMappings(providerId: string) {
    const requestId = mappingsRequestId.current + 1;
    mappingsRequestId.current = requestId;
    if (!providerId) {
      setMappings([]);
      return;
    }
    setMappings([]);
    setError("");
    try {
      const items = await api.listSSOGroupMappings(providerId);
      if (mappingsRequestId.current === requestId && selectedProviderIdRef.current === providerId) {
        setMappings(items);
      }
    } catch (err) {
      if (mappingsRequestId.current === requestId) {
        setError(readError(err));
      }
    }
  }

  useEffect(() => {
    void loadProviders();
  }, []);

  useEffect(() => {
    if (selectedProvider) {
      setProviderDraft(providerToInput(selectedProvider));
      void loadMappings(selectedProvider.id);
    } else {
      setProviderDraft(emptyProviderInput());
      setMappings([]);
    }
  }, [selectedProviderId, providers]);

  useEffect(() => {
    setMappingDraft((current) => ({ ...current, team_id: current.team_id || teams[0]?.id || "" }));
  }, [teams]);

  async function saveProvider(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError("");
    try {
      const saved = selectedProvider
        ? await api.updateSSOProvider(selectedProvider.id, providerDraft)
        : await api.createSSOProvider(providerDraft);
      await loadProviders(saved.id);
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  async function deleteProvider(providerId: string) {
    if (!window.confirm("Delete this SSO provider and its mappings?")) {
      return;
    }
    setLoading(true);
    setError("");
    try {
      await api.deleteSSOProvider(providerId);
      await loadProviders("");
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  async function saveMapping(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedProviderId) {
      setError("SSO provider is required.");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const providerId = selectedProviderIdRef.current;
      await api.createSSOGroupMapping(providerId, mappingDraft);
      setMappingDraft(emptyMappingInput(mappingDraft.team_id));
      if (selectedProviderIdRef.current === providerId) {
        await loadMappings(providerId);
      }
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  async function deleteMapping(mappingId: string) {
    const providerId = selectedProviderIdRef.current;
    if (!providerId || !window.confirm("Delete this group mapping?")) {
      return;
    }
    setLoading(true);
    setError("");
    try {
      await api.deleteSSOGroupMapping(providerId, mappingId);
      if (selectedProviderIdRef.current === providerId) {
        await loadMappings(providerId);
      }
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <>
      <section className="surface">
        <SectionHeading
          title="SSO providers"
          meta={providers.length}
          actions={(
            <div className="button-row">
              <button className="icon-button" type="button" aria-label="Refresh SSO providers" onClick={() => void loadProviders()}>
                <RefreshCw size={16} aria-hidden="true" />
              </button>
              <button
                className="ghost-button"
                type="button"
                onClick={() => {
                  selectProvider("");
                  setProviderDraft(emptyProviderInput());
                }}
              >
                <Plus size={16} aria-hidden="true" />
                New
              </button>
            </div>
          )}
        />
        {error && <div className="banner error" role="alert">{error}</div>}
        <div className="table-wrap sso-provider-table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Issuer</th>
                <th>Status</th>
                <th className="actions-cell">Actions</th>
              </tr>
            </thead>
            <tbody>
              {providers.map((provider) => (
                <tr key={provider.id}>
                  <td>{provider.name}</td>
                  <td>{provider.kind}</td>
                  <td><code>{provider.issuer_url}</code></td>
                  <td><span className={provider.enabled ? "status-pill neutral" : "status-pill warning"}>{provider.enabled ? "enabled" : "disabled"}</span></td>
                  <td className="actions-cell">
                    <div className="button-row">
                      <button className="icon-button" type="button" aria-label={`Edit ${provider.name}`} onClick={() => selectProvider(provider.id)}>
                        <Pencil size={16} aria-hidden="true" />
                      </button>
                      <button className="icon-button danger-icon" type="button" aria-label={`Delete ${provider.name}`} onClick={() => void deleteProvider(provider.id)}>
                        <Trash2 size={16} aria-hidden="true" />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <SSOProviderForm draft={providerDraft} busy={loading} onChange={setProviderDraft} onSubmit={saveProvider} />
      </section>

      <section className="surface">
        <SectionHeading title="Group mappings" meta={mappings.length} />
        {selectedProvider ? (
          <>
            <SSOMappingForm teams={teams} draft={mappingDraft} busy={loading} onChange={setMappingDraft} onSubmit={saveMapping} />
            <SSOMappingTable mappings={mappings} onDelete={(mappingId) => void deleteMapping(mappingId)} />
          </>
        ) : (
          <div className="table-placeholder">No SSO provider selected</div>
        )}
      </section>

	      {selectedProvider && <DirectoryAutomationPanel key={selectedProvider.id} api={api} provider={selectedProvider} teams={teams} />}
    </>
  );
}

function SSOProviderForm({
  draft,
  busy,
  onChange,
  onSubmit,
}: {
  draft: SSOProviderInput;
  busy: boolean;
  onChange: (draft: SSOProviderInput) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
	const protectedResource = draft.protected_resource;
	const updateProtectedResource = (patch: Partial<SSOProviderInput["protected_resource"]>) => {
		onChange({ ...draft, protected_resource: { ...protectedResource, ...patch } });
	};
	const updateScopeMapping = (index: number, patch: Partial<SSOProviderInput["protected_resource"]["scope_mappings"][number]>) => {
		updateProtectedResource({
			scope_mappings: protectedResource.scope_mappings.map((mapping, current) => current === index ? { ...mapping, ...patch } : mapping),
		});
	};

	return (
    <form className="edit-grid" onSubmit={onSubmit}>
      <label htmlFor="sso-provider-name">Name</label>
      <input id="sso-provider-name" value={draft.name} onChange={(event) => onChange({ ...draft, name: event.target.value })} />
      <label htmlFor="sso-provider-kind">Kind</label>
      <select
        id="sso-provider-kind"
        value={draft.kind}
	        onChange={(event) => {
	          const kind = event.target.value as SSOProviderInput["kind"];
	          const previousDefault = draft.kind === "azure_ad" ? "oid" : "sub";
	          const nextDefault = kind === "azure_ad" ? "oid" : "sub";
	          onChange({ ...draft, kind, identity_claim: draft.identity_claim === previousDefault ? nextDefault : draft.identity_claim });
	        }}
      >
        <option value="azure_ad">Azure AD</option>
        <option value="pingone">PingOne</option>
        <option value="generic_oidc">OIDC</option>
      </select>
      <label htmlFor="sso-issuer-url">Issuer URL</label>
      <input id="sso-issuer-url" value={draft.issuer_url} onChange={(event) => onChange({ ...draft, issuer_url: event.target.value })} />
      <label htmlFor="sso-tenant-id">Tenant ID</label>
      <input id="sso-tenant-id" value={draft.tenant_id} onChange={(event) => onChange({ ...draft, tenant_id: event.target.value })} placeholder="Azure Entra tenant GUID" />
      <label htmlFor="sso-identity-claim">Stable identity claim</label>
      <input id="sso-identity-claim" value={draft.identity_claim} onChange={(event) => onChange({ ...draft, identity_claim: event.target.value })} placeholder={draft.kind === "azure_ad" ? "oid" : "sub"} />
      <label htmlFor="sso-client-id">Client ID</label>
      <input id="sso-client-id" value={draft.client_id} onChange={(event) => onChange({ ...draft, client_id: event.target.value })} />
      <label htmlFor="sso-client-secret-env">Client secret env</label>
      <input id="sso-client-secret-env" value={draft.client_secret_env} onChange={(event) => onChange({ ...draft, client_secret_env: event.target.value })} />
      <label htmlFor="sso-scopes">Scopes</label>
      <input id="sso-scopes" value={draft.scopes.join(", ")} onChange={(event) => onChange({ ...draft, scopes: splitList(event.target.value) })} />
      <label htmlFor="sso-group-claims">Group claims</label>
      <input id="sso-group-claims" value={draft.group_claims.join(", ")} onChange={(event) => onChange({ ...draft, group_claims: splitList(event.target.value) })} />
      <label htmlFor="sso-groups-endpoint">Groups endpoint</label>
      <input id="sso-groups-endpoint" value={draft.groups_endpoint} onChange={(event) => onChange({ ...draft, groups_endpoint: event.target.value })} />
		<label htmlFor="sso-groups-scopes">Groups scopes</label>
		<input id="sso-groups-scopes" value={draft.groups_scopes.join(", ")} onChange={(event) => onChange({ ...draft, groups_scopes: splitList(event.target.value) })} />
		<label>OAuth protected resource</label>
		<fieldset className="oauth-provider-config">
			<legend>MCP access tokens</legend>
			<label className="toggle-row span" htmlFor="sso-oauth-enabled">
				<span>Accept OAuth JWTs on MCP</span>
				<input id="sso-oauth-enabled" type="checkbox" checked={protectedResource.enabled} onChange={(event) => updateProtectedResource({ enabled: event.target.checked })} />
			</label>
			<label htmlFor="sso-oauth-audiences">Allowed audiences</label>
			<input id="sso-oauth-audiences" value={protectedResource.audiences.join(", ")} onChange={(event) => updateProtectedResource({ audiences: splitList(event.target.value) })} placeholder="api://dense-mem" />
			<label htmlFor="sso-oauth-jwks-source">JWKS source</label>
			<select id="sso-oauth-jwks-source" value={protectedResource.jwks_source} onChange={(event) => updateProtectedResource({ jwks_source: event.target.value as "discovery" | "static", jwks_uri: event.target.value === "discovery" ? "" : protectedResource.jwks_uri })}>
				<option value="discovery">OIDC discovery</option>
				<option value="static">Static JWKS URL</option>
			</select>
			<label htmlFor="sso-oauth-jwks-uri">JWKS URL</label>
			<input id="sso-oauth-jwks-uri" value={protectedResource.jwks_uri} disabled={protectedResource.jwks_source === "discovery"} onChange={(event) => updateProtectedResource({ jwks_uri: event.target.value })} placeholder="https://idp.example.com/.well-known/jwks.json" />
			<label htmlFor="sso-oauth-algorithms">Signature algorithms</label>
			<input id="sso-oauth-algorithms" value={protectedResource.algorithms.join(", ")} onChange={(event) => updateProtectedResource({ algorithms: splitList(event.target.value) })} />
			<label htmlFor="sso-oauth-scope-claim">Scope claim</label>
			<input id="sso-oauth-scope-claim" value={protectedResource.scope_claim} onChange={(event) => updateProtectedResource({ scope_claim: event.target.value })} placeholder="scope or scp" />
			<label htmlFor="sso-oauth-team-claim">Team claim</label>
			<input id="sso-oauth-team-claim" value={protectedResource.team_claim} onChange={(event) => updateProtectedResource({ team_claim: event.target.value })} placeholder="Optional UUID claim" />
			<div className="oauth-mapping-heading span">
				<strong>Scope mappings</strong>
				<button className="ghost-button compact" type="button" onClick={() => updateProtectedResource({ scope_mappings: [...protectedResource.scope_mappings, { external_scope: "", internal_scopes: ["read"] }] })}>
					<Plus size={15} aria-hidden="true" />
					Add mapping
				</button>
			</div>
			{protectedResource.scope_mappings.map((mapping, index) => (
				<div className="oauth-scope-mapping span" key={`${mapping.external_scope}-${index}`}>
					<label htmlFor={`sso-oauth-external-scope-${index}`}>External scope</label>
					<input id={`sso-oauth-external-scope-${index}`} value={mapping.external_scope} onChange={(event) => updateScopeMapping(index, { external_scope: event.target.value })} />
					<div className="permission-checkbox-group" aria-label={`Internal scopes for ${mapping.external_scope || `mapping ${index + 1}`}`}>
						{(["read", "write", "feedback:read"] as const).map((scope) => (
							<label className="permission-checkbox" key={scope}>
								<input
									type="checkbox"
									checked={mapping.internal_scopes.includes(scope)}
									onChange={(event) => updateScopeMapping(index, { internal_scopes: event.target.checked ? [...mapping.internal_scopes, scope] : mapping.internal_scopes.filter((value) => value !== scope) })}
								/>
								{scope}
							</label>
						))}
					</div>
					<button className="icon-button danger-icon" type="button" aria-label={`Remove OAuth scope mapping ${index + 1}`} onClick={() => updateProtectedResource({ scope_mappings: protectedResource.scope_mappings.filter((_, current) => current !== index) })}>
						<Trash2 size={15} aria-hidden="true" />
					</button>
				</div>
			))}
		</fieldset>
		<label htmlFor="sso-enabled">Enabled</label>
      <label className="checkbox-row" htmlFor="sso-enabled">
        <input id="sso-enabled" type="checkbox" checked={draft.enabled} onChange={(event) => onChange({ ...draft, enabled: event.target.checked })} />
        <span>{draft.enabled ? "enabled" : "disabled"}</span>
      </label>
      <div className="button-row span">
        <button className="primary-button" type="submit" disabled={busy}>
          <Check size={16} aria-hidden="true" />
          Save provider
        </button>
      </div>
    </form>
  );
}

function SSOMappingForm({
  teams,
  draft,
  busy,
  onChange,
  onSubmit,
}: {
  teams: Team[];
  draft: SSOGroupMappingInput;
  busy: boolean;
  onChange: (draft: SSOGroupMappingInput) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  function updateRole(role: MembershipRole) {
    onChange({ ...draft, role, scopes: role === "manager" ? ["read", "write", ...(draft.scopes.includes("feedback:read") ? ["feedback:read"] : [])] : draft.scopes });
  }
  const feedbackAccess = draft.scopes.includes("feedback:read");

  return (
    <form className="inline-form sso-mapping-form" onSubmit={onSubmit}>
      <label htmlFor="sso-map-team">Team</label>
      <select id="sso-map-team" value={draft.team_id} onChange={(event) => onChange({ ...draft, team_id: event.target.value })}>
        {teams.map((team) => <option value={team.id} key={team.id}>{team.name}</option>)}
      </select>
      <label htmlFor="sso-map-group-id">Group ID</label>
      <input id="sso-map-group-id" value={draft.group_id} onChange={(event) => onChange({ ...draft, group_id: event.target.value })} />
      <label htmlFor="sso-map-role">Role</label>
      <select id="sso-map-role" value={draft.role} onChange={(event) => updateRole(event.target.value as MembershipRole)}>
        <option value="member">Member</option>
        <option value="manager">Manager</option>
      </select>
      {draft.role === "member" && (
        <>
          <label htmlFor="sso-map-permission">Permission</label>
          <select
            id="sso-map-permission"
            value={draft.scopes.includes("write") ? "read_write" : "read"}
            onChange={(event) => onChange({ ...draft, scopes: [...(event.target.value === "read_write" ? ["read", "write"] : ["read"]), ...(feedbackAccess ? ["feedback:read"] : [])] })}
          >
            <option value="read">Read</option>
            <option value="read_write">Read/write</option>
          </select>
        </>
      )}
      <label className="toggle-row span" htmlFor="sso-map-feedback-access">
        <input id="sso-map-feedback-access" type="checkbox" checked={feedbackAccess} onChange={(event) => onChange({ ...draft, scopes: event.target.checked ? [...draft.scopes, "feedback:read"] : draft.scopes.filter((scope) => scope !== "feedback:read") })} />
        <span>Recall feedback access</span>
      </label>
      <label className="toggle-row span" htmlFor="sso-map-enabled">
        <span>Enabled</span>
        <input id="sso-map-enabled" type="checkbox" checked={draft.enabled} onChange={(event) => onChange({ ...draft, enabled: event.target.checked })} />
      </label>
      <button className="primary-button compact" type="submit" disabled={busy || teams.length === 0}>
        <Plus size={16} aria-hidden="true" />
        Add
      </button>
    </form>
  );
}

function SSOMappingTable({ mappings, onDelete }: { mappings: SSOGroupMapping[]; onDelete: (mappingId: string) => void }) {
  if (mappings.length === 0) {
    return <div className="table-placeholder">No group mappings</div>;
  }
  return (
    <div className="table-wrap">
      <table className="data-table">
        <thead>
          <tr>
            <th>Team</th>
            <th>Group</th>
            <th>Permission</th>
            <th>Role</th>
            <th>Status</th>
            <th className="actions-cell">Actions</th>
          </tr>
        </thead>
        <tbody>
          {mappings.map((mapping) => (
            <tr key={mapping.id}>
              <td>{mapping.team_name || shortId(mapping.team_id)}</td>
              <td><code>{mapping.group_id}</code></td>
              <td>{membershipGrantLabel(mapping.scopes)}</td>
              <td>{membershipRoleLabel(mapping.role)}</td>
              <td><span className={mapping.enabled ? "status-pill neutral" : "status-pill warning"}>{mapping.enabled ? "enabled" : "disabled"}</span></td>
              <td className="actions-cell">
                <button className="icon-button danger-icon" type="button" aria-label="Delete group mapping" onClick={() => onDelete(mapping.id)}>
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

function emptyProviderInput(): SSOProviderInput {
  return {
    name: "",
    kind: "azure_ad",
    issuer_url: "",
    tenant_id: "",
    identity_claim: "oid",
    client_id: "",
    client_secret_env: "",
    scopes: ["openid", "profile", "email"],
    group_claims: ["groups"],
		groups_endpoint: "",
		groups_scopes: [],
		protected_resource: {
			enabled: false,
			audiences: [],
			jwks_source: "discovery",
			jwks_uri: "",
			algorithms: ["RS256"],
			scope_claim: "scope",
			scope_mappings: [],
			team_claim: "",
		},
		enabled: true,
  };
}

function providerToInput(provider: SSOProvider): SSOProviderInput {
  return {
    name: provider.name,
    kind: provider.kind,
    issuer_url: provider.issuer_url,
    tenant_id: provider.tenant_id,
    identity_claim: provider.identity_claim,
    client_id: provider.client_id,
    client_secret_env: provider.client_secret_env,
    scopes: provider.scopes,
    group_claims: provider.group_claims,
		groups_endpoint: provider.groups_endpoint,
		groups_scopes: provider.groups_scopes,
		protected_resource: provider.protected_resource,
		enabled: provider.enabled,
  };
}

function emptyMappingInput(teamId: string): SSOGroupMappingInput {
  return {
    team_id: teamId,
    group_id: "",
    scopes: ["read"],
    role: "member",
    enabled: true,
  };
}

function splitList(value: string): string[] {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}
