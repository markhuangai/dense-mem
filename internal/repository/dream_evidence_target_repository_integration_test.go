package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	evidenceTargetTestLimit  = 20
	evidenceContextTestLimit = 10
)

func TestEvidenceDiscoveryTargetsAreBoundAndTeamScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-targets", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-owner")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-other-team")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, otherTeamID, "evidence-dream-other-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	documentVectors := map[string][]float32{}
	for index := 0; index < evidenceTargetTestLimit+2; index++ {
		result, err := ledger.CreateIngest(ctx, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: fmt.Sprintf("evidence-dream-%d", index),
			RequestHash: fmt.Sprintf("evidence-dream-hash-%d", index), Evidence: []EvidenceInput{{
				Content:      fmt.Sprintf("Team evidence target %d.", index),
				InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
			}},
		})
		require.NoError(t, err)
		fragment := requireTestEvidenceFragment(t, result)
		document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
			SourceVersion: 1, DocumentText: fragment.Content,
		})
		require.NoError(t, err)
		documentVectors[document.SearchDocumentID] = []float32{1, 0}
	}
	other, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: otherTeamID, OwnerProfileID: otherOwnerID, IdempotencyKey: "evidence-dream-other",
		RequestHash: "evidence-dream-other-hash", Evidence: []EvidenceInput{{
			Content: "Other team evidence.", InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	otherFragment := requireTestEvidenceFragment(t, other)
	otherDocument, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: otherTeamID, OwnerProfileID: otherOwnerID, SourceKind: "evidence", SourceID: otherFragment.FragmentID,
		SourceVersion: 1, DocumentText: otherFragment.Content,
	})
	require.NoError(t, err)
	documentVectors[otherDocument.SearchDocumentID] = []float32{1, 0}
	completeSearchDocumentsForTest(t, search, teamID, documentVectorsForTeam(documentVectors, teamID, appDB, rls))
	completeSearchDocumentsForTest(t, search, otherTeamID, documentVectorsForTeam(documentVectors, otherTeamID, appDB, rls))

	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit+10, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, evidenceTargetTestLimit)
	for _, target := range targets {
		require.NotEqual(t, otherFragment.FragmentID, target.Target.FragmentID)
		require.LessOrEqual(t, len(target.Contexts), evidenceContextTestLimit)
	}
}

func TestEvidenceDiscoveryContextsPrioritizeSameSourceAndExcludePrivateSpaces(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-context-order", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-context-order-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-context-order-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	create := func(createCtx context.Context, key, sourceRef, content, spaceID string, spaceGeneration int64) (EvidenceFragment, *SearchDocumentResult) {
		result, err := ledger.CreateIngest(createCtx, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, SpaceID: spaceID, SpaceGeneration: spaceGeneration,
			IdempotencyKey: key, RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{
				Content: content, SourceRef: sourceRef, InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
			}},
		})
		require.NoError(t, err)
		fragment := requireTestEvidenceFragment(t, result)
		document, err := search.UpsertSearchDocument(createCtx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
			SourceVersion: 1, DocumentText: content, SpaceID: spaceID, SpaceGeneration: spaceGeneration,
		})
		require.NoError(t, err)
		return fragment, document
	}

	target, targetDocument := create(ctx, "evidence-dream-context-target", "same-source", "Target evidence anchors the source context.", "", 0)
	sameSource, sameSourceDocument := create(ctx, "evidence-dream-context-same", "same-source", "Same source evidence is prioritized even when its vector is distant.", "", 0)
	vectorNear, vectorNearDocument := create(ctx, "evidence-dream-context-near", "other-source", "A different source has the nearest vector.", "", 0)
	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: privateSpace.ID, Kind: domain.MemorySpaceProfilePrivate}})
	private, privateDocument := create(privateCtx, "evidence-dream-context-private", "private-source", "Private evidence must never enter shared discovery context.", privateSpace.ID.String(), privateSpaceGeneration(t, ctx, adminDB, rls, privateSpace.ID))
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{
		targetDocument.SearchDocumentID:     {1, 0},
		sameSourceDocument.SearchDocumentID: {0, 1},
		vectorNearDocument.SearchDocumentID: {0.99, 0.01},
	})
	require.NoError(t, search.CompleteSearchDocumentsWithEmbeddings(privateCtx, CompleteSearchDocumentsWithEmbeddingsInput{
		TeamID: teamID, OwnerProfileID: ownerID, Documents: []SearchDocumentEmbedding{{
			TeamID: teamID, SearchDocumentID: privateDocument.SearchDocumentID, OwnerProfileID: ownerID,
			SourceKind: "evidence", SourceID: private.FragmentID, SourceVersion: 1,
			DocumentText: private.Content, DocumentHash: searchDocumentHash(private.Content), DocumentVersion: privateDocument.DocumentVersion,
			ProjectionFormat: privateDocument.ProjectionFormat, ProjectionGenerationID: privateDocument.ProjectionGenerationID,
			EmbeddingContractID: privateDocument.EmbeddingContractID, EmbeddingDimensions: privateDocument.EmbeddingDimensions,
			Embedding: []float32{1, 0}, SpaceID: privateDocument.SpaceID, SpaceGeneration: privateDocument.SpaceGeneration,
		}},
	}))

	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	for _, item := range targets {
		require.NotEqual(t, private.FragmentID, item.Target.EvidenceID, "private evidence must not be selected as a discovery target")
	}
	var selected *EvidenceDiscoveryTargetInput
	for index := range targets {
		if targets[index].Target.EvidenceID == target.FragmentID {
			selected = &targets[index]
			break
		}
	}
	require.NotNil(t, selected)
	require.GreaterOrEqual(t, len(selected.Contexts), 3)
	require.Equal(t, target.FragmentID, selected.Contexts[0].EvidenceID)
	require.Equal(t, sameSource.FragmentID, selected.Contexts[1].EvidenceID, "same-source evidence must precede vector-only candidates")
	require.Equal(t, vectorNear.FragmentID, selected.Contexts[2].EvidenceID)
	for _, item := range selected.Contexts {
		require.NotEqual(t, private.FragmentID, item.EvidenceID, "private evidence must not enter shared discovery context")
	}
}

