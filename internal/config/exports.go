package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func loadExports(cfg *Config) error {
	for _, option := range []struct {
		name   string
		target *bool
	}{
		{"OTLP_ENABLED", &cfg.OTLPEnabled}, {"AUDIT_EXPORT_ENABLED", &cfg.AuditExportEnabled}, {"DIAGNOSTIC_BUNDLE_ENABLED", &cfg.DiagnosticBundleEnabled},
	} {
		value, err := parseBoolOrDefault(option.name, false)
		if err != nil {
			return err
		}
		*option.target = value
	}
	cfg.OTLPTraceEndpoint = strings.TrimSpace(os.Getenv("OTLP_TRACE_ENDPOINT"))
	cfg.OTLPMetricEndpoint = strings.TrimSpace(os.Getenv("OTLP_METRIC_ENDPOINT"))
	cfg.OTLPTraceHeadersFile = os.Getenv("OTLP_TRACE_HEADERS_FILE")
	cfg.OTLPMetricHeadersFile = os.Getenv("OTLP_METRIC_HEADERS_FILE")
	if err := cfg.ValidateExports(); err != nil {
		return err
	}
	if !cfg.OTLPEnabled {
		return nil
	}
	var err error
	cfg.OTLPTraceHeaders, err = readExportHeaders(cfg.OTLPTraceHeadersFile, "OTLP_TRACE_HEADERS_FILE")
	if err != nil {
		return err
	}
	cfg.OTLPMetricHeaders, err = readExportHeaders(cfg.OTLPMetricHeadersFile, "OTLP_METRIC_HEADERS_FILE")
	return err
}

func (cfg *Config) ValidateExports() error {
	if !cfg.OTLPEnabled {
		return nil
	}
	if !cfg.TelemetryEnabled {
		return &ValidationError{Field: "OTLP_ENABLED", Message: "requires TELEMETRY_ENABLED=true"}
	}
	if cfg.OTLPTraceEndpoint == "" && cfg.OTLPMetricEndpoint == "" {
		return &ValidationError{Field: "OTLP_ENABLED", Message: "requires a trace or metric endpoint"}
	}
	for _, destination := range []struct{ field, endpoint, headers string }{
		{"OTLP_TRACE_ENDPOINT", cfg.OTLPTraceEndpoint, cfg.OTLPTraceHeadersFile}, {"OTLP_METRIC_ENDPOINT", cfg.OTLPMetricEndpoint, cfg.OTLPMetricHeadersFile},
	} {
		if destination.endpoint == "" {
			if destination.headers != "" {
				return &ValidationError{Field: destination.field, Message: "required when a headers file is configured"}
			}
			continue
		}
		u, err := url.Parse(destination.endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return &ValidationError{Field: destination.field, Message: "must be an absolute HTTP(S) URL without credentials, query, or fragment"}
		}
	}
	return nil
}

func readExportHeaders(path, field string) (map[string]string, error) {
	headers := make(map[string]string)
	if path == "" {
		return headers, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, &ValidationError{Field: field, Message: "header file is unavailable"}
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	invalid := func() (map[string]string, error) {
		return nil, &ValidationError{Field: field, Message: "must contain a JSON object of at most 16 valid HTTP headers within 8192 bytes"}
	}
	if err != nil || len(raw) > 8192 {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&headers); err != nil || headers == nil || len(headers) > 16 {
		return invalid()
	}
	if decoder.Decode(new(any)) != io.EOF {
		return invalid()
	}
	canonical := make(map[string]string, len(headers))
	for key, value := range headers {
		if key == "" || len(key) > 128 || len(value) > 2048 || strings.TrimSpace(value) != value {
			return invalid()
		}
		for _, c := range key {
			if !strings.ContainsRune("!#$%&'*+-.^_`|~", c) && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
				return invalid()
			}
		}
		for _, c := range value {
			if c < 32 || c > 126 {
				return invalid()
			}
		}
		name := http.CanonicalHeaderKey(key)
		switch name {
		case "Host", "Content-Length", "Content-Type", "Connection", "Transfer-Encoding", "Accept-Encoding":
			return invalid()
		}
		if _, exists := canonical[name]; exists {
			return invalid()
		}
		canonical[name] = value
	}
	return canonical, nil
}

func (cfg *Config) ExportHeaderSecrets() []string {
	secrets := make([]string, 0, len(cfg.OTLPTraceHeaders)+len(cfg.OTLPMetricHeaders))
	for _, headers := range []map[string]string{cfg.OTLPTraceHeaders, cfg.OTLPMetricHeaders} {
		for name, value := range headers {
			secrets = append(secrets, value)
			if strings.EqualFold(name, "Authorization") {
				parts := strings.Fields(value)
				if len(parts) == 2 {
					secrets = append(secrets, parts[1])
					if strings.EqualFold(parts[0], "Basic") {
						if decoded, err := base64.StdEncoding.DecodeString(parts[1]); err == nil {
							secrets = append(secrets, string(decoded))
							if _, password, ok := strings.Cut(string(decoded), ":"); ok {
								secrets = append(secrets, password)
							}
						}
					}
				}
			}
			if strings.EqualFold(name, "Cookie") {
				request := http.Request{Header: http.Header{"Cookie": []string{value}}}
				for _, cookie := range request.Cookies() {
					secrets = append(secrets, cookie.Value)
				}
			}
		}
	}
	return secrets
}
