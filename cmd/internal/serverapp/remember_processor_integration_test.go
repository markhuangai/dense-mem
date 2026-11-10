//go:build integration

package serverapp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	rememberprocessor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	rememberProcessorIntegrationRole     = "densemem_processor_test"
	rememberProcessorIntegrationPassword = "densemem_processor_test"
)

func TestEvidenceOnlyPostgresRetiredRememberReceipts(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID, ownerID := uuid.New(), uuid.New()
	require.NoError(t, accesspostgres.NewTeamRepository(adminDB, rls).Create(ctx, &domain.Team{ID: teamID, Name: "retired-" + uuid.NewString()}))
	require.NoError(t, accesspostgres.NewCredentialRepository(adminDB, rls, nil).CreateCredential(ctx, &domain.Credential{
		ID: ownerID, TeamID: teamID, Name: "retired owner", KeyHash: "synthetic", KeyPrefix: strings.ReplaceAll(ownerID.String(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"},
	}))
	actorCtx := rememberProcessorActorContext(t, ctx, adminDB, rls, teamID, ownerID)
	ledger := knowledgepostgres.NewStore(appDB, rls)
	transcript, records := captureOntologyPublicationConsole(t, func() {
		logger := observability.NewWithProtector(slog.LevelDebug, observability.NewCredentialProtector("retirement-credential-canary"))
		service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{Ledger: ledger, Logger: logger})})
		for _, version := range []string{"dense-mem.v2.7.0", "dense-mem.v2.6.8", "dense-mem.v2.6.3", "remember_request_hash_v1"} {
			for _, outcome := range []string{"completed", "rejected", "quarantined", "failed"} {
				t.Run(version+"/"+outcome, func(t *testing.T) {
					key, id, evidenceID := "retired-"+uuid.NewString(), uuid.NewString(), uuid.NewString()
					saved := map[string]any{"contract_version": version, "processing_state": outcome, "evidence": []any{map[string]any{"evidence_id": evidenceID}}}
					require.NoError(t, ledger.RecordRememberAttempt(actorCtx, knowledgecontract.RememberAttemptRecordInput{
						TeamID: teamID.String(), OwnerProfileID: ownerID.String(), AttemptID: id, IdempotencyKey: key, RequestHash: "original-graph-hash-retirement-credential-canary", ContractVersion: version, SubmissionKind: "remember", Outcome: outcome, RetryabilitySet: true, PublicResult: saved,
					}))
					request := rememberapp.RememberRequest{TargetSpace: domain.MemorySpaceTeamShared, IdempotencyKey: key, Evidence: []rememberapp.RememberEvidenceInput{{Content: "Exact current Evidence must not duplicate a retired operation."}}}
					for range 2 {
						result, err := service.Remember(actorCtx, request)
						require.Nil(t, result)
						var failure *rememberapp.RememberProcessError
						require.ErrorAs(t, err, &failure)
						require.Len(t, failure.Status.Errors, 1)
						require.Equal(t, "retired_contract", failure.Status.Errors[0].Code)
						require.False(t, failure.Status.Errors[0].Retryable)
					}
					require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
						var hash, storedVersion, storedOutcome string
						var raw []byte
						if err := tx.Raw(`SELECT request_hash,contract_version,outcome,public_result FROM remember_attempts WHERE team_id=? AND attempt_id=?`, teamID, id).Row().Scan(&hash, &storedVersion, &storedOutcome, &raw); err != nil {
							return err
						}
						require.Equal(t, "original-graph-hash-retirement-credential-canary", hash)
						require.Equal(t, version, storedVersion)
						require.Equal(t, outcome, storedOutcome)
						expected, err := json.Marshal(saved)
						require.NoError(t, err)
						require.JSONEq(t, string(expected), string(raw))
						return nil
					}))
				})
			}
		}
		assertRememberProcessorIntegrationCanonicalCounts(t, adminDB, rls, teamID, 0)
	})
	require.NotContains(t, transcript, "retirement-credential-canary")
	retired := 0
	for _, record := range records {
		if record["msg"] != "memory_contract_retired" {
			continue
		}
		retired++
		require.Equal(t, "WARN", record["level"])
		require.Equal(t, teamID.String(), record["team_id"])
		require.Equal(t, ownerID.String(), record["owner_profile_id"])
		require.NotEmpty(t, record["attempt_id"])
		require.NotEmpty(t, record["idempotency_key"])
		require.Contains(t, []string{"completed", "rejected", "quarantined", "failed"}, record["original_outcome"])
		require.Equal(t, "original-graph-hash-[REDACTED]", record["original_request_hash"])
		require.Equal(t, domain.ContractVersion, record["contract_version"])
		require.Equal(t, "factual_graph_removed", record["reason"])
		require.Equal(t, "retired_contract", record["outcome"])
	}
	require.Equal(t, 32, retired)

}

