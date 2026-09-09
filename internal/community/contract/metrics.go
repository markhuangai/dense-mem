package contract

import "context"

// CommunityMetrics is optional so existing metric recorders remain source
// compatible while Community owns the metric labels and event helpers.
type CommunityMetrics interface {
	ObserveCommunityRun(context.Context, string, int, int, int)
	ObserveCommunitySummary(context.Context, string, int)
	ObserveCommunityRecall(context.Context, string, int, int)
}

func RecordCommunityRun(ctx context.Context, metrics any, status string, nodes, edges, communities int) {
	if recorder, ok := metrics.(CommunityMetrics); ok {
		recorder.ObserveCommunityRun(ctx, status, nodes, edges, communities)
	}
}

func RecordCommunitySummary(ctx context.Context, metrics any, outcome string, attempts int) {
	if recorder, ok := metrics.(CommunityMetrics); ok {
		recorder.ObserveCommunitySummary(ctx, outcome, attempts)
	}
}

func RecordCommunityRecall(ctx context.Context, metrics any, outcome string, communities, relationships int) {
	if recorder, ok := metrics.(CommunityMetrics); ok {
		recorder.ObserveCommunityRecall(ctx, outcome, communities, relationships)
	}
}
