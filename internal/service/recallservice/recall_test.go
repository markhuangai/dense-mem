package recallservice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes -----------------------------------------------------------------

type fakeSemanticSearcher struct {
	mu        sync.Mutex
	hits      []semanticsearch.SearchHit
	lastLimit int
	errFunc   func() error
	onCall    func()
}

func (f *fakeSemanticSearcher) QueryVectorIndex(ctx context.Context, profileID string, vec []float32, limit int) ([]semanticsearch.SearchHit, error) {
	f.mu.Lock()
	f.lastLimit = limit
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall()
	}
	if f.errFunc != nil {
		if err := f.errFunc(); err != nil {
			return nil, err
		}
	}
	out := make([]semanticsearch.SearchHit, len(f.hits))
	for i, h := range f.hits {
		h.ProfileID = profileID
		if h.Type == "" {
			h.Type = "fragment"
		}
		out[i] = h
	}
	return out, nil
}

type fakeKeywordSearcher struct {
	mu        sync.Mutex
	hits      []keywordsearch.FragmentSearchResult
	lastLimit int
	errFunc   func() error
	onCall    func()
}

func (f *fakeKeywordSearcher) SearchContent(ctx context.Context, profileID string, query string, labels []string, limit int) ([]keywordsearch.FragmentSearchResult, error) {
	f.mu.Lock()
	f.lastLimit = limit
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall()
	}
	if f.errFunc != nil {
		if err := f.errFunc(); err != nil {
			return nil, err
		}
	}
	out := make([]keywordsearch.FragmentSearchResult, len(f.hits))
	for i, h := range f.hits {
		h.ProfileID = profileID
		out[i] = h
	}
	return out, nil
}

type fakeHydrator struct {
	frags          map[string]*domain.Fragment
	callCount      int32
	batchCallCount int32
	missIDs        map[string]bool
	batchErr       error
}

func (f *fakeHydrator) GetByID(ctx context.Context, profileID, fragmentID string) (*domain.Fragment, error) {
	atomic.AddInt32(&f.callCount, 1)
	if f.missIDs != nil && f.missIDs[fragmentID] {
		return nil, errors.New("fragment not found")
	}
	if frag, ok := f.frags[fragmentID]; ok {
		return frag, nil
	}
	return &domain.Fragment{FragmentID: fragmentID, ProfileID: profileID, Content: fragmentID + " content"}, nil
}

func (f *fakeHydrator) GetByIDs(ctx context.Context, profileID string, fragmentIDs []string) (map[string]*domain.Fragment, error) {
	atomic.AddInt32(&f.batchCallCount, 1)
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	out := make(map[string]*domain.Fragment, len(fragmentIDs))
	for _, fragmentID := range fragmentIDs {
		if f.missIDs != nil && f.missIDs[fragmentID] {
			continue
		}
		if frag, ok := f.frags[fragmentID]; ok {
			out[fragmentID] = frag
			continue
		}
		out[fragmentID] = &domain.Fragment{FragmentID: fragmentID, ProfileID: profileID, Content: fragmentID + " content"}
	}
	return out, nil
}

type fakeFactSearcher struct {
	results      []FactRecallResult
	lastQuery    string
	lastLimit    int
	err          error
	respectLimit bool
}

