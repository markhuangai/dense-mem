package memoryservice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/verifier"
	"github.com/stretchr/testify/require"
)

type stubPlacementStore struct {
	created domain.MemoryPlacementRun
}

func (s *stubPlacementStore) CreateRun(_ context.Context, run domain.MemoryPlacementRun) error {
	s.created = run
	return nil
}

func (s *stubPlacementStore) GetRun(_ context.Context, _, _ string) (*domain.MemoryPlacementRun, error) {
	return nil, nil
}

func (s *stubPlacementStore) ClaimNextQueuedRun(context.Context) (*domain.MemoryPlacementRun, error) {
	return nil, nil
}

func (s *stubPlacementStore) SaveRun(_ context.Context, run domain.MemoryPlacementRun) error {
	s.created = run
	return nil
}

func (s *stubPlacementStore) CreateDispute(context.Context, domain.MemoryDisputeSession) error {
	return nil
}

func (s *stubPlacementStore) GetDispute(context.Context, string, string) (*domain.MemoryDisputeSession, error) {
	return nil, nil
}

func (s *stubPlacementStore) SaveDispute(context.Context, domain.MemoryDisputeSession) error {
	return nil
}

func (s *stubPlacementStore) CreateDisputeAndSaveRun(_ context.Context, _ domain.MemoryDisputeSession, run domain.MemoryPlacementRun) error {
	s.created = run
	return nil
}

func (s *stubPlacementStore) UpdateDisputeWithRun(context.Context, string, string, repository.DisputeRunUpdate) (*domain.MemoryDisputeSession, *domain.MemoryPlacementRun, error) {
	return nil, nil, nil
}

type stubSemanticStore struct {
	inputs []repository.SemanticRememberInput
	err    error
}

func (s *stubSemanticStore) StoreRemember(_ context.Context, input repository.SemanticRememberInput) (*repository.SemanticRememberResult, error) {
	s.inputs = append(s.inputs, input)
	if s.err != nil {
		return nil, s.err
	}
	result := &repository.SemanticRememberResult{
		Evidence: make([]domain.SemanticEvidenceFragment, 0, len(input.Evidence)),
	}
	for i := range input.Evidence {
		result.Evidence = append(result.Evidence, domain.SemanticEvidenceFragment{
			TeamID:     input.TeamID,
			FragmentID: uuid.NewString(),
			Content:    input.Evidence[i].Content,
			CreatedAt:  time.Now().UTC(),
		})
	}
	for i := range input.Relationships {
		if input.Relationships[i].ObservationOnly {
			continue
		}
		result.Relationships = append(result.Relationships, domain.SemanticRelationship{
			TeamID:         input.TeamID,
			RelationshipID: uuid.NewString(),
			Tier:           input.Relationships[i].Tier,
			Status:         input.Relationships[i].Status,
			Predicate:      input.Relationships[i].Predicate,
		})
		result.RelationshipInputIndexes = append(result.RelationshipInputIndexes, i)
	}
	return result, nil
}

func (s *stubSemanticStore) LoadSemanticVerifierContext(context.Context, string, []repository.SemanticRelationshipInput) (repository.SemanticVerifierContext, error) {
	return repository.SemanticVerifierContext{}, nil
}

func (s *stubSemanticStore) TraceRelationship(context.Context, string, string) (*domain.SemanticTraceResult, error) {
	return nil, nil
}

func (s *stubSemanticStore) SearchRecallLexicalCandidates(context.Context, domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error) {
	return domain.SemanticRecallCandidateBatch{}, nil
}

func (s *stubSemanticStore) SearchRecallVectorCandidates(context.Context, domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error) {
	return domain.SemanticRecallCandidateBatch{}, nil
}

func (s *stubSemanticStore) SearchRecallAdjacencyCandidates(context.Context, domain.SemanticRecallSearchScope, []domain.SemanticRecallEntitySeed) ([]domain.SemanticRecallCandidate, error) {
	return nil, nil
}

func (s *stubSemanticStore) HydrateRecallEvidence(context.Context, domain.SemanticRecallSearchScope, []string, []string) ([]domain.SemanticRecallResult, error) {
	return nil, nil
}

