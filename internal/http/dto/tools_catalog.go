package dto

// ToolCatalogEntry is the public JSON shape for one tool in GET /api/v1/tools.
// Intentionally omits internal Go references: package paths, struct names,
// function types, and domain types never surface in the response (AC-32).
type ToolCatalogEntry struct {
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	InputSchema     map[string]any `json:"input_schema"`
	OutputSchema    map[string]any `json:"output_schema"`
	RequiredScopes  []string       `json:"required_scopes"`
	ContractVersion string         `json:"contract_version,omitempty"`
	FeatureGate     string         `json:"feature_gate,omitempty"`
	Visibility      string         `json:"visibility,omitempty"`
}

// ToolCatalogResponse is the envelope for the catalog listing.
type ToolCatalogResponse struct {
	Tools []ToolCatalogEntry `json:"tools"`
}
