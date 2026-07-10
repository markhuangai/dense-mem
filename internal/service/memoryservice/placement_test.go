package memoryservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
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

func (s *stubPlacementStore) SaveRunWithTransitions(_ context.Context, run domain.MemoryPlacementRun, _ []domain.AssertionTransitionEvent) error {
	return s.SaveRun(context.Background(), run)
}

func (s *stubPlacementStore) AppendTransitionEvents(context.Context, []domain.AssertionTransitionEvent) error {
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

type statefulPlacementStore struct {
	mu           sync.Mutex
	run          domain.MemoryPlacementRun
	dispute      domain.MemoryDisputeSession
	savedRun     domain.MemoryPlacementRun
	savedDispute domain.MemoryDisputeSession
	transitions  []domain.AssertionTransitionEvent
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
	run := clonePlacementRun(s.run)
	s.run.Status = domain.MemoryPlacementProcessing
	return &run, nil
}

func (s *statefulPlacementStore) SaveRun(_ context.Context, run domain.MemoryPlacementRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = clonePlacementRun(run)
	s.savedRun = clonePlacementRun(run)
	return nil
}

func (s *statefulPlacementStore) SaveRunWithTransitions(_ context.Context, run domain.MemoryPlacementRun, events []domain.AssertionTransitionEvent) error {
	if err := s.SaveRun(context.Background(), run); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitions = append(s.transitions, events...)
	return nil
}

func (s *statefulPlacementStore) AppendTransitionEvents(_ context.Context, events []domain.AssertionTransitionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitions = append(s.transitions, events...)
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

func (s *statefulPlacementStore) transitionCopy() []domain.AssertionTransitionEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.AssertionTransitionEvent(nil), s.transitions...)
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
		Proposal: testMemoryProposal("x"),
	})

	require.ErrorContains(t, err, "embed failed")
	require.Equal(t, domain.MemoryPlacementFailed, store.created.Status)
	require.Equal(t, "embed failed", store.created.Error)
	require.Len(t, store.created.Items, 1)
	require.Equal(t, "failed", store.created.Items[0].Status)
	require.Equal(t, "embed failed", store.created.Items[0].Error)
}

func TestRememberValidatesRequiredDependenciesAndEvidence(t *testing.T) {
	t.Run("missing fragment create service", func(t *testing.T) {
		svc := New(Dependencies{})

		_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
			Evidence: []EvidenceInput{{Content: "memory"}},
		})

		require.ErrorContains(t, err, "fragment create service is required")
	})

	t.Run("missing placement store", func(t *testing.T) {
		svc := New(Dependencies{FragmentCreate: &stubFragmentCreate{}})

		_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
			Evidence: []EvidenceInput{{Content: "memory"}},
		})

		require.ErrorContains(t, err, "placement store is required")
	})

	t.Run("empty evidence", func(t *testing.T) {
		svc := New(Dependencies{FragmentCreate: &stubFragmentCreate{}, PlacementStore: &stubPlacementStore{}})

		_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{})

		require.ErrorContains(t, err, "evidence is required")
	})

	t.Run("blank evidence content", func(t *testing.T) {
		svc := New(Dependencies{FragmentCreate: &stubFragmentCreate{}, PlacementStore: &stubPlacementStore{}})

		_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
			Evidence: []EvidenceInput{{Content: " "}},
		})

		require.ErrorContains(t, err, "evidence[0].content is required")
	})
}

