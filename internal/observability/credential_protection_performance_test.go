package observability

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialProtectorKeepsEncodedPrefixMatches(t *testing.T) {
	for _, first := range []string{"A", " ", "+", "%", "\\", "\"", "\n", "\t"} {
		secret := first + "PrivateCredential37"
		forms := []string{
			secret,
			url.QueryEscape(secret),
			encodeCredentialTestUnicodeLayer(secret),
			url.QueryEscape(encodeCredentialTestUnicodeLayer(secret)),
			encodeCredentialTestUnicodeLayer(url.QueryEscape(secret)),
		}
		for _, form := range forms {
			t.Run(form, func(t *testing.T) {
				got := NewCredentialProtector(secret).Snapshot("prefix: "+form+"; suffix", MaxOperationMetadataBytes)
				require.Equal(t, CredentialProtectionAvailable, got.UnavailableReason)
				require.Equal(t, "prefix: "+CredentialProtectionRedacted+"; suffix", got.Value)
			})
		}
	}
}

func BenchmarkCredentialProtectorSQLDiagnostic(b *testing.B) {
	protector := NewCredentialProtector("postgresql://operator:database-credential@postgres/memory", "database-credential", "decision-provider-private-credential", "control-private-credential")
	query := strings.Repeat("\n SELECT relationship_id, predicate_key FROM memory_relationships WHERE team_id = $1 AND space_id = $2;", 40)
	b.ReportAllocs()
	for b.Loop() {
		got := protector.Snapshot(query, MaxOperationMetadataBytes, "dense-mem-presented-api-credential")
		if got.UnavailableReason != CredentialProtectionAvailable || got.Value != query {
			b.Fatal("SQL diagnostic must remain available and unchanged")
		}
	}
}