type stubSemanticReviewer struct {
	result semanticReviewResult
	err    error
}

func (s *stubSemanticReviewer) ReviewSemantic(context.Context, semanticReviewRequest) (semanticReviewResult, error) {
	if s.err != nil {
		return semanticReviewResult{}, s.err
	}
	if len(s.result.Relationships) > 0 {
		return s.result, nil
	}
	return semanticReviewResult{
		Model: "stub-semantic-reviewer",
		Relationships: []repository.SemanticRelationshipInput{{
			SubjectName:   "Dense-Mem",
			SubjectKind:   domain.SemanticEntityProject,
			Predicate:     "uses",
			Polarity:      domain.PolarityPlus,
			ObjectName:    "Postgres",
			ObjectKind:    domain.SemanticEntityConcept,
			EvidenceIndex: 0,
			Quote:         "Dense-Mem uses Postgres.",
			SpanStart:     0,
			SpanEnd:       len("Dense-Mem uses Postgres."),
			Tier:          domain.SemanticTierCandidate,
			Status:        domain.SemanticStatusActive,
			Confidence:    0.7,
		}},
	}, nil
}

type statefulPlacementStore struct {
	mu           sync.Mutex
	run          domain.MemoryPlacementRun
	dispute      domain.MemoryDisputeSession
	savedRun     domain.MemoryPlacementRun
	savedDispute domain.MemoryDisputeSession
}

func (s *statefulPlacementStore) CreateRun(_ context.Context, run domain.MemoryPlacementRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = clonePlacementRun(run)
	return nil
}

func (s *statefulPlacementStore) GetRun(_ context.Context, profileID, ingestID string) (*domain.MemoryPlacementRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.ProfileID != profileID || s.run.IngestID != ingestID {
		return nil, nil
	}
	run := clonePlacementRun(s.run)
	return &run, nil
}

func (s *statefulPlacementStore) ClaimNextQueuedRun(context.Context) (*domain.MemoryPlacementRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.Status != domain.MemoryPlacementQueued {
		return nil, nil
	}
	if !s.run.AvailableAt.IsZero() && s.run.AvailableAt.After(time.Now().UTC()) {
		return nil, nil
	}
	s.run.Status = domain.MemoryPlacementProcessing
	s.run.Attempts++
	now := time.Now().UTC()
	s.run.StartedAt = &now
	s.run.UpdatedAt = now
	run := clonePlacementRun(s.run)
	return &run, nil
}

func (s *statefulPlacementStore) SaveRun(_ context.Context, run domain.MemoryPlacementRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = clonePlacementRun(run)
	s.savedRun = clonePlacementRun(run)
	return nil
}

func (s *statefulPlacementStore) CreateDispute(_ context.Context, dispute domain.MemoryDisputeSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispute = cloneDisputeSession(dispute)
	return nil
}

func (s *statefulPlacementStore) GetDispute(_ context.Context, profileID, disputeID string) (*domain.MemoryDisputeSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dispute.ProfileID != profileID || s.dispute.DisputeID != disputeID {
		return nil, nil
	}
	dispute := cloneDisputeSession(s.dispute)
	return &dispute, nil
}

func (s *statefulPlacementStore) SaveDispute(_ context.Context, dispute domain.MemoryDisputeSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispute = cloneDisputeSession(dispute)
	s.savedDispute = cloneDisputeSession(dispute)
	return nil
}

func (s *statefulPlacementStore) CreateDisputeAndSaveRun(_ context.Context, dispute domain.MemoryDisputeSession, run domain.MemoryPlacementRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispute = cloneDisputeSession(dispute)
	s.savedDispute = cloneDisputeSession(dispute)
	s.run = clonePlacementRun(run)
	s.savedRun = clonePlacementRun(run)
	return nil
}