func TestEvidenceDiscoveryNodesRankEvidenceMentionsBeforeTheGlobalBound(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-node-relevance", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-node-relevance-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-node-relevance-owner")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	for index := 0; index < 100; index++ {
		createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", fmt.Sprintf("Historical Node %03d", index))
	}
	relevant := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Relevant Node")
	const content = "The target evidence specifically names Relevant Node for discovery."
	ingest, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-node-relevance-ingest",
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

	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.NotEmpty(t, targets[0].Nodes)
	require.Equal(t, relevant.EntityID, targets[0].Nodes[0].ID, "the mentioned newer node must be selected before the 100-node bound")
}

func TestEvidenceDiscoveryPredicatesRankTargetRelevantDefinitionsBeforeTheGlobalBound(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-predicate-relevance", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-predicate-relevance-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-predicate-relevance-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	const relevantPredicate = "zz_late_target_relation"
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for index := 0; index < 101; index++ {
			if err := tx.Exec(`
				INSERT INTO team_predicate_definitions (
				    team_id, predicate_key, version, aliases, allowed_subject_kinds,
				    allowed_object_kinds, relationship_kind, current_cardinality,
				    lifecycle_state, origin, metadata
				) VALUES (?, ?, 1, ARRAY[]::text[], ARRAY['project','product','entity']::text[],
				          ARRAY['project','product','entity']::text[], 'state', 'many',
				          'active', 'test', '{}'::jsonb)
			`, teamID, fmt.Sprintf("zz_filler_%03d", index)).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
			    team_id, predicate_key, version, aliases, allowed_subject_kinds,
			    allowed_object_kinds, relationship_kind, current_cardinality,
			    lifecycle_state, origin, metadata
			) VALUES (?, ?, 1, ARRAY['target relation']::text[], ARRAY['project','product','entity']::text[],
			          ARRAY['project','product','entity']::text[], 'state', 'many',
			          'active', 'test', '{}'::jsonb)
		`, teamID, relevantPredicate).Error
	}))
	content := "The target explicitly describes zz_late_target_relation between supplied nodes."
	ingest, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-predicate-target",
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

	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	keys := make([]string, 0, len(targets[0].AllowedPredicates))
	for _, predicate := range targets[0].AllowedPredicates {
		keys = append(keys, predicate.PredicateKey)
	}
	require.Contains(t, keys, relevantPredicate, "a relevant predicate after the alphabetical 100th entry must remain allowlisted")
	require.Equal(t, relevantPredicate, targets[0].AllowedPredicates[0].PredicateKey)
}

func TestEvidenceDiscoveryTargetPassCapStopsAfterTwoRecordedEvaluations(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-pass-cap", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-pass-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-pass-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	result, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-pass", RequestHash: "evidence-dream-pass-hash",
		Evidence: []EvidenceInput{{Content: "Evidence evaluated twice.", InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"}}},
	})
	require.NoError(t, err)
	fragment := requireTestEvidenceFragment(t, result)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
		SourceVersion: 1, DocumentText: fragment.Content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})

	run, err := semantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, RunDate: "2026-09-04", WindowKey: "hour:2026-09-04T03", Lane: "evidence_discovery",
		LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	for pass := 1; pass <= 2; pass++ {
		require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				INSERT INTO dream_evidence_target_evaluations (
				    team_id, run_id, space_id, space_generation, target_evidence_id,
				    target_content_hash, pass_number, provider_model
				) VALUES (?, ?::uuid, dense_mem_team_shared_space(?::uuid), dense_mem_team_shared_generation(?::uuid), ?::uuid, ?, ?, ?)
			`, teamID, run.RunID, teamID, teamID, targets[0].Target.EvidenceID, targets[0].Target.ContentHash, pass, "test-model").Error
		}))
	}
	targets, err = semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Empty(t, targets)
}

