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
	frags     map[string]*domain.Fragment
	callCount int32
	missIDs   map[string]bool
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

type fakeFactSearcher struct {
	results   []FactRecallResult
	lastQuery string
	lastLimit int
}

func (f *fakeFactSearcher) SearchActive(ctx context.Context, profileID string, query string, limit int) ([]FactRecallResult, error) {
	f.lastQuery = query
	f.lastLimit = limit
	out := make([]FactRecallResult, len(f.results))
	copy(out, f.results)
	return out, nil
}

type fakeClaimSearcher struct {
	results   []ClaimRecallResult
	lastQuery string
	lastLimit int
}

func (f *fakeClaimSearcher) SearchValidated(ctx context.Context, profileID string, query string, limit int) ([]ClaimRecallResult, error) {
	f.lastQuery = query
	f.lastLimit = limit
	out := make([]ClaimRecallResult, len(f.results))
	copy(out, f.results)
	return out, nil
}

type fakeFactGetter struct {
	facts map[string]*domain.Fact
}

func (f *fakeFactGetter) Get(ctx context.Context, profileID string, factID string) (*domain.Fact, error) {
	if fact, ok := f.facts[factID]; ok {
		return fact, nil
	}
	return nil, errors.New("fact not found")
}

type fakeClaimGetter struct {
	claims map[string]*domain.Claim
}

func (f *fakeClaimGetter) Get(ctx context.Context, profileID string, claimID string) (*domain.Claim, error) {
	if claim, ok := f.claims[claimID]; ok {
		return claim, nil
	}
	return nil, errors.New("claim not found")
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
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

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
