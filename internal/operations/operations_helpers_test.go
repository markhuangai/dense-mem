package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/dream"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
)

func TestCheckActiveAuthorityRequiresCompatibleMarker(t *testing.T) {
	compatible := AuthorityBootstrap{
		Mode: AuthorityActive,
		Marker: &domain.CompatibilityMarker{
			MarkerKind: domain.MigrationMarkerKindCutover,
			Version:    CutoverMarkerVersion,
			Status:     domain.MigrationMarkerCompatible,
		},
	}
	require.NoError(t, CheckActiveAuthority(compatible))

	for _, authority := range []AuthorityBootstrap{
		{},
		{Mode: AuthorityActive},
		{Mode: AuthorityMode("inactive"), Marker: compatible.Marker},
		{Mode: AuthorityActive, Marker: &domain.CompatibilityMarker{Status: domain.MigrationMarkerCorrupt}},
		{Mode: AuthorityActive, Marker: &domain.CompatibilityMarker{
			MarkerKind: domain.MigrationMarkerKindCutover,
			Version:    "wrong-version",
			Status:     domain.MigrationMarkerCompatible,
		}},
		{Mode: AuthorityActive, Marker: &domain.CompatibilityMarker{
			MarkerKind: "wrong-kind",
			Version:    CutoverMarkerVersion,
			Status:     domain.MigrationMarkerCompatible,
		}},
	} {
		require.ErrorIs(t, CheckActiveAuthority(authority), ErrAuthorityBlocked)
	}
}

func TestInvariantScanCompatibilityReportsRemovedService(t *testing.T) {
	service := NewInvariantScanService(nil, nil)

	result, err := service.Scan(context.Background())
	require.ErrorIs(t, err, ErrInvariantScanRemoved)
	require.Equal(t, "removed", result.Status)

	result, err = service.ScanWithAudit(context.Background(), nil, "", "", "")
	require.ErrorIs(t, err, ErrInvariantScanRemoved)
	require.Equal(t, "removed", result.Status)
}

func TestCheckSearchReadinessReportsBoundedReasons(t *testing.T) {
	require.ErrorContains(t, CheckSearchReadiness(context.Background(), nil), "search repository is required")

	readErr := errors.New("read failed")
	search := searchReadinessStub{err: readErr}
	require.ErrorIs(t, CheckSearchReadiness(context.Background(), search), readErr)

	search = searchReadinessStub{readiness: &searchcontract.SearchReadiness{Ready: true}}
	require.NoError(t, CheckSearchReadiness(context.Background(), search))

	search = searchReadinessStub{readiness: &searchcontract.SearchReadiness{Reasons: []searchcontract.SearchReadinessReason{
		{Message: "stale index"},
		{Code: "missing_contract"},
		{},
	}}}
	err := CheckSearchReadiness(context.Background(), search)
	require.ErrorIs(t, err, knowledgecontract.ErrSearchContractMismatch)
	require.ErrorContains(t, err, "stale index; missing_contract")

	search = searchReadinessStub{readiness: &searchcontract.SearchReadiness{Reasons: []searchcontract.SearchReadinessReason{{}}}}
	require.ErrorContains(t, CheckSearchReadiness(context.Background(), search), "search readiness check failed")
}

type searchReadinessStub struct {
	readiness *searchcontract.SearchReadiness
	err       error
}

func (s searchReadinessStub) CheckSearchReadiness(context.Context) (*searchcontract.SearchReadiness, error) {
	return s.readiness, s.err
}

func TestTelemetryHelperNoopAndCanceledPaths(t *testing.T) {
	require.Error(t, RefreshTelemetryPricingCache(context.Background(), nil))
	pricingErr := errors.New("pricing unavailable")
	pricing := telemetryPricingStub{refreshErr: pricingErr}
	require.ErrorIs(t, RefreshTelemetryPricingCache(context.Background(), pricing), pricingErr)

	input := 1.25
	output := 2.5
	embedding := 3.75
	pricing = telemetryPricingStub{cached: domain.TelemetryPricingRuntimeConfig{
		VerifierInputUSDPerMillionTokens:  &input,
		VerifierOutputUSDPerMillionTokens: &output,
		EmbeddingInputUSDPerMillionTokens: &embedding,
	}, cachedOK: true}
	resolver := NewTelemetryPricingResolver(pricing)
	resolved, err := resolver.ResolveAIPricing(context.Background())
	require.NoError(t, err)
	require.Equal(t, input, *resolved.VerifierInputUSDPerMillionTokens)
	require.Equal(t, output, *resolved.VerifierOutputUSDPerMillionTokens)
	require.Equal(t, embedding, *resolved.EmbeddingInputUSDPerMillionTokens)
	input = 99
	require.Equal(t, 1.25, *resolved.VerifierInputUSDPerMillionTokens)
	require.Implements(t, (*observability.AIPricingResolver)(nil), resolver)
	_, err = NewTelemetryPricingResolver(telemetryPricingStub{}).ResolveAIPricing(context.Background())
	require.ErrorContains(t, err, "snapshot unavailable")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RefreshTelemetryPricingCacheUntilCanceled(ctx, nil, nil)
	ConfigureTelemetryFeatures(nil, nil, nil)
	ConfigureTelemetryFeatures(NewPrometheusTelemetryService("", 0), nil, nil)
}

