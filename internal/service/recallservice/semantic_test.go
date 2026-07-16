package recallservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type stubSemanticRecallStore struct {
	lexicalCalls   int
	vectorCalls    int
	adjacencyCalls int
	hydrateCalls   int

	lexicalScope domain.SemanticRecallSearchScope
	vectorScope  domain.SemanticRecallSearchScope
	adjScope     domain.SemanticRecallSearchScope
	hydrateScope domain.SemanticRecallSearchScope
	lastSeeds    []domain.SemanticRecallEntitySeed
	hydrateIDs   []string
	preferredIDs []string

	lexicalBatch        domain.SemanticRecallCandidateBatch
	vectorBatch         domain.SemanticRecallCandidateBatch
	adjacencyCandidates []domain.SemanticRecallCandidate
	hydrated            []domain.SemanticRecallResult
	err                 error
}

func newStubSemanticRecallStore() *stubSemanticRecallStore {
	return &stubSemanticRecallStore{
		lexicalBatch: domain.SemanticRecallCandidateBatch{
			Candidates: []domain.SemanticRecallCandidate{{
				EvidenceID:      "frag-1",
				Branch:          domain.SemanticRecallBranchEvidenceText,
				Rank:            1,
				PreciseMatch:    true,
				RelationshipIDs: []string{"rel-1"},
			}},
			EntitySeeds: []domain.SemanticRecallEntitySeed{{
				EntityID: "6d89fe01-7b0d-4fc0-8c28-f5cc89b8bc9c",
				Rank:     1,
				Exact:    true,
				Score:    1,
			}},
		},
		hydrated: []domain.SemanticRecallResult{{
			Evidence: &domain.SemanticEvidenceFragment{
				FragmentID: "frag-1",
				Content:    "Dense-Mem uses Postgres.",
			},
			Relationships: []domain.SemanticRelationship{{
				RelationshipID:    "rel-1",
				Tier:              domain.SemanticTierValidatedClaim,
				Status:            domain.SemanticStatusActive,
				Predicate:         "uses",
				SubjectEntityID:   "subject-1",
				SubjectEntityName: "Dense-Mem",
			}},
			Supports: []domain.SemanticRelationshipSupport{{
				RelationshipID: "rel-1",
				FragmentID:     "frag-1",
			}},
		}},
	}
}

func (s *stubSemanticRecallStore) SearchRecallLexicalCandidates(_ context.Context, scope domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error) {
	s.lexicalCalls++
	s.lexicalScope = scope
	if s.err != nil {
		return domain.SemanticRecallCandidateBatch{}, s.err
	}
	return s.lexicalBatch, nil
}

func (s *stubSemanticRecallStore) SearchRecallVectorCandidates(_ context.Context, scope domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error) {
	s.vectorCalls++
	s.vectorScope = scope
	if s.err != nil {
		return domain.SemanticRecallCandidateBatch{}, s.err
	}
	return s.vectorBatch, nil
}

func (s *stubSemanticRecallStore) SearchRecallAdjacencyCandidates(_ context.Context, scope domain.SemanticRecallSearchScope, seeds []domain.SemanticRecallEntitySeed) ([]domain.SemanticRecallCandidate, error) {
	s.adjacencyCalls++
	s.adjScope = scope
	s.lastSeeds = append([]domain.SemanticRecallEntitySeed(nil), seeds...)
	if s.err != nil {
		return nil, s.err
	}
	return s.adjacencyCandidates, nil
}

func (s *stubSemanticRecallStore) HydrateRecallEvidence(_ context.Context, scope domain.SemanticRecallSearchScope, evidenceIDs, preferredRelationshipIDs []string) ([]domain.SemanticRecallResult, error) {
	s.hydrateCalls++
	s.hydrateScope = scope
	s.hydrateIDs = append([]string(nil), evidenceIDs...)
	s.preferredIDs = append([]string(nil), preferredRelationshipIDs...)
	if s.err != nil {
		return nil, s.err
	}
	return s.hydrated, nil
}

type stubSemanticRecallEmbedder struct {
	calls int
	err   error
}

func (s *stubSemanticRecallEmbedder) Embed(context.Context, string) ([]float32, string, error) {
	s.calls++
	if s.err != nil {
		return nil, "", s.err
	}
	return []float32{0.1, 0.2, 0.3}, "stub-embedding", nil
}

