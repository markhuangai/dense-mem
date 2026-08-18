export type MembershipRole = "manager" | "member";

export type OAuthScopeMapping = {
  external_scope: string;
  internal_scopes: Array<"read" | "write" | "feedback:read">;
};

export type OAuthProtectedResourceConfig = {
  enabled: boolean;
  audiences: string[];
  jwks_source: "discovery" | "static";
  jwks_uri: string;
  algorithms: string[];
  scope_claim: string;
  scope_mappings: OAuthScopeMapping[];
  team_claim: string;
};

export type SSOProvider = {
  id: string;
  name: string;
  kind: "azure_ad" | "pingone" | "generic_oidc";
  issuer_url: string;
  tenant_id: string;
  identity_claim: string;
  client_id: string;
  client_secret_env: string;
  scopes: string[];
  group_claims: string[];
  groups_endpoint: string;
  groups_scopes: string[];
  protected_resource: OAuthProtectedResourceConfig;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type SSOGroupMapping = {
  id: string;
  provider_id: string;
  team_id: string;
  team_name: string;
  group_id: string;
  group_name: string;
  scopes: string[];
  role: MembershipRole;
  enabled: boolean;
  origin: string;
  retired_at: string | null;
  created_at: string;
  updated_at: string;
};

export type SSOProviderInput = {
  name: string;
  kind: SSOProvider["kind"];
  issuer_url: string;
  tenant_id: string;
  identity_claim: string;
  client_id: string;
  client_secret_env: string;
  scopes: string[];
  group_claims: string[];
  groups_endpoint: string;
  groups_scopes: string[];
  protected_resource: OAuthProtectedResourceConfig;
  enabled: boolean;
};

export type SSOGroupMappingInput = {
  team_id: string;
  group_id: string;
  scopes: string[];
  role: MembershipRole;
  enabled: boolean;
};

export type DirectoryRoleEntitlement = {
  role: MembershipRole;
  scopes: string[];
};

export type DirectoryConnector = {
  id: string;
  provider_id: string;
  status: "disabled" | "observe" | "active";
  group_pattern: string;
  role_entitlements: Record<string, DirectoryRoleEntitlement>;
  max_auto_teams: number;
  credential_version: number;
  scim_path: string;
  last_activation_at: string | null;
  created_at: string;
  updated_at: string;
};

export type DirectoryConnectorInput = {
  group_pattern: string;
  role_entitlements: Record<string, DirectoryRoleEntitlement>;
  max_auto_teams: number;
};

export type DirectoryCredential = {
  connector_id: string;
  credential_version: number;
  bearer_token: string;
  oauth_client_id: string;
  oauth_client_secret: string;
};

export type DirectoryConnectorCreateResult = {
  connector: DirectoryConnector;
  credential: DirectoryCredential;
};

export type DirectoryPreview = {
  version: string;
  candidates: Array<{
    group_id: string;
    external_id: string;
    display_name: string;
    team_id: string;
    team_name: string;
    entitlement: DirectoryRoleEntitlement;
    binding_origin: string;
  }>;
  issues: Array<{
    kind: string;
    detail: string;
    active: boolean;
  }>;
};

export type ControlAdminGroup = {
  id: string;
  provider_id: string;
  group_id: string;
  group_name: string;
  enabled: boolean;
  retired_at: string | null;
  created_at: string;
  updated_at: string;
};

export type ControlAdminGroupInput = {
  group_id: string;
  group_name: string;
  enabled: boolean;
};
