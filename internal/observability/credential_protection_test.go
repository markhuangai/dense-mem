package observability

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialProtectorPreservesAdmittedContentAndProtectsSuppliedSecrets(t *testing.T) {
	configured := "configured-key"
	escaped := `a"b`
	userinfoSecret := "p@ss:word"
	goEscaped := fmt.Sprintf("quoted=%q", escaped)
	queryEscaped := "https://example.test/path?credential=" + url.QueryEscape(userinfoSecret)
	pathEscaped := "https://example.test/" + url.PathEscape(userinfoSecret)
	userinfoEscaped := (&url.URL{
		Scheme: "https",
		Host:   "example.test",
		User:   url.UserPassword("user", userinfoSecret),
	}).String()

	value := map[string]any{
		"token":    "fictional-token",
		"password": "fictional-password",
		"secret":   "fictional-secret",
		"detail":   "failed with " + configured,
		"go":       goEscaped,
		"query":    queryEscaped,
		"path":     pathEscaped,
		"userinfo": userinfoEscaped,
		"nested": []any{
			map[string]any{"authorization": "Bearer auth-secret"},
		},
	}

	got := NewCredentialProtector(configured, escaped, userinfoSecret).Snapshot(value, 4096, "auth-secret")
	require.Empty(t, got.UnavailableReason)

	encoded, err := json.Marshal(got.Value)
	require.NoError(t, err)
	output := string(encoded)
	require.Contains(t, output, "fictional-token")
	require.Contains(t, output, "fictional-password")
	require.Contains(t, output, "fictional-secret")
	require.NotContains(t, output, configured)
	require.NotContains(t, output, strconv.Quote(escaped))
	require.NotContains(t, output, url.QueryEscape(userinfoSecret))
	require.NotContains(t, output, url.PathEscape(userinfoSecret))
	require.NotContains(t, output, userinfoEscaped)
	require.NotContains(t, output, "auth-secret")
	require.Contains(t, output, CredentialProtectionRedacted)
}

func TestCredentialProtectorProtectsJSONByteContentAndErrors(t *testing.T) {
	protector := NewCredentialProtector("provider-secret")

	jsonValue := protector.Snapshot([]byte(`{"token":"fictional","detail":"provider-secret"}`), 256)
	require.Empty(t, jsonValue.UnavailableReason)
	decoded, ok := jsonValue.Value.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fictional", decoded["token"])
	require.Equal(t, CredentialProtectionRedacted, decoded["detail"])

	errorValue := protector.Snapshot(errors.New("provider failed: provider-secret"), 256)
	require.Empty(t, errorValue.UnavailableReason)
	require.Equal(t, "provider failed: "+CredentialProtectionRedacted, errorValue.Value)
	_, retainsError := errorValue.Value.(error)
	require.False(t, retainsError)

	budgeted := protector.Snapshot([]byte(`{"x":"1234567890"}`), 18)
	require.Empty(t, budgeted.UnavailableReason)
	require.Equal(t, map[string]any{"x": "1234567890"}, budgeted.Value)
}

func TestCredentialProtectorSnapshotsWithoutMutation(t *testing.T) {
	value := map[string]any{
		"nested": []any{map[string]any{"message": "before configured"}},
	}
	before, err := json.Marshal(value)
	require.NoError(t, err)
	beforeHash := sha256.Sum256(before)

	got := NewCredentialProtector("credential").Snapshot(value, 1024)
	require.Empty(t, got.UnavailableReason)
	afterCapture, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, beforeHash, sha256.Sum256(afterCapture))
	value["nested"].([]any)[0].(map[string]any)["message"] = "after"

	snapshot, err := json.Marshal(got.Value)
	require.NoError(t, err)
	require.JSONEq(t, `{"nested":[{"message":"before configured"}]}`, string(snapshot))
}

func TestCredentialProtectorUsesLongestOverlappingVariant(t *testing.T) {
	got := NewCredentialProtector("secret", "secret-long").Snapshot("secret-long secret", 128)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "[REDACTED] [REDACTED]", got.Value)
}