func TestRememberQueuesPlacementAndMapsEvidenceFragments(t *testing.T) {
	fragmentCreate := &stubFragmentCreate{res: &fragmentservice.CreateResult{
		Fragment: &domain.Fragment{
			FragmentID: "fragment-dup",
			ProfileID:  "profile-1",
			Content:    "memory",
			CreatedAt:  time.Now().UTC(),
		},
		Duplicate:   true,
		DuplicateOf: "fragment-old",
	}}
	store := &stubPlacementStore{}
	svc := New(Dependencies{FragmentCreate: fragmentCreate, PlacementStore: store})

	res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
		Evidence: []EvidenceInput{{
			Content:        "memory",
			Source:         "chat",
			IdempotencyKey: "idem-1",
			Labels:         []string{"work"},
			Metadata:       map[string]any{"channel": "cli"},
		}},
		Proposal: testMemoryProposal("memory"),
	})

	require.NoError(t, err)
	require.NotEmpty(t, res.IngestID)
	require.Equal(t, "queued", res.Status)
	require.Equal(t, "get_memory_placement", res.StatusTool)
	require.Len(t, res.Evidence, 1)
	require.Equal(t, "duplicate", res.Evidence[0].Status)
	require.Equal(t, "fragment-old", res.Evidence[0].DuplicateOf)
	require.Equal(t, res.IngestID, store.created.IngestID)
	require.Len(t, store.created.Items, 1)
	require.Equal(t, "fragment-dup", store.created.Items[0].FragmentID)
	require.Equal(t, "conversation", fragmentCreate.req.SourceType)
	require.Equal(t, "primary", fragmentCreate.req.Authority)
	require.Equal(t, "chat", fragmentCreate.req.Source)
	require.Equal(t, "idem-1", fragmentCreate.req.IdempotencyKey)
	require.Equal(t, []string{"work"}, fragmentCreate.req.Labels)
	require.Equal(t, map[string]any{"channel": "cli"}, fragmentCreate.req.Metadata)
	require.Equal(t, 0.95, fragmentCreate.req.SourceQuality)
}

func TestGetMemoryPlacementValidationAndLookup(t *testing.T) {
	t.Run("missing placement store", func(t *testing.T) {
		svc := New(Dependencies{})

		_, err := svc.GetMemoryPlacement(context.Background(), "profile-1", PlacementStatusRequest{IngestID: "ingest-1"})

		require.ErrorContains(t, err, "placement store is required")
	})

	t.Run("blank ingest id", func(t *testing.T) {
		svc := New(Dependencies{PlacementStore: &statefulPlacementStore{}})

		_, err := svc.GetMemoryPlacement(context.Background(), "profile-1", PlacementStatusRequest{IngestID: " "})

		require.ErrorContains(t, err, "ingest_id is required")
	})

	t.Run("not found", func(t *testing.T) {
		svc := New(Dependencies{PlacementStore: &statefulPlacementStore{}})

		_, err := svc.GetMemoryPlacement(context.Background(), "profile-1", PlacementStatusRequest{IngestID: "missing"})

		require.ErrorContains(t, err, "placement not found")
	})

	t.Run("found", func(t *testing.T) {
		store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
			IngestID:  "ingest-1",
			ProfileID: "profile-1",
			Status:    domain.MemoryPlacementCompleted,
		}}
		svc := New(Dependencies{PlacementStore: store})

		res, err := svc.GetMemoryPlacement(context.Background(), "profile-1", PlacementStatusRequest{IngestID: "ingest-1"})

		require.NoError(t, err)
		require.Equal(t, "ingest-1", res.Run.IngestID)
		require.Equal(t, domain.MemoryPlacementCompleted, res.Run.Status)
	})
}

func TestProcessNextPlacementHandlesNoWorkAndMissingStore(t *testing.T) {
	t.Run("missing placement store", func(t *testing.T) {
		svc := New(Dependencies{})

		processed, err := svc.ProcessNextPlacement(context.Background())

		require.False(t, processed)
		require.ErrorContains(t, err, "placement store is required")
	})

	t.Run("no queued work", func(t *testing.T) {
		store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
			IngestID:  "ingest-1",
			ProfileID: "profile-1",
			Status:    domain.MemoryPlacementCompleted,
		}}
		svc := New(Dependencies{PlacementStore: store})

		processed, err := svc.ProcessNextPlacement(context.Background())

		require.NoError(t, err)
		require.False(t, processed)
	})
}

