package memoryservice

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/stretchr/testify/require"
)

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