func (f *fakeFactSearcher) SearchActive(ctx context.Context, profileID string, query string, limit int) ([]FactRecallResult, error) {
	f.lastQuery = query
	f.lastLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	out := make([]FactRecallResult, len(f.results))
	copy(out, f.results)
	if f.respectLimit && limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type fakeClaimSearcher struct {
	results      []ClaimRecallResult
	lastQuery    string
	lastLimit    int
	err          error
	respectLimit bool
}

func (f *fakeClaimSearcher) SearchValidated(ctx context.Context, profileID string, query string, limit int) ([]ClaimRecallResult, error) {
	f.lastQuery = query
	f.lastLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	out := make([]ClaimRecallResult, len(f.results))
	copy(out, f.results)
	if f.respectLimit && limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type fakeCommunityExpander struct {
	expansion   CommunityExpansion
	err         error
	calls       int
	lastProfile string
	lastQuery   string
	lastOptions CommunityExpansionOptions
}

func (f *fakeCommunityExpander) Expand(ctx context.Context, profileID string, query string, opts CommunityExpansionOptions) (CommunityExpansion, error) {
	f.calls++
	f.lastProfile = profileID
	f.lastQuery = query
	f.lastOptions = opts
	if f.err != nil {
		return CommunityExpansion{}, f.err
	}
	return f.expansion, nil
}

type fakeFactGetter struct {
	facts          map[string]*domain.Fact
	callCount      int32
	batchCallCount int32
	batchErr       error
}

func (f *fakeFactGetter) Get(ctx context.Context, profileID string, factID string) (*domain.Fact, error) {
	atomic.AddInt32(&f.callCount, 1)
	if fact, ok := f.facts[factID]; ok {
		return fact, nil
	}
	return nil, errors.New("fact not found")
}

func (f *fakeFactGetter) GetByIDs(ctx context.Context, profileID string, factIDs []string) (map[string]*domain.Fact, error) {
	atomic.AddInt32(&f.batchCallCount, 1)
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	out := make(map[string]*domain.Fact, len(factIDs))
	for _, factID := range factIDs {
		if fact, ok := f.facts[factID]; ok {
			out[factID] = fact
		}
	}
	return out, nil
}

type fakeClaimGetter struct {
	claims         map[string]*domain.Claim
	callCount      int32
	batchCallCount int32
	batchErr       error
}

func (f *fakeClaimGetter) Get(ctx context.Context, profileID string, claimID string) (*domain.Claim, error) {
	atomic.AddInt32(&f.callCount, 1)
	if claim, ok := f.claims[claimID]; ok {
		return claim, nil
	}
	return nil, errors.New("claim not found")
}

func (f *fakeClaimGetter) GetByIDs(ctx context.Context, profileID string, claimIDs []string) (map[string]*domain.Claim, error) {
	atomic.AddInt32(&f.batchCallCount, 1)
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	out := make(map[string]*domain.Claim, len(claimIDs))
	for _, claimID := range claimIDs {
		if claim, ok := f.claims[claimID]; ok {
			out[claimID] = claim
		}
	}
	return out, nil
}

type getOnlyFragmentHydrator struct {
	frags     map[string]*domain.Fragment
	callCount int32
}

func (g *getOnlyFragmentHydrator) GetByID(ctx context.Context, profileID, fragmentID string) (*domain.Fragment, error) {
	atomic.AddInt32(&g.callCount, 1)
	if frag, ok := g.frags[fragmentID]; ok {
		return frag, nil
	}
	return &domain.Fragment{FragmentID: fragmentID, ProfileID: profileID, Content: fragmentID + " content"}, nil
}

type getOnlyFactGetter struct {
	facts     map[string]*domain.Fact
	callCount int32
}

func (g *getOnlyFactGetter) Get(ctx context.Context, profileID string, factID string) (*domain.Fact, error) {
	atomic.AddInt32(&g.callCount, 1)
	if fact, ok := g.facts[factID]; ok {
		return fact, nil
	}
	return nil, errors.New("fact not found")
}

type getOnlyClaimGetter struct {
	claims    map[string]*domain.Claim
	callCount int32
}

func (g *getOnlyClaimGetter) Get(ctx context.Context, profileID string, claimID string) (*domain.Claim, error) {
	atomic.AddInt32(&g.callCount, 1)
	if claim, ok := g.claims[claimID]; ok {
		return claim, nil
	}
	return nil, errors.New("claim not found")
}

type fakeLogger struct {
	warns  int32
	errors int32
}

func (l *fakeLogger) Info(string, ...observability.LogAttr) {}

func (l *fakeLogger) Error(string, error, ...observability.LogAttr) {
	atomic.AddInt32(&l.errors, 1)
}

func (l *fakeLogger) Warn(string, ...observability.LogAttr) {
	atomic.AddInt32(&l.warns, 1)
}

func (l *fakeLogger) Debug(string, ...observability.LogAttr) {}

func (l *fakeLogger) With(...observability.LogAttr) observability.LogProvider {
	return l
}

// --- tests -----------------------------------------------------------------

// TestRecallService_HybridMergesFragmentOnly — backpressure case (AC-39).
// Semantic branch returns f1, f2. Keyword branch returns f2, f3 plus a
// fact-typed hit. Merged output must include fragments only, deduped by id.
func TestRecallService_HybridMergesFragmentOnly(t *testing.T) {
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f1", Type: "fragment"},
			{ID: "f2", Type: "fragment"},
			{ID: "fact-x", Type: "fact"}, // must be filtered out
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{FragmentID: "f2"},
			{FragmentID: "f3"},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, metrics)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "test", Limit: 10})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 fragment hits, got %d", len(out))
	}
	seen := map[string]bool{}
	for _, h := range out {
		if h.Fragment == nil {
			t.Fatalf("hit has nil fragment")
		}
		if strings.HasPrefix(h.Fragment.FragmentID, "fact") {
			t.Errorf("fact-typed id %q leaked into fragment-only output", h.Fragment.FragmentID)
		}
		if seen[h.Fragment.FragmentID] {
			t.Errorf("id %q appeared twice; recall must dedupe by id", h.Fragment.FragmentID)
		}
		seen[h.Fragment.FragmentID] = true
	}
	// f2 is present in both branches — it must have both ranks set.
	for _, h := range out {
		if h.Fragment.FragmentID == "f2" {
			if h.SemanticRank == 0 || h.KeywordRank == 0 {
				t.Errorf("f2 ranks = (sem=%d, kw=%d); both branches should populate", h.SemanticRank, h.KeywordRank)
			}
		}
	}
	samples := metrics.RecallSamples()
	require.Len(t, samples, 1)
	require.Equal(t, 3, samples[0].ResultCount)
	require.Equal(t, "ok", samples[0].Outcome)
}

func TestRecallService_UsesBatchFragmentHydration(t *testing.T) {
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f1", Type: "fragment"},
			{ID: "f2", Type: "fragment"},
		},
	}
	kw := &fakeKeywordSearcher{}
	emb := &stubEmbedding{DimensionsResult: 4}
	hydrator := &fakeHydrator{}
	svc := NewRecallService(emb, sem, kw, hydrator, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "test", Limit: 2})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hydrator.batchCallCount))
	assert.Equal(t, int32(0), atomic.LoadInt32(&hydrator.callCount))
}

func TestRecallTemporalWindowHelpers(t *testing.T) {
	base := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	before := base.Add(-time.Hour)
	after := base.Add(time.Hour)

	require.False(t, factMatchesRecallWindow(nil, &base, &base))
	require.False(t, claimMatchesRecallWindow(nil, &base, &base))

	fact := &domain.Fact{RecordedAt: base, ValidFrom: &base}
	require.False(t, factMatchesRecallWindow(fact, &before, nil))
	fact.ValidFrom = nil
	fact.ValidTo = &base
	require.False(t, factMatchesRecallWindow(fact, &base, nil))
	fact.ValidTo = nil
	require.False(t, factMatchesRecallWindow(fact, nil, &before))
	fact.RecordedTo = &base
	require.False(t, factMatchesRecallWindow(fact, nil, &base))
	fact.RecordedTo = nil
	require.True(t, factMatchesRecallWindow(fact, &after, &after))

	claim := &domain.Claim{RecordedAt: base, ValidFrom: &base}
	require.False(t, claimMatchesRecallWindow(claim, &before, nil))
	claim.ValidFrom = nil
	claim.ValidTo = &base
	require.False(t, claimMatchesRecallWindow(claim, &base, nil))
	claim.ValidTo = nil
	require.False(t, claimMatchesRecallWindow(claim, nil, &before))
	claim.RecordedTo = &base
	require.False(t, claimMatchesRecallWindow(claim, nil, &base))
	claim.RecordedTo = nil
	require.True(t, claimMatchesRecallWindow(claim, &after, &after))

	factCandidate := FactRecallResult{RecordedAt: base, ValidFrom: &base}
	require.False(t, factCandidateMatchesRecallWindow(factCandidate, &before, nil))
	factCandidate.ValidFrom = nil
	factCandidate.ValidTo = &base
	require.False(t, factCandidateMatchesRecallWindow(factCandidate, &base, nil))
	factCandidate.ValidTo = nil
	require.False(t, factCandidateMatchesRecallWindow(factCandidate, nil, &before))
	factCandidate.RecordedTo = &base
	require.False(t, factCandidateMatchesRecallWindow(factCandidate, nil, &base))
	factCandidate.RecordedTo = nil
	require.True(t, factCandidateMatchesRecallWindow(factCandidate, &after, &after))

	claimCandidate := ClaimRecallResult{RecordedAt: base, ValidFrom: &base}
	require.False(t, claimCandidateMatchesRecallWindow(claimCandidate, &before, nil))
	claimCandidate.ValidFrom = nil
	claimCandidate.ValidTo = &base
	require.False(t, claimCandidateMatchesRecallWindow(claimCandidate, &base, nil))
	claimCandidate.ValidTo = nil
	require.False(t, claimCandidateMatchesRecallWindow(claimCandidate, nil, &before))
	claimCandidate.RecordedTo = &base
	require.False(t, claimCandidateMatchesRecallWindow(claimCandidate, nil, &base))
	claimCandidate.RecordedTo = nil
	require.True(t, claimCandidateMatchesRecallWindow(claimCandidate, &after, &after))
}