func TestProcessPlacementRunClassifiesEvidenceItems(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{}
	promote := &stubFactPromote{}
	svc := New(Dependencies{
		ClaimCreate:    &stubClaimCreate{},
		ClaimVerify:    &stubClaimVerify{},
		FactPromote:    promote,
		PlacementStore: store,
	})
	run := &domain.MemoryPlacementRun{
		IngestID:  "ingest-1",
		ProfileID: "profile-1",
		Status:    domain.MemoryPlacementQueued,
		Evidence: []domain.MemoryEvidence{
			{Index: 1, Content: "This is not true."},
			{Index: 2, Content: "A loose note without a personal claim."},
			{Index: 3, Content: "I prefer Go.", IdempotencyKey: "idem-3"},
		},
		Items: []domain.MemoryPlacementItem{
			{ItemID: "item-missing", EvidenceIndex: 0, FragmentID: "fragment-missing", CreatedAt: now, UpdatedAt: now},
			{ItemID: "item-false", EvidenceIndex: 1, FragmentID: "fragment-false", CreatedAt: now, UpdatedAt: now},
			{ItemID: "item-fragment", EvidenceIndex: 2, FragmentID: "fragment-only", CreatedAt: now, UpdatedAt: now},
			{ItemID: "item-promoted", EvidenceIndex: 3, FragmentID: "fragment-promoted", CreatedAt: now, UpdatedAt: now},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	err := svc.processPlacementRun(context.Background(), run)

	require.NoError(t, err)
	savedRun := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementCompleted, savedRun.Status)
	require.NotNil(t, savedRun.StartedAt)
	require.NotNil(t, savedRun.CompletedAt)
	require.Equal(t, domain.MemoryPlacementNeedsEvidence, savedRun.Items[0].Category)
	require.Equal(t, domain.MemoryPlacementRejectedFalse, savedRun.Items[1].Category)
	require.Equal(t, domain.MemoryPlacementFragmentOnly, savedRun.Items[2].Category)
	require.Equal(t, domain.MemoryPlacementPromotedFact, savedRun.Items[3].Category)
	require.Equal(t, "claim-1", savedRun.Items[3].ClaimID)
	require.Equal(t, "fact-1", savedRun.Items[3].FactID)
	require.Equal(t, 1, promote.called)
}

func TestProcessNextPlacementClaimsQueuedRun(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:  "ingest-1",
		ProfileID: "profile-1",
		Status:    domain.MemoryPlacementQueued,
		Evidence:  []domain.MemoryEvidence{{Index: 0, Content: "I like Rust."}},
		Items: []domain.MemoryPlacementItem{{
			ItemID:        "item-1",
			EvidenceIndex: 0,
			FragmentID:    "fragment-1",
			CreatedAt:     now,
			UpdatedAt:     now,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}}
	svc := New(Dependencies{
		ClaimCreate:    &stubClaimCreate{},
		ClaimVerify:    &stubClaimVerify{},
		FactPromote:    &stubFactPromote{},
		PlacementStore: store,
	})

	processed, err := svc.ProcessNextPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	savedRun := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementCompleted, savedRun.Status)
	require.Equal(t, domain.MemoryPlacementPromotedFact, savedRun.Items[0].Category)
}

func TestStartPlacementWorkerProcessesQueuedRun(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:  "ingest-worker",
		ProfileID: "profile-1",
		Status:    domain.MemoryPlacementQueued,
		Evidence:  []domain.MemoryEvidence{{Index: 0, Content: "Reject this memory as incorrect."}},
		Items: []domain.MemoryPlacementItem{{
			ItemID:        "item-1",
			EvidenceIndex: 0,
			FragmentID:    "fragment-1",
			CreatedAt:     now,
			UpdatedAt:     now,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}}
	svc := New(Dependencies{PlacementStore: store})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc.StartPlacementWorker(ctx, 0)

	require.Eventually(t, func() bool {
		savedRun := store.savedRunCopy()
		return savedRun.Status == domain.MemoryPlacementCompleted &&
			len(savedRun.Items) == 1 &&
			savedRun.Items[0].Category == domain.MemoryPlacementRejectedFalse
	}, time.Second, 10*time.Millisecond)
}

