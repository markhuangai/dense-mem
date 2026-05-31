package skillpackservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ArtifactHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

var ErrUnsafeURL = errors.New("unsafe skill pack URL")

func defaultHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			dialAddress, err := safeDialAddress(ctx, network, host, port)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, dialAddress)
		},
	}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return validateArtifactURL(req.URL.String())
	}
	return client
}

func fetchArtifact(ctx context.Context, client ArtifactHTTPClient, rawURL string) ([]byte, error) {
	if err := validateArtifactURL(rawURL); err != nil {
		return nil, err
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("skill pack fetch: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("skill pack fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("skill pack fetch: status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxArtifactBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("skill pack fetch: read: %w", err)
	}
	if len(data) > maxArtifactBytes {
		return nil, fmt.Errorf("%w: artifact exceeds %d bytes", ErrInvalidArtifact, maxArtifactBytes)
	}
	return data, nil
}

func validateArtifactURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: invalid URL", ErrUnsafeURL)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%w: only https URLs are allowed", ErrUnsafeURL)
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return fmt.Errorf("%w: URL host is required", ErrUnsafeURL)
	}
	return nil
}

func rejectUnsafeHost(ctx context.Context, host string) error {
	_, err := safeDialAddress(ctx, "tcp", host, "0")
	return err
}

func safeDialAddress(ctx context.Context, network, host, port string) (string, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("%w: resolve host: %v", ErrUnsafeURL, err)
	}
	return safeDialAddressFromResolved(network, port, ips)
}

func safeDialAddressFromResolved(network, port string, ips []net.IPAddr) (string, error) {
	if len(ips) == 0 {
		return "", fmt.Errorf("%w: host resolved to no addresses", ErrUnsafeURL)
	}
	var selected net.IP
	for _, addr := range ips {
		if isUnsafeIP(addr.IP) {
			return "", fmt.Errorf("%w: blocked private or local address", ErrUnsafeURL)
		}
		if selected == nil && ipMatchesNetwork(network, addr.IP) {
			selected = addr.IP
		}
	}
	if selected == nil {
		return "", fmt.Errorf("%w: host resolved to no %s addresses", ErrUnsafeURL, network)
	}
	return net.JoinHostPort(selected.String(), port), nil
}

func ipMatchesNetwork(network string, ip net.IP) bool {
	switch network {
	case "tcp4":
		return ip.To4() != nil
	case "tcp6":
		return ip.To4() == nil && ip.To16() != nil
	default:
		return true
	}
}

func isUnsafeIP(ip net.IP) bool {
	ip = ip.To16()
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}
