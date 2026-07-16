package memoryservice

import (
	"context"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/stretchr/testify/require"
)

func TestSemanticEvidenceInputsPreserveProductionMetadata(t *testing.T) {
	inputs := semanticEvidenceInputs([]EvidenceInput{{
		Content:        " Dense-Mem uses Postgres. ",
		SourceType:     "document",
		Source:         "public-rag",
		Authority:      "secondary",
		SourceGroup:    " seed-group ",
		IdempotencyKey: "idem-1",
		Labels:         []string{"rag", "seed"},
		Metadata: map[string]any{
			"source_doc_id": "doc-1",
		},
	}})

	require.Len(t, inputs, 1)
	require.Equal(t, "Dense-Mem uses Postgres.", inputs[0].Content)
	require.Equal(t, domain.SourceTypeDocument, inputs[0].SourceType)
	require.Equal(t, "public-rag", inputs[0].Source)
	require.Equal(t, "doc-1", inputs[0].SourceDocID)
	require.Equal(t, "seed-group", inputs[0].SourceGroup)
	require.Equal(t, domain.AuthoritySecondary, inputs[0].Authority)
	require.Equal(t, "idem-1", inputs[0].IdempotencyKey)
	require.Equal(t, []string{"rag", "seed"}, inputs[0].Labels)
	require.Equal(t, map[string]any{"source_doc_id": "doc-1"}, inputs[0].Metadata)
}

func TestSemanticEvidenceInputsDefaultInvalidSourceAndAuthority(t *testing.T) {
	inputs := semanticEvidenceInputs([]EvidenceInput{{
		Content:    "memory",
		SourceType: "typo",
		Source:     "fallback-doc",
		Authority:  "rumor",
		Metadata:   map[string]any{"source_doc_id": 42},
	}})

	require.Len(t, inputs, 1)
	require.Equal(t, domain.SourceTypeConversation, inputs[0].SourceType)
	require.Equal(t, domain.AuthorityPrimary, inputs[0].Authority)
	require.Equal(t, "42", inputs[0].SourceDocID)
}

func TestRememberValidatesRequiredDependenciesAndEvidence(t *testing.T) {
	t.Run("missing semantic and fragment storage", func(t *testing.T) {
		svc := New(Dependencies{PlacementStore: &stubPlacementStore{}})

		_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
			Evidence: []EvidenceInput{{Content: "memory"}},
		})

		require.ErrorContains(t, err, "semantic store is required")
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

func TestProcessNextPlacementFailsReclaimedRunOverMaxAttempts(t *testing.T) {
	now := time.Now().UTC()
	store := &statefulPlacementStore{run: domain.MemoryPlacementRun{
		IngestID:  "ingest-stale",
		ProfileID: "profile-1",
		Status:    domain.MemoryPlacementQueued,
		Attempts:  5,
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
		ClaimCreate:          &stubClaimCreate{},
		ClaimVerify:          &stubClaimVerify{},
		FactPromote:          &stubFactPromote{},
		PlacementStore:       store,
		PlacementMaxAttempts: 5,
	})

	processed, err := svc.ProcessNextPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	savedRun := store.savedRunCopy()
	require.Equal(t, domain.MemoryPlacementFailed, savedRun.Status)
	require.Equal(t, 6, savedRun.Attempts)
	require.Contains(t, savedRun.Error, "exceeded max attempts")
	require.Equal(t, "failed", savedRun.Items[0].Status)
	require.Contains(t, savedRun.Items[0].Error, "exceeded max attempts")
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
	})

	require.NoError(t, err)
	require.NotEmpty(t, res.IngestID)
	require.Eventually(t, func() bool {
		savedRun := store.savedRunCopy()
		return savedRun.Status == domain.MemoryPlacementCompleted &&
			len(savedRun.Items) == 1 &&
			savedRun.Items[0].Category == domain.MemoryPlacementRejectedFalse
	}, time.Second, 10*time.Millisecond)
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