func TestCredentialProtectorUsesLongestEncodedOverlap(t *testing.T) {
	shorter := "<>"
	longer := shorter + "\ncd"
	jsonPrefix, err := json.Marshal(shorter)
	require.NoError(t, err)

	for _, format := range []struct {
		name   string
		prefix string
	}{
		{name: "json", prefix: string(jsonPrefix[1 : len(jsonPrefix)-1])},
		{name: "url", prefix: url.QueryEscape(shorter)},
	} {
		for _, source := range []struct {
			name          string
			configured    []string
			authenticated []string
		}{
			{name: "configured", configured: []string{shorter, longer}},
			{name: "authenticated", configured: []string{shorter}, authenticated: []string{longer}},
		} {
			t.Run(format.name+"/"+source.name, func(t *testing.T) {
				input := "before " + format.prefix + longer[len(shorter):] + " after"
				got := NewCredentialProtector(source.configured...).Snapshot(input, 128, source.authenticated...)
				require.Empty(t, got.UnavailableReason)
				require.Equal(t, "before "+CredentialProtectionRedacted+" after", got.Value)
				encoded, err := json.Marshal(got.Value)
				require.NoError(t, err)
				require.Equal(t, `"before [REDACTED] after"`, string(encoded))
			})
		}
	}
}

func TestCredentialProtectorMatchesMixedCasePercentEscapes(t *testing.T) {
	got := NewCredentialProtector("/:").Snapshot("postgres://u:%2f%3A@host/db", 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "postgres://u:"+CredentialProtectionRedacted+"@host/db", got.Value)

	got = NewCredentialProtector("abc").Snapshot("credential=%61bc", 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "credential="+CredentialProtectionRedacted, got.Value)

	got = NewCredentialProtector("a b c").Snapshot("token=a+b%20c", 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "token="+CredentialProtectionRedacted, got.Value)

	literal := NewCredentialProtector("foo%2F").Snapshot("foo%2f", 64)
	require.Empty(t, literal.UnavailableReason)
	require.Equal(t, "foo%2f", literal.Value)
}

func TestCredentialProtectorMatchesMixedCaseJSONUnicodeEscapes(t *testing.T) {
	got := NewCredentialProtector("<").Snapshot(`{"password":"\u003C"}`, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, `{"password":"`+CredentialProtectionRedacted+`"}`, got.Value)

	got = NewCredentialProtector("secret-key").Snapshot(`{"password":"\u0073ecret-key"}`, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, `{"password":"`+CredentialProtectionRedacted+`"}`, got.Value)

	jsonValue := NewCredentialProtector("abc").Snapshot([]byte(`{"password":"%61bc"}`), 256)
	require.Empty(t, jsonValue.UnavailableReason)
	require.Equal(t, map[string]any{"password": CredentialProtectionRedacted}, jsonValue.Value)
}

func TestCredentialProtectorMatchesComposedJSONAndPercentEncoding(t *testing.T) {
	input := `{"url":"?token=\u002561"}`

	got := NewCredentialProtector("a").Snapshot(input, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, `{"url":"?token=`+CredentialProtectionRedacted+`"}`, got.Value)

	got = NewCredentialProtector("a").Snapshot([]byte(input), 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, map[string]any{"url": "?token=" + CredentialProtectionRedacted}, got.Value)

	input = `https://e.test/?payload=%7B%22password%22%3A%22%5Cu0073ecret-key%22%7D`
	got = NewCredentialProtector("secret-key").Snapshot(input, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, `https://e.test/?payload=%7B%22password%22%3A%22`+CredentialProtectionRedacted+`%22%7D`, got.Value)
}

func TestCredentialProtectorMatchesNestedEncodingForPercentPrefixedCredential(t *testing.T) {
	secret := "%secret-key"
	input := encodeCredentialTestUnicodeLayer(url.QueryEscape(encodeCredentialTestUnicodeLayer(secret)))

	got := NewCredentialProtector(secret).Snapshot(input, 1024)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, CredentialProtectionRedacted, got.Value)

	got = NewCredentialProtector("%secret").Snapshot("%2525secret", 1000)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, CredentialProtectionRedacted, got.Value)
}

func TestCredentialProtectorMatchesRepeatedEncodingLayers(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		input  string
		expect string
	}{
		{name: "repeated percent encoding", secret: "a", input: "token=%2561", expect: "token=" + CredentialProtectionRedacted},
		{name: "repeated percent encoding preserves plus", secret: "a+b", input: "token=a%252Bb", expect: "token=" + CredentialProtectionRedacted},
		{name: "repeated unicode escaping", secret: "a", input: `token=\u005Cu0061`, expect: "token=" + CredentialProtectionRedacted},
		{name: "repeated percent encoding of slash", secret: "secret/key", input: "url=secret%252Fkey", expect: "url=" + CredentialProtectionRedacted},
		{name: "repeated unicode escaping of prefix", secret: "secret-key", input: `token=\u005Cu0073ecret-key`, expect: "token=" + CredentialProtectionRedacted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewCredentialProtector(test.secret).Snapshot(test.input, 256)
			require.Empty(t, got.UnavailableReason)
			require.Equal(t, test.expect, got.Value)
		})
	}
}