func TestRememberServiceRejectsMigratedAttemptThroughPostgres(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.New()
	ownerID := uuid.New()
	teamName := "remember migrated conflict " + uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, teamName).Error
	}))
	credential := &domain.Credential{
		ID: ownerID, TeamID: teamID, Name: "remember migrated conflict owner",
		KeyHash: "remember-migrated-conflict-hash", KeyPrefix: strings.ReplaceAll(teamID.String(), "-", "")[:24],
		KeySuffix: "owner", Scopes: []string{"read", "write"},
	}
	require.NoError(t, accesspostgres.NewCredentialRepository(adminDB, rls, nil).CreateCredential(ctx, credential))

	ledger := knowledgepostgres.NewStore(appDB, rls)
	evidence := []rememberapp.RememberEvidenceInput{{Content: "A migrated Remember attempt."}}
	key := "migrated-conflict-" + uuid.NewString()
	requestHash, err := rememberapp.CanonicalRequestBodyHash(evidence)
	require.NoError(t, err)
	attemptID := uuid.New()
	require.NoError(t, ledger.RecordRememberAttempt(ctx, knowledgecontract.RememberAttemptRecordInput{
		TeamID: teamID.String(), OwnerProfileID: ownerID.String(), AttemptID: attemptID.String(),
		IdempotencyKey: key, RequestHash: requestHash, ContractVersion: "remember_request_hash_v1",
		SubmissionKind: "remember", Outcome: "completed", PublicResult: map[string]any{
			"contract_version": "dense-mem.v2.6.1", "submission_id": attemptID.String(),
			"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
			"correlation_id": attemptID.String(), "evidence": []any{map[string]any{
				"disposition": "stored", "evidence_id": uuid.NewString(), "evidence_index": 0,
				"superseded_evidence_ids": []any{}, "search_state": "current",
			}}, "relationship_results": []any{}, "errors": []any{},
		},
	}))

	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: ledger, Assessor: nil, EmbeddingExecutor: nil, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Metrics: observability.NoopDiscoverabilityMetrics(),
	})
	service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
	actorCtx := rememberProcessorActorContext(t, ctx, adminDB, rls, teamID, ownerID)

	result, err := service.Remember(actorCtx, rememberapp.RememberRequest{TargetSpace: domain.MemorySpaceTeamShared, Evidence: evidence, IdempotencyKey: key})
	require.Nil(t, result)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberRetiredContract)
	require.Equal(t, "retired_contract", processErr.Status.Errors[0].Code)
	require.Equal(t, "failed", processErr.Status.ProcessingState)

	var totalCount int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*) FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		`, teamID, ownerID, key).Scan(&totalCount).Error
	}))
	require.Equal(t, 1, totalCount)

	for _, testCase := range []struct {
		name               string
		cause              error
		recordFailureDelay time.Duration
	}{
		{name: "duplicate_candidate", cause: knowledgepostgres.ErrRememberDuplicateCandidateStale, recordFailureDelay: 2300 * time.Millisecond},
		{name: "exact_reference", cause: knowledgecontract.ErrRememberExactReferenceStale},
		{name: "evidence_conflict", cause: knowledgepostgres.ErrEvidenceConflictStaleInput},
	} {
		t.Run("stale commit replay/"+testCase.name, func(t *testing.T) {
			realLedger := knowledgepostgres.NewStore(appDB, rls)
			ledger := newRememberProcessorIntegrationStaleLedger(realLedger, testCase.cause)
			ledger.recordFailureDelay = testCase.recordFailureDelay
			processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
				Ledger: ledger, Assessor: &rememberProcessorIntegrationRegistrationAssessor{},
				EmbeddingExecutor: semanticwrite.NewProviderExecutor(rememberProcessorIntegrationEmbedder{}), Limits: assessor.DefaultSemanticAssessmentLimits(),
				Metrics: observability.NoopDiscoverabilityMetrics(), IsStaleInput: rememberapp.IsRememberStaleInputError,
			})
			service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
			evidence := []rememberapp.RememberEvidenceInput{{Content: "A stale Remember commit must be replayable."}}
			request := rememberapp.RememberRequest{
				TargetSpace: domain.MemorySpaceTeamShared,
				Evidence:    evidence, IdempotencyKey: "stale-commit-" + testCase.name + "-" + uuid.NewString(),
			}

			firstResult, firstErr := service.Remember(actorCtx, request)
			require.Nil(t, firstResult)
			var firstProcessErr *rememberapp.RememberProcessError
			require.ErrorAs(t, firstErr, &firstProcessErr)
			require.ErrorIs(t, firstErr, rememberapp.ErrRememberStaleInput)
			require.ErrorIs(t, firstErr, testCase.cause)
			require.NotContains(t, firstErr.Error(), testCase.cause.Error())
			require.Equal(t, string(rememberapp.SubmissionErrorStaleInput), firstProcessErr.Status.Errors[0].Code)
			require.False(t, firstProcessErr.Status.Errors[0].Retryable)
			require.Equal(t, "failed", firstProcessErr.Status.ProcessingState)
			require.Equal(t, "not_required", firstProcessErr.Status.SearchState)
			require.Len(t, firstProcessErr.Status.Evidence, 1)
			require.Equal(t, "stale_input", firstProcessErr.Status.Evidence[0].Reason)
			require.Equal(t, 1, ledger.commitCalls)

			persisted, loadErr := ledger.LoadRememberAttempt(ctx, knowledgecontract.RememberAttemptLookupInput{
				TeamID: teamID.String(), OwnerProfileID: ownerID.String(), IdempotencyKey: request.IdempotencyKey,
			})
			require.NoError(t, loadErr)
			require.Equal(t, "failed", persisted.Outcome)
			require.False(t, persisted.Retryable)
			require.Equal(t, string(rememberapp.SubmissionErrorStaleInput), persisted.PublicResult["errors"].([]any)[0].(map[string]any)["code"])

			replayResult, replayErr := service.Remember(actorCtx, request)
			require.Nil(t, replayResult)
			var replayProcessErr *rememberapp.RememberProcessError
			require.ErrorAs(t, replayErr, &replayProcessErr)
			require.ErrorIs(t, replayErr, rememberapp.ErrRememberPersistence)
			require.Equal(t, firstProcessErr.Status, replayProcessErr.Status)
			require.Equal(t, firstProcessErr.Result, replayProcessErr.Result)
			require.Equal(t, 1, ledger.commitCalls, "a terminal stale failure must replay without another commit")
		})
	}
}

type rememberProcessorIntegrationRegistrationAssessor struct{ calls, repairs int }

func (p *rememberProcessorIntegrationRegistrationAssessor) Assess(
	_ context.Context, request assessor.SemanticAssessmentRequest,
) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	p.calls++
	response := rememberProcessorIntegrationAssessmentResponse(request)
	return rememberProcessorIntegrationAssessmentSession{}, assessor.SemanticAssessmentTurn{Response: response, Turn: 1}, nil
}

func (p *rememberProcessorIntegrationRegistrationAssessor) Repair(
	context.Context, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRepairRequest,
) (assessor.SemanticAssessmentTurn, error) {
	p.repairs++
	return assessor.SemanticAssessmentTurn{}, fmt.Errorf("valid Evidence response unexpectedly required repair")
}

func (*rememberProcessorIntegrationRegistrationAssessor) ModelName() string {
	return "remember-integration-evidence-assessor"
}

var _ assessor.Provider = (*rememberProcessorIntegrationRegistrationAssessor)(nil)

type rememberProcessorIntegrationSearchEmbedder struct {
	model      string
	dimensions int
}

func (e rememberProcessorIntegrationSearchEmbedder) Embed(context.Context, string) ([]float32, string, error) {
	vector := make([]float32, e.dimensions)
	vector[0] = 1
	return vector, e.model, nil
}

func (e rememberProcessorIntegrationSearchEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	vectors := make([][]float32, len(texts))
	for index := range vectors {
		vectors[index] = make([]float32, e.dimensions)
		vectors[index][0] = 1
	}
	return vectors, e.model, nil
}

func (e rememberProcessorIntegrationSearchEmbedder) ModelName() string { return e.model }
func (e rememberProcessorIntegrationSearchEmbedder) Dimensions() int   { return e.dimensions }
func (rememberProcessorIntegrationSearchEmbedder) IsAvailable() bool   { return true }

var _ embeddingcontract.EmbeddingProviderInterface = rememberProcessorIntegrationSearchEmbedder{}

func ensureRememberProcessorIntegrationSearchContract(
	t *testing.T, adminDB *gorm.DB, rls *storagepostgres.RLS,
) (string, int) {
	t.Helper()
	var model string
	var dimensions int
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		lookup := func() error {
			return tx.Raw(`
				SELECT contract.model, contract.dimensions
				FROM search_index_generations AS generation
				JOIN embedding_contracts AS contract
				  ON contract.embedding_contract_id = generation.embedding_contract_id
				WHERE generation.activation_state = 'active'
				  AND contract.lifecycle_state = 'active'
				  AND contract.distance_metric = 'cosine'
				ORDER BY contract.version DESC, generation.generation DESC, generation.created_at DESC
				LIMIT 1
			`).Row().Scan(&model, &dimensions)
		}
		if err := lookup(); err == nil {
			return nil
		} else if err != sql.ErrNoRows {
			return err
		}
		contractID, generationID := uuid.New(), uuid.New()
		if err := tx.Exec(`
			INSERT INTO embedding_contracts (
			    embedding_contract_id, contract_key, version, provider, model,
			    dimensions, distance_metric, vector_normalization,
			    document_format_version, query_format_version, lifecycle_state
			) VALUES (?::uuid, ?, 1, 'test', 'remember-integration-embedding',
			          1, 'cosine', 'provider', 1, 1, 'active')
		`, contractID, "remember-predicate-drift-"+uuid.NewString()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO search_index_generations (
			    search_index_generation_id, generation, embedding_contract_id,
			    embedding_dimensions, ann_strategy, operator_class,
			    indexed_expression, physical_index_name, exact_max_rows,
			    allow_exact_fallback, activation_state, activated_at
			) VALUES (?::uuid, 1, ?::uuid, 1, 'exact', '', '', '', 10000, false, 'active', now())
		`, generationID, contractID).Error; err != nil {
			return err
		}
		return lookup()
	}))
	require.NotEmpty(t, model)
	require.Positive(t, dimensions)
	return model, dimensions
}

