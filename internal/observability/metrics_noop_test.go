package observability

import (
	"log/slog"
	"testing"
)

func TestNoopDiscoverabilityMetricsImplementsAllMethods(t *testing.T) {
	var metrics DiscoverabilityMetrics = NoopDiscoverabilityMetrics()

	metrics.ObserveEmbeddingLatency(12.5, "ok")
	metrics.IncEmbeddingError("timeout")
	metrics.ObserveRecallLatency(3.5)
	metrics.IncFragmentCreate("created")
	metrics.IncClaimCreate("duplicate", "content_hash")
	metrics.IncVerifyVerdict("verified")
	metrics.IncPromotionOutcome("promoted")
	metrics.ObservePromoteLockWait(0.25)
	metrics.IncFragmentRetract()
	metrics.IncFactNeedsRevalidation()
	metrics.IncCommunityDetect("ok")
	metrics.ObserveCommunityDetect(1.25, 42)
}

func TestLogAttrConstructors(t *testing.T) {
	if got := Int("count", 3); got.Key != "count" || got.Value != 3 {
		t.Fatalf("Int attr = %#v; want count=3", got)
	}
	if got := Bool("enabled", true); got.Key != "enabled" || got.Value != true {
		t.Fatalf("Bool attr = %#v; want enabled=true", got)
	}
	if New(slog.LevelInfo) == nil {
		t.Fatal("New returned nil logger")
	}
}
