package service

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func editableSSOConfigKeys() []string {
	return []string{
		domain.AppConfigSSOPublicBaseURL,
		domain.AppConfigMCPPublicBaseURL,
		domain.AppConfigSCIMPublicBaseURL,
		domain.AppConfigControlPublicBaseURL,
		domain.AppConfigSSOEntitlementCacheTTLSeconds,
		domain.AppConfigSSOSessionTTLSeconds,
		domain.AppConfigSSOStateTTLSeconds,
		domain.AppConfigSSOHTTPTimeoutSeconds,
		domain.AppConfigSSOCookieSecure,
	}
}

func normalizeMCPPublicBaseURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 2048 || raw != strings.TrimSpace(raw) {
		return "", fmt.Errorf("MCP_PUBLIC_BASE_URL must be a bounded exact URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("MCP_PUBLIC_BASE_URL must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && mcpLoopbackHost(parsed.Hostname())) {
		return "", fmt.Errorf("MCP_PUBLIC_BASE_URL must use https except for loopback http")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return "", fmt.Errorf("MCP_PUBLIC_BASE_URL has an invalid port")
		}
	}
	basePath := strings.TrimSuffix(parsed.Path, "/")
	if parsed.RawPath != "" || strings.ContainsAny(parsed.Path, "{} \t?#") || strings.Contains(parsed.Path, "//") || (parsed.Path != "" && parsed.Path != "/" && path.Clean(parsed.Path) != basePath) {
		return "", fmt.Errorf("MCP_PUBLIC_BASE_URL path must be canonical and literal")
	}
	return strings.TrimSuffix(raw, "/"), nil
}

func mcpLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
