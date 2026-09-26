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
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

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
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const (
	rememberProcessorIntegrationRole     = "densemem_processor_test"
	rememberProcessorIntegrationPassword = "densemem_processor_test"
)

func TestRememberServiceRejectsHistoricalOutcomesThroughPostgres(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.New()
	ownerID := uuid.New()
	teamName := "remember historical outcomes " + uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, teamName).Error
	}))
	credential := &domain.Credential{
		ID: ownerID, TeamID: teamID, Name: "remember historical outcomes owner",
		KeyHash: "remember-historical-outcomes-hash", KeyPrefix: strings.ReplaceAll(teamID.String(), "-", "")[:24],
		KeySuffix: "owner", Scopes: []string{"read", "write"},
	}
	require.NoError(t, accesspostgres.NewCredentialRepository(adminDB, rls, nil).CreateCredential(ctx, credential))

	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgecontract.ConflictRuntimeConfig{})
	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: ledger, Assessor: nil, Embedder: nil, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Metrics: observability.NoopDiscoverabilityMetrics(),
	})
	service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, OwnerID: ownerID, Role: "member", AuthMethod: "api_key",
		Grants: []string{"read", "write"},
	})

	for _, outcome := range []string{"rejected", "quarantined", "replayed"} {
		t.Run(outcome, func(t *testing.T) {
			key := "historical-" + outcome + "-" + uuid.NewString()
			evidence := []rememberapp.RememberEvidenceInput{{Content: "A retained historical Remember result."}}
			req := rememberapp.RememberRequest{Evidence: evidence, IdempotencyKey: key}
			hash, err := rememberapp.CanonicalRequestBodyHash(evidence, nil, nil)
			require.NoError(t, err)
			attemptID := uuid.New()
			insertHistoricalRememberOutcome(t, ctx, adminDB, appDB, rls, teamID, ownerID, attemptID, key, hash, outcome)

			result, err := service.Remember(actorCtx, req)
			require.Nil(t, result)
			var processErr *rememberapp.RememberProcessError
			require.ErrorAs(t, err, &processErr)
			require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
			require.NotNil(t, processErr.Status)
			require.Equal(t, string(rememberapp.SubmissionErrorIdempotencyConflict), processErr.Status.Errors[0].Code)
			require.Equal(t, "failed", processErr.Status.ProcessingState)
		})
	}
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

	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgecontract.ConflictRuntimeConfig{})
	evidence := []rememberapp.RememberEvidenceInput{{Content: "A migrated Remember attempt."}}
	key := "migrated-conflict-" + uuid.NewString()
	requestHash, err := rememberapp.CanonicalRequestBodyHash(evidence, nil, nil)
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
		Ledger: ledger, Assessor: nil, Embedder: nil, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Metrics: observability.NoopDiscoverabilityMetrics(),
	})
	service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, OwnerID: ownerID, Role: "member", AuthMethod: "api_key",
		Grants: []string{"read", "write"},
	})

	result, err := service.Remember(actorCtx, rememberapp.RememberRequest{Evidence: evidence, IdempotencyKey: key})
	require.Nil(t, result)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
	require.Equal(t, string(rememberapp.SubmissionErrorIdempotencyConflict), processErr.Status.Errors[0].Code)
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
		{name: "conflict_context", cause: knowledgepostgres.ErrConflictContextStale, recordFailureDelay: 2300 * time.Millisecond},
		{name: "exact_reference", cause: knowledgepostgres.ErrRememberExactReferenceStale},
		{name: "correction_target", cause: knowledgepostgres.ErrCorrectionTargetStale},
	} {
		t.Run("stale commit replay/"+testCase.name, func(t *testing.T) {
			realLedger := knowledgepostgres.NewStore(appDB, rls, knowledgecontract.ConflictRuntimeConfig{})
			ledger := newRememberProcessorIntegrationStaleLedger(realLedger, testCase.cause)
			ledger.recordFailureDelay = testCase.recordFailureDelay
			processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
				Ledger: ledger, Catalog: rememberProcessorIntegrationCatalog{}, Assessor: rememberProcessorIntegrationAssessor{},
				Embedder: rememberProcessorIntegrationEmbedder{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
				Metrics: observability.NoopDiscoverabilityMetrics(), IsStaleInput: rememberapp.IsRememberStaleInputError,
			})
			service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
			evidence := []rememberapp.RememberEvidenceInput{{Content: "A stale Remember commit must be replayable."}}
			request := rememberapp.RememberRequest{
				Evidence: evidence, IdempotencyKey: "stale-commit-" + testCase.name + "-" + uuid.NewString(),
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

func TestRememberPredicateRegistrationCatalogDriftThroughPostgres(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID, ownerID := uuid.New(), uuid.New()
	predicateKey := "run_owned_durable_memory_in"
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, "predicate drift "+uuid.NewString()).Error
	}))
	require.NoError(t, accesspostgres.NewCredentialRepository(adminDB, rls, nil).CreateCredential(ctx, &domain.Credential{
		ID: ownerID, TeamID: teamID, Name: "predicate drift owner",
		KeyHash: "predicate-drift-" + ownerID.String(), KeyPrefix: strings.ReplaceAll(ownerID.String(), "-", "")[:24],
		KeySuffix: "owner", Scopes: []string{"read", "write"}, Role: "member",
	}))
	model, dimensions := ensureRememberProcessorIntegrationSearchContract(t, adminDB, rls)

	store := knowledgepostgres.NewStore(appDB, rls, knowledgecontract.ConflictRuntimeConfig{})
	catalog := &rememberProcessorIntegrationDriftCatalog{SubmissionAssessmentCatalog: store}
	catalog.afterSuccessfulValidation = func(input knowledgecontract.SubmissionPredicateRegistrationValidationInput) error {
		if len(input.Registrations) != 1 || input.Registrations[0].PredicateKey != predicateKey {
			return fmt.Errorf("unexpected predicate registration at preflight: %+v", input.Registrations)
		}
		catalog.validations++
		if catalog.validations != 1 {
			return nil
		}
		return insertRememberProcessorIntegrationPredicate(t, adminDB, rls, teamID, predicateKey, 1, "retired")
	}
	provider := &rememberProcessorIntegrationRegistrationAssessor{}
	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: store, Catalog: catalog, Assessor: provider,
		Embedder: rememberProcessorIntegrationSearchEmbedder{model: model, dimensions: dimensions},
		Limits:   assessor.DefaultSemanticAssessmentLimits(), Metrics: observability.NoopDiscoverabilityMetrics(),
	})
	service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, OwnerID: ownerID, Role: "member", AuthMethod: "api_key",
		Grants: []string{"read", "write"},
	})
	request := rememberapp.RememberRequest{
		Evidence: []rememberapp.RememberEvidenceInput{{
			Content: "Dense-Mem stores durable memory in PostgreSQL.", SourceType: "manual", ForceInsert: true,
		}},
		RelationshipHints: []map[string]any{{
			"ref": "durable-store", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": predicateKey},
			"object":    map[string]any{"value": map[string]any{"type": "string", "value": "PostgreSQL"}},
			"polarity":  "+", "evidence_indices": []any{0},
		}},
		IdempotencyKey: "predicate-drift-" + uuid.NewString(),
	}

	first, firstErr := service.Remember(actorCtx, request)
	require.Nil(t, first)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, firstErr, &processErr)
	require.ErrorIs(t, firstErr, rememberapp.ErrRememberCommitConflict)
	require.NotNil(t, processErr.Status)
	require.Equal(t, 1, catalog.validations)
	require.Equal(t, 1, provider.calls)
	require.Equal(t, "failed", processErr.Status.ProcessingState)
	require.Equal(t, "not_required", processErr.Status.SearchState)
	require.Len(t, processErr.Status.Errors, 1)
	terminalError := processErr.Status.Errors[0]
	require.Equal(t, string(rememberapp.SubmissionErrorCommitConflict), terminalError.Code)
	require.Equal(t, "predicate_catalog_changed", terminalError.ReasonCode)
	require.True(t, terminalError.Retryable)
	require.Equal(t, string(rememberapp.SubmissionNextActionRetrySameRequest), terminalError.NextAction)
	require.Len(t, processErr.Status.Evidence, 1)
	require.Equal(t, "not_stored", processErr.Status.Evidence[0].Disposition)
	require.Len(t, processErr.Status.RelationshipResults, 1)
	require.Equal(t, "not_stored", processErr.Status.RelationshipResults[0].Disposition)
	assertRememberProcessorIntegrationCanonicalCounts(t, adminDB, rls, teamID, 0)

	attempt, err := store.LoadRememberAttempt(ctx, knowledgecontract.RememberAttemptLookupInput{
		TeamID: teamID.String(), OwnerProfileID: ownerID.String(), IdempotencyKey: request.IdempotencyKey,
	})
	require.NoError(t, err)
	require.Equal(t, "failed", attempt.Outcome)
	require.True(t, attempt.Retryable)
	require.Equal(t, "predicate_catalog_changed", attempt.PublicResult["errors"].([]any)[0].(map[string]any)["reason_code"])

	require.NoError(t, insertRememberProcessorIntegrationPredicate(t, adminDB, rls, teamID, predicateKey, 2, "active"))
	retried, retryErr := service.Remember(actorCtx, request)
	require.NoError(t, retryErr)
	require.NotNil(t, retried)
	require.Equal(t, "completed", retried.ProcessingState)
	require.Empty(t, retried.Errors)
	require.Equal(t, 2, provider.calls, "a retryable failure must reassess the same saved request")
	assertRememberProcessorIntegrationCanonicalCounts(t, adminDB, rls, teamID, 1)

	replayed, replayErr := service.Remember(actorCtx, request)
	require.NoError(t, replayErr)
	require.Equal(t, retried, replayed)
	require.Equal(t, 2, provider.calls, "a completed exact replay must not reassess or commit")
	assertRememberProcessorIntegrationCanonicalCounts(t, adminDB, rls, teamID, 1)

	for _, testCase := range []struct {
		name, code string
		cause      error
	}{
		{name: "database", code: string(rememberapp.SubmissionErrorDatabaseFailure), cause: errors.New("private catalog outage detail")},
		{name: "cancelled", code: string(rememberapp.SubmissionErrorRequestCancelled), cause: context.Canceled},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			validated := false
			catalog.afterSuccessfulValidation = func(input knowledgecontract.SubmissionPredicateRegistrationValidationInput) error {
				if len(input.Registrations) != 1 || input.Registrations[0].PredicateKey != predicateKey {
					return fmt.Errorf("unexpected predicate registration at preflight: %+v", input.Registrations)
				}
				validated = true
				return testCase.cause
			}
			failureRequest := request
			failureRequest.Evidence = []rememberapp.RememberEvidenceInput{request.Evidence[0]}
			failureRequest.Evidence[0].Content += " [" + testCase.name + "]"
			failureRequest.IdempotencyKey = "predicate-validation-" + testCase.name + "-" + uuid.NewString()
			callsBefore := provider.calls

			result, failure := service.Remember(actorCtx, failureRequest)
			require.Nil(t, result)
			var failed *rememberapp.RememberProcessError
			require.ErrorAs(t, failure, &failed)
			require.NotNil(t, failed.Status)
			require.True(t, validated, "the real PostgreSQL catalog must validate before the injected error")
			require.Equal(t, callsBefore+1, provider.calls)
			require.Zero(t, provider.repairs)
			require.Len(t, failed.Status.Errors, 1)
			publicError := failed.Status.Errors[0]
			require.Equal(t, testCase.code, publicError.Code)
			require.Equal(t, "remember_assessment_failed", publicError.ReasonCode)
			require.Equal(t, "remember.assessment", publicError.Details["component"])
			require.Equal(t, true, publicError.Details["server_owned"])
			require.True(t, publicError.Retryable)
			require.Equal(t, "failed", failed.Status.ProcessingState)
			require.Len(t, failed.Status.Evidence, 1)
			require.Equal(t, "not_stored", failed.Status.Evidence[0].Disposition)
			require.Len(t, failed.Status.RelationshipResults, 1)
			require.Equal(t, "not_stored", failed.Status.RelationshipResults[0].Disposition)
			publicJSON, err := json.Marshal(failed.Status)
			require.NoError(t, err)
			require.NotContains(t, string(publicJSON), "private catalog outage detail")

			var persisted struct {
				Phase string
				Code  string
				Turns int
			}
			var eventMetadata []byte
			var invocation struct {
				Phase string
				Code  string
			}
			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				if err := tx.Raw(`
					SELECT failed_phase, error_code, assessor_turns
					FROM remember_attempts
					WHERE team_id = ?::uuid AND attempt_id = ?::uuid
				`, teamID, failed.Status.SubmissionID).Row().Scan(&persisted.Phase, &persisted.Code, &persisted.Turns); err != nil {
					return err
				}
				if err := tx.Raw(`
					SELECT metadata FROM remember_attempt_events
					WHERE team_id = ?::uuid AND attempt_id = ?::uuid AND sequence_no = 1
				`, teamID, failed.Status.SubmissionID).Row().Scan(&eventMetadata); err != nil {
					return err
				}
				return tx.Raw(`
					SELECT failed_phase, error_code FROM remember_invocation_diagnostics
					WHERE team_id = ?::uuid AND canonical_attempt_id = ?::uuid
					  AND classification = 'execution'
				`, teamID, failed.Status.SubmissionID).Row().Scan(&invocation.Phase, &invocation.Code)
			}))
			require.Equal(t, "assessment", persisted.Phase)
			require.Equal(t, testCase.code, persisted.Code)
			require.Equal(t, 1, persisted.Turns)
			require.Equal(t, "assessment", invocation.Phase)
			require.Equal(t, testCase.code, invocation.Code)
			var event map[string]any
			require.NoError(t, json.Unmarshal(eventMetadata, &event))
			require.Equal(t, float64(1), event["assessor_turns"])
			require.Equal(t, testCase.code, event["error_code"])
			require.NotContains(t, event, "assessor_validation")
			require.NotContains(t, string(eventMetadata), "private catalog outage detail")
			assertRememberProcessorIntegrationCanonicalCounts(t, adminDB, rls, teamID, 1)
		})
	}
}

