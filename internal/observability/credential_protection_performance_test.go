package observability

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
)

func TestCredentialProtectorKeepsEncodedPrefixMatches(t *testing.T) {
	for _, first := range []string{"A", " ", "+", "%", "\\", "\"", "\n", "\t", "9", "é", "😀"} {
		secret := first + "PrivateCredential37"
		var unicode strings.Builder
		for _, unit := range utf16.Encode([]rune(secret)) {
			fmt.Fprintf(&unicode, "\\u%04X", unit)
		}
		forms := []string{
			secret,
			url.QueryEscape(secret),
			unicode.String(),
			url.QueryEscape(unicode.String()),
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

func TestCredentialProtectorPreservesNumericPayloadAroundEncodedCredentials(t *testing.T) {
	const secret = "provider-credential"
	const presented = "presented-credential"
	for _, encoded := range []string{secret, url.QueryEscape(encodeCredentialTestUnicodeLayer(secret)), encodeCredentialTestUnicodeLayer(url.QueryEscape(secret))} {
		prefix := ` {"data":[` + strings.Repeat("0.00012345678,", 16000) + `0],"secret":"`
		suffix := `","password":"ordinary admitted text","authentication":"` + presented + `"} `
		body := prefix + encoded + suffix
		ctx := WithAuthenticationSecrets(context.Background(), presented)
		got, reason := ProtectDiagnosticText(ctx, NewCredentialProtector(secret), []byte(body), 1<<20)
		require.Equal(t, CredentialProtectionAvailable, reason)
		require.Equal(t, prefix+CredentialProtectionRedacted+strings.ReplaceAll(suffix, presented, CredentialProtectionRedacted), string(got))
	}
}

func BenchmarkCredentialProtectorEmbeddingResponse(b *testing.B) {
	protector := NewCredentialProtector("synthetic-provider-key", "synthetic-authentication-material", "synthetic-database-password")
	body := []byte(`{"data":[` + strings.Repeat("0.00012345678,", 1536*16) + `0],"model":"fixture"}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		got, reason := ProtectDiagnosticText(context.Background(), protector, body, 16<<20)
		if reason != CredentialProtectionAvailable || string(got) != string(body) {
			b.Fatal("complete embedding body must remain exact and available")
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