func TestRememberSignalsPlacementWorker(t *testing.T) {
	store := &statefulPlacementStore{}
	svc := New(Dependencies{
		FragmentCreate: &stubFragmentCreate{},
		PlacementStore: store,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.StartPlacementWorker(ctx, time.Hour)

	res, err := svc.Remember(ctx, "profile-1", RememberRequest{
		Evidence: []EvidenceInput{{Content: "Reject this memory as incorrect."}},
		Proposal: testMemoryProposal("Reject this memory as incorrect."),
	})

	require.NoError(t, err)
	require.NotEmpty(t, res.IngestID)
	require.Eventually(t, func() bool {
		savedRun := store.savedRunCopy()
		return savedRun.Status == domain.MemoryPlacementFailed &&
			strings.Contains(savedRun.Error, "graph reviewer is required")
	}, time.Second, 10*time.Millisecond)
}

func testMemoryProposal(content string) domain.MemoryProposal {
	return domain.MemoryProposal{
		Entities: []domain.MemoryEntityProposal{
			{Ref: "subject", Name: "Subject", Type: "concept"},
			{Ref: "object", Name: "Object", Type: "concept"},
		},
		Relationships: []domain.MemoryRelationshipProposal{{
			ProposalID:   "relationship-1",
			SubjectRef:   "subject",
			Predicate:    "relates_to",
			ObjectRef:    "object",
			PolicyFamily: domain.AssertionPolicyMultiState,
			Polarity:     domain.PolarityPlus,
			Modality:     domain.ModalityAssertion,
			Evidence:     []domain.MemoryEvidenceRef{{EvidenceIndex: 0, Start: 0, End: len(content)}},
		}},
	}
}

func TestStartPlacementWorkerSkipsMissingStore(t *testing.T) {
	svc := New(Dependencies{})

	svc.StartPlacementWorker(context.Background(), time.Millisecond)
}

func TestPlacementFromClaimOutcome(t *testing.T) {
	cases := []struct {
		name string
		in   ClaimOutcome
		want domain.MemoryPlacementCategory
	}{
		{
			name: "fact object",
			in:   ClaimOutcome{Fact: &domain.Fact{FactID: "fact-1"}},
			want: domain.MemoryPlacementPromotedFact,
		},
		{
			name: "promoted status",
			in:   ClaimOutcome{Status: "promoted"},
			want: domain.MemoryPlacementPromotedFact,
		},
		{
			name: "promoted marker",
			in:   ClaimOutcome{Promotion: "promoted"},
			want: domain.MemoryPlacementPromotedFact,
		},
		{
			name: "validated claim",
			in:   ClaimOutcome{Status: string(domain.StatusValidated)},
			want: domain.MemoryPlacementValidatedClaim,
		},
		{
			name: "candidate claim",
			in:   ClaimOutcome{Status: string(domain.StatusCandidate)},
			want: domain.MemoryPlacementCandidateClaim,
		},
		{
			name: "disputed claim",
			in:   ClaimOutcome{Status: string(domain.StatusDisputed)},
			want: domain.MemoryPlacementRejectedFalse,
		},
		{
			name: "rejected claim",
			in:   ClaimOutcome{Status: string(domain.StatusRejected)},
			want: domain.MemoryPlacementRejectedFalse,
		},
		{
			name: "unsupported predicate",
			in:   ClaimOutcome{Status: "predicate_not_supported"},
			want: domain.MemoryPlacementFragmentOnly,
		},
		{
			name: "invalid",
			in:   ClaimOutcome{Status: "invalid"},
			want: domain.MemoryPlacementFragmentOnly,
		},
		{
			name: "error needs evidence",
			in:   ClaimOutcome{Error: "verifier unavailable"},
			want: domain.MemoryPlacementNeedsEvidence,
		},
		{
			name: "default",
			in:   ClaimOutcome{},
			want: domain.MemoryPlacementFragmentOnly,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := placementFromClaimOutcome(tc.in)

			require.Equal(t, tc.want, got)
			require.NotEmpty(t, reason)
		})
	}
}

func TestPlacementLogUsesConfiguredLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(Dependencies{Logger: logger})

	require.Same(t, logger, svc.log())
	require.NotNil(t, New(Dependencies{}).log())
}

