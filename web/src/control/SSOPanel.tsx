import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/primitives/tabs";
import { useConfirmation } from "../ui/confirmation";
import { useUnsavedChanges, useViewTransition } from "../ui/navigation";
import { Button } from "../ui/primitives/button";
import { Table, TableHeader, TableRow, TableHead, TableBody, TableCell } from "../ui/primitives/table";
import { Label } from "../ui/primitives/label";
import { Input } from "../ui/primitives/input";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Switch } from "../ui/primitives/switch";
import { Checkbox } from "../ui/primitives/checkbox";
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
import { FieldRow, SectionHeading } from "../ui/components";
import { DirectoryAutomationPanel } from "./DirectoryAutomationPanel";
import { membershipGrantLabel, membershipRoleLabel, readError, shortId } from "./utils";

export function SSOPanel({ api, teams }: { api: ControlApi; teams: Team[] }) {
  const confirm = useConfirmation();
  const transition = useViewTransition();
  const [activeTab, setActiveTab] = useState("providers");
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

  useUnsavedChanges(JSON.stringify(providerDraft) !== JSON.stringify(selectedProvider ? providerToInput(selectedProvider) : emptyProviderInput()) || JSON.stringify(mappingDraft) !== JSON.stringify(emptyMappingInput(teams[0]?.id ?? "")));

  function selectProvider(providerId: string) {
    if (providerId !== selectedProviderIdRef.current) setMappingDraft(emptyMappingInput(teams[0]?.id ?? ""));
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
    if (!await confirm({ title: "Delete?", description: "Delete this SSO provider and its mappings?", confirmLabel: "Delete", destructive: true })) {
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
    if (!providerId || !await confirm({ title: "Delete?", description: "Delete this group mapping?", confirmLabel: "Delete", destructive: true })) {
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
    <Tabs value={activeTab} onValueChange={setActiveTab} className="panel-tabs">
      <SectionHeading title="Identity & access" subtitle={selectedProvider ? `Provider: ${selectedProvider.name}` : "Manage sign-in providers, group mappings, and directory provisioning."} />
      <TabsList aria-label="Identity and access sections"><TabsTrigger value="providers">Providers</TabsTrigger><TabsTrigger value="mappings">Mappings</TabsTrigger><TabsTrigger value="directory">Directory provisioning</TabsTrigger></TabsList>
      <TabsContent value="providers" forceMount hidden={activeTab !== "providers"}><section className="surface">
        <SectionHeading
          title="SSO providers"
          meta={providers.length}
          actions={(
            <div className="button-row">
              <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label="Refresh SSO providers" onClick={() => void transition(() => loadProviders())}>
                <RefreshCw size={16} aria-hidden="true" />
              </Button>
              <Button variant="outline"
                className="ghost-button"
                type="button"
                onClick={() => void transition(() => {
                  selectProvider("");
                  setProviderDraft(emptyProviderInput());
                })}
              >
                <Plus size={16} aria-hidden="true" />
                New
              </Button>
            </div>
          )}
        />
        {error && <div className="banner error" role="alert">{error}</div>}
        <div className="table-wrap sso-provider-table-wrap">
          <Table className="data-table">
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead>Issuer</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="actions-cell">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {providers.map((provider) => (
                <TableRow key={provider.id}>
                  <TableCell>{provider.name}</TableCell>
                  <TableCell>{provider.kind}</TableCell>
                  <TableCell><code>{provider.issuer_url}</code></TableCell>
                  <TableCell><span className={provider.enabled ? "status-pill neutral" : "status-pill warning"}>{provider.enabled ? "enabled" : "disabled"}</span></TableCell>
                  <TableCell className="actions-cell">
                    <div className="button-row">
                      <Button variant="ghost" size="icon" className="icon-button" type="button" aria-label={`Edit ${provider.name}`} onClick={() => void transition(() => selectProvider(provider.id))}>
                        <Pencil size={16} aria-hidden="true" />
                      </Button>
                      <Button variant="ghost" size="icon" className="icon-button danger-icon" type="button" aria-label={`Delete ${provider.name}`} onClick={() => void deleteProvider(provider.id)}>
                        <Trash2 size={16} aria-hidden="true" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        <SSOProviderForm key={selectedProviderId || "new"} draft={providerDraft} busy={loading} onChange={setProviderDraft} onSubmit={saveProvider} />
      </section></TabsContent>

      <TabsContent value="mappings" forceMount hidden={activeTab !== "mappings"}><section className="surface">
        <SectionHeading title="Group mappings" meta={mappings.length} />
        {selectedProvider ? (
          <>
            <SSOMappingForm teams={teams} draft={mappingDraft} busy={loading} onChange={setMappingDraft} onSubmit={saveMapping} />
            <SSOMappingTable mappings={mappings} onDelete={(mappingId) => void deleteMapping(mappingId)} />
          </>
        ) : (
          <div className="table-placeholder">No SSO provider selected</div>
        )}
      </section></TabsContent>

      <TabsContent value="directory" forceMount hidden={activeTab !== "directory"}>
	      {selectedProvider && <DirectoryAutomationPanel key={selectedProvider.id} api={api} provider={selectedProvider} teams={teams} />}
      {!selectedProvider && <div className="table-placeholder">Select a provider in Providers to configure directory provisioning.</div>}
      </TabsContent>
    </Tabs>
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
	const scopeMappingIDs = useRef<string[]>([]);
	const nextScopeMappingID = useRef(0);
	while (scopeMappingIDs.current.length < protectedResource.scope_mappings.length) {
		scopeMappingIDs.current.push(`oauth-scope-mapping-${nextScopeMappingID.current}`);
		nextScopeMappingID.current += 1;
	}
	if (scopeMappingIDs.current.length > protectedResource.scope_mappings.length) {
		scopeMappingIDs.current.length = protectedResource.scope_mappings.length;
	}
	const updateProtectedResource = (patch: Partial<SSOProviderInput["protected_resource"]>) => {
		onChange({ ...draft, protected_resource: { ...protectedResource, ...patch } });
	};
	const updateScopeMapping = (index: number, patch: Partial<SSOProviderInput["protected_resource"]["scope_mappings"][number]>) => {
		updateProtectedResource({
			scope_mappings: protectedResource.scope_mappings.map((mapping, current) => current === index ? { ...mapping, ...patch } : mapping),
		});
	};
	const addScopeMapping = () => {
		scopeMappingIDs.current.push(`oauth-scope-mapping-${nextScopeMappingID.current}`);
		nextScopeMappingID.current += 1;
		updateProtectedResource({ scope_mappings: [...protectedResource.scope_mappings, { external_scope: "", internal_scopes: ["read"] }] });
	};
	const removeScopeMapping = (index: number) => {
		scopeMappingIDs.current = scopeMappingIDs.current.filter((_, current) => current !== index);
		updateProtectedResource({ scope_mappings: protectedResource.scope_mappings.filter((_, current) => current !== index) });
	};

	return (
    <form className="edit-grid" onSubmit={onSubmit}>
      <fieldset className="setting-group"><legend>Connection and identity</legend>
      <FieldRow label="Name" htmlFor="sso-provider-name">
        <Input id="sso-provider-name" value={draft.name} onChange={(event) => onChange({ ...draft, name: event.target.value })} />
      </FieldRow>
      <FieldRow label="Kind" htmlFor="sso-provider-kind">
        <NativeSelect
          id="sso-provider-kind"
          value={draft.kind}
                  onChange={(event) => {
                    const kind = event.target.value as SSOProviderInput["kind"];
                    const previousDefault = draft.kind === "azure_ad" ? "oid" : "sub";
                    const nextDefault = kind === "azure_ad" ? "oid" : "sub";
                    onChange({ ...draft, kind, identity_claim: draft.identity_claim === previousDefault ? nextDefault : draft.identity_claim });
                  }}
        >
          <NativeSelectOption value="azure_ad">Azure AD</NativeSelectOption>
          <NativeSelectOption value="pingone">PingOne</NativeSelectOption>
          <NativeSelectOption value="generic_oidc">OIDC</NativeSelectOption>
        </NativeSelect>
      </FieldRow>
      <FieldRow label="Issuer URL" htmlFor="sso-issuer-url">
        <Input id="sso-issuer-url" value={draft.issuer_url} onChange={(event) => onChange({ ...draft, issuer_url: event.target.value })} />
      </FieldRow>
      <FieldRow label="Tenant ID" htmlFor="sso-tenant-id">
        <Input id="sso-tenant-id" value={draft.tenant_id} onChange={(event) => onChange({ ...draft, tenant_id: event.target.value })} placeholder="Azure Entra tenant GUID" />
      </FieldRow>
      <FieldRow label="Stable identity claim" htmlFor="sso-identity-claim">
        <Input id="sso-identity-claim" value={draft.identity_claim} onChange={(event) => onChange({ ...draft, identity_claim: event.target.value })} placeholder={draft.kind === "azure_ad" ? "oid" : "sub"} />
      </FieldRow>
      <FieldRow label="Client ID" htmlFor="sso-client-id">
        <Input id="sso-client-id" value={draft.client_id} onChange={(event) => onChange({ ...draft, client_id: event.target.value })} />
      </FieldRow>
      <FieldRow label="Client secret env" htmlFor="sso-client-secret-env">
        <Input id="sso-client-secret-env" value={draft.client_secret_env} onChange={(event) => onChange({ ...draft, client_secret_env: event.target.value })} />
      </FieldRow>
      </fieldset>
      <fieldset className="setting-group"><legend>Claims and groups</legend>
      <FieldRow label="Scopes" htmlFor="sso-scopes">
        <Input id="sso-scopes" value={draft.scopes.join(", ")} onChange={(event) => onChange({ ...draft, scopes: splitList(event.target.value) })} />
      </FieldRow>
      <FieldRow label="Group claims" htmlFor="sso-group-claims">
        <Input id="sso-group-claims" value={draft.group_claims.join(", ")} onChange={(event) => onChange({ ...draft, group_claims: splitList(event.target.value) })} />
      </FieldRow>
      <FieldRow label="Groups endpoint" htmlFor="sso-groups-endpoint">
        <Input id="sso-groups-endpoint" value={draft.groups_endpoint} onChange={(event) => onChange({ ...draft, groups_endpoint: event.target.value })} />
      </FieldRow>
		<FieldRow label="Groups scopes" htmlFor="sso-groups-scopes">
    <Input id="sso-groups-scopes" value={draft.groups_scopes.join(", ")} onChange={(event) => onChange({ ...draft, groups_scopes: splitList(event.target.value) })} />
  </FieldRow>
		</fieldset>
		<fieldset className="oauth-provider-config setting-group">
			<legend>MCP access tokens</legend>
			<Label className="toggle-row span" htmlFor="sso-oauth-enabled">
				<span>Accept OAuth JWTs on MCP</span>
				<Switch id="sso-oauth-enabled" checked={protectedResource.enabled} onCheckedChange={(event) => updateProtectedResource({ enabled: (event === true) })} />
			</Label>
			<FieldRow label="Allowed audiences" htmlFor="sso-oauth-audiences">
     <Input id="sso-oauth-audiences" value={protectedResource.audiences.join(", ")} onChange={(event) => updateProtectedResource({ audiences: splitList(event.target.value) })} placeholder="api://dense-mem" />
   </FieldRow>
			<FieldRow label="JWKS source" htmlFor="sso-oauth-jwks-source">
     <NativeSelect id="sso-oauth-jwks-source" value={protectedResource.jwks_source} onChange={(event) => updateProtectedResource({ jwks_source: event.target.value as "discovery" | "static", jwks_uri: event.target.value === "discovery" ? "" : protectedResource.jwks_uri })}>
            <NativeSelectOption value="discovery">OIDC discovery</NativeSelectOption>
            <NativeSelectOption value="static">Static JWKS URL</NativeSelectOption>
          </NativeSelect>
   </FieldRow>
			<FieldRow label="JWKS URL" htmlFor="sso-oauth-jwks-uri">
     <Input id="sso-oauth-jwks-uri" value={protectedResource.jwks_uri} disabled={protectedResource.jwks_source === "discovery"} onChange={(event) => updateProtectedResource({ jwks_uri: event.target.value })} placeholder="https://idp.example.com/.well-known/jwks.json" />
   </FieldRow>
			<FieldRow label="Signature algorithms" htmlFor="sso-oauth-algorithms">
     <Input id="sso-oauth-algorithms" value={protectedResource.algorithms.join(", ")} onChange={(event) => updateProtectedResource({ algorithms: splitList(event.target.value) })} />
   </FieldRow>
			<FieldRow label="Scope claim" htmlFor="sso-oauth-scope-claim">
     <Input id="sso-oauth-scope-claim" value={protectedResource.scope_claim} onChange={(event) => updateProtectedResource({ scope_claim: event.target.value })} placeholder="scope or scp" />
   </FieldRow>
			<FieldRow label="Team claim" htmlFor="sso-oauth-team-claim">
     <Input id="sso-oauth-team-claim" value={protectedResource.team_claim} onChange={(event) => updateProtectedResource({ team_claim: event.target.value })} placeholder="Optional UUID claim" />
   </FieldRow>
			<div className="oauth-mapping-heading span">
				<strong>Scope mappings</strong>
				<Button variant="outline" className="ghost-button compact" type="button" onClick={addScopeMapping}>
					<Plus size={15} aria-hidden="true" />
					Add mapping
				</Button>
			</div>
			{protectedResource.scope_mappings.map((mapping, index) => {
				const mappingID = scopeMappingIDs.current[index];
				return (
					<div className="oauth-scope-mapping span" key={mappingID}>
						<FieldRow label="External scope" htmlFor={`${mappingID}-external-scope`}>
        <Input id={`${mappingID}-external-scope`} value={mapping.external_scope} onChange={(event) => updateScopeMapping(index, { external_scope: event.target.value })} />
      </FieldRow>
						<div className="permission-checkbox-group" aria-label={`Internal scopes for ${mapping.external_scope || `mapping ${index + 1}`}`}>
							{(["read", "write", "feedback:read"] as const).map((scope) => (
								<Label className="permission-checkbox" key={scope}>
									<Checkbox
										checked={mapping.internal_scopes.includes(scope)}
										onCheckedChange={(event) => updateScopeMapping(index, { internal_scopes: (event === true) ? [...mapping.internal_scopes, scope] : mapping.internal_scopes.filter((value) => value !== scope) })}
									/>
									{scope}
								</Label>
							))}
						</div>
						<Button variant="ghost" size="icon" className="icon-button danger-icon" type="button" aria-label={`Remove OAuth scope mapping ${index + 1}`} onClick={() => removeScopeMapping(index)}>
							<Trash2 size={15} aria-hidden="true" />
						</Button>
					</div>
				);
			})}
		</fieldset>
		<FieldRow label="Enabled" htmlFor="sso-enabled">
    <Label className="checkbox-row" htmlFor="sso-enabled">
          <Checkbox id="sso-enabled" checked={draft.enabled} onCheckedChange={(event) => onChange({ ...draft, enabled: (event === true) })} />
          <span>{draft.enabled ? "enabled" : "disabled"}</span>
        </Label>
  </FieldRow>
      <div className="button-row span">
        <Button variant="default" className="primary-button" type="submit" disabled={busy}>
          <Check size={16} aria-hidden="true" />
          Save provider
        </Button>
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
      <FieldRow label="Team" htmlFor="sso-map-team">
        <NativeSelect id="sso-map-team" value={draft.team_id} onChange={(event) => onChange({ ...draft, team_id: event.target.value })}>
          {teams.map((team) => <NativeSelectOption value={team.id} key={team.id}>{team.name}</NativeSelectOption>)}
        </NativeSelect>
      </FieldRow>
      <FieldRow label="Group ID" htmlFor="sso-map-group-id">
        <Input id="sso-map-group-id" value={draft.group_id} onChange={(event) => onChange({ ...draft, group_id: event.target.value })} />
      </FieldRow>
      <FieldRow label="Role" htmlFor="sso-map-role">
        <NativeSelect id="sso-map-role" value={draft.role} onChange={(event) => updateRole(event.target.value as MembershipRole)}>
          <NativeSelectOption value="member">Member</NativeSelectOption>
          <NativeSelectOption value="manager">Manager</NativeSelectOption>
        </NativeSelect>
      </FieldRow>
      {draft.role === "member" && (
        <>
          <FieldRow label="Permission" htmlFor="sso-map-permission">
            <NativeSelect
              id="sso-map-permission"
              value={draft.scopes.includes("write") ? "read_write" : "read"}
              onChange={(event) => onChange({ ...draft, scopes: [...(event.target.value === "read_write" ? ["read", "write"] : ["read"]), ...(feedbackAccess ? ["feedback:read"] : [])] })}
            >
              <NativeSelectOption value="read">Read</NativeSelectOption>
              <NativeSelectOption value="read_write">Read/write</NativeSelectOption>
            </NativeSelect>
          </FieldRow>
        </>
      )}
      <Label className="toggle-row span" htmlFor="sso-map-feedback-access">
        <Switch id="sso-map-feedback-access" checked={feedbackAccess} onCheckedChange={(event) => onChange({ ...draft, scopes: (event === true) ? [...draft.scopes, "feedback:read"] : draft.scopes.filter((scope) => scope !== "feedback:read") })} />
        <span>Recall feedback access</span>
      </Label>
      <Label className="toggle-row span" htmlFor="sso-map-enabled">
        <span>Enabled</span>
        <Switch id="sso-map-enabled" checked={draft.enabled} onCheckedChange={(event) => onChange({ ...draft, enabled: (event === true) })} />
      </Label>
      <Button variant="default" className="primary-button compact" type="submit" disabled={busy || teams.length === 0}>
        <Plus size={16} aria-hidden="true" />
        Add
      </Button>
    </form>
  );
}

function SSOMappingTable({ mappings, onDelete }: { mappings: SSOGroupMapping[]; onDelete: (mappingId: string) => void }) {
  if (mappings.length === 0) {
    return <div className="table-placeholder">No group mappings</div>;
  }
  return (
    <div className="table-wrap">
      <Table className="data-table">
        <TableHeader>
          <TableRow>
            <TableHead>Team</TableHead>
            <TableHead>Group</TableHead>
            <TableHead>Permission</TableHead>
            <TableHead>Role</TableHead>
            <TableHead>Status</TableHead>
            <TableHead className="actions-cell">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {mappings.map((mapping) => (
            <TableRow key={mapping.id}>
              <TableCell>{mapping.team_name || shortId(mapping.team_id)}</TableCell>
              <TableCell><code>{mapping.group_id}</code></TableCell>
              <TableCell>{membershipGrantLabel(mapping.scopes)}</TableCell>
              <TableCell>{membershipRoleLabel(mapping.role)}</TableCell>
              <TableCell><span className={mapping.enabled ? "status-pill neutral" : "status-pill warning"}>{mapping.enabled ? "enabled" : "disabled"}</span></TableCell>
              <TableCell className="actions-cell">
                <Button variant="ghost" size="icon" className="icon-button danger-icon" type="button" aria-label="Delete group mapping" onClick={() => onDelete(mapping.id)}>
                  <Trash2 size={16} aria-hidden="true" />
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
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