func (s *statefulPlacementStore) UpdateDisputeWithRun(_ context.Context, profileID, disputeID string, update repository.DisputeRunUpdate) (*domain.MemoryDisputeSession, *domain.MemoryPlacementRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dispute.ProfileID != profileID || s.dispute.DisputeID != disputeID {
		return nil, nil, nil
	}
	if s.run.ProfileID != profileID || s.run.IngestID != s.dispute.IngestID {
		session := cloneDisputeSession(s.dispute)
		return &session, nil, nil
	}
	session := cloneDisputeSession(s.dispute)
	run := clonePlacementRun(s.run)
	if update != nil {
		if err := update(&session, &run); err != nil {
			return nil, nil, err
		}
	}
	s.dispute = cloneDisputeSession(session)
	s.savedDispute = cloneDisputeSession(session)
	s.run = clonePlacementRun(run)
	s.savedRun = clonePlacementRun(run)
	savedSession := cloneDisputeSession(s.savedDispute)
	savedRun := clonePlacementRun(s.savedRun)
	return &savedSession, &savedRun, nil
}

func (s *statefulPlacementStore) savedRunCopy() domain.MemoryPlacementRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePlacementRun(s.savedRun)
}

func (s *statefulPlacementStore) savedDisputeCopy() domain.MemoryDisputeSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneDisputeSession(s.savedDispute)
}

func (s *statefulPlacementStore) disputeCreated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dispute.DisputeID != ""
}

func clonePlacementRun(run domain.MemoryPlacementRun) domain.MemoryPlacementRun {
	run.Evidence = append([]domain.MemoryEvidence(nil), run.Evidence...)
	run.Items = append([]domain.MemoryPlacementItem(nil), run.Items...)
	return run
}

func cloneDisputeSession(session domain.MemoryDisputeSession) domain.MemoryDisputeSession {
	session.Turns = append([]domain.MemoryDisputeTurn(nil), session.Turns...)
	for i := range session.Turns {
		session.Turns[i].Evidence = append([]domain.MemoryEvidence(nil), session.Turns[i].Evidence...)
	}
	return session
}

func TestRememberReturnsFragmentCreateError(t *testing.T) {
	store := &stubPlacementStore{}
	svc := New(Dependencies{
		FragmentCreate: &stubFragmentCreate{err: errors.New("embed failed")},
		PlacementStore: store,
	})

	_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
		Evidence: []EvidenceInput{{Content: "x"}},
	})

	require.ErrorContains(t, err, "embed failed")
	require.Equal(t, domain.MemoryPlacementFailed, store.created.Status)
	require.Equal(t, "embed failed", store.created.Error)
	require.Len(t, store.created.Items, 1)
	require.Equal(t, "failed", store.created.Items[0].Status)
	require.Equal(t, "embed failed", store.created.Items[0].Error)
}

func TestRememberStoresSemanticEvidenceWithActorScope(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	store := &stubPlacementStore{}
	semanticStore := &stubSemanticStore{}
	svc := New(Dependencies{
		PlacementStore: store,
		SemanticStore:  semanticStore,
	})
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID:      teamID,
		TeamName:    "Dense-Mem",
		ProfileID:   profileID,
		ProfileName: "codex",
	})

	res, err := svc.Remember(ctx, "fallback", RememberRequest{
		Evidence: []EvidenceInput{{
			Content: "Dense-Mem uses Postgres.",
			Metadata: map[string]any{
				"source_doc_id": "doc-1",
			},
		}},
	})

	require.NoError(t, err)
	require.Equal(t, string(domain.MemoryPlacementQueued), res.Status)
	require.Len(t, semanticStore.inputs, 1)
	require.Equal(t, teamID.String(), semanticStore.inputs[0].TeamID)
	require.Equal(t, profileID.String(), semanticStore.inputs[0].OwnerProfileID)
	require.Equal(t, "doc-1", semanticStore.inputs[0].Evidence[0].SourceDocID)
	require.Equal(t, teamID.String(), store.created.ProfileID)
	require.Equal(t, profileID.String(), store.created.OwnerProfileID)
	require.Len(t, store.created.Items, 1)
	require.NotEmpty(t, store.created.Items[0].FragmentID)
}

