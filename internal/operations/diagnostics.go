package operations

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const DiagnosticMaxBytes = 1 << 20

var BuildVersion = "development"

type DiagnosticPresence struct {
	Telemetry         bool `json:"telemetry"`
	OTLP              bool `json:"otlp"`
	TraceDestination  bool `json:"trace_destination"`
	MetricDestination bool `json:"metric_destination"`
	TraceHeaders      bool `json:"trace_headers"`
	MetricHeaders     bool `json:"metric_headers"`
	AuditExport       bool `json:"audit_export"`
	DiagnosticBundle  bool `json:"diagnostic_bundle"`
	Redis             bool `json:"redis"`
}

type DiagnosticDependency struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type DiagnosticCheck struct {
	Name  string
	Check func(context.Context) error
}

type DiagnosticBundle struct {
	Version         int                        `json:"version"`
	GeneratedAt     time.Time                  `json:"generated_at"`
	BuildVersion    string                     `json:"build_version"`
	ConfigPresence  DiagnosticPresence         `json:"config_presence"`
	Dependencies    []DiagnosticDependency     `json:"dependencies"`
	SchemaStatus    string                     `json:"schema_status"`
	AuthorityStatus string                     `json:"authority_status"`
	Exporters       observability.ExportHealth `json:"exporters"`
	Operations      *domain.UsageMetricTotal   `json:"operations,omitempty"`
	Unavailable     []string                   `json:"unavailable"`
}

type DiagnosticService struct {
	presence  DiagnosticPresence
	checks    []DiagnosticCheck
	usage     UsageMetricsReader
	exporters interface {
		Health() observability.ExportHealth
	}
	authority AuthorityBootstrap
}

func NewDiagnosticService(presence DiagnosticPresence, checks []DiagnosticCheck, usage UsageMetricsReader, exporters interface {
	Health() observability.ExportHealth
}, authority AuthorityBootstrap) *DiagnosticService {
	return &DiagnosticService{presence: presence, checks: append([]DiagnosticCheck(nil), checks...), usage: usage, exporters: exporters, authority: authority}
}

func diagnosticBuildVersion() string {
	if regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,127}$`).MatchString(BuildVersion) && BuildVersion != "development" {
		return BuildVersion
	}
	info, ok := debug.ReadBuildInfo()
	if ok && regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.-]+)?$`).MatchString(info.Main.Version) {
		return info.Main.Version
	}
	return "development"
}

func (s *DiagnosticService) Bundle(ctx context.Context) ([]byte, error) {
	if s == nil {
		return nil, errors.New("diagnostics unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	bundle := DiagnosticBundle{Version: 1, GeneratedAt: time.Now().UTC(), BuildVersion: diagnosticBuildVersion(), ConfigPresence: s.presence, Dependencies: make([]DiagnosticDependency, 0), SchemaStatus: "unavailable", AuthorityStatus: "unavailable", Unavailable: make([]string, 0)}
	if CheckActiveAuthority(s.authority) == nil {
		bundle.AuthorityStatus = "active"
	} else {
		bundle.Unavailable = append(bundle.Unavailable, "authority")
	}
	allowed := map[string]bool{"postgres": true, "postgres_topology": true, "pgvector": true, "authority": true, "search_readiness": true, "redis": true, "operation_log_sink": true}
	seen := make(map[string]bool)
	for _, check := range s.checks {
		if !allowed[check.Name] || seen[check.Name] {
			continue
		}
		seen[check.Name] = true
		state := DiagnosticDependency{Name: check.Name, Status: "unavailable", Reason: "not_configured"}
		if check.Check != nil {
			err := check.Check(ctx)
			if err == nil {
				state.Status = "healthy"
				state.Reason = ""
			} else {
				state.Reason = "check_failed"
			}
		}
		bundle.Dependencies = append(bundle.Dependencies, state)
		if state.Status == "unavailable" {
			bundle.Unavailable = append(bundle.Unavailable, check.Name)
		}
		if check.Name == "authority" && state.Status == "healthy" {
			bundle.SchemaStatus = "compatible"
		}
	}
	if !seen["authority"] {
		bundle.Unavailable = append(bundle.Unavailable, "schema")
	}
	if s.exporters != nil {
		bundle.Exporters = s.exporters.Health()
	} else {
		bundle.Unavailable = append(bundle.Unavailable, "exporters")
	}
	if s.usage != nil {
		now := bundle.GeneratedAt
		snapshot, err := s.usage.Snapshot(ctx, domain.UsageMetricsFilter{From: now.Add(-time.Hour), To: now})
		if err == nil && snapshot != nil {
			total := snapshot.System
			bundle.Operations = &total
		}
	}
	if bundle.Operations == nil {
		bundle.Unavailable = append(bundle.Unavailable, "operations")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(raw) > DiagnosticMaxBytes {
		return nil, errors.New("diagnostic bundle exceeds size bound")
	}
	return raw, nil
}
