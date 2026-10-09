package operations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestDiagnosticBundleProjectsSafeStatesAndUnavailableSections(t *testing.T) {
	canary := "secret-prompt-evidence-database-error-canary"
	service := NewDiagnosticService(DiagnosticPresence{AuditExport: true}, []DiagnosticCheck{
		{Name: "postgres", Check: func(context.Context) error { return errors.New(canary) }},
		{Name: canary, Check: func(context.Context) error { t.Fatal("unapproved dependency evaluated"); return nil }},
		{Name: "pgvector", Check: func(context.Context) error { return nil }},
	}, nil, nil, AuthorityBootstrap{})
	raw, err := service.Bundle(context.Background())
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), DiagnosticMaxBytes)
	require.NotContains(t, string(raw), canary)
	var bundle DiagnosticBundle
	require.NoError(t, json.Unmarshal(raw, &bundle))
	require.Equal(t, 1, bundle.Version)
	require.Len(t, bundle.Dependencies, 2)
	require.Equal(t, "unavailable", bundle.Dependencies[0].Status)
	require.Equal(t, "check_failed", bundle.Dependencies[0].Reason)
	require.Equal(t, "healthy", bundle.Dependencies[1].Status)
	require.Contains(t, bundle.Unavailable, "schema")
	require.Contains(t, bundle.Unavailable, "operations")
	require.Contains(t, bundle.Unavailable, "exporters")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.Bundle(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDiagnosticBundleReportsCompatibleAuthorityAndDisabledExporters(t *testing.T) {
	var exporters *observability.OTLP
	service := NewDiagnosticService(DiagnosticPresence{}, []DiagnosticCheck{{Name: "authority", Check: func(context.Context) error { return nil }}}, nil, exporters, AuthorityBootstrap{
		Mode: AuthorityActive,
		Marker: &domain.CompatibilityMarker{
			MarkerKind: domain.MigrationMarkerKindCutover,
			Version:    CutoverMarkerVersion,
			Status:     domain.MigrationMarkerCompatible,
		},
	})
	raw, err := service.Bundle(context.Background())
	require.NoError(t, err)
	var bundle DiagnosticBundle
	require.NoError(t, json.Unmarshal(raw, &bundle))
	require.Equal(t, "active", bundle.AuthorityStatus)
	require.Equal(t, "compatible", bundle.SchemaStatus)
	require.False(t, bundle.Exporters.Traces.Enabled)
	require.False(t, bundle.Exporters.Metrics.Enabled)
	require.Equal(t, observability.OTLPSpanQueueSize, bundle.Exporters.QueueCapacity)
	require.NotContains(t, bundle.Unavailable, "authority")
	require.NotContains(t, bundle.Unavailable, "schema")
}
