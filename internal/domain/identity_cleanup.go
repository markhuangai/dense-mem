package domain

// IdentityCleanupBlocker is a bounded, operator-safe reason why irreversible
// legacy identity cleanup cannot run yet.
type IdentityCleanupBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type IdentityCleanupPreflight struct {
	Ready              bool                     `json:"ready"`
	BridgeState        string                   `json:"bridge_state"`
	LegacyProfileCount int64                    `json:"legacy_profile_count"`
	IdentityCount      int64                    `json:"identity_count"`
	MembershipCount    int64                    `json:"membership_count"`
	CredentialCount    int64                    `json:"credential_count"`
	AliasCount         int64                    `json:"alias_count"`
	UnresolvedCount    int64                    `json:"unresolved_count"`
	Blockers           []IdentityCleanupBlocker `json:"blockers"`
}