func TestCredentialProtectorMatchesExactlyMaximumEncodingLayers(t *testing.T) {
	encoded := "%71"
	for index := 1; index < maxCredentialDecodeLayers; index++ {
		encoded = url.QueryEscape(encoded)
	}

	got := NewCredentialProtector("q").Snapshot("token="+encoded, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "token="+CredentialProtectionRedacted, got.Value)

	got = NewCredentialProtector("q").Snapshot("token=%25252578", 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "token=%25252578", got.Value)
}

func TestCredentialProtectorMatchesNestedUnicodeEncodingWithinBound(t *testing.T) {
	encoded := `\u0061`
	encoded = encodeCredentialTestUnicodeLayer(encoded)

	got := NewCredentialProtector("a").Snapshot("token="+encoded, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "token="+CredentialProtectionRedacted, got.Value)
}

func TestCredentialProtectorMatchesMixedEncodingExpansionWithinBound(t *testing.T) {
	encoded := "a"
	for index := 0; index < maxCredentialDecodeLayers; index++ {
		encoded = encodeCredentialTestGoUnicodeLayer(encoded)
		encoded = url.QueryEscape(encoded)
	}

	got := NewCredentialProtector("a").Snapshot(encoded, len(encoded)+100)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, CredentialProtectionRedacted, got.Value)
}

func TestCredentialProtectorKeepsTruncatedCandidatesThroughDecodePasses(t *testing.T) {
	encoded := "a+b"
	for index := 0; index < maxCredentialDecodeLayers-1; index++ {
		encoded = encodeCredentialTestUnicodeLayer(encoded)
	}
	encoded = url.QueryEscape(encoded)

	got := NewCredentialProtector("a+b").Snapshot(encoded, len(encoded)+100)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, CredentialProtectionRedacted, got.Value)
}

func TestCredentialProtectorBoundsLongEncodedCandidates(t *testing.T) {
	secret := strings.Repeat("a", credentialCandidateBufferLimit) + "b"
	input := strings.Repeat("%61", 1000)

	got := NewCredentialProtector(secret).Snapshot(input, len(input)+2)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, input, got.Value)
}

func TestCredentialProtectorBoundsStreamingMatchWork(t *testing.T) {
	secret := strings.Repeat("a", 3000) + "b"
	input := strings.Repeat("%61", 3000)

	got := NewCredentialProtector(secret).Snapshot(input, len(input)*2)
	require.Equal(t, CredentialProtectionBudgetExceeded, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorBoundsFinalMatchWork(t *testing.T) {
	secret := strings.Repeat(`\n`, 3000) + "b"
	input := strings.Repeat("\n", 3000)

	got := NewCredentialProtector(secret).Snapshot(input, 6002)
	require.Equal(t, CredentialProtectionBudgetExceeded, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorRejectsDefiniteTruncatedMismatch(t *testing.T) {
	secret := strings.Repeat("a", credentialLiteralCandidateLimit+1) + "b"
	input := strings.Repeat(`\u0061`, 1000)

	got := NewCredentialProtector(secret).Snapshot(input, len(input)*2+2)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, input, got.Value)
}

func encodeCredentialTestUnicodeLayer(text string) string {
	var builder strings.Builder
	builder.Grow(len(text) * 6)
	for index := 0; index < len(text); index++ {
		fmt.Fprintf(&builder, `\u%04X`, text[index])
	}
	return builder.String()
}

func encodeCredentialTestGoUnicodeLayer(text string) string {
	var builder strings.Builder
	builder.Grow(len(text) * 10)
	for index := 0; index < len(text); index++ {
		fmt.Fprintf(&builder, `\U%08X`, text[index])
	}
	return builder.String()
}

func TestCredentialProtectorLeavesMalformedEncodingCandidatesUntouched(t *testing.T) {
	input := strings.Repeat("%", 256)
	got := NewCredentialProtector("q").Snapshot(input, 1024)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, input, got.Value)
}

func TestCredentialProtectorLeavesNonmatchingEncodedPrefixesUntouched(t *testing.T) {
	input := strings.Repeat("%61", 256)

	got := NewCredentialProtector(strings.Repeat("b", 32)).Snapshot(input, len(input)+2)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, input, got.Value)

	sharedPrefix := strings.Repeat("%61", 256)
	got = NewCredentialProtector(strings.Repeat("a", 31)+"b").Snapshot(sharedPrefix, len(sharedPrefix)+2)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, sharedPrefix, got.Value)

	nestedPrefix := strings.Repeat("%2561", 256)
	got = NewCredentialProtector(strings.Repeat("a", 31)+"b").Snapshot(nestedPrefix, len(nestedPrefix)+2)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, nestedPrefix, got.Value)

	longSharedPrefix := strings.Repeat("%61", 256)
	got = NewCredentialProtector(strings.Repeat("a", credentialLiteralCandidateLimit)+"b").Snapshot(longSharedPrefix, len(longSharedPrefix)+2)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, longSharedPrefix, got.Value)
}

