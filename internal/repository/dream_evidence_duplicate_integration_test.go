package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestEvidenceDiscoveryEvaluationRejectsMixedDuplicateResponseAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-duplicate-response", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-duplicate-response-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-duplicate-response-owner")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "PostgreSQL")
	novelObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Redis")
	const content = "Dense-Mem uses PostgreSQL and Redis for durable memory."
	ingest, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-duplicate-ingest",
		RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{
			Content: content, InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	fragment := requireTestEvidenceFragment(t, ingest)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
		SourceVersion: 1, DocumentText: content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})
	run, err := semantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, RunDate: "2026-09-04", WindowKey: "hour:2026-09-04T07", Lane: domain.DreamLaneEvidenceDiscovery,
		LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	target := targets[0].Target
	sourceGroupKey := "ingest:" + ingest.IngestID
	proposal := func(objectID, statement string) UpsertHypothesisInput {
		return UpsertHypothesisInput{
			Statement: statement, Rationale: "The target excerpt names the supplied endpoints.",
			SubjectEntityID: subject.EntityID, PredicateKey: "uses", PredicateVersion: 1, ObjectEntityID: objectID,
			GeneratorKind: "provider", GeneratorVersion: "derivation-test", Lane: domain.DreamLaneEvidenceDiscovery,
			SourceEvidenceIDs: []string{target.FragmentID}, EvidenceDerivations: []EvidenceDerivationSource{{
				EvidenceID: target.FragmentID, FragmentID: target.FragmentID, SourceGroupKey: sourceGroupKey,
				SpanStart: 0, SpanEnd: len([]rune(content)), Quote: content, Authority: target.Authority,
			}},
		}
	}
	firstProposal := proposal(object.EntityID, "Dense-Mem may use PostgreSQL for durable memory.")
	err = semantic.WithEvidenceDiscoveryTargetLock(ctx, teamID, target.EvidenceID, target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Equal(t, 1, attempt.PassNumber)
		_, err := semantic.PersistEvidenceDiscoveryEvaluation(ctx, EvidenceDiscoveryEvaluationInput{
			TeamID: teamID, RunID: run.RunID, LeaseToken: run.LeaseToken,
			AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, Target: target,
			PassNumber: attempt.PassNumber, ProviderModel: "derivation-test-model", ProviderProposals: 1,
			AcceptedProposals: 1, CreatedHypotheses: 1, Proposals: []UpsertHypothesisInput{firstProposal},
		})
		return err
	})
	require.NoError(t, err)

	err = semantic.WithEvidenceDiscoveryTargetLock(ctx, teamID, target.EvidenceID, target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Equal(t, 2, attempt.PassNumber)
		_, err := semantic.PersistEvidenceDiscoveryEvaluation(ctx, EvidenceDiscoveryEvaluationInput{
			TeamID: teamID, RunID: run.RunID, LeaseToken: run.LeaseToken,
			AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, Target: target,
			PassNumber: attempt.PassNumber, ProviderModel: "derivation-test-model", ProviderProposals: 2,
			AcceptedProposals: 2, CreatedHypotheses: 2, Proposals: []UpsertHypothesisInput{
				proposal(novelObject.EntityID, "Dense-Mem may use Redis for durable memory."),
				firstProposal,
			},
		})
		return err
	})
	require.ErrorIs(t, err, ErrDreamExactHypothesisExists)
	totals, err := semantic.LoadEvidenceDiscoveryRunTotals(ctx, teamID, run.RunID)
	require.NoError(t, err)
	require.Equal(t, 1, totals.Evaluated, "the mixed duplicate response must not persist a second evaluation")
	require.Equal(t, 1, totals.Created, "the novel proposal must roll back with the duplicate")
	records, _, err := semantic.ListHypotheses(ctx, ListHypothesesInput{TeamID: teamID, Status: "proposed", Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 1, "the mixed duplicate response must not partially insert hypotheses")
	require.Equal(t, object.EntityID, records[0].ObjectEntityID)
}
