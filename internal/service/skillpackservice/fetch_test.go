package skillpackservice

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFetchArtifactValidatesURLAndReadsBoundedBody(t *testing.T) {
	called := false
	data, err := fetchArtifact(context.Background(), artifactClientFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.Method != http.MethodGet || req.Header.Get("Accept") != "application/json" || req.URL.String() != "https://example.com/pack.json" {
			t.Fatalf("request = %s %s Accept=%q", req.Method, req.URL.String(), req.Header.Get("Accept"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"format":"dense-mem.memory-pack.v2"}`)),
		}, nil
	}), "https://example.com/pack.json")
	if err != nil {
		t.Fatalf("fetchArtifact: %v", err)
	}
	if !called || string(data) != `{"format":"dense-mem.memory-pack.v2"}` {
		t.Fatalf("called/data = %v/%q", called, string(data))
	}

	_, err = fetchArtifact(context.Background(), nil, "http://example.com/pack.json")
	if !errors.Is(err, ErrUnsafeURL) || !strings.Contains(err.Error(), "only https") {
		t.Fatalf("http URL err = %v", err)
	}
	_, err = fetchArtifact(context.Background(), artifactClientFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(""))}, nil
	}), "https://example.com/pack.json")
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("status err = %v", err)
	}
	_, err = fetchArtifact(context.Background(), artifactClientFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxArtifactBytes+1)))}, nil
	}), "https://example.com/pack.json")
	if !errors.Is(err, ErrInvalidArtifact) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized err = %v", err)
	}
	_, err = fetchArtifact(context.Background(), artifactClientFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	}), "https://example.com/pack.json")
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("client err = %v", err)
	}
}

func TestSafeDialAddressFromResolvedRejectsUnsafeAddresses(t *testing.T) {
	dialAddress, err := safeDialAddressFromResolved("tcp4", "443", []net.IPAddr{
		{IP: net.ParseIP("2001:4860:4860::8888")},
		{IP: net.ParseIP("8.8.8.8")},
	})
	if err != nil {
		t.Fatalf("safe public address rejected: %v", err)
	}
	if dialAddress != "8.8.8.8:443" {
		t.Fatalf("dial address = %q", dialAddress)
	}

	for _, tc := range []struct {
		name    string
		network string
		ips     []net.IPAddr
		want    string
	}{
		{name: "empty", network: "tcp", ips: nil, want: "no addresses"},
		{name: "private", network: "tcp", ips: []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}, want: "blocked private or local address"},
		{name: "loopback", network: "tcp", ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, want: "blocked private or local address"},
		{name: "wrong family", network: "tcp6", ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, want: "no tcp6 addresses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := safeDialAddressFromResolved(tc.network, "443", tc.ips)
			if !errors.Is(err, ErrUnsafeURL) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("safeDialAddressFromResolved err = %v, want %q", err, tc.want)
			}
		})
	}

	if !ipMatchesNetwork("tcp6", net.ParseIP("2001:4860:4860::8888")) || ipMatchesNetwork("tcp4", net.ParseIP("2001:4860:4860::8888")) {
		t.Fatal("ipMatchesNetwork did not distinguish IPv4 and IPv6")
	}
	if !isUnsafeIP(net.ParseIP("0.0.0.0")) || isUnsafeIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("isUnsafeIP public/unspecified classification failed")
	}
}

func TestDefaultHTTPClientRejectsUnsafeRedirects(t *testing.T) {
	client := defaultHTTPClient()
	if client.Timeout != 10*time.Second || client.CheckRedirect == nil {
		t.Fatalf("default client = %+v", client)
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.com/pack.json", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := client.CheckRedirect(req, nil); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("unsafe redirect err = %v", err)
	}
}

type artifactClientFunc func(*http.Request) (*http.Response, error)

func (f artifactClientFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}