func assertRememberProcessorIntegrationCanonicalCounts(
	t *testing.T, adminDB *gorm.DB, rls *storagepostgres.RLS, teamID uuid.UUID, want int64,
) {
	t.Helper()
	for _, table := range []string{
		"knowledge_ingests", "evidence_fragments", "semantic_assessments",
	} {
		var count int64
		require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
			return tx.Table(table).Where("team_id = ?::uuid", teamID).Count(&count).Error
		}))
		require.Equal(t, want, count, table)
	}
	var documents int64
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		return tx.Table("search_documents").Where("team_id = ?::uuid", teamID).Count(&documents).Error
	}))
	if want == 0 {
		require.Zero(t, documents)
	} else {
		require.Positive(t, documents)
	}
}

type rememberProcessorIntegrationStaleLedger struct {
	remembercontract.Persistence
	stale              error
	contractID         string
	model              string
	generationID       string
	commitCalls        int
	recordFailureDelay time.Duration
}

func newRememberProcessorIntegrationStaleLedger(base remembercontract.Persistence, stale error) *rememberProcessorIntegrationStaleLedger {
	return &rememberProcessorIntegrationStaleLedger{
		Persistence: base, stale: stale,
		contractID:   "11111111-1111-1111-1111-111111111111",
		model:        "remember-integration-embedding",
		generationID: "22222222-2222-2222-2222-222222222222",
	}
}