func TestCredentialProtectorLeavesMalformedEscapesAfterCandidateUntouched(t *testing.T) {
	for _, input := range []string{
		"a%41%zz",
		`a%41\zz`,
		`a%41\xzz`,
		`a%41\400`,
		`a%41\u12xz`,
		`a%41\U00110000`,
		`a%41\uD800\uxxxx`,
		"a%41\\",
	} {
		got := NewCredentialProtector("aAB").Snapshot(input, 256)
		require.Empty(t, got.UnavailableReason)
		require.Equal(t, input, got.Value)
	}
}

func TestCredentialProtectorBoundsDecodedCandidatePrefilter(t *testing.T) {
	secret := strings.Repeat("a", credentialLiteralCandidateLimit+1) + "/"
	encoded := url.QueryEscape(secret)
	encoded = "%61" + encoded[1:]

	got := NewCredentialProtector(secret).Snapshot("token="+encoded, 512)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "token="+CredentialProtectionRedacted, got.Value)

	input := "token=+x"
	got = NewCredentialProtector("secret").Snapshot(input, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, input, got.Value)
}

func TestCredentialCandidatePrefilterBounds(t *testing.T) {
	var full credentialCandidateBuffer
	full.length = len(full.bytes)
	require.False(t, credentialCandidateAppend(&full, 'x'))
	require.True(t, full.truncated)

	left := credentialCandidateBuffer{length: 1}
	right := credentialCandidateBuffer{length: 1}
	left.bytes[0] = 'a'
	right.bytes[0] = 'b'
	require.False(t, credentialCandidateBuffersEqual(left, right))
	require.True(t, credentialDecodedLiteralCandidate("", "", false, false))
}