func TestRecallSmallHelpersAndNilLoggers(t *testing.T) {
	require.Nil(t, sanitizeEmbeddingError(nil))
	require.Equal(t, "recall: embedding timeout", sanitizeEmbeddingError(embedding.ErrEmbeddingTimeout).Error())
	require.Equal(t, "recall: embedding rate limited", sanitizeEmbeddingError(embedding.ErrEmbeddingRateLimit).Error())
	require.Equal(t, "recall: embedding provider error", sanitizeEmbeddingError(embedding.ErrEmbeddingProvider).Error())
	require.Equal(t, "recall: embedding unavailable", sanitizeEmbeddingError(errors.New("other")).Error())
	require.Equal(t, MaxLimit, clampLimit(MaxLimit+100))
	require.Equal(t, DefaultLimit, clampLimit(0))

	svc := &recallService{}
	svc.logKeywordError(errors.New("keyword failed"))
	svc.logHydrateError("fragment-1", errors.New("hydrate failed"))
	svc.logEmbeddingError(errors.New("embedding failed"))
}

// TestRecallService_OverfetchesVectorBranch — AC-40 overfetch requirement.
func TestRecallService_OverfetchesVectorBranch(t *testing.T) {
	sem := &fakeSemanticSearcher{}
	kw := &fakeKeywordSearcher{}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	_, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if sem.lastLimit < 30 {
		t.Errorf("semantic branch lastLimit = %d; want ≥ 30 (10x overfetch)", sem.lastLimit)
	}
	if kw.lastLimit < 30 {
		t.Errorf("keyword branch lastLimit = %d; want ≥ 30 (10x overfetch)", kw.lastLimit)
	}
}

// TestRecallService_EmbeddingFailureReturnsSanitizedError — AC-40 fail-closed.
func TestRecallService_EmbeddingFailureReturnsSanitizedError(t *testing.T) {
	emb := &stubEmbedding{
		EmbedFunc: func(context.Context, string) ([]float32, string, error) {
			return nil, "", &embedding.ProviderError{Provider: "openai", Message: "auth failed for sk-secret-123"}
		},
	}
	svc := NewRecallService(emb, &fakeSemanticSearcher{}, &fakeKeywordSearcher{}, &fakeHydrator{}, nil, nil)

	_, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3})
	if err == nil {
		t.Fatal("expected error from embedding failure")
	}
	if !errors.Is(err, ErrEmbeddingUnavailable) {
		t.Errorf("err = %v; want ErrEmbeddingUnavailable", err)
	}
	// The sanitized error text must NOT leak provider specifics.
	if strings.Contains(err.Error(), "sk-") || strings.Contains(err.Error(), "openai") {
		t.Errorf("sanitized error leaked provider detail: %q", err.Error())
	}
}

// TestRecallService_RunsBranchesInParallel — uses a barrier that deadlocks
// unless both branches are in flight simultaneously.
func TestRecallService_RunsBranchesInParallel(t *testing.T) {
	barrier := make(chan struct{}, 2)
	release := make(chan struct{})

	sem := &fakeSemanticSearcher{
		onCall: func() {
			barrier <- struct{}{}
			<-release
		},
	}
	kw := &fakeKeywordSearcher{
		onCall: func() {
			barrier <- struct{}{}
			<-release
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	done := make(chan error, 1)
	go func() {
		_, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 1})
		done <- err
	}()

	// If branches ran serially, only one signal would arrive before `release`
	// is closed — this receive would block forever. The test harness fails
	// the test if it deadlocks.
	<-barrier
	<-barrier
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("Recall: %v", err)
	}
}

// TestRecallService_ClampsAndDefaultsLimit covers AC-38 validation bounds.
func TestRecallService_ClampsAndDefaultsLimit(t *testing.T) {
	cases := []struct {
		name     string
		input    int
		wantMult int // sem.lastLimit should equal wantMult * OverfetchMultiplier
	}{
		{"zero defaults to 10", 0, DefaultLimit},
		{"negative defaults to 10", -5, DefaultLimit},
		{"above max clamps to 50", 999, MaxLimit},
		{"within range passes through", 7, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sem := &fakeSemanticSearcher{}
			kw := &fakeKeywordSearcher{}
			emb := &stubEmbedding{DimensionsResult: 4}
			svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)
			_, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: tc.input})
			if err != nil {
				t.Fatalf("Recall: %v", err)
			}
			want := tc.wantMult * OverfetchMultiplier
			if sem.lastLimit != want {
				t.Errorf("sem.lastLimit = %d; want %d", sem.lastLimit, want)
			}
		})
	}
}