func TestProcessNextPlacementStoresSemanticRelationship(t *testing.T) {
	teamID := uuid.NewString()
	profileID := uuid.NewString()
	store := &statefulPlacementStore{}
	semanticStore := &stubSemanticStore{}
	svc := New(Dependencies{
		PlacementStore:   store,
		SemanticStore:    semanticStore,
		SemanticReviewer: &stubSemanticReviewer{},
		SemanticVerifier: &stubSemanticVerifier{},
	})
	_, err := svc.Remember(context.Background(), teamID, RememberRequest{
		Evidence: []EvidenceInput{{Content: "Dense-Mem uses Postgres."}},
	})
	require.NoError(t, err)
	store.run.OwnerProfileID = profileID

	processed, err := svc.ProcessNextPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, semanticStore.inputs, 2)
	require.Len(t, semanticStore.inputs[1].Relationships, 1)
	require.Equal(t, "uses", semanticStore.inputs[1].Relationships[0].Predicate)
	saved := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementCompleted, saved.Status)
	require.Len(t, saved.Items, 1)
	require.Equal(t, domain.MemoryPlacementEvidenceProcessed, saved.Items[0].Category)
	require.Len(t, saved.Items[0].RelationshipOutcomes, 1)
	require.NotEmpty(t, saved.Items[0].RelationshipOutcomes[0].RelationshipID)
	require.Equal(t, "relationship_validated_claim", saved.Items[0].RelationshipOutcomes[0].Category)
}

func TestProcessNextPlacementRetriesTransientSemanticReviewerFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:          "ingest-retry",
		ProfileID:         uuid.NewString(),
		OwnerProfileID:    uuid.NewString(),
		Status:            domain.MemoryPlacementQueued,
		CheckAfterSeconds: 60,
		StatusTool:        "get_memory_placement",
		Evidence:          []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem uses Postgres."}},
		Items: []domain.MemoryPlacementItem{{
			ItemID:        uuid.NewString(),
			EvidenceIndex: 0,
			FragmentID:    uuid.NewString(),
			Category:      domain.MemoryPlacementFragmentOnly,
			Status:        string(domain.MemoryPlacementQueued),
			Reason:        "evidence stored; awaiting Dense-Mem verifier placement",
			CreatedAt:     now,
			UpdatedAt:     now,
		}},
		AvailableAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}}
	semanticStore := &stubSemanticStore{}
	svc := New(Dependencies{
		PlacementStore:       store,
		SemanticStore:        semanticStore,
		SemanticReviewer:     &stubSemanticReviewer{err: &verifier.TimeoutError{Provider: "stub", Message: "deadline"}},
		SemanticVerifier:     &stubSemanticVerifier{},
		PlacementMaxAttempts: 5,
	})

	processed, err := svc.ProcessNextPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Empty(t, semanticStore.inputs)
	saved := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementQueued, saved.Status)
	require.Equal(t, 1, saved.Attempts)
	require.Equal(t, 5, saved.CheckAfterSeconds)
	require.True(t, saved.AvailableAt.After(saved.UpdatedAt))
	require.Nil(t, saved.StartedAt)
	require.Nil(t, saved.CompletedAt)
	require.Len(t, saved.Items, 1)
	require.Equal(t, string(domain.MemoryPlacementQueued), saved.Items[0].Status)
	require.Contains(t, saved.Items[0].Error, "verifier request timed out")
}

func TestProcessNextPlacementRetriesMalformedSemanticVerifierResponse(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:          "ingest-malformed",
		ProfileID:         uuid.NewString(),
		OwnerProfileID:    uuid.NewString(),
		Status:            domain.MemoryPlacementQueued,
		CheckAfterSeconds: 60,
		StatusTool:        "get_memory_placement",
		Evidence:          []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem uses Postgres."}},
		Items: []domain.MemoryPlacementItem{{
			ItemID:        uuid.NewString(),
			EvidenceIndex: 0,
			FragmentID:    uuid.NewString(),
			Category:      domain.MemoryPlacementFragmentOnly,
			Status:        string(domain.MemoryPlacementQueued),
			Reason:        "evidence stored; awaiting Dense-Mem verifier placement",
			CreatedAt:     now,
			UpdatedAt:     now,
		}},
		AvailableAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}}
	semanticStore := &stubSemanticStore{}
	svc := New(Dependencies{
		PlacementStore:       store,
		SemanticStore:        semanticStore,
		SemanticReviewer:     &stubSemanticReviewer{},
		SemanticVerifier:     &stubSemanticVerifier{err: &verifier.MalformedResponseError{Provider: "stub", Message: "entity_results coverage mismatch"}},
		PlacementMaxAttempts: 5,
	})

	processed, err := svc.ProcessNextPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Empty(t, semanticStore.inputs)
	saved := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementQueued, saved.Status)
	require.Equal(t, 1, saved.Attempts)
	require.Equal(t, 5, saved.CheckAfterSeconds)
	require.True(t, saved.AvailableAt.After(saved.UpdatedAt))
	require.Len(t, saved.Items, 1)
	require.Equal(t, string(domain.MemoryPlacementQueued), saved.Items[0].Status)
	require.Contains(t, saved.Items[0].Error, "verifier malformed response")
}