func TestCredentialProtectorFailsClosedWhenEncodingLayersExceedBound(t *testing.T) {
	encoded := "%71"
	for index := 1; index < maxCredentialDecodeLayers+1; index++ {
		encoded = url.QueryEscape(encoded)
	}

	got := NewCredentialProtector("q").Snapshot("token="+encoded, 256)
	require.Equal(t, CredentialProtectionEncodingLimitExceeded, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorRejectsGoQuotedCredentialSerialization(t *testing.T) {
	got := NewCredentialProtector(`\x01`).Snapshot(string([]byte{1}), 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)

	got = NewCredentialProtector(`\x01`).Snapshot(map[string]any{
		"nested": []any{string([]byte{1})},
	}, 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)

	got = NewCredentialProtector(`\u00e9`).Snapshot("é", 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)

	got = NewCredentialProtector(`\u00e9`).Snapshot(map[string]any{"é": "safe"}, 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorMatchesSurrogateUnicodeEscapes(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		input  string
	}{
		{name: "surrogate pair", secret: "😀", input: `{"password":"\uD83D\uDE00"}`},
		{name: "unpaired high surrogate", secret: "�", input: `{"password":"\uD800"}`},
		{name: "unpaired low surrogate", secret: "�", input: `{"password":"\uDC00"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewCredentialProtector(test.secret).Snapshot(test.input, 256)
			require.Empty(t, got.UnavailableReason)
			require.Equal(t, `{"password":"`+CredentialProtectionRedacted+`"}`, got.Value)
		})
	}
}

func TestCredentialProtectorMatchesJSONShortEscapes(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		input  string
	}{
		{name: "quote", secret: `a"b`, input: `{"password":"a\"b"}`},
		{name: "backslash", secret: `a\b`, input: `{"password":"a\\b"}`},
		{name: "solidus", secret: "a/b", input: `{"password":"a\/b"}`},
		{name: "backspace", secret: "a\bb", input: `{"password":"a\bb"}`},
		{name: "form feed", secret: "a\fb", input: `{"password":"a\fb"}`},
		{name: "line feed", secret: "a\nb", input: `{"password":"a\nb"}`},
		{name: "carriage return", secret: "a\rb", input: `{"password":"a\rb"}`},
		{name: "tab", secret: "a\tb", input: `{"password":"a\tb"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewCredentialProtector(test.secret).Snapshot(test.input, 256)
			require.Empty(t, got.UnavailableReason)
			require.Equal(t, `{"password":"`+CredentialProtectionRedacted+`"}`, got.Value)
		})
	}
}

func TestCredentialProtectorMatchesNoncanonicalGoEscapes(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		input  string
	}{
		{name: "hex byte", secret: "secret-key", input: `{"password":"\x73ecret-key"}`},
		{name: "hex bytes for utf8", secret: "é", input: `{"password":"\xC3\xA9"}`},
		{name: "unicode", secret: "secret-key", input: `{"password":"\U00000073ecret-key"}`},
		{name: "octal byte", secret: "secret-key", input: `{"password":"\163ecret-key"}`},
		{name: "alert", secret: "a\ab", input: `{"password":"a\ab"}`},
		{name: "vertical tab", secret: "a\vb", input: `{"password":"a\vb"}`},
		{name: "apostrophe", secret: "a'b", input: `{"password":"a\'b"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewCredentialProtector(test.secret).Snapshot(test.input, 256)
			require.Empty(t, got.UnavailableReason)
			require.Equal(t, `{"password":"`+CredentialProtectionRedacted+`"}`, got.Value)
		})
	}
}

func TestCredentialProtectorFailsClosedWhenJSONSerializationRevealsCredential(t *testing.T) {
	got := NewCredentialProtector(`\u003c`).Snapshot("<", 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorFailsClosedWhenMarkerContainsCredential(t *testing.T) {
	tests := []string{"REDACTED", "DACT"}
	for _, secret := range tests {
		got := NewCredentialProtector(secret).Snapshot("token="+secret, 256)
		require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
		require.Nil(t, got.Value)
	}

	for _, test := range []struct {
		secret string
		input  string
	}{
		{secret: "]x", input: "]xx"},
		{secret: "x[", input: "xx["},
	} {
		got := NewCredentialProtector(test.secret).Snapshot(test.input, 256)
		require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
		require.Nil(t, got.Value)
	}
}

func TestCredentialProtectorProtectsMapKeysAndRejectsRedactionCollisions(t *testing.T) {
	got := NewCredentialProtector("credential").Snapshot(map[string]any{"credential": "safe"}, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, map[string]any{CredentialProtectionRedacted: "safe"}, got.Value)

	got = NewCredentialProtector("credential").Snapshot(map[string]any{
		"credential":                 "first",
		CredentialProtectionRedacted: "second",
	}, 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorHandlesNilAndEmptyConfiguration(t *testing.T) {
	var nilProtector *CredentialProtector
	got := nilProtector.Snapshot("safe", 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "safe", got.Value)
	require.Empty(t, userInfoEscape(""))

	got = NewCredentialProtector("").Snapshot("safe", 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, "safe", got.Value)

	got = NewCredentialProtector("same").Snapshot("same", 256, "same")
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, CredentialProtectionRedacted, got.Value)
}

func TestCredentialProtectorRejectsNilChannelsAndFunctions(t *testing.T) {
	var nilChannel chan int
	got := NewCredentialProtector("secret").Snapshot(nilChannel, 256)
	require.Equal(t, CredentialProtectionUnsupported, got.UnavailableReason)
	require.Nil(t, got.Value)

	var nilFunction func()
	got = NewCredentialProtector("secret").Snapshot(nilFunction, 256)
	require.Equal(t, CredentialProtectionUnsupported, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorRejectsURLSerializedCredential(t *testing.T) {
	got := NewCredentialProtector("%2F").Snapshot("/", 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)

	got = NewCredentialProtector("%3C").Snapshot("<", 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)

	got = NewCredentialProtector("%2F").Snapshot("%25252578/", 1024)
	require.NotEmpty(t, got.UnavailableReason)
	require.Nil(t, got.Value)

	got = NewCredentialProtector(`a%22%3A%22b`).Snapshot(map[string]any{"a": "b"}, 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorKeepsUnavailableStatusIndependentOfSecrets(t *testing.T) {
	tests := []struct {
		name          string
		configured    []string
		authenticated []string
	}{
		{name: "exhausted string fallback", configured: []string{"%22", "diagnostic_unavailable"}},
		{name: "configured reason", configured: []string{"invalid_max_bytes", "_", "%EE%80%80", `\ue000`}},
		{name: "authenticated reason", authenticated: []string{"%22", "diagnostic_unavailable", "invalid_max_bytes"}},
		{name: "serialized reason", configured: []string{`%22invalid_max_bytes%22`}},
		{name: "status digits", configured: []string{"0", "1", "2", "3", "4", "5", "6", "7", "8"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewCredentialProtector(test.configured...).Snapshot("safe", 0, test.authenticated...)
			require.Equal(t, CredentialProtectionInvalidBudget, got.UnavailableReason)
			require.Nil(t, got.Value)
		})
	}
}

func TestCredentialProtectorLeavesTruncatedEncodedCredentialsUntouched(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		input  string
	}{
		{name: "unicode escape", secret: "secret", input: `token=\u0073ecre`},
		{name: "percent escape", secret: "abc", input: "token=%61b"},
		{name: "multibyte percent escape", secret: "é", input: "token=%C3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewCredentialProtector(test.secret).Snapshot(test.input, 256)
			require.Empty(t, got.UnavailableReason)
			require.Equal(t, test.input, got.Value)
		})
	}
}

func TestCredentialVariantMatcherModes(t *testing.T) {
	unicodeVariant := credentialVariant{text: "é", allowUnicodeEncoding: true}
	consumed, ok := credentialPrefix(`\u00E9`, unicodeVariant)
	require.True(t, ok)
	require.Equal(t, 6, consumed)

	plainVariant := credentialVariant{text: "plain"}
	consumed, ok = credentialPrefix("plain", plainVariant)
	require.True(t, ok)
	require.Equal(t, len(plainVariant.text), consumed)
	_, ok = credentialPrefix("other", plainVariant)
	require.False(t, ok)

	merged := mergeCredentialVariants(
		[]credentialVariant{{text: "same"}},
		[]credentialVariant{{text: "same", allowUnicodeEncoding: true}},
	)
	require.Len(t, merged, 2)
	require.True(t, merged[0].allowUnicodeEncoding)
}

func TestCredentialProtectorRejectsUnsafeValuesWithoutRawFallback(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		budget int
		reason CredentialProtectionUnavailableReason
	}{
		{name: "invalid budget", value: "safe", budget: 0, reason: CredentialProtectionInvalidBudget},
		{name: "budget exceeded", value: strings.Repeat("界", 10), budget: 3, reason: CredentialProtectionBudgetExceeded},
		{name: "unsupported struct", value: struct{ Value string }{Value: "safe"}, budget: 128, reason: CredentialProtectionUnsupported},
		{name: "unsupported map key", value: map[int]string{1: "safe"}, budget: 128, reason: CredentialProtectionUnsupported},
		{name: "invalid map key encoding", value: map[string]string{string([]byte{0xff}): "safe"}, budget: 128, reason: CredentialProtectionInvalidEncoding},
		{name: "invalid string encoding", value: string([]byte{0xff}), budget: 128, reason: CredentialProtectionInvalidEncoding},
		{name: "invalid byte encoding", value: []byte{0xff}, budget: 128, reason: CredentialProtectionInvalidEncoding},
		{name: "nonfinite float", value: math.NaN(), budget: 128, reason: CredentialProtectionFormattingFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewCredentialProtector("safe").Snapshot(test.value, test.budget)
			require.Equal(t, test.reason, got.UnavailableReason)
			require.Nil(t, got.Value)
		})
	}

	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	got := NewCredentialProtector("safe").Snapshot(cyclicMap, 1024)
	require.Equal(t, CredentialProtectionCycleDetected, got.UnavailableReason)
	require.Nil(t, got.Value)

	cyclicSlice := []any{nil}
	cyclicSlice[0] = cyclicSlice
	got = NewCredentialProtector("safe").Snapshot(cyclicSlice, 1024)
	require.Equal(t, CredentialProtectionCycleDetected, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorRejectsExcessiveDepth(t *testing.T) {
	var value any = "leaf"
	for index := 0; index < MaxCredentialProtectionDepth; index++ {
		value = map[string]any{"next": value}
	}

	got := NewCredentialProtector("leaf").Snapshot(value, 1<<20)
	require.Empty(t, got.UnavailableReason)
	require.NotNil(t, got.Value)

	value = "leaf"
	for index := 0; index < MaxCredentialProtectionDepth+1; index++ {
		value = map[string]any{"next": value}
	}

	got = NewCredentialProtector("leaf").Snapshot(value, 1<<20)
	require.Equal(t, CredentialProtectionDepthExceeded, got.UnavailableReason)
	require.Nil(t, got.Value)

	value = []byte(`{"next":"leaf"}`)
	for index := 0; index < MaxCredentialProtectionDepth; index++ {
		value = map[string]any{"next": value}
	}
	got = NewCredentialProtector("leaf").Snapshot(value, 1<<20)
	require.Equal(t, CredentialProtectionDepthExceeded, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorBoundsTraversalBeforeLargeCopies(t *testing.T) {
	largeBytes := make([]byte, 1<<20)
	got := NewCredentialProtector("secret").Snapshot(largeBytes, 128)
	require.Equal(t, CredentialProtectionBudgetExceeded, got.UnavailableReason)
	require.Nil(t, got.Value)

	largeSlice := make([]any, 1<<20)
	got = NewCredentialProtector("secret").Snapshot(largeSlice, 32)
	require.Equal(t, CredentialProtectionBudgetExceeded, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorSnapshotsPointersAndArrays(t *testing.T) {
	pointed := "contains secret"
	value := [2]any{&pointed, [2]string{"safe", "secret"}}

	got := NewCredentialProtector("secret").Snapshot(value, 256)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, []any{"contains [REDACTED]", []any{"safe", "[REDACTED]"}}, got.Value)

	var nilValue any
	got = NewCredentialProtector("secret").Snapshot(nilValue, 4)
	require.Empty(t, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorSnapshotsScalarAndNilValues(t *testing.T) {
	value := map[string]any{
		"bool":    true,
		"int":     int64(-3),
		"uint":    uint64(4),
		"float":   1.5,
		"number":  json.Number("9007199254740993"),
		"nil_map": map[string]string(nil),
		"nil_ptr": (*string)(nil),
	}

	got := NewCredentialProtector("secret").Snapshot(value, 512)
	require.Empty(t, got.UnavailableReason)
	encoded, err := json.Marshal(got.Value)
	require.NoError(t, err)
	require.JSONEq(t, `{"bool":true,"float":1.5,"int":-3,"nil_map":null,"nil_ptr":null,"number":9007199254740993,"uint":4}`, string(encoded))
}

func TestCredentialProtectorPreservesFloat32Precision(t *testing.T) {
	got := NewCredentialProtector("secret").Snapshot(float32(1.2), 3)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, float32(1.2), got.Value)

	encoded, err := json.Marshal(got.Value)
	require.NoError(t, err)
	require.Equal(t, "1.2", string(encoded))
	require.Equal(t, CredentialProtectionBudgetExceeded, NewCredentialProtector("secret").Snapshot(float32(1.2), 2).UnavailableReason)
}

func TestCredentialProtectorChecksGoFormattedFloatCredentials(t *testing.T) {
	for _, value := range []any{float32(1e20), float64(1e20), float32(1e-7), float64(1e-7)} {
		t.Run(fmt.Sprintf("%T/%v", value, value), func(t *testing.T) {
			secret := fmt.Sprint(value)
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), secret)

			for _, input := range []any{value, []any{value}, map[string]any{"amount": value}} {
				got := NewCredentialProtector(secret).Snapshot(input, 256)
				require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
				require.Nil(t, got.Value)

				got = NewCredentialProtector().Snapshot(input, 256, secret)
				require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
				require.Nil(t, got.Value)
			}

			got := NewCredentialProtector("unrelated").Snapshot(value, len(encoded))
			require.Equal(t, CredentialProtectionAvailable, got.UnavailableReason)
			require.Equal(t, value, got.Value)
			require.Equal(t, secret, fmt.Sprint(got.Value))
			require.Equal(t, CredentialProtectionBudgetExceeded,
				NewCredentialProtector("unrelated").Snapshot(value, len(encoded)-1).UnavailableReason)
		})
	}
}

func TestCredentialProtectorDetachedAuthenticationSecretsAndUnicodeByteBudget(t *testing.T) {
	protector := NewCredentialProtector("configured")
	value := map[string]any{"message": "configured auth-only"}
	got := protector.Snapshot(value, 256, "auth-only")
	require.Empty(t, got.UnavailableReason)
	before, err := json.Marshal(value)
	require.NoError(t, err)
	got = protector.Snapshot(value, 256, "auth-only")
	after, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
	value["message"] = "changed"
	require.Equal(t, map[string]any{"message": "[REDACTED] [REDACTED]"}, got.Value)
	independent := protector.Snapshot(map[string]any{"message": "auth-only"}, 256)
	require.Empty(t, independent.UnavailableReason)
	require.Equal(t, map[string]any{"message": "auth-only"}, independent.Value)

	text := strings.Repeat("界", 4)
	encoded, err := json.Marshal(text)
	require.NoError(t, err)
	require.Empty(t, protector.Snapshot(text, len(encoded)).UnavailableReason)
	require.Equal(t, CredentialProtectionBudgetExceeded, protector.Snapshot(text, len(encoded)-1).UnavailableReason)

	if reflect.DeepEqual(got.Value, value) {
		t.Fatal("snapshot unexpectedly retained the caller map")
	}
}

func TestCredentialProtectorBudgetMatchesJSONEncoding(t *testing.T) {
	for _, text := range []string{"<>&", "\b\t\n\f\r", "line\u2028separator"} {
		encoded, err := json.Marshal(text)
		require.NoError(t, err)
		require.Empty(t, NewCredentialProtector("secret").Snapshot(text, len(encoded)).UnavailableReason)
		require.Equal(t, CredentialProtectionBudgetExceeded, NewCredentialProtector("secret").Snapshot(text, len(encoded)-1).UnavailableReason)
	}
}

func TestCredentialProtectorPreservesLosslessJSONNumbers(t *testing.T) {
	for _, input := range []string{`9007199254740993`, `1e1000000`} {
		got := NewCredentialProtector("secret").Snapshot([]byte(input), 128)
		require.Empty(t, got.UnavailableReason)
		encoded, err := json.Marshal(got.Value)
		require.NoError(t, err)
		require.Equal(t, input, string(encoded))
	}
}

func TestCredentialProtectorDoesNotRechargeParsedJSONBytes(t *testing.T) {
	got := NewCredentialProtector("secret").Snapshot([]byte("123"), 3)
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, json.Number("123"), got.Value)
}

func TestCredentialProtectorRejectsInvalidJSONNumbers(t *testing.T) {
	got := NewCredentialProtector("secret").Snapshot(json.Number("not-a-number"), 128)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorBoundsRedactionExpansion(t *testing.T) {
	encoded, err := json.Marshal(CredentialProtectionRedacted)
	require.NoError(t, err)

	got := NewCredentialProtector("secret").Snapshot("secret", len(encoded))
	require.Empty(t, got.UnavailableReason)
	require.Equal(t, CredentialProtectionRedacted, got.Value)

	got = NewCredentialProtector("secret").Snapshot("secret", len(encoded)-1)
	require.Equal(t, CredentialProtectionBudgetExceeded, got.UnavailableReason)
	require.Nil(t, got.Value)
}

type panicDiagnosticError struct{}

func (panicDiagnosticError) Error() string { panic("diagnostic formatting failed") }

func TestCredentialProtectorConvertsFormattingPanicsToBoundedFailure(t *testing.T) {
	got := NewCredentialProtector("secret").Snapshot(panicDiagnosticError{}, 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)

	got = NewCredentialProtector("formatting_failed").Snapshot(panicDiagnosticError{}, 256)
	require.Equal(t, CredentialProtectionFormattingFailed, got.UnavailableReason)
	require.Nil(t, got.Value)
}

func TestCredentialProtectorOmitsUnavailableMetadataFromJSON(t *testing.T) {
	got := NewCredentialProtector("%22", "diagnostic_unavailable").Snapshot("safe", 0)
	require.Equal(t, CredentialProtectionInvalidBudget, got.UnavailableReason)
	require.Nil(t, got.Value)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, `{"Value":null}`, string(encoded))
}