// TestRecallService_RejectsBlankQuery defends AC-38 at the service boundary.
func TestRecallService_RejectsBlankQuery(t *testing.T) {
	svc := NewRecallService(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{}, &fakeKeywordSearcher{}, &fakeHydrator{}, nil, nil,
	)
	_, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "   ", Limit: 3})
	if err == nil {
		t.Fatal("expected error for blank query")
	}
}

func TestRecallService_TierEnrichmentUsesQueryMatchedSearchers(t *testing.T) {
	sem := &fakeSemanticSearcher{}
	kw := &fakeKeywordSearcher{}
	factSearcher := &fakeFactSearcher{
		results: []FactRecallResult{{FactID: "fact-1", ProfileID: "pA"}},
	}
	claimSearcher := &fakeClaimSearcher{
		results: []ClaimRecallResult{{ClaimID: "claim-1", ProfileID: "pA"}},
	}
	factGetter := &fakeFactGetter{
		facts: map[string]*domain.Fact{
			"fact-1": {
				FactID:     "fact-1",
				ProfileID:  "pA",
				Status:     domain.FactStatusActive,
				TruthScore: 0.95,
				RecordedAt: time.Now().UTC(),
			},
		},
	}
	claimGetter := &fakeClaimGetter{
		claims: map[string]*domain.Claim{
			"claim-1": {
				ClaimID:     "claim-1",
				ProfileID:   "pA",
				Status:      domain.StatusValidated,
				ExtractConf: 0.8,
				RecordedAt:  time.Now().UTC(),
			},
		},
	}

	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		&fakeHydrator{},
		factSearcher,
		factGetter,
		claimSearcher,
		claimGetter,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "mars mission", Limit: 5})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "mars mission", factSearcher.lastQuery)
	assert.Equal(t, "mars mission", claimSearcher.lastQuery)
	assert.Equal(t, TierActiveFact, out[0].Tier)
	require.NotNil(t, out[0].Fact)
	assert.Equal(t, "fact-1", out[0].Fact.FactID)
	assert.Equal(t, TierValidatedClaim, out[1].Tier)
	require.NotNil(t, out[1].Claim)
	assert.Equal(t, "claim-1", out[1].Claim.ClaimID)
	assert.Equal(t, int32(1), atomic.LoadInt32(&factGetter.batchCallCount))
	assert.Equal(t, int32(0), atomic.LoadInt32(&factGetter.callCount))
	assert.Equal(t, int32(1), atomic.LoadInt32(&claimGetter.batchCallCount))
	assert.Equal(t, int32(0), atomic.LoadInt32(&claimGetter.callCount))
}

func TestRecallService_EvidenceSourceQueryPrefersFragmentOverDerivedFact(t *testing.T) {
	query := "Which source note says service TIER-E-001 owner uses owner-lumen?"
	content := "Source note from ops. service TIER-E-001 owner uses owner-lumen."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{{ID: "fragment-evidence", Type: "fragment", ProfileID: "pA", Content: content}},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{{FragmentID: "fragment-evidence", ProfileID: "pA", Content: content}},
	}
	factSearcher := &fakeFactSearcher{
		results: []FactRecallResult{{FactID: "fact-derived", ProfileID: "pA"}},
	}
	factGetter := &fakeFactGetter{
		facts: map[string]*domain.Fact{
			"fact-derived": {
				FactID:     "fact-derived",
				ProfileID:  "pA",
				Subject:    "service TIER-E-001 owner",
				Predicate:  "uses",
				Object:     "owner-lumen",
				Status:     domain.FactStatusActive,
				TruthScore: 0.99,
				RecordedAt: time.Now().UTC(),
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		&fakeHydrator{},
		factSearcher,
		factGetter,
		nil,
		nil,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 1})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Fragment)
	require.Equal(t, "fragment-evidence", out[0].Fragment.FragmentID)
}

func TestRecallService_SourceOfTruthQueryKeepsFactTier(t *testing.T) {
	query := "What is the source of truth owner for service TIER-E-001?"
	content := "Source of truth runbook. service TIER-E-001 owner uses owner-lumen."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{{ID: "fragment-source-of-truth", Type: "fragment", ProfileID: "pA", Content: content}},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{{FragmentID: "fragment-source-of-truth", ProfileID: "pA", Content: content}},
	}
	factSearcher := &fakeFactSearcher{
		results: []FactRecallResult{{FactID: "fact-authoritative", ProfileID: "pA"}},
	}
	factGetter := &fakeFactGetter{
		facts: map[string]*domain.Fact{
			"fact-authoritative": {
				FactID:     "fact-authoritative",
				ProfileID:  "pA",
				Subject:    "service TIER-E-001 owner",
				Predicate:  "uses",
				Object:     "owner-lumen",
				Status:     domain.FactStatusActive,
				TruthScore: 0.99,
				RecordedAt: time.Now().UTC(),
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		&fakeHydrator{},
		factSearcher,
		factGetter,
		nil,
		nil,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 1})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-authoritative", out[0].Fact.FactID)
}

func TestRecallService_TierEnrichmentDowngradesOverlayFacts(t *testing.T) {
	factSearcher := &fakeFactSearcher{
		results: []FactRecallResult{{
			FactID:         "fact-overlay",
			ProfileID:      "pA",
			AuthorityState: "overlay",
		}},
	}
	factGetter := &fakeFactGetter{
		facts: map[string]*domain.Fact{
			"fact-overlay": {
				FactID:     "fact-overlay",
				ProfileID:  "pA",
				Status:     domain.FactStatusActive,
				TruthScore: 0.95,
				RecordedAt: time.Now().UTC(),
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{},
		factSearcher,
		factGetter,
		nil,
		nil,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "platform choice", Limit: 5})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, TierConflict, out[0].Tier)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "overlay", out[0].Fact.AuthorityState)
}