func TestSemanticRecallNormalizesFollowUpIDs(t *testing.T) {
	store := newStubSemanticRecallStore()
	svc := NewSemanticRecallService(store)
	relationshipID := "7d89fe01-7b0d-4fc0-8c28-f5cc89b8bc9c"
	entityID := "6d89fe01-7b0d-4fc0-8c28-f5cc89b8bc9c"
	evidenceID := "5d89fe01-7b0d-4fc0-8c28-f5cc89b8bc9c"

	_, err := svc.Recall(context.Background(), "team-1", RecallRequest{
		Query:                "Postgres",
		KnownEvidenceIDs:     []string{" " + evidenceID + " ", evidenceID},
		KnownRelationshipIDs: []string{" " + relationshipID + " ", relationshipID},
		ExpandFromEntityIDs:  []string{" " + entityID + " ", entityID},
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(store.hydrateScope.KnownRelationshipIDs) != 1 || store.hydrateScope.KnownRelationshipIDs[0] != relationshipID {
		t.Fatalf("known relationship ids = %#v, want [%s]", store.hydrateScope.KnownRelationshipIDs, relationshipID)
	}
	if len(store.hydrateScope.KnownEvidenceIDs) != 1 || store.hydrateScope.KnownEvidenceIDs[0] != evidenceID {
		t.Fatalf("known evidence ids = %#v, want [%s]", store.hydrateScope.KnownEvidenceIDs, evidenceID)
	}
	if len(store.hydrateScope.ExpandFromEntityIDs) != 1 || store.hydrateScope.ExpandFromEntityIDs[0] != entityID {
		t.Fatalf("expand ids = %#v, want [%s]", store.hydrateScope.ExpandFromEntityIDs, entityID)
	}
}

func TestSemanticRecallRejectsInvalidOrOversizedFollowUpIDs(t *testing.T) {
	svc := NewSemanticRecallService(newStubSemanticRecallStore())
	if _, err := svc.Recall(context.Background(), "team-1", RecallRequest{
		Query:                "Postgres",
		KnownRelationshipIDs: []string{"not-a-relationship-id"},
	}); err == nil || !strings.Contains(err.Error(), "known_relationship_ids contains an invalid uuid") {
		t.Fatalf("invalid known_relationship_ids error = %v", err)
	}

	values := make([]string, MaxKnownRelationshipIDs+1)
	for i := range values {
		values[i] = "7d89fe01-7b0d-4fc0-8c28-f5cc89b8bc9c"
	}
	if _, err := svc.Recall(context.Background(), "team-1", RecallRequest{
		Query:                "Postgres",
		KnownRelationshipIDs: values,
	}); err == nil || !strings.Contains(err.Error(), "exceeds 200") {
		t.Fatalf("oversized known_relationship_ids error = %v", err)
	}

	if _, err := svc.Recall(context.Background(), "team-1", RecallRequest{
		Query:            "Postgres",
		KnownEvidenceIDs: []string{"not-an-evidence-id"},
	}); err == nil || !strings.Contains(err.Error(), "known_evidence_ids contains an invalid uuid") {
		t.Fatalf("invalid known_evidence_ids error = %v", err)
	}
}

func TestSemanticRecallReturnsHydratedEvidence(t *testing.T) {
	store := newStubSemanticRecallStore()
	svc := NewSemanticRecallService(store)

	hits, err := svc.Recall(context.Background(), "team-1", RecallRequest{Query: "Postgres"})

	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if store.lexicalCalls != 1 || store.vectorCalls != 0 || store.adjacencyCalls != 1 || store.hydrateCalls != 1 {
		t.Fatalf("calls lexical=%d vector=%d adjacency=%d hydrate=%d, want 1/0/1/1", store.lexicalCalls, store.vectorCalls, store.adjacencyCalls, store.hydrateCalls)
	}
	if len(hits) != 1 || hits[0].Evidence == nil || hits[0].Evidence.FragmentID != "frag-1" {
		t.Fatalf("hits = %#v, want evidence hit", hits)
	}
	if len(hits[0].Relationships) != 1 || hits[0].Relationships[0].RelationshipID != "rel-1" {
		t.Fatalf("relationships = %#v, want rel-1 path hint", hits[0].Relationships)
	}
}

func TestSemanticRecallIgnoresLegacyIncludeEvidence(t *testing.T) {
	for _, req := range []RecallRequest{
		{Query: "Postgres", IncludeEvidence: true},
	} {
		store := newStubSemanticRecallStore()
		svc := NewSemanticRecallService(store)

		hits, err := svc.Recall(context.Background(), "team-1", req)

		if err != nil {
			t.Fatalf("Recall() error = %v", err)
		}
		if store.hydrateCalls != 1 || len(hits) != 1 || hits[0].Evidence == nil {
			t.Fatalf("hydrateCalls=%d hits=%#v, want one evidence result", store.hydrateCalls, hits)
		}
	}
}

func TestSemanticRecallEmbedsQueryWhenConfigured(t *testing.T) {
	store := newStubSemanticRecallStore()
	store.vectorBatch = domain.SemanticRecallCandidateBatch{Candidates: []domain.SemanticRecallCandidate{{
		EvidenceID:      "frag-1",
		Branch:          domain.SemanticRecallBranchEvidenceVector,
		Rank:            1,
		RelationshipIDs: []string{"rel-1"},
	}}}
	embedder := &stubSemanticRecallEmbedder{}
	svc := NewSemanticRecallService(store, embedder)

	_, err := svc.Recall(context.Background(), "team-1", RecallRequest{Query: "Postgres"})

	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if embedder.calls != 1 || store.vectorCalls != 1 {
		t.Fatalf("embedder/vector calls = %d/%d, want 1/1", embedder.calls, store.vectorCalls)
	}
	if got, want := store.vectorScope.Embedding, []float32{0.1, 0.2, 0.3}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("query embedding = %#v, want %#v", got, want)
	}
	if store.vectorScope.EmbeddingContractID != "stub-embedding:3:semantic_search_document_v1" {
		t.Fatalf("embedding contract = %q", store.vectorScope.EmbeddingContractID)
	}
}

func TestSemanticRecallBranchPriorityCanPreferVectorRank(t *testing.T) {
	store := newStubSemanticRecallStore()
	store.lexicalBatch = domain.SemanticRecallCandidateBatch{Candidates: []domain.SemanticRecallCandidate{
		{EvidenceID: "lexical-near-miss", Branch: domain.SemanticRecallBranchEvidenceText, Rank: 1},
		{EvidenceID: "required", Branch: domain.SemanticRecallBranchEvidenceText, Rank: 30},
	}}
	store.vectorBatch = domain.SemanticRecallCandidateBatch{Candidates: []domain.SemanticRecallCandidate{
		{EvidenceID: "required", Branch: domain.SemanticRecallBranchEvidenceVector, Rank: 1},
		{EvidenceID: "lexical-near-miss", Branch: domain.SemanticRecallBranchEvidenceVector, Rank: 14},
	}}
	store.hydrated = []domain.SemanticRecallResult{
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "required", Content: "required"}},
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "lexical-near-miss", Content: "near miss"}},
	}
	ranking := DefaultSemanticRecallRankingProfile()
	ranking.FusionMode = SemanticRecallFusionBranchPriority
	ranking.BranchPriority = []domain.SemanticRecallBranch{
		domain.SemanticRecallBranchExact,
		domain.SemanticRecallBranchEvidenceVector,
		domain.SemanticRecallBranchEvidenceText,
	}
	svc := NewSemanticRecallServiceWithRanking(store, ranking, &stubSemanticRecallEmbedder{})

	hits, err := svc.Recall(context.Background(), "team-1", RecallRequest{Query: "semantic query", Limit: 2})

	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 2 || hits[0].Evidence == nil || hits[0].Evidence.FragmentID != "required" {
		t.Fatalf("hits = %#v, want vector rank-1 evidence first", hits)
	}
}