func TestEvidenceDiscoveryRecoveryReportsDistinctTargetCount(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-recovery-target-count", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-recovery-target-count-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-recovery-target-count-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	documentVectors := map[string][]float32{}
	for index := 0; index < 2; index++ {
		result, err := ledger.CreateIngest(ctx, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID,
			IdempotencyKey: fmt.Sprintf("evidence-dream-recovery-target-count-%d", index),
			RequestHash:    fmt.Sprintf("evidence-dream-recovery-target-count-hash-%d", index),
			Evidence: []EvidenceInput{{
				Content:      fmt.Sprintf("Recovery target count evidence %d.", index),
				InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
			}},
		})
		require.NoError(t, err)
		fragment := requireTestEvidenceFragment(t, result)
		document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
			SourceVersion: 1, DocumentText: fragment.Content,
		})
		require.NoError(t, err)
		documentVectors[document.SearchDocumentID] = []float32{1, 0}
	}
	completeSearchDocumentsForTest(t, search, teamID, documentVectors)

	scheduledFor := time.Now().UTC().Add(-time.Hour)
	run, err := semantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, RunDate: scheduledFor.Format("2006-01-02"),
		WindowKey: "hour:" + scheduledFor.Format("2006-01-02T15"), ScheduledFor: &scheduledFor,
		LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(-time.Minute),
		Lane: domain.DreamLaneEvidenceDiscovery,
	})
	require.NoError(t, err)
	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	completedTarget := targets[0].Target
	for pass := 1; pass <= 2; pass++ {
		require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				INSERT INTO dream_evidence_target_evaluations (
				    team_id, run_id, space_id, space_generation, target_evidence_id,
				    target_content_hash, pass_number, provider_model, created_hypotheses
				) VALUES (?, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, 'recovery-count-test', 1)
			`, teamID, run.RunID, completedTarget.SpaceID, completedTarget.SpaceGeneration,
				completedTarget.EvidenceID, completedTarget.ContentHash, pass).Error
		}))
	}

	totals, err := semantic.LoadEvidenceDiscoveryRunTotals(ctx, teamID, run.RunID)
	require.NoError(t, err)
	require.Equal(t, 1, totals.TargetCount)
	require.Equal(t, []string{completedTarget.EvidenceID + ":" + completedTarget.ContentHash}, totals.TargetKeys)
	remaining, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, remaining, 1)

	recovered, err := semantic.ClaimRecoverableScheduledDreamCycle(ctx, DreamCycleRecoveryClaimInput{
		TeamID: teamID, LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
		MaxAttempts: 3, Lane: domain.DreamLaneEvidenceDiscovery,
	})
	require.NoError(t, err)
	require.NotNil(t, recovered)
	allTargetKeys := map[string]struct{}{}
	for _, key := range totals.TargetKeys {
		allTargetKeys[key] = struct{}{}
	}
	allTargetKeys[remaining[0].Target.EvidenceID+":"+remaining[0].Target.ContentHash] = struct{}{}
	require.Len(t, allTargetKeys, 2)
	require.NoError(t, semantic.CompleteScheduledDreamCycle(ctx, DreamCycleCompleteInput{
		TeamID: teamID, RunID: recovered.RunID, LeaseToken: recovered.LeaseToken,
		Status: "completed", Lane: domain.DreamLaneEvidenceDiscovery,
		EvidenceTargets: len(allTargetKeys), EvaluatedEvidenceTargets: totals.Evaluated,
	}))

	runs, err := semantic.ListDreamCyclesForTeam(ctx, teamID, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, 2, runs[0].EvidenceTargets, "recovery must persist the union of prior and selected targets")
}

func TestEvidenceDiscoveryTargetLockCapsConcurrentProviderPasses(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-concurrent-pass-cap", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-concurrent-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-concurrent-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	firstSemantic := NewSemanticRepository(appDB, rls)
	secondSemantic := NewSemanticRepository(appDB, rls)
	result, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-concurrent-target",
		RequestHash: "evidence-dream-concurrent-target-hash", Evidence: []EvidenceInput{{
			Content: "Concurrent evidence target.", InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	fragment := requireTestEvidenceFragment(t, result)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
		SourceVersion: 1, DocumentText: fragment.Content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})
	run, err := firstSemantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, RunDate: "2026-09-04", WindowKey: "hour:2026-09-04T04", Lane: "evidence_discovery",
		LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	targets, err := firstSemantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	target := targets[0].Target

	start := make(chan struct{})
	errs := make(chan error, 3)
	var providerCalls atomic.Int32
	var wg sync.WaitGroup
	for index, semantic := range []*SemanticRepositoryImpl{firstSemantic, secondSemantic, firstSemantic} {
		wg.Add(1)
		go func(worker int, repo *SemanticRepositoryImpl) {
			defer wg.Done()
			<-start
			err := repo.WithEvidenceDiscoveryTargetLock(ctx, teamID, target.EvidenceID, target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
				if attempt.PassNumber == 0 {
					return nil
				}
				providerCalls.Add(1)
				if err := repo.MarkEvidenceDiscoveryAttemptValidated(ctx, EvidenceDiscoveryAttemptValidationInput{
					TeamID: teamID, AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, AcceptedProposals: 1,
				}); err != nil {
					return err
				}
				return rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
					return tx.Exec(`
						INSERT INTO dream_evidence_target_evaluations (
							team_id, run_id, space_id, space_generation, target_evidence_id,
							target_content_hash, pass_number, provider_model, created_hypotheses
						) VALUES (?, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, 'concurrent-test', 1)
					`, teamID, run.RunID, target.SpaceID, target.SpaceGeneration,
						target.EvidenceID, target.ContentHash, attempt.PassNumber).Error
				})
			})
			if err != nil {
				errs <- fmt.Errorf("worker %d: %w", worker, err)
			}
		}(index, semantic)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 2, providerCalls.Load(), "concurrent workers must not dispatch a third provider pass")
	remaining, err := firstSemantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func TestEvidenceDiscoveryValidatedAttemptConsumesPassAfterPersistenceFailure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-persistence-failure", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-persistence-failure-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-persistence-failure-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	first := NewSemanticRepository(appDB, rls)
	second := NewSemanticRepository(appDB, rls)
	result, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-persistence-failure-target",
		RequestHash: "evidence-dream-persistence-failure-target-hash", Evidence: []EvidenceInput{{
			Content: "A validated provider response must consume its pass.", InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	fragment := requireTestEvidenceFragment(t, result)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
		SourceVersion: 1, DocumentText: fragment.Content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})
	targets, err := first.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	target := targets[0].Target

	var providerCalls int
	err = first.WithEvidenceDiscoveryTargetLock(ctx, teamID, target.EvidenceID, target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Equal(t, 1, attempt.PassNumber)
		providerCalls++
		require.NoError(t, first.MarkEvidenceDiscoveryAttemptValidated(ctx, EvidenceDiscoveryAttemptValidationInput{
			TeamID: teamID, AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, AcceptedProposals: 1,
		}))
		return errors.New("simulated persistence failure after validated provider response")
	})
	require.Error(t, err)

	err = second.WithEvidenceDiscoveryTargetLock(ctx, teamID, target.EvidenceID, target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Equal(t, 2, attempt.PassNumber, "validated pass one must not be retried")
		providerCalls++
		return second.MarkEvidenceDiscoveryAttemptValidated(ctx, EvidenceDiscoveryAttemptValidationInput{
			TeamID: teamID, AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, AcceptedProposals: 1,
		})
	})
	require.NoError(t, err)
	err = first.WithEvidenceDiscoveryTargetLock(ctx, teamID, target.EvidenceID, target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Zero(t, attempt.PassNumber)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, providerCalls)
}

func TestEvidenceDiscoveryTargetOrderingUsesAttemptFallback(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-ordering-fallback", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-ordering-fallback-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-ordering-fallback-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	createTarget := func(key, content string) EvidenceFragment {
		result, err := ledger.CreateIngest(ctx, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key,
			RequestHash: key + "-hash", Evidence: []EvidenceInput{{
				Content: content, InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
			}},
		})
		require.NoError(t, err)
		return requireTestEvidenceFragment(t, result)
	}
	older := createTarget("evidence-dream-ordering-older", "An older persisted evidence assessment.")
	newer := createTarget("evidence-dream-ordering-newer", "A newer validated but unpersisted evidence assessment.")
	documents := make(map[string][]float32, 2)
	for _, fragment := range []EvidenceFragment{older, newer} {
		document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
			SourceVersion: 1, DocumentText: fragment.Content,
		})
		require.NoError(t, err)
		documents[document.SearchDocumentID] = []float32{1, 0}
	}
	completeSearchDocumentsForTest(t, search, teamID, documents)
	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	targetByID := map[string]EvidenceTarget{}
	for _, target := range targets {
		targetByID[target.Target.EvidenceID] = target.Target
	}
	olderTarget, ok := targetByID[older.FragmentID]
	require.True(t, ok)
	newerTarget, ok := targetByID[newer.FragmentID]
	require.True(t, ok)
	run, err := semantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, RunDate: "2026-09-04", WindowKey: "hour:2026-09-04T05", Lane: "evidence_discovery",
		LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO dream_evidence_target_evaluations (
			    team_id, run_id, space_id, space_generation, target_evidence_id,
			    target_content_hash, pass_number, provider_model, created_hypotheses, created_at
			) VALUES (?, ?::uuid, ?::uuid, ?, ?::uuid, ?, 1, 'ordering-test', 1,
			          now() - interval '1 hour')
		`, teamID, run.RunID, olderTarget.SpaceID, olderTarget.SpaceGeneration,
			olderTarget.EvidenceID, olderTarget.ContentHash).Error
	}))
	require.NoError(t, semantic.WithEvidenceDiscoveryTargetLock(ctx, teamID, newerTarget.EvidenceID, newerTarget.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Equal(t, 1, attempt.PassNumber)
		return semantic.MarkEvidenceDiscoveryAttemptValidated(ctx, EvidenceDiscoveryAttemptValidationInput{
			TeamID: teamID, AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, AcceptedProposals: 1,
		})
	}))
	ordered, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, ordered, 2)
	require.Equal(t, older.FragmentID, ordered[0].Target.EvidenceID, "older effective assessment must precede newer unpersisted attempt")
}

