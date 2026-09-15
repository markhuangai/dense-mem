package contract

import (
	"context"
	"testing"
)

type communityMetricsRecorder struct {
	runs, summaries, recalls int
}

func (r *communityMetricsRecorder) ObserveCommunityRun(context.Context, string, int, int, int) {
	r.runs++
}
func (r *communityMetricsRecorder) ObserveCommunitySummary(context.Context, string, int) {
	r.summaries++
}
func (r *communityMetricsRecorder) ObserveCommunityRecall(context.Context, string, int, int) {
	r.recalls++
}

func TestCommunityMetricHelpersOnlyCallCompatibleRecorders(t *testing.T) {
	recorder := &communityMetricsRecorder{}
	ctx := context.Background()
	RecordCommunityRun(ctx, recorder, "completed", 1, 2, 3)
	RecordCommunitySummary(ctx, recorder, "ok", 1)
	RecordCommunityRecall(ctx, recorder, "ok", 1, 2)
	RecordCommunityRun(ctx, struct{}{}, "ignored", 0, 0, 0)
	if recorder.runs != 1 || recorder.summaries != 1 || recorder.recalls != 1 {
		t.Fatalf("recorder counts = %#v", recorder)
	}
}

func TestCommunityRecordConvertsToDomainValue(t *testing.T) {
	record := CommunityRecord{CommunityID: "community", TeamID: "team", Summary: "summary", SummaryVersion: "2", MemberCount: 3, TopEntities: []string{"a"}, TopPredicates: []string{"uses"}}
	got := record.DomainCommunity()
	if got == nil || got.CommunityID != record.CommunityID || got.MemberCount != record.MemberCount || got.Level != 0 {
		t.Fatalf("domain community = %#v", got)
	}
	got.TopEntities[0] = "changed"
	if record.TopEntities[0] == "changed" {
		t.Fatal("domain conversion aliased entity slice")
	}
}