func TestConfigureTelemetryFeaturesEvaluatesApplicationAndTeamSettings(t *testing.T) {
	appConfig := telemetryAppConfigStub{}
	dreams := telemetryDreamServiceStub{}
	prometheus := NewPrometheusTelemetryService("", 0)
	ConfigureTelemetryFeatures(prometheus, appConfig, dreams)

	snapshot, err := prometheus.Snapshot(context.Background(), TelemetryFilter{Window: "1h", Scope: "system"})
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Cards)

	teamID := uuid.New()
	snapshot, err = prometheus.Snapshot(context.Background(), TelemetryFilter{Window: "1h", Scope: "team", TeamID: &teamID})
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Cards)
}

func TestTelemetryEmptyBuildersAndWindowBounds(t *testing.T) {
	require.NotEmpty(t, telemetryEmptyCards())
	require.NotEmpty(t, telemetryEmptyWindowedCards())
	require.NotEmpty(t, telemetryEmptyCurrentCards())
	require.NotEmpty(t, telemetryEmptySeries())
	require.NotEmpty(t, telemetryEmptySeriesForAudience(true))
	require.NotEmpty(t, telemetryEmptyActivitySeriesForAudience(true))
	require.NotNil(t, telemetryEmptyStateSeries())
	require.EqualValues(t, 60, telemetryWindowSeconds(""))
	require.EqualValues(t, 60, telemetryWindowSeconds("bad"))
	require.EqualValues(t, 60, telemetryWindowSeconds("0m"))
	require.EqualValues(t, 60, telemetryWindowSeconds("1x"))
	require.EqualValues(t, 1, telemetryWindowSeconds("1s"))
	require.EqualValues(t, 120, telemetryWindowSeconds("2m"))
	require.EqualValues(t, 3600, telemetryWindowSeconds("1h"))
	require.EqualValues(t, 86400, telemetryWindowSeconds("1d"))
}

func TestTelemetryFeatureErrorsAndRangeBuilders(t *testing.T) {
	teamID := uuid.New()
	service := NewPrometheusTelemetryService("", 0)
	service.SetFeatureResolver(TelemetryFeatureResolver{
		RecallFeedbackEnabled: func(context.Context) (bool, error) { return false, errors.New("recall unavailable") },
		DreamingEnabled:       func(context.Context, *uuid.UUID) (bool, error) { return false, errors.New("dream unavailable") },
	})
	states := service.telemetryFeatureStates(context.Background(), TelemetryScope{Type: "team", TeamID: &teamID})
	require.True(t, states["recall"].Set)
	require.True(t, states["dream"].Set)

	require.NotEmpty(t, telemetryRangeHistogramAverage("latency", "{team_id=\"x\"}", "", "1m", 1000))
	require.NotEmpty(t, telemetryRangeHistogramQuantile("latency", "{team_id=\"x\"}", "le=\"1\"", "1m", 0.95, 1))
	require.NotEmpty(t, telemetryRangeSparseCounterRate("requests", "{team_id=\"x\"}", "1m"))
	require.NotEmpty(t, telemetryRangeSparseHistogramQuantile("latency", "", "", "1m", 0.5, 1))
	require.NotEmpty(t, telemetryRangeRecallFeedbackRate("{team_id=\"x\"}", "reason=\"useful\"", "1m"))
	require.NotEmpty(t, telemetryRangeSparseHistogramAverage("latency", "{team_id=\"x\"}", "reason=\"useful\"", "1m", 1000))
}

type telemetryAppConfigStub struct {
	settings.AppConfigService
}

func (telemetryAppConfigStub) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{Enabled: false}, nil
}

func (telemetryAppConfigStub) DreamingRuntimeConfig(context.Context) (domain.DreamingRuntimeConfig, error) {
	return domain.DreamingRuntimeConfig{Enabled: false}, nil
}

type telemetryDreamServiceStub struct {
	dream.Service
}

func (telemetryDreamServiceStub) EffectiveConfig(context.Context, string) (dream.EffectiveConfig, error) {
	return dream.EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: false}}, nil
}

type telemetryPricingStub struct {
	refreshErr error
	cached     domain.TelemetryPricingRuntimeConfig
	cachedOK   bool
}

func (s telemetryPricingStub) TelemetryPricingRuntimeConfig(context.Context) (domain.TelemetryPricingRuntimeConfig, error) {
	return domain.TelemetryPricingRuntimeConfig{}, s.refreshErr
}

func (s telemetryPricingStub) CachedTelemetryPricingRuntimeConfig() (domain.TelemetryPricingRuntimeConfig, bool) {
	return s.cached, s.cachedOK
}