func TestEvidenceDiscoveryMarkerFailureDoesNotReclaimStartedDispatch(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-marker-failure", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-marker-failure-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-marker-failure-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	result, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-marker-failure-target",
		RequestHash: "evidence-dream-marker-failure-target-hash", Evidence: []EvidenceInput{{
			Content:      "A started dispatch must not be reclaimed after marker failure.",
			InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	fragment := requireTestEvidenceFragment(t, result)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
		SourceVersion: 1, DocumentText: fragment.Content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})
	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	target := targets[0].Target

	var providerCalls int
	err = semantic.WithEvidenceDiscoveryTargetLock(ctx, teamID, target.EvidenceID, target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Equal(t, 1, attempt.PassNumber)
		providerCalls++
		require.NoError(t, semantic.MarkEvidenceDiscoveryAttemptDispatched(ctx, EvidenceDiscoveryAttemptValidationInput{
			TeamID: teamID, AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken,
		}))
		failedCtx, cancel := context.WithCancel(ctx)
		cancel()
		return semantic.MarkEvidenceDiscoveryAttemptValidated(failedCtx, EvidenceDiscoveryAttemptValidationInput{
			TeamID: teamID, AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, AcceptedProposals: 1,
		})
	})
	require.Error(t, err)

	err = semantic.WithEvidenceDiscoveryTargetLock(ctx, teamID, target.EvidenceID, target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Zero(t, attempt.PassNumber, "a failed validation marker must not permit another provider call")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, providerCalls)
}