func TestExtractServerClaimAndFalseEvidenceHelpers(t *testing.T) {
	claim, ok := extractServerClaim(domain.MemoryEvidence{
		Content:        "Dense-Mem uses Postgres.",
		IdempotencyKey: "idem-1",
	}, "fragment-1")
	require.True(t, ok)
	require.Equal(t, "Dense-Mem", claim.Subject)
	require.Equal(t, "uses", claim.Predicate)
	require.Equal(t, "Postgres", claim.Object)
	require.Equal(t, []string{"fragment-1"}, claim.SupportedBy)
	require.Equal(t, "idem-1", claim.PipelineRunID)

	_, ok = extractServerClaim(domain.MemoryEvidence{Content: "I prefer ."}, "fragment-empty")
	require.False(t, ok)
	_, ok = extractServerClaim(domain.MemoryEvidence{Content: "No extractable personal claim."}, "fragment-none")
	require.False(t, ok)
	_, ok = extractServerClaim(domain.MemoryEvidence{Content: " "}, "fragment-blank")
	require.False(t, ok)

	require.True(t, evidenceLooksFalse([]EvidenceInput{{Content: "This contradicts the memory."}}))
	require.False(t, evidenceLooksFalse([]EvidenceInput{{Content: "This supports the memory."}}))
}

func TestDisputeMemoryPlacementAcceptsPromotedEvidence(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:          "ingest-1",
		ProfileID:         "profile-1",
		Status:            domain.MemoryPlacementCompleted,
		CheckAfterSeconds: 60,
		StatusTool:        "get_memory_placement",
		Evidence: []domain.MemoryEvidence{{
			Index:   0,
			Content: "The user prefers an old editor.",
		}},
		Items: []domain.MemoryPlacementItem{{
			ItemID:        "item-1",
			IngestID:      "ingest-1",
			ProfileID:     "profile-1",
			EvidenceIndex: 0,
			FragmentID:    "fragment-original",
			Category:      domain.MemoryPlacementRejectedFalse,
			Status:        "completed",
			CreatedAt:     now,
			UpdatedAt:     now,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}}
	fragmentCreate := &stubFragmentCreate{res: &fragmentservice.CreateResult{Fragment: &domain.Fragment{
		FragmentID: "fragment-dispute",
		ProfileID:  "profile-1",
		Content:    "I prefer vim.",
		CreatedAt:  now,
	}}}
	promote := &stubFactPromote{fact: &domain.Fact{
		FactID:              "fact-1",
		ProfileID:           "profile-1",
		PromotedFromClaimID: "claim-1",
	}}
	svc := New(Dependencies{
		FragmentCreate: fragmentCreate,
		ClaimCreate:    &stubClaimCreate{},
		ClaimVerify:    &stubClaimVerify{},
		FactPromote:    promote,
		PlacementStore: store,
	})

	res, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{
		IngestID:        "ingest-1",
		PlacementItemID: "item-1",
		Message:         "The placement missed my current preference.",
		Evidence: []EvidenceInput{{
			Content: "I prefer vim.",
			Source:  "chat",
		}},
	})

	require.NoError(t, err)
	require.Equal(t, domain.MemoryDisputeAcceptedPromoted, res.Session.Status)
	require.NotEmpty(t, res.Session.DisputeID)
	require.Len(t, res.Session.Turns, 1)
	require.Equal(t, "client", res.Session.Turns[0].Role)
	savedRun := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementDisputeAccepted, savedRun.Items[0].Category)
	require.Equal(t, "claim-1", savedRun.Items[0].ClaimID)
	require.Equal(t, "fact-1", savedRun.Items[0].FactID)
	require.Equal(t, 1, fragmentCreate.called)
	require.Equal(t, "conversation", fragmentCreate.req.SourceType)
	require.Equal(t, "primary", fragmentCreate.req.Authority)
	require.Equal(t, 1, promote.called)
}

