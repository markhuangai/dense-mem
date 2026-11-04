package observability

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProviderTestHTTPIdentifiesRealDNSAndConnectionFailures(t *testing.T) {
	dns, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer dns.Close()
	go func() {
		for {
			connection, err := dns.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				var length [2]byte
				if _, err := io.ReadFull(connection, length[:]); err != nil {
					t.Error(err)
					return
				}
				packet := make([]byte, int(length[0])<<8|int(length[1]))
				if _, err := io.ReadFull(connection, packet); err != nil {
					t.Error(err)
					return
				}
				packet[2] |= 0x80
				packet[3] = (packet[3] & 0xf0) | 3
				if _, err := connection.Write(append(length[:], packet...)); err != nil {
					t.Error(err)
				}
			}()
		}
	}()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", dns.Addr().String())
	}}
	var observation HTTPDiagnosticObservation
	ctx := WithHTTPDiagnosticObserver(context.Background(), func(value HTTPDiagnosticObservation) { observation = value })
	transport := &http.Transport{DialContext: (&net.Dialer{Resolver: resolver}).DialContext}
	defer transport.CloseIdleConnections()
	client := DiagnosticHTTPClient(&http.Client{Transport: transport, Timeout: time.Second}, nil, NewCredentialProtector())
	request, err := http.NewRequestWithContext(ctx, "POST", "http://provider-test.invalid", nil)
	require.NoError(t, err)
	_, err = client.Do(request)
	require.Error(t, err)
	require.ErrorContains(t, err, "no such host")
	require.Equal(t, "dns", observation.FailurePhase)
	require.NotNil(t, observation.DNSMS)
	require.Contains(t, observation.Error, "no such host")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	request, err = http.NewRequestWithContext(ctx, "POST", "http://"+address, nil)
	require.NoError(t, err)
	_, err = client.Do(request)
	require.Error(t, err)
	require.Equal(t, "connect", observation.FailurePhase)
	require.NotNil(t, observation.ConnectMS)
	require.Zero(t, observation.StatusCode)
}

func TestProviderTestHTTPReportsResponseReadFailuresAndTimeouts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
	}))
	defer server.Close()
	var observation HTTPDiagnosticObservation
	ctx := WithHTTPDiagnosticObserver(context.Background(), func(value HTTPDiagnosticObservation) { observation = value })
	client := DiagnosticHTTPClient(&http.Client{Timeout: 100 * time.Millisecond}, nil, NewCredentialProtector())
	request, err := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	_, err = io.ReadAll(response.Body)
	require.Error(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, "response_body", observation.FailurePhase)
	require.Equal(t, "interrupted", observation.ResponseCaptureState)
	require.Equal(t, 200, observation.StatusCode)
	require.Contains(t, observation.Error, "unexpected EOF")
	request, err = http.NewRequestWithContext(ctx, "POST", server.URL+"/slow", nil)
	require.NoError(t, err)
	_, err = client.Do(request)
	require.Error(t, err)
	require.Equal(t, "response_headers", observation.FailurePhase)
	require.Zero(t, observation.StatusCode)
}
