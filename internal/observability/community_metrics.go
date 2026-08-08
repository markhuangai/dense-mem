package observability

import "context"

// CommunityMetrics is optional so existing metric fakes remain source
// compatible while snapshot and recall telemetry gain bounded labels.
type CommunityMetrics interface {
	ObserveCommunityRun(ctx context.Context, status string, nodes, edges, communities int)
	ObserveCommunitySummary(ctx context.Context, outcome string, attempts int)
	ObserveCommunityRecall(ctx context.Context, outcome string, communities, relationships int)
}

func RecordCommunityRun(ctx context.Context, metrics DiscoverabilityMetrics, status string, nodes, edges, communities int) {
	if recorder, ok := metrics.(CommunityMetrics); ok {
		recorder.ObserveCommunityRun(ctx, status, nodes, edges, communities)
	}
}

func RecordCommunitySummary(ctx context.Context, metrics DiscoverabilityMetrics, outcome string, attempts int) {
	if recorder, ok := metrics.(CommunityMetrics); ok {
		recorder.ObserveCommunitySummary(ctx, outcome, attempts)
	}
}

func RecordCommunityRecall(ctx context.Context, metrics DiscoverabilityMetrics, outcome string, communities, relationships int) {
	if recorder, ok := metrics.(CommunityMetrics); ok {
		recorder.ObserveCommunityRecall(ctx, outcome, communities, relationships)
	}
}