func TestDisputeMemoryPlacementRejectsContradictoryEvidence(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:          "ingest-1",
		ProfileID:         "profile-1",
		Status:            domain.MemoryPlacementCompleted,
		CheckAfterSeconds: 60,
		StatusTool:        "get_memory_placement",
		Items: []domain.MemoryPlacementItem{{
			ItemID:        "item-1",
			IngestID:      "ingest-1",
			ProfileID:     "profile-1",
			EvidenceIndex: 0,
			FragmentID:    "fragment-original",
			Category:      domain.MemoryPlacementNeedsEvidence,
			Status:        "completed",
			CreatedAt:     now,
			UpdatedAt:     now,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}}
	promote := &stubFactPromote{}
	svc := New(Dependencies{
		FragmentCreate: &stubFragmentCreate{},
		ClaimCreate:    &stubClaimCreate{},
		ClaimVerify:    &stubClaimVerify{},
		FactPromote:    promote,
		PlacementStore: store,
	})

	res, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{
		IngestID:        "ingest-1",
		PlacementItemID: "item-1",
		Message:         "That memory is not true.",
		Evidence:        []EvidenceInput{{Content: "Reject this memory because it is incorrect."}},
	})

	require.NoError(t, err)
	require.Equal(t, domain.MemoryDisputeRejectedExplained, res.Session.Status)
	require.Contains(t, res.Session.FinalReason, "kept the placement rejected")
	savedRun := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementDisputeRejected, savedRun.Items[0].Category)
	require.Empty(t, savedRun.Items[0].ClaimID)
	require.Empty(t, savedRun.Items[0].FactID)
	require.Equal(t, 0, promote.called)
}

