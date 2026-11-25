package observability

import (
	"bytes"
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

func TestIndexedCredentialMatchAgreesWithFullVariantScan(t *testing.T) {
	variants := credentialVariants([]string{"key", "key-longer", "x-key", "a+b", "%literal", "\\value", "\"quoted", "🙂", "é", "�", "\nkey"})
	index := newCredentialVariantIndex(variants)
	inputs := []string{"key-longer", "xx x-key", "%6bey-longer", `\uD83D\uDE42`, `\uD800`, `\251`, `\x6Bey`, `\u00E9`, `\qinvalid`, "\\\\\"quoted", "unrelated"}
	for _, seed := range []string{"key", "key-longer", "a+b", "%literal", "\\value", "\"quoted"} {
		for depth := 1; depth <= maxCredentialDecodeLayers+1; depth++ {
			percent := seed
			escapes := seed
			reverse := seed
			for pass := 0; pass < depth; pass++ {
				percent = url.QueryEscape(percent)
				if pass == 0 {
					percent = "%" + fmt.Sprintf("%02X", seed[0]) + url.QueryEscape(seed[1:])
				}
				escapes = url.QueryEscape(encodeCredentialTestUnicodeLayer(escapes))
				reverse = encodeCredentialTestUnicodeLayer(url.QueryEscape(reverse))
			}
			inputs = append(inputs, percent, escapes, reverse)
		}
	}
	for _, input := range inputs {
		for _, tail := range []string{"", strings.Repeat("suffix", 120)} {
			current := input + tail
			for position := range min(96, len(current)) {
				suffix := current[position:]
				var want credentialVariant
				consumed := 0
				exhausted := false
				for _, candidate := range variants {
					length, ok, depth := credentialPrefixDetailed(suffix, candidate)
					exhausted = exhausted || depth
					if ok && length > consumed {
						want, consumed = candidate, length
					}
				}
				got, length, matched, depth := index.matchAt(suffix, nil)
				require.Equal(t, consumed, length, "input %q at byte %d", input[:min(len(input), 64)], position)
				require.Equal(t, want, got)
				require.Equal(t, consumed > 0, matched)
				require.Equal(t, consumed == 0 && exhausted, depth)
			}
		}
	}
}

func TestIndexedCredentialMatchPreservesMixedEscapeDepthFailures(t *testing.T) {
	protector := NewCredentialProtector("provider-secret")
	for count := 8; count < 16; count++ {
		for _, tail := range []string{"%22", "%6E", "n"} {
			text := strings.Repeat("%5C", count) + tail
			wantDepth := false
			for _, variant := range protector.index.variants {
				_, _, exhausted := credentialPrefixDetailed(text, variant)
				wantDepth = wantDepth || exhausted
			}
			_, _, matched, depth := protector.index.matchAt(text, nil)
			require.False(t, matched)
			require.Equal(t, wantDepth, depth, "escape count %d, suffix %s", count, tail)
			if count == 8 || tail == "%22" {
				got := protector.Snapshot(text, 2048)
				require.Equal(t, CredentialProtectionEncodingLimitExceeded, got.UnavailableReason)
				require.Nil(t, got.Value)
			}
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

func TestCredentialProtectorPreservesLiteralRunsAroundEncodedCredentials(t *testing.T) {
	const secret = "private-provider-credential"
	forms := []string{secret, "%70rivate-provider-credential", encodeCredentialTestUnicodeLayer(secret), url.QueryEscape(encodeCredentialTestUnicodeLayer(secret))}
	for _, size := range []int{0, 1, 63, 64, 639, 640, 641, 2048} {
		for _, form := range forms {
			t.Run(fmt.Sprintf("padding=%d/form=%s", size, form), func(t *testing.T) {
				prefix := ` {"content":"` + strings.Repeat("x", size) + `%zz\q+ordinary `
				suffix := ` ` + strings.Repeat("y", size) + `","password":"ordinary admitted text","unicode":"é😀"} `
				body := prefix + form + suffix
				got, reason := ProtectDiagnosticText(context.Background(), NewCredentialProtector(secret), []byte(body), 1<<20)
				require.Equal(t, CredentialProtectionAvailable, reason)
				require.Equal(t, prefix+CredentialProtectionRedacted+suffix, string(got))
			})
		}
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

func credentialTestAssessmentBody(value string) []byte {
	return []byte(`{"messages":[{"role":"system","content":"Classify every item.\nReturn complete decisions."},{"role":"user","content":"` +
		strings.Repeat(`{\"ref\":\"source\",\"text\":\"Atlas uses PostgreSQL.\\nKeep ordinary password and token fields exact.\"},`, 20) +
		value + `"}],"response_format":{"type":"json_schema"}}`)
}

func TestCredentialProtectorKeepsStructuredAssessmentBodies(t *testing.T) {
	const secret = "a+b"
	encoded := secret
	for range maxCredentialDecodeLayers - 1 {
		encoded = encodeCredentialTestUnicodeLayer(encoded)
	}
	protector := NewCredentialProtector("dense-mem-ontology-e2e-key", "synthetic-control-credential", "disabled", secret)
	for _, value := range []string{"ordinary admitted text", url.QueryEscape(encoded)} {
		body := credentialTestAssessmentBody(value)
		got, reason := ProtectDiagnosticText(context.Background(), protector, body, 1<<20)
		require.Equal(t, CredentialProtectionAvailable, reason)
		require.Equal(t, bytes.ReplaceAll(body, []byte(url.QueryEscape(encoded)), []byte(CredentialProtectionRedacted)), got)
	}
}

func BenchmarkCredentialProtectorStructuredAssessment(b *testing.B) {
	protector := NewCredentialProtector("postgres://operator:synthetic-database-credential@postgres/memory", "dense-mem-ontology-e2e-key", "synthetic-control-credential", "disabled")
	body := credentialTestAssessmentBody("ordinary admitted text")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		got, reason := ProtectDiagnosticText(context.Background(), protector, body, 1<<20)
		if reason != CredentialProtectionAvailable || !bytes.Equal(body, got) {
			b.Fatal("complete structured assessment must remain exact and available")
		}
	}
}