func TestNewSemanticRecallRankingProfileParsesConfig(t *testing.T) {
	profile, err := NewSemanticRecallRankingProfile(false, 25, "exact=3,evidence_text=1,evidence_vector=4", "exact,evidence_vector,evidence_text", 8, 80, 240)
	if err != nil {
		t.Fatalf("NewSemanticRecallRankingProfile() error = %v", err)
	}
	if profile.FusionMode != SemanticRecallFusionBranchPriority {
		t.Fatalf("FusionMode = %q, want branch_priority", profile.FusionMode)
	}
	if profile.RRFK != 25 || profile.BranchLimitMultiplier != 8 || profile.BranchLimitFloor != 80 || profile.BranchLimitMax != 240 {
		t.Fatalf("profile limits = %#v", profile)
	}
	if profile.BranchWeights[domain.SemanticRecallBranchEvidenceVector] != 4 {
		t.Fatalf("vector weight = %v, want 4", profile.BranchWeights[domain.SemanticRecallBranchEvidenceVector])
	}
	if got := profile.BranchPriority[1]; got != domain.SemanticRecallBranchEvidenceVector {
		t.Fatalf("priority[1] = %q, want evidence_vector", got)
	}
}

func TestNewSemanticRecallRankingProfileRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name           string
		branchWeights  string
		branchPriority string
	}{
		{name: "weight missing equals", branchWeights: "evidence_text"},
		{name: "weight unknown branch", branchWeights: "unknown=1"},
		{name: "weight not positive", branchWeights: "evidence_text=0"},
		{name: "priority unknown branch", branchPriority: "unknown"},
		{name: "priority duplicate branch", branchPriority: "exact,exact"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSemanticRecallRankingProfile(true, 10, tt.branchWeights, tt.branchPriority, 4, 40, 80)
			if err == nil {
				t.Fatal("NewSemanticRecallRankingProfile() error = nil, want invalid config error")
			}
		})
	}
}