func TestDisputeMemoryPlacementValidationAndOpenBranches(t *testing.T) {
	t.Run("missing placement store", func(t *testing.T) {
		svc := New(Dependencies{})

		_, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{IngestID: "ingest-1"})

		require.ErrorContains(t, err, "placement store is required")
	})

	t.Run("unknown dispute", func(t *testing.T) {
		svc := New(Dependencies{PlacementStore: &statefulPlacementStore{}})

		_, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{DisputeID: "dispute-missing"})

		require.ErrorContains(t, err, "dispute not found")
	})

	t.Run("missing ingest id", func(t *testing.T) {
		svc := New(Dependencies{PlacementStore: &statefulPlacementStore{}})

		_, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{})

		require.ErrorContains(t, err, "ingest_id is required")
	})

	t.Run("placement not found", func(t *testing.T) {
		svc := New(Dependencies{PlacementStore: &statefulPlacementStore{}})

		_, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{IngestID: "missing"})

		require.ErrorContains(t, err, "placement not found")
	})

	t.Run("placement item not found", func(t *testing.T) {
		store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
			IngestID:  "ingest-1",
			ProfileID: "profile-1",
			Status:    domain.MemoryPlacementCompleted,
		}}
		svc := New(Dependencies{PlacementStore: store})

		_, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{IngestID: "ingest-1"})

		require.ErrorContains(t, err, "placement item not found")
	})

	t.Run("unknown placement item does not create dispute", func(t *testing.T) {
		now := time.Now().UTC()
		store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
			IngestID:  "ingest-1",
			ProfileID: "profile-1",
			Status:    domain.MemoryPlacementCompleted,
			Items: []domain.MemoryPlacementItem{{
				ItemID:        "item-1",
				IngestID:      "ingest-1",
				ProfileID:     "profile-1",
				EvidenceIndex: 0,
				FragmentID:    "fragment-1",
				Category:      domain.MemoryPlacementNeedsEvidence,
				Status:        "completed",
				CreatedAt:     now,
				UpdatedAt:     now,
			}},
		}}
		svc := New(Dependencies{PlacementStore: store})

		_, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{
			IngestID:        "ingest-1",
			PlacementItemID: "item-missing",
			Message:         "Please review this.",
		})

		require.ErrorContains(t, err, "placement item not found")
		require.False(t, store.disputeCreated())
	})

	t.Run("positive dispute evidence requires fragment create service", func(t *testing.T) {
		now := time.Now().UTC()
		store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
			IngestID:  "ingest-1",
			ProfileID: "profile-1",
			Status:    domain.MemoryPlacementCompleted,
			Items: []domain.MemoryPlacementItem{{
				ItemID:        "item-1",
				IngestID:      "ingest-1",
				ProfileID:     "profile-1",
				EvidenceIndex: 0,
				FragmentID:    "fragment-1",
				Category:      domain.MemoryPlacementNeedsEvidence,
				Status:        "completed",
				CreatedAt:     now,
				UpdatedAt:     now,
			}},
		}}
		svc := New(Dependencies{PlacementStore: store})

		_, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{
			IngestID:        "ingest-1",
			PlacementItemID: "item-1",
			Message:         "This should be promoted.",
			Evidence:        []EvidenceInput{{Content: "I prefer vim."}},
		})

		require.ErrorContains(t, err, "fragment create service is required")
		require.False(t, store.disputeCreated())
	})

	t.Run("existing dispute remains open without enough evidence", func(t *testing.T) {
		now := time.Now().UTC()
		store := &statefulPlacementStore{
			run: domain.MemoryPlacementRun{
				IngestID:  "ingest-1",
				ProfileID: "profile-1",
				Status:    domain.MemoryPlacementCompleted,
				Items: []domain.MemoryPlacementItem{{
					ItemID:        "item-1",
					IngestID:      "ingest-1",
					ProfileID:     "profile-1",
					EvidenceIndex: 0,
					FragmentID:    "fragment-1",
					Category:      domain.MemoryPlacementNeedsEvidence,
					Status:        "completed",
					CreatedAt:     now,
					UpdatedAt:     now,
				}},
			},
			dispute: domain.MemoryDisputeSession{
				DisputeID:       "dispute-1",
				ProfileID:       "profile-1",
				IngestID:        "ingest-1",
				PlacementItemID: "item-1",
				Status:          domain.MemoryDisputeOpen,
				CreatedAt:       now,
				UpdatedAt:       now,
			},
		}
		svc := New(Dependencies{
			FragmentCreate: &stubFragmentCreate{},
			ClaimCreate:    &stubClaimCreate{},
			ClaimVerify:    &stubClaimVerify{},
			FactPromote:    &stubFactPromote{},
			PlacementStore: store,
		})

		res, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{
			DisputeID: "dispute-1",
			Message:   "Please review this again.",
			Evidence:  []EvidenceInput{{Content: "  "}},
		})

		require.NoError(t, err)
		require.Equal(t, domain.MemoryDisputeOpen, res.Session.Status)
		require.Contains(t, res.Session.FinalReason, "needs more evidence")
		savedDispute := store.savedDisputeCopy()
		savedRun := store.savedRunCopy()
		require.Len(t, savedDispute.Turns, 1)
		require.Equal(t, domain.MemoryPlacementNeedsEvidence, savedRun.Items[0].Category)
	})
}

func TestDisputeMemoryPlacementPreservesConcurrentContinuationTurns(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{
		run: domain.MemoryPlacementRun{
			IngestID:  "ingest-1",
			ProfileID: "profile-1",
			Status:    domain.MemoryPlacementCompleted,
			Items: []domain.MemoryPlacementItem{{
				ItemID:        "item-1",
				IngestID:      "ingest-1",
				ProfileID:     "profile-1",
				EvidenceIndex: 0,
				FragmentID:    "fragment-1",
				Category:      domain.MemoryPlacementNeedsEvidence,
				Status:        "completed",
				CreatedAt:     now,
				UpdatedAt:     now,
			}},
		},
		dispute: domain.MemoryDisputeSession{
			DisputeID:       "dispute-1",
			ProfileID:       "profile-1",
			IngestID:        "ingest-1",
			PlacementItemID: "item-1",
			Status:          domain.MemoryDisputeOpen,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}
	svc := New(Dependencies{PlacementStore: store})

	const turnCount = 8
	errs := make(chan error, turnCount)
	var wg sync.WaitGroup
	for i := 0; i < turnCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.DisputeMemoryPlacement(context.Background(), "profile-1", DisputeRequest{
				DisputeID: "dispute-1",
				Message:   "Please review this again.",
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	savedDispute := store.savedDisputeCopy()
	require.Len(t, savedDispute.Turns, turnCount)
}