// TestRecallService_RejectsEmptyProfileID enforces profile isolation input.
func TestRecallService_RejectsEmptyProfileID(t *testing.T) {
	svc := NewRecallService(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{}, &fakeKeywordSearcher{}, &fakeHydrator{}, nil, nil,
	)
	_, err := svc.Recall(context.Background(), "", RecallRequest{Query: "q", Limit: 3})
	if err == nil {
		t.Fatal("expected error for empty profile id")
	}
}

// TestRecallService_PostFiltersCrossProfileHits enforces AC-40 profile
// isolation even when the underlying index hands back a cross-profile hit.
func TestRecallService_PostFiltersCrossProfileHits(t *testing.T) {
	sem := &fakeSemanticSearcher{}
	// Inject hits directly bypassing the helper so ProfileID differs.
	sem.hits = []semanticsearch.SearchHit{
		{ID: "f-own", Type: "fragment", ProfileID: "pA"},
	}
	// Replace semantic searcher with one that returns both profiles.
	mixed := &mixedSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f-own", Type: "fragment", ProfileID: "pA"},
			{ID: "f-other", Type: "fragment", ProfileID: "pB"},
		},
	}
	kw := &fakeKeywordSearcher{}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, mixed, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 10})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, h := range out {
		if h.Fragment.FragmentID == "f-other" {
			t.Errorf("cross-profile fragment f-other leaked into pA output")
		}
	}
}

// mixedSemanticSearcher intentionally returns rows with varying team_id so
// the post-filter can be exercised.
type mixedSemanticSearcher struct {
	hits []semanticsearch.SearchHit
}

func (m *mixedSemanticSearcher) QueryVectorIndex(ctx context.Context, profileID string, vec []float32, limit int) ([]semanticsearch.SearchHit, error) {
	return m.hits, nil
}

// TestRecallService_RRFScoreOrdering verifies the RRF formula drives ordering.
func TestRecallService_RRFScoreOrdering(t *testing.T) {
	// f-top appears at rank 1 in both branches → highest score.
	// f-mid appears at rank 3 keyword only.
	// f-low appears at rank 5 semantic only.
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f-top", Type: "fragment"},
			{ID: "f-a", Type: "fragment"},
			{ID: "f-b", Type: "fragment"},
			{ID: "f-c", Type: "fragment"},
			{ID: "f-low", Type: "fragment"},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{FragmentID: "f-top"},
			{FragmentID: "f-a"},
			{FragmentID: "f-mid"},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 50})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(out) == 0 || out[0].Fragment.FragmentID != "f-top" {
		t.Fatalf("top result = %v; want f-top", idsOf(out))
	}
	// f-top must outscore every single-branch id.
	var topScore, midScore float64
	for _, h := range out {
		if h.Fragment.FragmentID == "f-top" {
			topScore = h.FinalScore
		}
		if h.Fragment.FragmentID == "f-mid" {
			midScore = h.FinalScore
		}
	}
	if topScore <= midScore {
		t.Errorf("top score %v must exceed single-branch score %v", topScore, midScore)
	}
}

func TestRecallService_CurrentnessRerankPrefersCurrentUpdate(t *testing.T) {
	query := "What is the current deployment window for service OBS-001?"
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:      "f-archived",
				Type:    "fragment",
				Content: "Archived 2025 calendar. service OBS-001 deployed at 02:01 UTC before the correction.",
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-current",
				Content:    "Current release calendar update dated 2026-06-28. service OBS-001 now deploys at 03:01 UTC.",
			},
			{
				FragmentID: "f-archived",
				Content:    "Archived 2025 calendar. service OBS-001 deployed at 02:01 UTC before the correction.",
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-current", out[0].Fragment.FragmentID)
}

func TestCurrentnessAdjustmentRequiresQueryIdentifiersForBoosts(t *testing.T) {
	query := "What is the current deployment window for service OBS-001?"
	neighbor := "Current release calendar update dated 2026-06-28. service OBS-002 now deploys at 03:02 UTC."

	require.Zero(t, currentnessAdjustment(query, neighbor))
}

func TestRecallService_CurrentnessRerankPrefersNewerDatedFragment(t *testing.T) {
	query := "Who is the current owner for account TMP-001?"
	oldCreated := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	newCreated := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:        "f-old-current",
				Type:      "fragment",
				Content:   "Current owner record. account TMP-001 owner is alice.",
				CreatedAt: oldCreated,
				UpdatedAt: oldCreated,
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-new-dated",
				Content:    "Owner record dated 2026-06-26. account TMP-001 owner is bob.",
				CreatedAt:  newCreated,
				UpdatedAt:  newCreated,
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-new-dated", out[0].Fragment.FragmentID)
}

func TestRecallService_ValidAtFiltersFutureFragmentMetadata(t *testing.T) {
	query := "As of 2026-06-22, what owner did service TIER-W-001 use?"
	validAt := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:       "f-future",
				Type:     "fragment",
				Content:  "Since 2026-06-27, service TIER-W-001 owner uses owner-cobalt.",
				Metadata: map[string]any{"valid_from": "2026-06-27T00:00:00Z"},
			},
			{
				ID:       "f-old",
				Type:     "fragment",
				Content:  "Since 2026-06-20, service TIER-W-001 owner uses owner-coral.",
				Metadata: map[string]any{"valid_from": "2026-06-20T00:00:00Z"},
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-future",
				Content:    "Since 2026-06-27, service TIER-W-001 owner uses owner-cobalt.",
				Metadata:   map[string]any{"valid_from": "2026-06-27T00:00:00Z"},
			},
			{
				FragmentID: "f-old",
				Content:    "Since 2026-06-20, service TIER-W-001 owner uses owner-coral.",
				Metadata:   map[string]any{"valid_from": "2026-06-20T00:00:00Z"},
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5, ValidAt: &validAt})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-old", out[0].Fragment.FragmentID)
	for _, hit := range out {
		if hit.Fragment != nil {
			require.NotEqual(t, "f-future", hit.Fragment.FragmentID)
		}
	}
}

func TestFragmentMetadataKnownAtRequiresExplicitRecordedMetadata(t *testing.T) {
	knownAt := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)

	require.True(t, fragmentMetadataMatchesRecallWindow(nil, nil, &knownAt))
	require.False(t, fragmentMetadataMatchesRecallWindow(
		map[string]any{"recorded_at": "2026-06-29T00:00:00Z"},
		nil,
		&knownAt,
	))
}