func (l *rememberProcessorIntegrationStaleLedger) PlanRememberDuplicateEmbeddings(_ context.Context, input knowledgecontract.RememberDuplicateCandidateInput) (*knowledgecontract.RememberDuplicateEmbeddingPlan, error) {
	plan := &knowledgecontract.RememberDuplicateEmbeddingPlan{
		Documents: []knowledgecontract.SearchDocumentForEmbedding{}, EmbeddingContractID: l.contractID,
		EmbeddingDimensions: 1, EmbeddingModel: l.model, SearchIndexGenerationID: l.generationID, IndexGeneration: 1,
	}
	seen := make(map[string]struct{}, len(input.Evidence))
	for _, evidence := range input.Evidence {
		if evidence.ForceInsert {
			continue
		}
		hash := rememberProcessorIntegrationDocumentHash(evidence.Content)
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		plan.Documents = append(plan.Documents, knowledgecontract.SearchDocumentForEmbedding{
			SearchDocumentResult: knowledgecontract.SearchDocumentResult{
				TeamID: input.TeamID, SearchDocumentID: "duplicate:" + evidence.FragmentID,
				OwnerProfileID: input.OwnerProfileID, SourceKind: "evidence", SourceID: evidence.FragmentID,
				SourceVersion: 1, ProjectionFormat: 1, EmbeddingContractID: l.contractID, EmbeddingDimensions: 1,
				SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
			},
			DocumentText: strings.TrimSpace(evidence.Content), DocumentHash: hash,
		})
	}
	return plan, nil
}

