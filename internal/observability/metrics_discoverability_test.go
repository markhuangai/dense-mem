package observability

import (
	"testing"
)

func TestNoopDiscoverabilityMetrics_NeverPanics(t *testing.T) {
	m := NoopDiscoverabilityMetrics()
	m.ObserveEmbeddingLatency(10, "ok")
	m.IncEmbeddingError("timeout")
	m.ObserveRecallLatency(5)
	m.ObserveRecall(5, 2, "ok")
	m.ObserveMemoryFunnelLatency("claim_to_verify", 1, "verified")
	m.IncFragmentCreate("created")
	m.IncClaimCreate("created", "")
	m.IncVerifyVerdict("verified")
	m.IncPromotionOutcome("promoted")
	m.ObservePromoteLockWait(0.001)
	m.IncFragmentRetract()
	m.IncFactNeedsRevalidation()
	m.IncCommunityDetect("ok")
	m.ObserveCommunityDetect(1.2, 10)
}

func TestInMemoryDiscoverabilityMetrics_RecordsEmbeddingLatency(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	m.ObserveEmbeddingLatency(123.4, "ok")
	m.ObserveEmbeddingLatency(987.6, "timeout")

	samples := m.EmbeddingSamples()
	if len(samples) != 2 {
		t.Fatalf("samples len = %d; want 2", len(samples))
	}
	if samples[0].DurationMs != 123.4 || samples[0].Outcome != "ok" {
		t.Errorf("sample[0] = %+v", samples[0])
	}
	if samples[1].DurationMs != 987.6 || samples[1].Outcome != "timeout" {
		t.Errorf("sample[1] = %+v", samples[1])
	}
}

func TestInMemoryDiscoverabilityMetrics_RecordsEmbeddingErrors(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	m.IncEmbeddingError("timeout")
	m.IncEmbeddingError("timeout")
	m.IncEmbeddingError("rate_limited")
	if got := m.EmbeddingErrorCount("timeout"); got != 2 {
		t.Errorf("timeout count = %d; want 2", got)
	}
	if got := m.EmbeddingErrorCount("rate_limited"); got != 1 {
		t.Errorf("rate_limited count = %d; want 1", got)
	}
	if got := m.EmbeddingErrorCount("never"); got != 0 {
		t.Errorf("unused code count = %d; want 0", got)
	}
}

func TestInMemoryDiscoverabilityMetrics_RecordsRecallLatency(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	m.ObserveRecallLatency(42.0)
	m.ObserveRecallLatency(7.5)
	out := m.RecallLatencies()
	if len(out) != 2 || out[0] != 42.0 || out[1] != 7.5 {
		t.Errorf("latencies = %+v", out)
	}
}

func TestInMemoryDiscoverabilityMetrics_RecordsRecallResults(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	m.ObserveRecall(42.0, 3, "ok")

	samples := m.RecallSamples()
	if len(samples) != 1 {
		t.Fatalf("recall samples = %d; want 1", len(samples))
	}
	if samples[0].DurationMs != 42.0 || samples[0].ResultCount != 3 || samples[0].Outcome != "ok" {
		t.Errorf("recall sample = %+v", samples[0])
	}
}

func TestInMemoryDiscoverabilityMetrics_RecordsMemoryFunnelLatency(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	m.ObserveMemoryFunnelLatency("claim_to_verify", 2.5, "verified")

	samples := m.MemoryFunnelSamples()
	if len(samples) != 1 {
		t.Fatalf("funnel samples = %d; want 1", len(samples))
	}
	if samples[0].Stage != "claim_to_verify" || samples[0].Seconds != 2.5 || samples[0].Outcome != "verified" {
		t.Errorf("funnel sample = %+v", samples[0])
	}
}

func TestInMemoryDiscoverabilityMetrics_RecordsFragmentCreateOutcomes(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	m.IncFragmentCreate("created")
	m.IncFragmentCreate("created")
	m.IncFragmentCreate("duplicate")
	if got := m.FragmentCreateCount("created"); got != 2 {
		t.Errorf("created = %d; want 2", got)
	}
	if got := m.FragmentCreateCount("duplicate"); got != 1 {
		t.Errorf("duplicate = %d; want 1", got)
	}
}