func TestRecallService_CurrentnessRerankDatedFragmentBeatsUndatedCurrentCue(t *testing.T) {
	query := "Who is the current owner for account TMP-001?"
	oldCreated := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	newCreated := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	oldCurrent := semanticsearch.SearchHit{
		ID:        "f-old-current",
		Type:      "fragment",
		Content:   "Current owner record. account TMP-001 owner is alice.",
		CreatedAt: oldCreated,
		UpdatedAt: oldCreated,
	}
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{oldCurrent},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-old-current",
				Content:    oldCurrent.Content,
				CreatedAt:  oldCreated,
				UpdatedAt:  oldCreated,
			},
			{
				FragmentID: "f-new-dated",
				Content:    "Owner record dated 2026-06-26. account TMP-001 owner is bob.",
				CreatedAt:  newCreated,
				UpdatedAt:  newCreated,
			},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-new-dated", out[0].Fragment.FragmentID)
}

func TestRecallService_CurrentnessRerankPrefersRelativeDatedFragment(t *testing.T) {
	query := "Who is the current owner for service TMP-002?"
	importedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:        "f-undated-current",
				Type:      "fragment",
				Content:   "Current owner note. service TMP-002 owner uses owner-moon.",
				CreatedAt: importedAt,
				UpdatedAt: importedAt,
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-relative-dated",
				Content:    "Owner update from yesterday. service TMP-002 owner uses owner-sun.",
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
			{
				FragmentID: "f-undated-current",
				Content:    "Current owner note. service TMP-002 owner uses owner-moon.",
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-relative-dated", out[0].Fragment.FragmentID)
}

func TestRecallService_CurrentnessRerankPrefersMonthNameDatedFragment(t *testing.T) {
	query := "Who is the current owner for service TMP-003?"
	importedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:        "f-undated-current",
				Type:      "fragment",
				Content:   "Current owner note. service TMP-003 owner uses owner-moon.",
				CreatedAt: importedAt,
				UpdatedAt: importedAt,
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-month-dated",
				Content:    "Owner update dated June 27, 2026. service TMP-003 owner uses owner-sun.",
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
			{
				FragmentID: "f-undated-current",
				Content:    "Current owner note. service TMP-003 owner uses owner-moon.",
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-month-dated", out[0].Fragment.FragmentID)
}

func TestRecallService_CurrentnessRerankHandlesActiveDecisionQueries(t *testing.T) {
	query := "What is the active launch decision for notice RET-046?"
	required := "Retraction update dated 2026-06-28. Earlier approval for notice RET-046 was withdrawn. The active decision is launch-paused-046."
	stale := "Original announcement said notice RET-046 was launch-approved-046 before the retraction."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f-stale", Type: "fragment", Content: stale},
			{ID: "f-required", Type: "fragment", Content: required},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{FragmentID: "f-required", Content: required},
			{FragmentID: "f-stale", Content: stale},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-required", out[0].Fragment.FragmentID)
}

func TestCurrentnessTemporalAdjustmentRequiresQueryIdentifiers(t *testing.T) {
	query := "Who is the current owner for account TMP-001?"
	newest := time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)
	frame := currentnessTemporalFrame{hasContentDate: true, newestContentDate: newest}
	neighbor := rrfEntry{
		Content:   "Owner record dated 2026-06-26. account TMP-002 owner is bob.",
		CreatedAt: newest,
		UpdatedAt: newest,
	}

	require.Zero(t, currentnessTemporalAdjustment(query, neighbor, frame))
}

func TestCurrentnessTemporalFramePrefersContentDatesOverBulkImportTimes(t *testing.T) {
	query := "What is the current deployment window for service OBS-001?"
	importedAt := time.Date(2026, 6, 28, 20, 0, 0, 0, time.UTC)
	entries := []rrfEntry{
		{
			Content:   "Current release calendar update dated 2026-06-28. service OBS-001 now deploys at 03:01 UTC.",
			CreatedAt: importedAt,
			UpdatedAt: importedAt,
		},
		{
			Content:   "Draft migration plan suggested 02:01 UTC for service OBS-001, but the plan was replaced.",
			CreatedAt: importedAt,
			UpdatedAt: importedAt,
		},
	}

	frame := currentnessTemporalFrameFor(query, entries)

	require.True(t, frame.hasContentDate)
	require.False(t, frame.useFragmentTimestamp)
	require.Positive(t, currentnessTemporalAdjustment(query, entries[0], frame))
	require.Negative(t, currentnessTemporalAdjustment(query, entries[1], frame))
}

func TestLatestTemporalDateInEntryParsesRelativeDatesFromFragmentTimestamp(t *testing.T) {
	anchor := time.Date(2026, 6, 28, 18, 30, 0, 0, time.UTC)
	cases := []struct {
		name    string
		content string
		want    time.Time
	}{
		{
			name:    "yesterday",
			content: "Owner update from yesterday. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "numeric days ago",
			content: "Owner update from 2 days ago. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "word days ago",
			content: "Owner update from two days ago. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "last week",
			content: "Owner update from last week. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "month name with year",
			content: "Owner update dated June 27, 2026. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "abbreviated month with anchored year",
			content: "Owner update dated Jun 27. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "day before month",
			content: "Owner update dated 27 June 2026. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := latestTemporalDateInEntry(rrfEntry{Content: tc.content, CreatedAt: anchor, UpdatedAt: anchor})

			require.Equal(t, tc.want, got)
		})
	}
}

func TestFilterNonPositiveRRFEntriesDropsZeroScoresWhenPositiveExists(t *testing.T) {
	entries := []rrfEntry{
		{id: "positive", FinalScore: 0.01},
		{id: "zero", FinalScore: 0},
	}

	out := filterNonPositiveRRFEntries(entries)

	require.Len(t, out, 1)
	require.Equal(t, "positive", out[0].id)
}