func TestSemanticRecallDeduplicatesByBranchAndEvidenceBeforeHydration(t *testing.T) {
	store := newStubSemanticRecallStore()
	store.lexicalBatch = domain.SemanticRecallCandidateBatch{Candidates: []domain.SemanticRecallCandidate{
		{EvidenceID: "frag-1", Branch: domain.SemanticRecallBranchEvidenceText, Rank: 1, RelationshipIDs: []string{"rel-1"}},
		{EvidenceID: "frag-1", Branch: domain.SemanticRecallBranchEvidenceText, Rank: 2, RelationshipIDs: []string{"rel-2"}},
		{EvidenceID: "frag-2", Branch: domain.SemanticRecallBranchEvidenceText, Rank: 3, RelationshipIDs: []string{"rel-3"}},
	}}
	store.hydrated = []domain.SemanticRecallResult{
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "frag-1", Content: "first"}},
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "frag-2", Content: "second"}},
	}
	svc := NewSemanticRecallService(store)

	hits, err := svc.Recall(context.Background(), "team-1", RecallRequest{Query: "Postgres", Limit: 2})

	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %#v, want two hydrated results", hits)
	}
	if len(store.preferredIDs) != 2 || store.preferredIDs[0] != "rel-1" || store.preferredIDs[1] != "rel-3" {
		t.Fatalf("preferred relationship ids = %#v, want best per branch/evidence only", store.preferredIDs)
	}
}

func TestSemanticRecallKeepsAdjacencyOutOfInitialRanking(t *testing.T) {
	store := newStubSemanticRecallStore()
	store.lexicalBatch = domain.SemanticRecallCandidateBatch{
		Candidates: []domain.SemanticRecallCandidate{{
			EvidenceID: "direct",
			Branch:     domain.SemanticRecallBranchEvidenceText,
			Rank:       10,
		}},
		EntitySeeds: []domain.SemanticRecallEntitySeed{{
			EntityID: "6d89fe01-7b0d-4fc0-8c28-f5cc89b8bc9c",
			Rank:     1,
			Exact:    true,
		}},
	}
	store.adjacencyCandidates = []domain.SemanticRecallCandidate{{
		EvidenceID:      "frontier",
		Branch:          domain.SemanticRecallBranchAdjacency,
		Rank:            1,
		RelationshipIDs: []string{"rel-frontier"},
	}}
	store.hydrated = []domain.SemanticRecallResult{
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "direct", Content: "Direct evidence"}},
		{
			Evidence: &domain.SemanticEvidenceFragment{FragmentID: "frontier", Content: "Frontier evidence"},
			Relationships: []domain.SemanticRelationship{{
				RelationshipID: "rel-frontier",
				Predicate:      "uses",
			}},
			Supports: []domain.SemanticRelationshipSupport{{
				RelationshipID: "rel-frontier",
				FragmentID:     "frontier",
			}},
		},
	}
	svc := NewSemanticRecallService(store)

	hits, err := svc.Recall(context.Background(), "team-1", RecallRequest{Query: "Postgres", Limit: 1})

	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %#v, want direct result plus discovery-only hit", hits)
	}
	if hits[0].Evidence == nil || hits[0].Evidence.FragmentID != "direct" {
		t.Fatalf("first hit = %#v, want direct evidence", hits[0])
	}
	if hits[1].Evidence != nil || len(hits[1].Relationships) != 1 || hits[1].Relationships[0].RelationshipID != "rel-frontier" {
		t.Fatalf("second hit = %#v, want discovery-only frontier relationship", hits[1])
	}
	if strings.Join(store.hydrateIDs, ",") != "direct,frontier" {
		t.Fatalf("hydrate ids = %#v, want direct and frontier", store.hydrateIDs)
	}
}