func (l *rememberProcessorIntegrationStaleLedger) ResolveRememberDuplicateCandidates(_ context.Context, input knowledgecontract.RememberDuplicateCandidateInput, _ []knowledgecontract.InlineEmbeddingResult) (*knowledgecontract.RememberDuplicateResolutionResult, error) {
	result := &knowledgecontract.RememberDuplicateResolutionResult{
		Exact: make([]knowledgecontract.RememberDuplicateResolution, len(input.Evidence)), Candidates: []knowledgecontract.RememberDuplicateCandidateGroup{},
	}
	for index, evidence := range input.Evidence {
		result.Exact[index] = knowledgecontract.RememberDuplicateResolution{
			EvidenceIndex: index, EvidenceID: fmt.Sprintf("evidence:%d", index), InputFragmentID: evidence.FragmentID, Disposition: "new",
		}
		if !evidence.ForceInsert {
			result.Candidates = append(result.Candidates, knowledgecontract.RememberDuplicateCandidateGroup{
				EvidenceIndex: index, EvidenceID: fmt.Sprintf("evidence:%d", index), Candidates: []knowledgecontract.RememberDuplicateCandidate{},
			})
		}
	}
	return result, nil
}

func (l *rememberProcessorIntegrationStaleLedger) PlanRememberEmbeddings(_ context.Context, _ knowledgecontract.SynchronousRememberCommitInput) (*knowledgecontract.InlineEmbeddingPlan, error) {
	return &knowledgecontract.InlineEmbeddingPlan{
		Documents: []knowledgecontract.SearchDocumentForEmbedding{}, EmbeddingContractID: l.contractID,
		EmbeddingDimensions: 1, EmbeddingModel: l.model, SearchIndexGenerationID: l.generationID, IndexGeneration: 1,
	}, nil
}