type rememberProcessorIntegrationDriftCatalog struct {
	remembercontract.SubmissionAssessmentCatalog
	afterSuccessfulValidation func(knowledgecontract.SubmissionPredicateRegistrationValidationInput) error
	validations               int
}

func (c *rememberProcessorIntegrationDriftCatalog) ValidateSubmissionPredicateRegistrations(
	ctx context.Context, input knowledgecontract.SubmissionPredicateRegistrationValidationInput,
) ([]knowledgecontract.SubmissionPredicateRegistrationIssue, error) {
	issues, err := c.SubmissionAssessmentCatalog.ValidateSubmissionPredicateRegistrations(ctx, input)
	if err != nil || len(issues) != 0 || c.afterSuccessfulValidation == nil {
		return issues, err
	}
	return nil, c.afterSuccessfulValidation(input)
}

type rememberProcessorIntegrationRegistrationAssessor struct{ calls, repairs int }

func (p *rememberProcessorIntegrationRegistrationAssessor) Assess(
	_ context.Context, request assessor.SemanticAssessmentRequest,
) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	p.calls++
	response := rememberProcessorIntegrationAssessmentResponse(request)
	for _, entity := range request.SubmittedEntities {
		if len(entity.Groundings) == 0 {
			return nil, assessor.SemanticAssessmentTurn{}, fmt.Errorf("submitted entity %q has no grounding", entity.Ref)
		}
		groundingRef := entity.Groundings[0].GroundingRef
		action := string(domain.EntityResolutionCreate)
		var candidateID *string
		if entity.KnownEntityID != "" {
			action = string(domain.EntityResolutionReuse)
			candidateID = &entity.KnownEntityID
		} else {
			for _, group := range request.EntityCandidateGroups {
				if group.GroundingRef != groundingRef || len(group.Candidates) != 1 || group.Candidates[0].Kind != entity.Kind {
					continue
				}
				action = string(domain.EntityResolutionReuse)
				candidateID = &group.Candidates[0].EntityID
				break
			}
		}
		response.EntityResults = append(response.EntityResults, assessor.SemanticAssessmentEntityResult{
			Ref: entity.Ref, GroundingRef: &groundingRef, Action: action, CandidateEntityID: candidateID,
		})
	}
	for _, relationship := range request.SubmittedRelationships {
		if len(relationship.EvidenceIDs) == 0 || len(request.Evidence) == 0 {
			return nil, assessor.SemanticAssessmentTurn{}, fmt.Errorf("submitted relationship %q has no evidence", relationship.Ref)
		}
		evidence := request.Evidence[0]
		startRef, startOK := assessor.SemanticAssessmentBoundaryRef(evidence, 0)
		endRef, endOK := assessor.SemanticAssessmentBoundaryRef(evidence, len([]rune(evidence.Content)))
		if !startOK || !endOK {
			return nil, assessor.SemanticAssessmentTurn{}, fmt.Errorf("evidence %q has no complete boundary range", evidence.EvidenceID)
		}
		rangeValue := assessor.SemanticAssessmentGroundedRange{
			EvidenceID: evidence.EvidenceID, StartRef: startRef, EndRef: endRef,
		}
		response.RelationshipResults = append(response.RelationshipResults, assessor.SemanticAssessmentRelationshipResult{
			Ref: relationship.Ref, Disposition: "stored", Splits: []assessor.SemanticAssessmentRelationshipSplit{{
				SplitIndex: 0, SubjectRef: relationship.SubjectRef, PredicateRange: rangeValue,
				PredicateStatus: "registration_required",
				PredicateRegistration: &assessor.SemanticAssessmentPredicateRegistration{
					PredicateKey: relationship.PredicateHint, RelationshipKind: "state", CurrentCardinality: "many",
				},
				ObjectRef: relationship.ObjectRef, ObjectValue: relationship.ObjectValue,
				ValueRange: &rangeValue, Polarity: relationship.Polarity,
				SupportRanges: []assessor.SemanticAssessmentGroundedRange{rangeValue},
				Evidence: []assessor.SemanticAssessmentEvidenceSpan{{
					EvidenceID: evidence.EvidenceID, Start: 0, End: len([]rune(evidence.Content)),
				}},
			}},
		})
	}
	return rememberProcessorIntegrationAssessmentSession{}, assessor.SemanticAssessmentTurn{Response: response, Turn: 1}, nil
}