func TestSemanticRecallHardAnchorMatchControlsInitialRank(t *testing.T) {
	store := newStubSemanticRecallStore()
	store.lexicalBatch = domain.SemanticRecallCandidateBatch{Candidates: []domain.SemanticRecallCandidate{
		{EvidenceID: "frag-no-anchor", Branch: domain.SemanticRecallBranchEvidenceText, Rank: 1},
		{EvidenceID: "frag-anchor", Branch: domain.SemanticRecallBranchEvidenceText, Rank: 20, AllHardAnchorsMatched: true},
	}}
	store.hydrated = []domain.SemanticRecallResult{
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "frag-anchor", Content: "case-123 matched"}},
		{Evidence: &domain.SemanticEvidenceFragment{FragmentID: "frag-no-anchor", Content: "generic"}},
	}
	svc := NewSemanticRecallService(store)

	hits, err := svc.Recall(context.Background(), "team-1", RecallRequest{Query: "case-123 status", Limit: 1})

	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Evidence.FragmentID != "frag-anchor" {
		t.Fatalf("hits = %#v, want hard-anchor evidence first", hits)
	}
}

func TestSemanticRecallForwardsTemporalScope(t *testing.T) {
	store := newStubSemanticRecallStore()
	validAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	knownAt := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	svc := NewSemanticRecallService(store)

	_, err := svc.Recall(context.Background(), "team-1", RecallRequest{
		Query:   "Postgres",
		ValidAt: &validAt,
		KnownAt: &knownAt,
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if !store.hydrateScope.ValidAt.Equal(validAt) || !store.hydrateScope.KnownAt.Equal(knownAt) {
		t.Fatalf("temporal scope = %v/%v", store.hydrateScope.ValidAt, store.hydrateScope.KnownAt)
	}
}

func TestSemanticRecallValidatesInputAndPropagatesErrors(t *testing.T) {
	svc := NewSemanticRecallService(newStubSemanticRecallStore())
	if _, err := svc.Recall(context.Background(), "", RecallRequest{Query: "q"}); err == nil {
		t.Fatal("Recall() missing team error = nil")
	}
	if _, err := svc.Recall(context.Background(), "team", RecallRequest{}); err == nil {
		t.Fatal("Recall() missing query error = nil")
	}

	want := errors.New("store failed")
	svc = NewSemanticRecallService(&stubSemanticRecallStore{err: want})
	if _, err := svc.Recall(context.Background(), "team", RecallRequest{Query: "q"}); !errors.Is(err, want) {
		t.Fatalf("Recall() error = %v, want %v", err, want)
	}

	embedderErr := errors.New("embedding secret detail")
	svc = NewSemanticRecallService(newStubSemanticRecallStore(), &stubSemanticRecallEmbedder{err: embedderErr})
	if _, err := svc.Recall(context.Background(), "team", RecallRequest{Query: "q"}); err == nil || err.Error() != "recall: embedding unavailable" {
		t.Fatalf("embedding error = %v, want sanitized error", err)
	}
}

func TestSemanticRecallQueryFeaturesAndBounds(t *testing.T) {
	id := uuid.NewString()
	features := parseSemanticRecallQuery(`What is the latest status for "Project Atlas" and case-123 around ` + id + `?`)

	if !features.TemporalIntent || !features.CurrentnessIntent {
		t.Fatalf("intent = temporal:%v current:%v, want both true", features.TemporalIntent, features.CurrentnessIntent)
	}
	if !containsString(features.EntityPhrases, "Project Atlas") || !containsString(features.EntityPhrases, "latest status") {
		t.Fatalf("entity phrases = %#v, want quoted phrase and adjacent term phrase", features.EntityPhrases)
	}
	if !containsString(features.HardAnchors, "case-123") || !containsString(features.HardAnchors, id) {
		t.Fatalf("hard anchors = %#v, want external id and uuid", features.HardAnchors)
	}
	if features.RelaxedQuery == "" || strings.Contains(features.RelaxedQuery, "What") {
		t.Fatalf("relaxed query = %q, want stop-word-stripped query", features.RelaxedQuery)
	}

	profile := NormalizeSemanticRecallRankingProfile(SemanticRecallRankingProfile{
		FusionMode:            "invalid",
		RRFK:                  -1,
		BranchLimitMultiplier: -1,
		BranchLimitFloor:      40,
		BranchLimitMax:        20,
		BranchWeights: map[domain.SemanticRecallBranch]float64{
			domain.SemanticRecallBranchAdjacency:      9,
			domain.SemanticRecallBranchEvidenceVector: 3,
		},
		BranchPriority: []domain.SemanticRecallBranch{
			domain.SemanticRecallBranchAdjacency,
			domain.SemanticRecallBranchEvidenceVector,
		},
	})
	if profile.FusionMode != SemanticRecallFusionBranchPriority || profile.RRFK != RRFConstant {
		t.Fatalf("normalized profile = %#v, want defaults for invalid fusion and rrf k", profile)
	}
	if profile.BranchLimitMax != profile.BranchLimitFloor {
		t.Fatalf("normalized branch limits = %d/%d, want max raised to floor", profile.BranchLimitFloor, profile.BranchLimitMax)
	}
	if profile.BranchWeights[domain.SemanticRecallBranchAdjacency] != 0 {
		t.Fatalf("adjacency weight = %v, want ignored non-initial branch", profile.BranchWeights[domain.SemanticRecallBranchAdjacency])
	}
	if got := semanticRecallBranchLimit(3, profile); got != profile.BranchLimitFloor {
		t.Fatalf("branch limit = %d, want floor %d", got, profile.BranchLimitFloor)
	}
	if got := semanticRecallEmbeddingContractID(" text-embedding ", 1536); got != "text-embedding:1536:semantic_search_document_v1" {
		t.Fatalf("embedding contract = %q", got)
	}
	if got := semanticRecallEmbeddingContractID("", 1536); got != "" {
		t.Fatalf("empty model contract = %q, want empty", got)
	}

	teamID := uuid.New()
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{TeamID: teamID})
	if got := semanticRecallTeamID(ctx, "fallback"); got != teamID.String() {
		t.Fatalf("team id = %q, want actor team", got)
	}
	if got := semanticRecallTeamID(context.Background(), " fallback "); got != "fallback" {
		t.Fatalf("fallback team id = %q, want trimmed fallback", got)
	}
}

func TestSemanticRecallRRFAndSeedMerge(t *testing.T) {
	now := time.Now().UTC()
	validFrom := now.Add(-24 * time.Hour)
	ranking := DefaultSemanticRecallRankingProfile()
	ranking.FusionMode = SemanticRecallFusionRRF
	ranking.RRFK = 10
	candidates := []domain.SemanticRecallCandidate{
		{
			EvidenceID:              "weaker",
			Branch:                  domain.SemanticRecallBranchEvidenceText,
			Rank:                    1,
			RelationshipIDs:         []string{"rel-b"},
			LatestRecordedAt:        now.Add(-48 * time.Hour),
			IndependentSourceGroups: 1,
		},
		{
			EvidenceID:              "strong",
			Branch:                  domain.SemanticRecallBranchEvidenceVector,
			Rank:                    2,
			RawScore:                0.3,
			RelationshipIDs:         []string{"rel-a", "rel-a"},
			ExactMatch:              true,
			PreciseMatch:            true,
			PhraseMatch:             true,
			FactSupport:             true,
			AllHardAnchorsMatched:   true,
			IndependentSourceGroups: 5,
			LatestValidFrom:         &validFrom,
			LatestRecordedAt:        now,
		},
		{
			EvidenceID:      "strong",
			Branch:          domain.SemanticRecallBranchEvidenceVector,
			Rank:            3,
			RawScore:        0.1,
			RelationshipIDs: []string{"rel-lower-rank"},
		},
		{
			EvidenceID:      "known",
			Branch:          domain.SemanticRecallBranchEvidenceText,
			Rank:            1,
			RelationshipIDs: []string{"rel-known"},
		},
		{
			EvidenceID:      "frontier",
			Branch:          domain.SemanticRecallBranchAdjacency,
			Rank:            1,
			RelationshipIDs: []string{"rel-frontier"},
		},
	}
	fused := fuseSemanticRecallCandidates(candidates, domain.SemanticRecallQueryFeatures{
		HardAnchors:       []string{"case-123"},
		TemporalIntent:    true,
		CurrentnessIntent: true,
	}, []string{"known"}, 2, ranking)

	if len(fused) != 1 || fused[0].evidenceID != "strong" {
		t.Fatalf("fused = %#v, want only hard-anchor-matched strong evidence", fused)
	}
	if strings.Join(fused[0].relationshipIDs, ",") != "rel-a" {
		t.Fatalf("relationship ids = %#v, want deduplicated best candidate relationship", fused[0].relationshipIDs)
	}
	if !semanticRecallCandidateBetter(domain.SemanticRecallCandidate{Rank: 2}, domain.SemanticRecallCandidate{Rank: 0}) {
		t.Fatal("candidate with positive rank should beat unranked candidate")
	}
	if !semanticRecallCandidateBetter(domain.SemanticRecallCandidate{Rank: 2, RawScore: 0.8}, domain.SemanticRecallCandidate{Rank: 2, RawScore: 0.4}) {
		t.Fatal("candidate with higher raw score should win rank tie")
	}
	if !semanticRecallCandidateBetter(domain.SemanticRecallCandidate{Rank: 2, RelationshipIDs: []string{"a"}}, domain.SemanticRecallCandidate{Rank: 2, RelationshipIDs: []string{"b"}}) {
		t.Fatal("candidate with lexically smaller relationship ids should win full tie")
	}
	if weight := semanticRecallBranchWeight(domain.SemanticRecallBranchAdjacency, ranking); weight != 0 {
		t.Fatalf("adjacency branch weight = %v, want 0", weight)
	}
	if minFloat64(2, 1) != 1 || minFloat64(1, 2) != 1 {
		t.Fatal("minFloat64 returned incorrect minimum")
	}

	merged := mergeSemanticEntitySeeds(
		[]domain.SemanticRecallEntitySeed{{EntityID: "entity-b", Rank: 5, Score: 0.2}},
		[]domain.SemanticRecallEntitySeed{{EntityID: "entity-b", Rank: 2, Exact: true, Score: 0.8}},
		[]string{"entity-c", " ", "entity-a"},
	)
	if len(merged) != 3 {
		t.Fatalf("merged seeds = %#v, want three non-empty unique seeds", merged)
	}
	if merged[0].EntityID != "entity-c" || !merged[0].Explicit {
		t.Fatalf("first seed = %#v, want explicit exact entity-c with best explicit rank", merged[0])
	}
	if merged[2].EntityID != "entity-b" || !merged[2].Exact || merged[2].Rank != 2 || merged[2].Score != 0.8 {
		t.Fatalf("merged entity-b = %#v, want best rank/exact/score preserved", merged[2])
	}

	discovery := selectSemanticDiscoveryCandidates([]domain.SemanticRecallCandidate{
		{EvidenceID: " ", RelationshipIDs: []string{"skip-empty"}},
		{EvidenceID: "known", RelationshipIDs: []string{"skip-known"}},
		{EvidenceID: "next", RelationshipIDs: []string{"rel-1", "rel-1"}},
		{EvidenceID: "next", RelationshipIDs: []string{"rel-2"}},
		{EvidenceID: "later", RelationshipIDs: []string{"rel-3"}},
	}, []string{"known"}, 1)
	if len(discovery) != 1 || discovery[0].EvidenceID != "next" || strings.Join(discovery[0].RelationshipIDs, ",") != "rel-1" {
		t.Fatalf("discovery = %#v, want first non-known evidence with deduped relationships", discovery)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
