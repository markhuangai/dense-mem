package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func TestCheckActiveAuthorityRequiresCompatibleMarker(t *testing.T) {
	compatible := AuthorityBootstrap{
		Mode: AuthorityActive,
		Marker: &domain.CompatibilityMarker{
			Status: domain.MigrationMarkerCompatible,
		},
	}
	require.NoError(t, CheckActiveAuthority(compatible))

	for _, authority := range []AuthorityBootstrap{
		{},
		{Mode: AuthorityActive},
		{Mode: AuthorityMode("inactive"), Marker: compatible.Marker},
		{Mode: AuthorityActive, Marker: &domain.CompatibilityMarker{Status: domain.MigrationMarkerCorrupt}},
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