func TestProcessNextPlacementClearsRetryErrorAfterSemanticSuccess(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:          "ingest-recovered",
		ProfileID:         uuid.NewString(),
		OwnerProfileID:    uuid.NewString(),
		Status:            domain.MemoryPlacementQueued,
		Attempts:          1,
		Error:             "verifier malformed response: previous attempt",
		CheckAfterSeconds: 5,
		StatusTool:        "get_memory_placement",
		Evidence:          []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem uses Postgres."}},
		Items: []domain.MemoryPlacementItem{{
			ItemID:        uuid.NewString(),
			EvidenceIndex: 0,
			FragmentID:    uuid.NewString(),
			Category:      domain.MemoryPlacementFragmentOnly,
			Status:        string(domain.MemoryPlacementQueued),
			Reason:        "evidence stored; awaiting Dense-Mem verifier placement",
			Error:         "verifier malformed response: previous attempt",
			CreatedAt:     now,
			UpdatedAt:     now,
		}},
		AvailableAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}}
	semanticStore := &stubSemanticStore{}
	svc := New(Dependencies{
		PlacementStore:   store,
		SemanticStore:    semanticStore,
		SemanticReviewer: &stubSemanticReviewer{},
		SemanticVerifier: &stubSemanticVerifier{},
	})

	processed, err := svc.ProcessNextPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	saved := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementCompleted, saved.Status)
	require.Equal(t, "", saved.Error)
	require.Len(t, saved.Items, 1)
	require.Equal(t, "completed", saved.Items[0].Status)
	require.Equal(t, "", saved.Items[0].Error)
}

func TestProcessNextPlacementFailsTransientSemanticReviewerAfterMaxAttempts(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:          "ingest-final-failure",
		ProfileID:         uuid.NewString(),
		OwnerProfileID:    uuid.NewString(),
		Status:            domain.MemoryPlacementQueued,
		Attempts:          4,
		CheckAfterSeconds: 60,
		StatusTool:        "get_memory_placement",
		Evidence:          []domain.MemoryEvidence{{Index: 0, Content: "Dense-Mem uses Postgres."}},
		Items: []domain.MemoryPlacementItem{{
			ItemID:        uuid.NewString(),
			EvidenceIndex: 0,
			FragmentID:    uuid.NewString(),
			Category:      domain.MemoryPlacementFragmentOnly,
			Status:        string(domain.MemoryPlacementQueued),
			Reason:        "evidence stored; awaiting Dense-Mem verifier placement",
			CreatedAt:     now,
			UpdatedAt:     now,
		}},
		AvailableAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}}
	svc := New(Dependencies{
		PlacementStore:       store,
		SemanticStore:        &stubSemanticStore{},
		SemanticReviewer:     &stubSemanticReviewer{err: &verifier.TimeoutError{Provider: "stub", Message: "deadline"}},
		SemanticVerifier:     &stubSemanticVerifier{},
		PlacementMaxAttempts: 5,
	})

	processed, err := svc.ProcessNextPlacement(context.Background())

	require.ErrorIs(t, err, verifier.ErrVerifierTimeout)
	require.True(t, processed)
	saved := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementFailed, saved.Status)
	require.Equal(t, 5, saved.Attempts)
	require.NotNil(t, saved.CompletedAt)
	require.Len(t, saved.Items, 1)
	require.Equal(t, "failed", saved.Items[0].Status)
	require.Contains(t, saved.Items[0].Error, "verifier request timed out")
}
