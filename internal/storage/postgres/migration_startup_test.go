package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIdentityBridgeFunctionExecutionAttributesMatch(t *testing.T) {
	valid := []string{"search_path=public"}
	for _, tc := range []struct {
		name          string
		legacyDefiner bool
		legacyConfig  []string
		ssoDefiner    bool
		ssoConfig     []string
		want          bool
	}{
		{name: "valid", legacyDefiner: true, legacyConfig: valid, ssoDefiner: true, ssoConfig: valid, want: true},
		{name: "legacy invoker", legacyConfig: valid, ssoDefiner: true, ssoConfig: valid},
		{name: "legacy search path missing", legacyDefiner: true, ssoDefiner: true, ssoConfig: valid},
		{name: "legacy extra config", legacyDefiner: true, legacyConfig: []string{"search_path=public", "statement_timeout=1s"}, ssoDefiner: true, ssoConfig: valid},
		{name: "sso invoker", legacyDefiner: true, legacyConfig: valid, ssoConfig: valid},
		{name: "sso search path changed", legacyDefiner: true, legacyConfig: valid, ssoDefiner: true, ssoConfig: []string{"search_path=pg_catalog"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IdentityBridgeFunctionExecutionAttributesMatch(tc.legacyDefiner, tc.legacyConfig, tc.ssoDefiner, tc.ssoConfig))
		})
	}
}
