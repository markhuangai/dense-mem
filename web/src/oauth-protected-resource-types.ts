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