func (l *rememberProcessorIntegrationStaleLedger) CommitRememberWithEmbeddings(context.Context, knowledgecontract.SynchronousRememberCommitInput, []knowledgecontract.InlineEmbeddingResult) (*knowledgecontract.SynchronousRememberCommitResult, error) {
	l.commitCalls++
	return nil, l.stale
}

func (l *rememberProcessorIntegrationStaleLedger) RecordRememberFailure(ctx context.Context, input knowledgecontract.RememberFailureRecordInput) error {
	if l.recordFailureDelay > 0 {
		timer := time.NewTimer(l.recordFailureDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return l.Persistence.RecordRememberFailure(ctx, input)
}

func rememberProcessorIntegrationDocumentHash(content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(sum[:])
}

type rememberProcessorIntegrationAssessmentSession struct{}

func (rememberProcessorIntegrationAssessmentSession) SessionID() string {
	return "remember-integration-assessment"
}

type rememberProcessorIntegrationAssessor struct{}

func (rememberProcessorIntegrationAssessor) Assess(_ context.Context, request assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	return rememberProcessorIntegrationAssessmentSession{}, assessor.SemanticAssessmentTurn{
		Response: rememberProcessorIntegrationAssessmentResponse(request), Turn: 1,
	}, nil
}

func (rememberProcessorIntegrationAssessor) Repair(_ context.Context, _ assessor.SemanticAssessmentSession, request assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	return assessor.SemanticAssessmentTurn{Response: rememberProcessorIntegrationAssessmentResponse(request.Request), Turn: 2}, nil
}

func (rememberProcessorIntegrationAssessor) ModelName() string {
	return "remember-integration-assessor"
}

func rememberProcessorIntegrationAssessmentResponse(request assessor.SemanticAssessmentRequest) assessor.SemanticAssessmentResponse {
	response := assessor.SemanticAssessmentResponse{
		RequestID: request.RequestID, EvidenceSecurityResults: []assessor.SemanticAssessmentEvidenceSecurityResult{},
		EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{},
		EvidenceConflictResults:    []assessor.SemanticAssessmentEvidenceConflictResult{},
	}
	for _, evidence := range request.Evidence {
		response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, assessor.SemanticAssessmentEvidenceSecurityResult{
			EvidenceID: evidence.EvidenceID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{},
		})
	}
	for _, group := range request.EvidenceEquivalenceCandidates {
		response.EvidenceEquivalenceResults = append(response.EvidenceEquivalenceResults, assessor.SemanticAssessmentEvidenceEquivalenceResult{
			EvidenceID: group.EvidenceID, Action: "new",
		})
	}
	return response
}

var _ assessor.Provider = rememberProcessorIntegrationAssessor{}

type rememberProcessorIntegrationEmbedder struct{}

func (rememberProcessorIntegrationEmbedder) Embed(context.Context, string) ([]float32, string, error) {
	return []float32{1}, "remember-integration-embedding", nil
}

func (rememberProcessorIntegrationEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	vectors := make([][]float32, len(texts))
	for index := range vectors {
		vectors[index] = []float32{1}
	}
	return vectors, "remember-integration-embedding", nil
}

func (rememberProcessorIntegrationEmbedder) ModelName() string {
	return "remember-integration-embedding"
}
func (rememberProcessorIntegrationEmbedder) Dimensions() int   { return 1 }
func (rememberProcessorIntegrationEmbedder) IsAvailable() bool { return true }

var _ embeddingcontract.EmbeddingProviderInterface = rememberProcessorIntegrationEmbedder{}

func insertHistoricalRememberOutcome(
	t *testing.T,
	ctx context.Context,
	adminDB, appDB *gorm.DB,
	rls *storagepostgres.RLS,
	teamID, ownerID, attemptID uuid.UUID,
	key, requestHash, outcome string,
) {
	t.Helper()
	ledger := knowledgepostgres.NewStore(appDB, rls)
	if outcome != "replayed" {
		require.NoError(t, ledger.RecordRememberAttempt(ctx, knowledgecontract.RememberAttemptRecordInput{
			TeamID: teamID.String(), OwnerProfileID: ownerID.String(), AttemptID: attemptID.String(),
			IdempotencyKey: key, RequestHash: requestHash, ContractVersion: domain.ContractVersion,
			SubmissionKind: "remember", Outcome: outcome, ErrorCode: "historical_" + outcome,
			PublicResult: map[string]any{"processing_state": "failed"},
		}))
		return
	}

	canonicalID := uuid.New()
	require.NoError(t, ledger.RecordRememberAttempt(ctx, knowledgecontract.RememberAttemptRecordInput{
		TeamID: teamID.String(), OwnerProfileID: ownerID.String(), AttemptID: canonicalID.String(),
		IdempotencyKey: key, RequestHash: requestHash, ContractVersion: domain.ContractVersion,
		SubmissionKind: "remember", Outcome: "completed",
		PublicResult: map[string]any{"processing_state": "completed"},
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
				contract_version, submission_kind, outcome, canonical_attempt_id, public_result, completed_at
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, ?, 'remember', 'replayed', ?::uuid, '{}'::jsonb, now())
		`, teamID, attemptID, ownerID, key, requestHash, domain.ContractVersion, canonicalID).Error
	}))
}

func rememberProcessorActorContext(t *testing.T, ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS, teamID, ownerID uuid.UUID) context.Context {
	t.Helper()
	key, err := accesspostgres.NewCredentialRepository(db, rls, nil).GetByIDForTeam(ctx, teamID, ownerID)
	require.NoError(t, err)
	return requestctx.WithActor(ctx, requestctx.Actor{TeamID: teamID, OwnerID: key.OwnerID, IdentityID: key.ActorIdentityID, MembershipID: key.MembershipID, CredentialID: &key.ID, AuthMethod: "api_key", Role: key.Role, Grants: key.Scopes, AllowedSpaces: key.MemorySpaces()})
}

func setupRememberProcessorIntegrationDB(t *testing.T) (*gorm.DB, *gorm.DB, *storagepostgres.RLS, func()) {
	t.Helper()
	dsn := strings.TrimSpace(storagepostgres.GetTestDSN())
	if dsn == "" {
		if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
			t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 for disposable Remember processor PostgreSQL tests")
		}
		withLifecyclePostgres(t, func(t *testing.T) { dsn = os.Getenv("POSTGRES_DSN") })
	} else if os.Getenv("DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS") != "1" {
		t.Skip("set DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS=1 to test an existing DATABASE_URL")
	}
	adminDB, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	migrator, err := storagepostgres.NewMigrator(adminDB)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))
	rls := storagepostgres.NewRLS()
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		return tx.Exec(fmt.Sprintf(`
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN
					CREATE ROLE %[1]s LOGIN PASSWORD '%[2]s' NOSUPERUSER NOBYPASSRLS;
				ELSE
					ALTER ROLE %[1]s WITH LOGIN PASSWORD '%[2]s' NOSUPERUSER NOBYPASSRLS;
				END IF;
			END $$;
			GRANT USAGE ON SCHEMA public TO %[1]s;
			GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %[1]s;
			GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %[1]s;
			GRANT EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID, UUID), dense_mem_lock_memory_space(UUID, UUID) TO %[1]s;
		`, rememberProcessorIntegrationRole, rememberProcessorIntegrationPassword)).Error
	}))
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	if parsed.Scheme == "" || parsed.Host == "" {
		t.Skip("Remember processor integration tests require DATABASE_URL in URL form")
	}
	parsed.User = url.UserPassword(rememberProcessorIntegrationRole, rememberProcessorIntegrationPassword)
	appDB, err := gorm.Open(gormpostgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	cleanup := func() {
		if sqlDB, err := appDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
			return tx.Exec(fmt.Sprintf(`
				REASSIGN OWNED BY %[1]s TO CURRENT_USER;
				DROP OWNED BY %[1]s;
				DROP ROLE IF EXISTS %[1]s;
			`, rememberProcessorIntegrationRole)).Error
		})
		if sqlDB, err := adminDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	return adminDB, appDB, rls, cleanup
}