func TestInMemoryDiscoverabilityMetrics_ConcurrentSafe(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	done := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			m.ObserveEmbeddingLatency(1, "ok")
			m.IncEmbeddingError("timeout")
			m.ObserveRecallLatency(1)
			m.ObserveRecall(1, 1, "ok")
			m.ObserveMemoryFunnelLatency("claim_to_verify", 1, "verified")
			m.IncFragmentCreate("created")
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
	if got := len(m.EmbeddingSamples()); got != 50 {
		t.Errorf("embedding samples = %d; want 50", got)
	}
	if got := m.FragmentCreateCount("created"); got != 50 {
		t.Errorf("fragment_create created = %d; want 50", got)
	}
}

// TestInMemoryKnowledgeMetrics verifies all knowledge-pipeline metrics methods
// introduced in Unit 5.
func TestInMemoryKnowledgeMetrics(t *testing.T) {
	t.Run("IncClaimCreate_records_outcome_and_dedupe_reason", func(t *testing.T) {
		m := NewInMemoryDiscoverabilityMetrics()
		m.IncClaimCreate("created", "")
		m.IncClaimCreate("duplicate", "exact_match")
		m.IncClaimCreate("duplicate", "semantic_similarity")

		samples := m.ClaimCreateSamples()
		if len(samples) != 3 {
			t.Fatalf("claim create samples = %d; want 3", len(samples))
		}
		if samples[0].Outcome != "created" || samples[0].DedupeReason != "" {
			t.Errorf("sample[0] = %+v", samples[0])
		}
		if samples[1].Outcome != "duplicate" || samples[1].DedupeReason != "exact_match" {
			t.Errorf("sample[1] = %+v", samples[1])
		}
		if samples[2].Outcome != "duplicate" || samples[2].DedupeReason != "semantic_similarity" {
			t.Errorf("sample[2] = %+v", samples[2])
		}
	})

	t.Run("IncVerifyVerdict_counts_by_outcome", func(t *testing.T) {
		m := NewInMemoryDiscoverabilityMetrics()
		m.IncVerifyVerdict("verified")
		m.IncVerifyVerdict("verified")
		m.IncVerifyVerdict("refuted")

		if got := m.VerifyVerdictCount("verified"); got != 2 {
			t.Errorf("verified = %d; want 2", got)
		}
		if got := m.VerifyVerdictCount("refuted"); got != 1 {
			t.Errorf("refuted = %d; want 1", got)
		}
		if got := m.VerifyVerdictCount("inconclusive"); got != 0 {
			t.Errorf("inconclusive = %d; want 0", got)
		}
	})

	t.Run("IncPromotionOutcome_counts_by_outcome", func(t *testing.T) {
		m := NewInMemoryDiscoverabilityMetrics()
		m.IncPromotionOutcome("promoted")
		m.IncPromotionOutcome("promoted")
		m.IncPromotionOutcome("skipped")

		if got := m.PromotionOutcomeCount("promoted"); got != 2 {
			t.Errorf("promoted = %d; want 2", got)
		}
		if got := m.PromotionOutcomeCount("skipped"); got != 1 {
			t.Errorf("skipped = %d; want 1", got)
		}
	})

	t.Run("ObservePromoteLockWait_records_durations", func(t *testing.T) {
		m := NewInMemoryDiscoverabilityMetrics()
		m.ObservePromoteLockWait(0.001)
		m.ObservePromoteLockWait(0.250)

		waits := m.PromoteLockWaits()
		if len(waits) != 2 {
			t.Fatalf("promote lock waits = %d; want 2", len(waits))
		}
		if waits[0] != 0.001 || waits[1] != 0.250 {
			t.Errorf("waits = %+v", waits)
		}
	})

	t.Run("IncFragmentRetract_counts_retracts", func(t *testing.T) {
		m := NewInMemoryDiscoverabilityMetrics()
		m.IncFragmentRetract()
		m.IncFragmentRetract()
		m.IncFragmentRetract()

		if got := m.FragmentRetractCount(); got != 3 {
			t.Errorf("fragment retracts = %d; want 3", got)
		}
	})

	t.Run("IncFactNeedsRevalidation_counts_events", func(t *testing.T) {
		m := NewInMemoryDiscoverabilityMetrics()
		m.IncFactNeedsRevalidation()
		m.IncFactNeedsRevalidation()

		if got := m.FactNeedsRevalidationCount(); got != 2 {
			t.Errorf("fact needs revalidation = %d; want 2", got)
		}
	})

	t.Run("IncCommunityDetect_counts_by_outcome", func(t *testing.T) {
		m := NewInMemoryDiscoverabilityMetrics()
		m.IncCommunityDetect("ok")
		m.IncCommunityDetect("ok")
		m.IncCommunityDetect("error")

		if got := m.CommunityDetectCount("ok"); got != 2 {
			t.Errorf("community detect ok = %d; want 2", got)
		}
		if got := m.CommunityDetectCount("error"); got != 1 {
			t.Errorf("community detect error = %d; want 1", got)
		}
	})

	t.Run("ObserveCommunityDetect_records_duration_and_nodes", func(t *testing.T) {
		m := NewInMemoryDiscoverabilityMetrics()
		m.ObserveCommunityDetect(1.5, 100)
		m.ObserveCommunityDetect(3.2, 500)

		samples := m.CommunityDetectSamples()
		if len(samples) != 2 {
			t.Fatalf("community detect samples = %d; want 2", len(samples))
		}
		if samples[0].DurationSeconds != 1.5 || samples[0].ProjectedNodes != 100 {
			t.Errorf("sample[0] = %+v", samples[0])
		}
		if samples[1].DurationSeconds != 3.2 || samples[1].ProjectedNodes != 500 {
			t.Errorf("sample[1] = %+v", samples[1])
		}
	})

	t.Run("noop_never_panics_with_new_methods", func(t *testing.T) {
		m := NoopDiscoverabilityMetrics()
		m.IncClaimCreate("created", "")
		m.IncVerifyVerdict("verified")
		m.IncPromotionOutcome("promoted")
		m.ObservePromoteLockWait(0.1)
		m.ObserveRecall(1, 1, "ok")
		m.ObserveMemoryFunnelLatency("claim_to_verify", 1, "verified")
		m.IncFragmentRetract()
		m.IncFactNeedsRevalidation()
		m.IncCommunityDetect("ok")
		m.ObserveCommunityDetect(1.0, 50)
	})

	// Cross-profile isolation: two separate InMemoryDiscoverabilityMetrics
	// instances represent two independent profiles and must not share state.
	// This mirrors the profile-isolation invariant enforced at the service layer.
	t.Run("cross_profile_isolation", func(t *testing.T) {
		profileA := NewInMemoryDiscoverabilityMetrics()
		profileB := NewInMemoryDiscoverabilityMetrics()

		profileA.IncClaimCreate("created", "")
		profileA.IncVerifyVerdict("verified")
		profileA.IncPromotionOutcome("promoted")
		profileA.IncFragmentRetract()
		profileA.IncFactNeedsRevalidation()
		profileA.IncCommunityDetect("ok")

		// profile B must see zero counts — no cross-profile bleed.
		if got := len(profileB.ClaimCreateSamples()); got != 0 {
			t.Errorf("profile B claim create samples = %d; want 0", got)
		}
		if got := profileB.VerifyVerdictCount("verified"); got != 0 {
			t.Errorf("profile B verify verdict = %d; want 0", got)
		}
		if got := profileB.PromotionOutcomeCount("promoted"); got != 0 {
			t.Errorf("profile B promotion outcome = %d; want 0", got)
		}
		if got := profileB.FragmentRetractCount(); got != 0 {
			t.Errorf("profile B fragment retracts = %d; want 0", got)
		}
		if got := profileB.FactNeedsRevalidationCount(); got != 0 {
			t.Errorf("profile B fact needs revalidation = %d; want 0", got)
		}
		if got := profileB.CommunityDetectCount("ok"); got != 0 {
			t.Errorf("profile B community detect ok = %d; want 0", got)
		}
	})
}
