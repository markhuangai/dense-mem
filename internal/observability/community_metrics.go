package observability

import (
	"context"

	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
)

// CommunityMetrics is a compatibility alias for the Community-owned metric
// contract. These helpers keep existing recorder callers stable until #382.
type CommunityMetrics = communitycontract.CommunityMetrics

func RecordCommunityRun(ctx context.Context, metrics DiscoverabilityMetrics, status string, nodes, edges, communities int) {
	communitycontract.RecordCommunityRun(ctx, metrics, status, nodes, edges, communities)
}

func RecordCommunitySummary(ctx context.Context, metrics DiscoverabilityMetrics, outcome string, attempts int) {
	communitycontract.RecordCommunitySummary(ctx, metrics, outcome, attempts)
}

func RecordCommunityRecall(ctx context.Context, metrics DiscoverabilityMetrics, outcome string, communities, relationships int) {
	communitycontract.RecordCommunityRecall(ctx, metrics, outcome, communities, relationships)
}