func TestFilterNonPositiveRRFEntriesKeepsAllZeroScoresWhenNoPositiveExists(t *testing.T) {
	entries := []rrfEntry{
		{id: "zero-1", FinalScore: 0},
		{id: "zero-2", FinalScore: 0},
	}

	out := filterNonPositiveRRFEntries(entries)

	require.Len(t, out, 2)
	require.Equal(t, "zero-1", out[0].id)
	require.Equal(t, "zero-2", out[1].id)
}

func TestRecallService_IdentifierSpecificityPrefersExactJobID(t *testing.T) {
	query := "What timeout should job UNT-013 use?"
	neighbor := "Runtime configuration. job UNT-003 must use a timeout of 13 minutes."
	required := "Runtime configuration. job UNT-013 must use a timeout of 23 minutes."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f-neighbor", Type: "fragment", Content: neighbor},
			{ID: "f-filler-1", Type: "fragment", Content: "Runtime configuration. job UNT-001 must use a timeout of 11 minutes."},
			{ID: "f-filler-2", Type: "fragment", Content: "Runtime configuration. job UNT-041 must use a timeout of 11 minutes."},
			{ID: "f-required", Type: "fragment", Content: required},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{FragmentID: "f-required", Content: required},
			{FragmentID: "f-filler-1", Content: "Runtime configuration. job UNT-001 must use a timeout of 11 minutes."},
			{FragmentID: "f-neighbor", Content: neighbor},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-required", out[0].Fragment.FragmentID)
}

func TestIdentifierSpecificityAdjustmentRequiresExactIdentifier(t *testing.T) {
	queryText := rerankText("What timeout should job UNT-013 use?")

	require.Positive(t, identifierSpecificityAdjustment(queryText, "Runtime configuration. job UNT-013 must use a timeout of 23 minutes."))
	require.Zero(t, identifierSpecificityAdjustment(queryText, "Runtime configuration. job UNT-003 must use a timeout of 13 minutes."))
}

func TestRerankIdentifiersSkipsISODateTokens(t *testing.T) {
	queryText := rerankText("As of 2026-06-22, what owner did service TIER-W-001 use?")

	require.Equal(t, []string{"tier-w-001"}, rerankIdentifiers(queryText))
}

func TestApplyIdentifierSpecificityAdjustmentsRequiresUnitValueQuery(t *testing.T) {
	entries := []rrfEntry{
		{
			Content:    "Current release calendar update dated 2026-06-28. service OBS-001 now deploys at 03:01 UTC.",
			FinalScore: 0.02,
		},
	}

	applyIdentifierSpecificityAdjustments("What is the current deployment window for service OBS-001?", entries)

	require.Equal(t, 0.02, entries[0].FinalScore)
}

func TestRecallService_CueRerankPrefersDirectiveEvidence(t *testing.T) {
	query := "Which pager should alerts for queue NEG-001 use?"
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:      "f-stale",
				Type:    "fragment",
				Content: "Before 2026, queue NEG-001 used pager-001-red for alerts.",
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-required",
				Content:    "Routing policy dated 2026-06-28. Alerts for queue NEG-001 must use pager-001-green.",
			},
			{
				FragmentID: "f-stale",
				Content:    "Before 2026, queue NEG-001 used pager-001-red for alerts.",
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-required", out[0].Fragment.FragmentID)
	for _, hit := range out {
		require.NotEqual(t, "f-stale", hit.Fragment.FragmentID)
	}
}

func TestRecallService_CueRerankSuppressesHistoricalSiblingWhenDirectiveExists(t *testing.T) {
	query := "Which pager should alerts for queue NEG-001 use?"
	stale := "Before 2026, queue NEG-001 used pager-001-red for alerts."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f-stale", Type: "fragment", Content: stale},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-required",
				Content:    "Routing policy dated 2026-06-28. Alerts for queue NEG-001 must use pager-001-green. Do not use pager-001-red for this queue.",
			},
			{FragmentID: "f-stale", Content: stale},
			{FragmentID: "f-filler-1", Content: "Queue NEG-001 inventory reference for alert routing review."},
			{FragmentID: "f-filler-2", Content: "Queue NEG-001 runbook index entry for pager ownership audits."},
			{FragmentID: "f-filler-3", Content: "Queue NEG-001 on-call calendar note for escalation metadata."},
			{FragmentID: "f-filler-4", Content: "Queue NEG-001 dashboard bookmark for support handoff."},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.Len(t, out, 5)
	for _, hit := range out {
		require.NotEqual(t, "f-stale", hit.Fragment.FragmentID)
	}
}

func TestHistoricalSelectionAdjustmentRequiresDirectiveFrame(t *testing.T) {
	query := "Which pager should alerts for queue NEG-001 use?"
	stale := "Before 2026, queue NEG-001 used pager-001-red for alerts."

	require.Zero(t, historicalSelectionAdjustment(query, stale, selectionCueFrame{}))
	require.Negative(t, historicalSelectionAdjustment(query, stale, selectionCueFrame{hasDirectiveMatch: true}))
}

func TestRecallCueAdjustmentRequiresQueryIdentifiersForBoosts(t *testing.T) {
	query := "Which endpoint should the west-030 region use for billing sync?"
	neighborTemplate := "Export routing rule. Enterprise tenants such as tenant CND-030 enterprise must use endpoint-enterprise-030."

	require.Zero(t, cueAdjustment(query, neighborTemplate))
}

func TestRecallService_AuthorityRerankPrefersSignedRunbook(t *testing.T) {
	query := "Which procedure does runbook AUT-061 require?"
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:      "f-chat",
				Type:    "fragment",
				Content: "An informal chat suggested procedure-061-chat for runbook AUT-061, but it was not approved.",
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-required",
				Content:    "Authoritative runbook signed by operations. runbook AUT-061 requires procedure-061-canonical.",
			},
			{
				FragmentID: "f-chat",
				Content:    "An informal chat suggested procedure-061-chat for runbook AUT-061, but it was not approved.",
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-required", out[0].Fragment.FragmentID)
}