func (p *rememberProcessorIntegrationRegistrationAssessor) Repair(
	context.Context, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRepairRequest,
) (assessor.SemanticAssessmentTurn, error) {
	p.repairs++
	return assessor.SemanticAssessmentTurn{}, fmt.Errorf("valid registration response unexpectedly required repair")
}

func (*rememberProcessorIntegrationRegistrationAssessor) ModelName() string {
	return "remember-integration-registration-assessor"
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

func insertRememberProcessorIntegrationPredicate(
	t *testing.T, adminDB *gorm.DB, rls *storagepostgres.RLS,
	teamID uuid.UUID, key string, version int, lifecycle string,
) error {
	t.Helper()
	return rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
			    team_id, predicate_key, version, aliases, allowed_subject_kinds,
			    allowed_object_kinds, relationship_kind, current_cardinality,
			    lifecycle_state, origin, metadata
			) VALUES (?::uuid, ?, ?, ARRAY[]::text[], ARRAY['project']::text[],
			          ARRAY['string']::text[], 'state', 'many', ?, 'fixture', '{}'::jsonb)
		`, teamID, key, version, lifecycle).Error
	})
}

func assertRememberProcessorIntegrationCanonicalCounts(
	t *testing.T, adminDB *gorm.DB, rls *storagepostgres.RLS, teamID uuid.UUID, want int64,
) {
	t.Helper()
	for _, table := range []string{
		"knowledge_ingests", "evidence_fragments", "semantic_assessments", "relationship_observations",
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

type rememberProcessorIntegrationCatalog struct{}

func (rememberProcessorIntegrationCatalog) ListSubmissionAssessmentEntityCatalog(context.Context, knowledgecontract.SubmissionAssessmentEntityCatalogInput) (knowledgecontract.SubmissionAssessmentEntityCatalogResult, error) {
	return knowledgecontract.SubmissionAssessmentEntityCatalogResult{Groups: []knowledgecontract.SubmissionAssessmentEntityCatalogGroup{}, Complete: true}, nil
}

func (rememberProcessorIntegrationCatalog) ResolveSemanticReviewPredicateCandidates(context.Context, knowledgecontract.SemanticReviewPredicateResolutionInput) ([]knowledgecontract.SemanticReviewPredicateResolution, error) {
	return []knowledgecontract.SemanticReviewPredicateResolution{}, nil
}

func (rememberProcessorIntegrationCatalog) ListSemanticAssessmentPredicateOptions(context.Context, knowledgecontract.SemanticAssessmentPredicateOptionsInput) ([]knowledgecontract.SemanticReviewPredicateCandidate, error) {
	return []knowledgecontract.SemanticReviewPredicateCandidate{}, nil
}

func (rememberProcessorIntegrationCatalog) ValidateSubmissionPredicateRegistrations(context.Context, knowledgecontract.SubmissionPredicateRegistrationValidationInput) ([]knowledgecontract.SubmissionPredicateRegistrationIssue, error) {
	return nil, nil
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
		EntityResults:              []assessor.SemanticAssessmentEntityResult{}, RelationshipResults: []assessor.SemanticAssessmentRelationshipResult{},
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
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgecontract.ConflictRuntimeConfig{})
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

func setupRememberProcessorIntegrationDB(t *testing.T) (*gorm.DB, *gorm.DB, *storagepostgres.RLS, func()) {
	t.Helper()
	if os.Getenv("DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS") != "1" {
		t.Skip("set DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS=1 to run PostgreSQL integration tests")
	}
	dsn := strings.TrimSpace(storagepostgres.GetTestDSN())
	if dsn == "" {
		t.Skip("DATABASE_URL is required for Remember processor PostgreSQL integration tests")
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
			GRANT EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID, UUID) TO %[1]s;
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