func TestEvidenceDiscoveryEvaluationPersistsDerivationsAndReadsThemBack(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-derivation-readback", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-derivation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-derivation-owner")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "PostgreSQL")
	const content = "Dense-Mem uses PostgreSQL for durable memory."
	ingest, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-derivation-ingest",
		RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{
			Content: content, InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	target := requireTestEvidenceFragment(t, ingest)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: target.FragmentID,
		SourceVersion: 1, DocumentText: content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})
	run, err := semantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, RunDate: "2026-09-04", WindowKey: "hour:2026-09-04T05", Lane: "evidence_discovery",
		LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Equal(t, ownerID, targets[0].Target.OwnerProfileID)
	sourceGroupKey := "ingest:" + ingest.IngestID

	var persisted DreamGenerationPersistResult
	err = semantic.WithEvidenceDiscoveryTargetLock(ctx, teamID, targets[0].Target.EvidenceID, targets[0].Target.ContentHash, func(attempt EvidenceDiscoveryAttempt) error {
		require.Equal(t, 1, attempt.PassNumber)
		if err := semantic.MarkEvidenceDiscoveryAttemptValidated(ctx, EvidenceDiscoveryAttemptValidationInput{
			TeamID: teamID, AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, AcceptedProposals: 1,
		}); err != nil {
			return err
		}
		persisted, err = semantic.PersistEvidenceDiscoveryEvaluation(ctx, EvidenceDiscoveryEvaluationInput{
			TeamID: teamID, RunID: run.RunID, LeaseToken: run.LeaseToken,
			AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken, Target: targets[0].Target,
			PassNumber: attempt.PassNumber, ProviderModel: "derivation-test-model", ProviderProposals: 1, AcceptedProposals: 1, CreatedHypotheses: 1,
			Proposals: []UpsertHypothesisInput{{
				Statement: "Dense-Mem may use PostgreSQL for durable memory.", Rationale: "The target excerpt names both supplied endpoints.",
				SubjectEntityID: subject.EntityID, PredicateKey: "uses", PredicateVersion: 1, ObjectEntityID: object.EntityID,
				GeneratorKind: "provider", GeneratorVersion: "derivation-test", Lane: "evidence_discovery",
				SourceEvidenceIDs: []string{target.FragmentID}, EvidenceDerivations: []EvidenceDerivationSource{{
					EvidenceID: target.FragmentID, FragmentID: target.FragmentID, SourceGroupKey: sourceGroupKey,
					SpanStart: 0, SpanEnd: len([]rune(content)), Quote: content, Authority: target.Authority,
				}},
			}},
		})
		return err
	})
	require.NoError(t, err)
	require.Equal(t, 1, persisted.Created)
	totals, err := semantic.LoadEvidenceDiscoveryRunTotals(ctx, teamID, run.RunID)
	require.NoError(t, err)
	require.Equal(t, 1, totals.TargetCount)
	require.Equal(t, []string{target.FragmentID + ":" + target.ContentHash}, totals.TargetKeys)
	require.Equal(t, 1, totals.Evaluated)
	require.Equal(t, 1, totals.Created)
	require.Equal(t, 1, totals.ProviderProposals)
	records, _, err := semantic.ListHypotheses(ctx, ListHypothesesInput{TeamID: teamID, Status: "proposed", Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "evidence_discovery", string(records[0].Lane))
	require.Equal(t, []string{target.FragmentID}, records[0].SourceEvidenceIDs)
	require.Len(t, records[0].EvidenceDerivations, 1)
	require.Equal(t, content, records[0].EvidenceDerivations[0].Quote)
	require.Equal(t, sourceGroupKey, records[0].EvidenceDerivations[0].SourceGroupKey)
	require.Equal(t, []string{ownerID}, records[0].SourceOwnerProfileIDs)
	loaded, err := semantic.GetHypothesis(ctx, GetHypothesisInput{TeamID: teamID, HypothesisID: records[0].HypothesisID})
	require.NoError(t, err)
	require.Len(t, loaded.EvidenceDerivations, 1)
	require.Equal(t, target.FragmentID, loaded.EvidenceDerivations[0].EvidenceID)
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-other-owner")
	_, err = semantic.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID: teamID, ActorProfileID: otherOwnerID, HypothesisID: records[0].HypothesisID,
		Status: "reinforced", Decision: "reinforce",
	})
	require.ErrorIs(t, err, ErrDreamHypothesisNotFound)
	updated, err := semantic.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID: teamID, ActorProfileID: ownerID, HypothesisID: records[0].HypothesisID,
		Status: "reinforced", Decision: "reinforce",
	})
	require.NoError(t, err)
	require.Equal(t, "reinforced", updated.Status)
	submission := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"evidence-dream-owner-submit", "Independent owner evidence for the hypothesis.")
	_, err = semantic.SubmitHypothesis(ctx, SubmitHypothesisInput{
		TeamID: teamID, ActorProfileID: otherOwnerID, HypothesisID: records[0].HypothesisID,
		Decision: "confirm_true", SubmittedIngestID: submission.IngestID,
	})
	require.ErrorIs(t, err, ErrDreamHypothesisNotFound)
	ownerSubmitted, err := semantic.SubmitHypothesis(ctx, SubmitHypothesisInput{
		TeamID: teamID, ActorProfileID: ownerID, HypothesisID: records[0].HypothesisID,
		Decision: "confirm_true", SubmittedIngestID: submission.IngestID,
	})
	require.NoError(t, err)
	require.Equal(t, submission.IngestID, ownerSubmitted.SubmittedIngestID)
	_, err = semantic.SubmitHypothesis(ctx, SubmitHypothesisInput{
		TeamID: teamID, ActorProfileID: otherOwnerID, HypothesisID: records[0].HypothesisID,
		Decision: "confirm_true", SubmittedIngestID: submission.IngestID,
	})
	require.ErrorIs(t, err, ErrDreamHypothesisNotFound, "an evidence-lane idempotency replay remains owner-bound")
}

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