func TestAuthorityAdjustmentRequiresQueryIdentifiersForBoosts(t *testing.T) {
	query := "Which procedure does runbook AUT-061 require?"
	neighbor := "Authoritative runbook signed by operations. runbook AUT-062 requires procedure-062-canonical."

	require.Zero(t, authorityAdjustment(query, neighbor))
}

func TestRecallService_CommunityExpansionDisabledByDefault(t *testing.T) {
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "f-direct", Type: "fragment"}}}
	kw := &fakeKeywordSearcher{}
	expander := &fakeCommunityExpander{
		expansion: CommunityExpansion{
			SelectedCommunities: 1,
			Fragments: []CommunityFragmentRecallResult{
				{FragmentID: "f-community", ProfileID: "pA", Score: 0.8},
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		&fakeHydrator{},
		nil,
		nil,
		nil,
		nil,
		0,
		nil,
		nil,
		WithCommunityExpander(expander),
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 5})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "f-direct", out[0].Fragment.FragmentID)
	require.Equal(t, 0, expander.calls)
}

func TestRecallService_CommunityExpansionNoCommunityFallback(t *testing.T) {
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "f-direct", Type: "fragment"}}}
	kw := &fakeKeywordSearcher{}
	expander := &fakeCommunityExpander{}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		&fakeHydrator{},
		nil,
		nil,
		nil,
		nil,
		0,
		nil,
		nil,
		WithCommunityExpander(expander),
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 5, UseCommunities: true})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "f-direct", out[0].Fragment.FragmentID)
	require.Equal(t, 1, expander.calls)
	require.Equal(t, "pA", expander.lastProfile)
}

func TestRecallService_CommunityExpansionFiltersProfile(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	expander := &fakeCommunityExpander{
		expansion: CommunityExpansion{
			SelectedCommunities: 1,
			Facts: []FactRecallResult{
				{FactID: "fact-other", ProfileID: "pB", RecordedAt: now},
				{FactID: "fact-own", ProfileID: "pA", RecordedAt: now},
			},
			Fragments: []CommunityFragmentRecallResult{
				{FragmentID: "frag-other", ProfileID: "pB", Score: 0.5},
				{FragmentID: "frag-own", ProfileID: "pA", Score: 0.4},
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{frags: map[string]*domain.Fragment{
			"frag-other": {FragmentID: "frag-other", ProfileID: "pB"},
			"frag-own":   {FragmentID: "frag-own", ProfileID: "pA"},
		}},
		nil,
		&fakeFactGetter{facts: map[string]*domain.Fact{
			"fact-other": {FactID: "fact-other", ProfileID: "pB", Status: domain.FactStatusActive, RecordedAt: now, TruthScore: 1},
			"fact-own":   {FactID: "fact-own", ProfileID: "pA", Status: domain.FactStatusActive, RecordedAt: now, TruthScore: 0.9},
		}},
		nil,
		nil,
		0,
		nil,
		nil,
		WithCommunityExpander(expander),
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 5, UseCommunities: true})
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-own", out[0].Fact.FactID)
	require.NotNil(t, out[1].Fragment)
	require.Equal(t, "frag-own", out[1].Fragment.FragmentID)
}

func TestRecallService_CommunityExpansionFillsUnusedSlotsAfterDirectRecall(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "f-direct", Type: "fragment"}}}
	expander := &fakeCommunityExpander{
		expansion: CommunityExpansion{
			SelectedCommunities: 1,
			Facts: []FactRecallResult{
				{FactID: "fact-community", ProfileID: "pA", RecordedAt: now},
			},
			Claims: []ClaimRecallResult{
				{ClaimID: "claim-community", ProfileID: "pA", RecordedAt: now},
			},
			Fragments: []CommunityFragmentRecallResult{
				{FragmentID: "fragment-community", ProfileID: "pA", Score: 0.7},
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		&fakeKeywordSearcher{},
		&fakeHydrator{frags: map[string]*domain.Fragment{
			"f-direct":           {FragmentID: "f-direct", ProfileID: "pA"},
			"fragment-community": {FragmentID: "fragment-community", ProfileID: "pA"},
		}},
		nil,
		&fakeFactGetter{facts: map[string]*domain.Fact{
			"fact-community": {FactID: "fact-community", ProfileID: "pA", Status: domain.FactStatusActive, RecordedAt: now, TruthScore: 0.9},
		}},
		nil,
		&fakeClaimGetter{claims: map[string]*domain.Claim{
			"claim-community": {ClaimID: "claim-community", ProfileID: "pA", Status: domain.StatusValidated, RecordedAt: now, ExtractConf: 0.8},
		}},
		0,
		nil,
		nil,
		WithCommunityExpander(expander),
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3, UseCommunities: true})
	require.NoError(t, err)
	require.Len(t, out, 3)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-community", out[0].Fact.FactID)
	require.NotNil(t, out[1].Claim)
	require.Equal(t, "claim-community", out[1].Claim.ClaimID)
	require.NotNil(t, out[2].Fragment)
	require.Equal(t, "f-direct", out[2].Fragment.FragmentID)
	require.Equal(t, DefaultCommunityExpansionCommunityLimit, expander.lastOptions.CommunityLimit)
	require.Equal(t, DefaultCommunityExpansionMembersPerCommunity, expander.lastOptions.MembersPerCommunity)
	require.Equal(t, DefaultCommunityExpansionCommunityLimit*DefaultCommunityExpansionMembersPerCommunity, expander.lastOptions.MaxCandidates)
}

func idsOf(hits []RecallHit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.Fragment.FragmentID
	}
	return ids
}

// TestRecallService_TruncatesToLimit enforces final-cap behavior (AC-40).
func TestRecallService_TruncatesToLimit(t *testing.T) {
	sem := &fakeSemanticSearcher{}
	for i := 0; i < 20; i++ {
		sem.hits = append(sem.hits, semanticsearch.SearchHit{ID: "f" + itoa(i), Type: "fragment"})
	}
	kw := &fakeKeywordSearcher{}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 5})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(out) != 5 {
		t.Fatalf("len(out) = %d; want 5 (capped by limit)", len(out))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	n := i
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
