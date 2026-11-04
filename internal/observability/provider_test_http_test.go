package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderTestHTTPObservesResponsesWithoutDebugLogging(t *testing.T) {
	const secret = "provider-test-operational-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+secret, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"`+secret+`","token":"admitted content"}`)
	}))
	defer server.Close()
	var observations []HTTPDiagnosticObservation
	ctx := WithHTTPDiagnosticObserver(context.Background(), func(value HTTPDiagnosticObservation) { observations = append(observations, value) })
	client := DiagnosticHTTPClient(server.Client(), nil, NewCredentialProtector(secret))
	for range 2 {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{"sample":"test"}`))
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+secret)
		response, err := client.Do(request)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Contains(t, string(body), secret)
	}
	require.Len(t, observations, 2)
	require.Equal(t, 401, observations[0].StatusCode)
	require.Equal(t, "captured", observations[0].RequestCaptureState)
	require.Equal(t, "captured", observations[0].ResponseCaptureState)
	require.NotNil(t, observations[0].ConnectMS)
	require.NotNil(t, observations[0].FirstByteMS)
	require.NotNil(t, observations[1].ConnectionReused)
	require.True(t, *observations[1].ConnectionReused)
	require.Nil(t, observations[1].ConnectMS)
	require.Nil(t, observations[1].TLSMS)
}

func TestProviderTestHTTPPreservesAdmittedContentAndReportsProtectionGaps(t *testing.T) {
	const secret = "provider diagnostic+credential"
	const authentication = "request-scoped-provider-test-authentication"
	body := `{"token":"ordinary user content","password":"ordinary data","echo":"` + url.QueryEscape(secret) + `","authentication":"` + authentication + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
	defer server.Close()
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
		for _, protection := range []bool{true, false} {
			var output bytes.Buffer
			var protector DiagnosticProtector
			if protection {
				protector = NewCredentialProtector(secret)
			}
			var observation HTTPDiagnosticObservation
			ctx := WithAuthenticationSecrets(context.Background(), authentication)
			ctx = WithHTTPDiagnosticObserver(ctx, func(value HTTPDiagnosticObservation) { observation = value })
			client := DiagnosticHTTPClient(server.Client(), slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level})), protector)
			request, err := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{"sample":"diagnostic"}`))
			require.NoError(t, err)
			response, err := client.Do(request)
			require.NoError(t, err)
			actual, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, body, string(actual))
			require.NotContains(t, output.String(), url.QueryEscape(secret))
			require.NotContains(t, output.String(), authentication)
			if protection {
				require.Equal(t, "captured", observation.ResponseCaptureState)
			} else {
				require.Equal(t, "unavailable", observation.ResponseCaptureState)
				require.NotEmpty(t, observation.CaptureReason)
			}
			if level == slog.LevelInfo {
				require.Empty(t, output.String())
				continue
			}
			if protection {
				var responseText string
				for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
					var record struct {
						Direction string              `json:"direction"`
						Chunk     DiagnosticBodyChunk `json:"payload_chunk"`
					}
					require.NoError(t, json.Unmarshal(line, &record))
					if record.Direction == "response" {
						responseText += record.Chunk.Body
					}
				}
				require.Contains(t, responseText, `"token":"ordinary user content"`)
				require.Contains(t, responseText, `"password":"ordinary data"`)
				require.Contains(t, responseText, CredentialProtectionRedacted)
			}
		}
	}
}

func TestProviderTestHTTPIdentifiesTLSFailures(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	var observed HTTPDiagnosticObservation
	ctx := WithHTTPDiagnosticObserver(context.Background(), func(value HTTPDiagnosticObservation) { observed = value })
	client := DiagnosticHTTPClient(&http.Client{}, nil, NewCredentialProtector())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	_, err = client.Do(request)
	require.Error(t, err)
	require.Equal(t, "tls", observed.FailurePhase)
	require.Zero(t, observed.StatusCode)
	require.Contains(t, observed.Error, "certificate")
	require.NotNil(t, observed.TLSMS)
}