func TestEvidenceDiscoveryTargetEligibilityFiltersAliasesQuarantineLifecycleStaleAndDeletionOnly(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-eligibility", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-eligibility-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-eligibility-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	createFixture := func(key, content string, item EvidenceInput) EvidenceFragment {
		item.Content = content
		result, err := ledger.CreateIngest(ctx, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: sha256Hex(content),
			Evidence: []EvidenceInput{item},
		})
		require.NoError(t, err)
		fragment := requireTestEvidenceFragment(t, result)
		document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
			SourceVersion: 1, DocumentText: content,
		})
		require.NoError(t, err)
		completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})
		return fragment
	}

	eligible := createFixture("eligibility-good", "Eligible evidence remains discoverable.", EvidenceInput{
		InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
	})
	canonical := createFixture("eligibility-canonical", "Alias canonical evidence.", EvidenceInput{
		ForceInsert: true, InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
	})
	alias := createFixture("eligibility-alias", "Alias canonical evidence.", EvidenceInput{
		ForceInsert: true, InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
	})
	quarantined := createFixture("eligibility-quarantine", "Quarantined evidence.", EvidenceInput{
		InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
	})
	var quarantineIngestID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT ingest_id::text FROM evidence_fragments WHERE team_id = ?::uuid AND fragment_id = ?::uuid`, teamID, quarantined.FragmentID).Scan(&quarantineIngestID).Error
	}))
	_, err := ledger.AppendSecurityEvent(ctx, SecurityEventInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: quarantineIngestID,
		FragmentID:         quarantined.FragmentID,
		SecurityEventDraft: SecurityEventDraft{EventKind: "reviewer_signal", Decision: "quarantine", Reason: "eligibility test"},
	})
	require.NoError(t, err)
	lifecycle := createFixture("eligibility-lifecycle", "Retracted evidence.", EvidenceInput{
		InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
	})
	_, err = ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, EvidenceIDs: []string{lifecycle.FragmentID},
		Reason: "eligibility test", IdempotencyKey: "eligibility-lifecycle-retract", RequestHash: "eligibility-lifecycle-retract-hash",
	})
	require.NoError(t, err)
	deletionOnly := createFixture("eligibility-deletion-only", "Deletion-only evidence.", EvidenceInput{
		InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
	})
	// The ingestion metadata marker is the same deletion-only signal used by
	// conflict cleanup and must exclude the fragment from discovery.
	var deletionIngestID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT ingest_id::text FROM evidence_fragments WHERE team_id = ?::uuid AND fragment_id = ?::uuid`, teamID, deletionOnly.FragmentID).Scan(&deletionIngestID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE knowledge_ingests SET metadata = jsonb_build_object('conflict_resolution_deletion_only', true) WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, deletionIngestID).Error
	}))
	stale := createFixture("eligibility-stale-v1", "Stale source revision evidence.", EvidenceInput{
		SourceKey: "eligibility-stale-source", SourceRevisionToken: "revision-one",
		InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
	})
	_, err = ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "eligibility-stale-v2", RequestHash: "eligibility-stale-v2-hash",
		Evidence: []EvidenceInput{{Content: "Current source revision evidence.", SourceKey: "eligibility-stale-source", SourceRevisionToken: "revision-two", ExpectedPreviousRevisionToken: "revision-one", InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"}}},
	})
	require.NoError(t, err)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_exact_aliases (
				team_id, alias_fragment_id, alias_owner_profile_id,
				canonical_fragment_id, canonical_owner_profile_id, space_id, space_generation
			) VALUES (?, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			          dense_mem_team_shared_space(?::uuid), dense_mem_team_shared_generation(?::uuid))
		`, teamID, alias.FragmentID, ownerID, canonical.FragmentID, ownerID, teamID, teamID).Error
	}))
	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	ids := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		ids[target.Target.EvidenceID] = struct{}{}
	}
	_, eligibleFound := ids[eligible.FragmentID]
	require.True(t, eligibleFound)
	for _, excluded := range []EvidenceFragment{alias, quarantined, lifecycle, deletionOnly, stale} {
		_, found := ids[excluded.FragmentID]
		require.False(t, found, "ineligible evidence %s was selected", excluded.FragmentID)
	}

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE teams SET status = 'archived' WHERE id = ?::uuid`, teamID).Error
	}))
	targets, err = semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Empty(t, targets, "archived teams must not provide evidence targets")
	_, err = semantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, RunDate: "2026-09-04", WindowKey: "hour:2026-09-04T06",
		Lane: "evidence_discovery", LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.ErrorIs(t, err, ErrTeamInactive)
	var evidenceRuns int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT COUNT(*) FROM dream_cycle_runs WHERE team_id = ?::uuid AND lane = 'evidence_discovery'`, teamID).Row().Scan(&evidenceRuns)
	}))
	require.Zero(t, evidenceRuns, "inactive teams must not create evidence-lane runs")
}

func TestEvidenceDiscoveryRecoveryClaimsOnlyItsLane(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-recovery-team")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-recovery-owner")
	semantic := NewSemanticRepository(appDB, rls)
	for _, lane := range []string{"graph", "evidence_discovery"} {
		scheduledFor := time.Now().UTC().Add(-time.Minute)
		_, err := semantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
			TeamID: teamID, InitiatedByProfileID: profileID, RunDate: "2026-09-04",
			WindowKey: lane + ":expired", Lane: domain.DreamLane(lane), ScheduledFor: &scheduledFor,
			LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(-time.Minute),
		})
		require.NoError(t, err)
	}
	evidenceRun, err := semantic.ClaimRecoverableScheduledDreamCycle(ctx, DreamCycleRecoveryClaimInput{
		TeamID: teamID, Lane: "evidence_discovery", LeaseToken: uuid.NewString(),
		LeaseUntil: time.Now().UTC().Add(time.Minute), MaxAttempts: 5,
	})
	require.NoError(t, err)
	require.NotNil(t, evidenceRun)
	require.Equal(t, "evidence_discovery", string(evidenceRun.Lane))
	require.True(t, evidenceRun.Claimed)
	graphRun, err := semantic.ClaimRecoverableScheduledDreamCycle(ctx, DreamCycleRecoveryClaimInput{
		TeamID: teamID, Lane: "graph", LeaseToken: uuid.NewString(),
		LeaseUntil: time.Now().UTC().Add(time.Minute), MaxAttempts: 5,
	})
	require.NoError(t, err)
	require.NotNil(t, graphRun)
	require.Equal(t, "graph", string(graphRun.Lane))
	require.True(t, graphRun.Claimed)
}

func documentVectorsForTeam(all map[string][]float32, teamID string, db *gorm.DB, rls interface {
	WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
}) map[string][]float32 {
	// Search document IDs are globally opaque to this test. Select only the IDs
	// owned by the requested team before completing embeddings.
	filtered := map[string][]float32{}
	_ = rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT search_document_id::text FROM search_documents WHERE team_id = ?::uuid AND search_document_id = ANY(?::uuid[])`, teamID, pq.Array(stringKeys(all))).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			filtered[id] = all[id]
		}
		return rows.Err()
	})
	return filtered
}
