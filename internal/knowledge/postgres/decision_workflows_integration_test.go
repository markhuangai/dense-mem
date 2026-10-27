//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	conflictassessment "github.com/markhuangai/dense-mem/internal/conflict/assessment"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	conflictreview "github.com/markhuangai/dense-mem/internal/conflict/review"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	decisionprovider "github.com/markhuangai/dense-mem/internal/provider/decision"
	"github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type decisionWorkflowHTTP struct {
	calls      atomic.Int64
	status     atomic.Int64
	onDecision func(string)
}

func (f *decisionWorkflowHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model          string                           `json:"model"`
		Input          string                           `json:"input"`
		Questions      []modelprovider.DecisionQuestion `json:"questions"`
		Messages       []modelprovider.Message          `json:"messages"`
		ResponseFormat struct {
			JSONSchema struct {
				Name   string `json:"name"`
				Schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/decisions") {
		f.calls.Add(1)
		if f.onDecision != nil {
			f.onDecision(request.Input)
		}
		if status := int(f.status.Load()); status != 0 {
			http.Error(w, "fixture decision failure", status)
			return
		}
		answers := []map[string]any{}
		for _, question := range request.Questions {
			answer := map[string]any{"name": question.Name, "type": question.Type}
			switch question.Type {
			case modelprovider.DecisionPredicate:
				probability := 0.001
				if strings.HasPrefix(question.Name, "dedup_") {
					probability = 0.999
				}
				if question.Name == "sufficiently_supported" {
					probability = 0.999
				}
				answer["probability"] = probability
			case modelprovider.DecisionChoice:
				probabilities := []map[string]any{}
				for index, option := range question.Choices {
					p := 0
					if index == 0 {
						p = 1
					}
					probabilities = append(probabilities, map[string]any{"value": option.Value, "probability": p})
				}
				answer["choice"], answer["confidence"], answer["probabilities"] = question.Choices[0].Value, 0.01, probabilities
			case modelprovider.DecisionScore:
				probabilities := []map[string]any{}
				for index, level := range question.Levels {
					p := 0
					if index == len(question.Levels)-1 {
						p = 1
					}
					probabilities = append(probabilities, map[string]any{"value": index, "label": level.Label, "probability": p})
				}
				answer["score"], answer["confidence"], answer["probabilities"] = len(question.Levels)-1, 1, probabilities
			}
			answers = append(answers, answer)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": request.Model, "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 0}})
		return
	}
	if len(request.Messages) < 2 {
		http.Error(w, "missing messages", 400)
		return
	}
	output, err := sessionFactsProviderResponse(request.ResponseFormat.JSONSchema.Name, []byte(request.Messages[1].Content))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	body, _ := json.Marshal(output)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	for key := range fields {
		if _, included := request.ResponseFormat.JSONSchema.Schema.Properties[key]; !included {
			delete(fields, key)
		}
	}
	body, _ = json.Marshal(fields)
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(body)}}}})
}

func TestDecisionRememberPostgresAtomicityReplayIsolationAndSettingsSnapshot(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	installOwnerSearchContract(t, admin, rls, "decision-workflows", 3, 1)
	team := createLedgerTeam(t, admin, rls, "decision-workflows")
	ownerA := createLedgerProfile(t, admin, rls, team, "decision-a")
	ownerB := createLedgerProfile(t, admin, rls, team, "decision-b")
	otherTeam := createLedgerTeam(t, admin, rls, "decision-other")
	ownerC := createLedgerProfile(t, admin, rls, otherTeam, "decision-c")
	shared, generation := duplicateTeamSharedSpace(t, admin, rls, team)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	for _, fixture := range []struct{ team, owner, kind, content string }{{team, ownerB, "private", "PRIVATE DECISION CANARY"}, {otherTeam, ownerC, "team_shared", "FOREIGN DECISION CANARY"}} {
		space, gen := duplicateTeamSharedSpace(t, admin, rls, fixture.team)
		if fixture.kind == "private" {
			space, gen = duplicatePrivateSpace(t, admin, rls, fixture.team, fixture.owner)
		}
		input := duplicateRememberInput(fixture.team, fixture.owner, fixture.kind, fixture.content, false)
		input.SpaceID, input.SpaceGeneration = space, gen
		fixtureCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: uuid.MustParse(space), Kind: domain.MemorySpaceKind(fixture.kind)}})
		commitDuplicateFixture(t, fixtureCtx, store, input)
	}
	repository := settingspostgres.NewAppConfigRepository(app, rls)
	original, err := repository.List(ctx)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
			for _, key := range []string{domain.AppConfigDecisionSecurity, domain.AppConfigUpdateTimeKey} {
				if entry, ok := original[key]; ok {
					if err := tx.Exec(`INSERT INTO app_config(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at`, key, entry.Value, entry.UpdatedAt).Error; err != nil {
						return err
					}
				} else if err := tx.Exec(`DELETE FROM app_config WHERE key=?`, key).Error; err != nil {
					return err
				}
			}
			return nil
		}))
	}()
	options := settings.DecisionSettingsOptions{Provider: domain.DecisionProviderStatus{Configured: true, Provider: "openai", Model: "gpt-6-luna"}}
	controls := settings.NewAppConfigServiceWithDecisions(repository, nil, options)
	initial, err := controls.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	fixture := &decisionWorkflowHTTP{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	protector := observability.NewCredentialProtector("decision-fixture-key")
	logger := observability.New(slog.LevelError)
	decisions, err := decisionprovider.New(config.DecisionProviderConfig{Provider: "openai", APIURL: server.URL + "/v1", APIKey: "decision-fixture-key", Model: "gpt-6-luna", MaxInputTokens: 32000, MaxQuestionInputTokens: 32000, MaxConcurrency: 2, TimeoutSeconds: 10}, "o200k_base", server.Client(), logger, protector, nil)
	require.NoError(t, err)
	generative := assessorprovider.NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: server.URL + "/v1", AIVerifierAPIKey: "decision-fixture-key", AIVerifierModel: "fixture", AIVerifierMaxConcurrency: 2}, server.Client())
	api := processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store, Catalog: store, Assessor: generative, Decisions: decisions, DecisionSettings: controls, Embedder: &sessionFactsEmbedder{}, Limits: assessor.DefaultSemanticAssessmentLimits(), Logger: logger, DiagnosticProtector: protector})
	input := rememberapp.RememberProcessRequest{TeamID: team, OwnerProfileID: ownerA, SpaceID: shared, SpaceGeneration: generation, SpaceKind: domain.MemorySpaceTeamShared, IdempotencyKey: "decision-initial", RequestHash: "decision-initial", Evidence: rememberapp.RepositoryEvidenceInputs([]rememberapp.RememberEvidenceInput{{Content: "Decision shared evidence uses PostgreSQL.", SourceType: "manual"}})}
	var unauthorized atomic.Bool
	fixture.onDecision = func(raw string) {
		if strings.Contains(raw, "PRIVATE DECISION CANARY") || strings.Contains(raw, "FOREIGN DECISION CANARY") {
			unauthorized.Store(true)
		}
	}
	first, err := api.ProcessRemember(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "completed", first.ProcessingState)
	require.False(t, unauthorized.Load())
	count := fixture.calls.Load()
	_, err = controls.UpdateDecisionSettings(ctx, map[string]string{domain.AppConfigDecisionSecurity: "false"}, "control", "", "snapshot-change")
	require.NoError(t, err)
	replay, err := api.ProcessRemember(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first.SubmissionID, replay.SubmissionID)
	require.Equal(t, count, fixture.calls.Load(), "replay must retain its original receipt without a provider call")
	_, err = controls.UpdateDecisionSettings(ctx, map[string]string{domain.AppConfigDecisionSecurity: "true"}, "control", "", "snapshot-reset")
	require.NoError(t, err)
	frozen, err := controls.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	fixture.onDecision = func(raw string) {
		if strings.Contains(raw, "PRIVATE DECISION CANARY") || strings.Contains(raw, "FOREIGN DECISION CANARY") {
			unauthorized.Store(true)
		}
		_, updateErr := controls.UpdateDecisionSettings(ctx, map[string]string{domain.AppConfigDecisionSecurity: "false"}, "control", "", "during-operation")
		if updateErr != nil {
			t.Error(updateErr)
		}
	}
	reused := input
	reused.IdempotencyKey, reused.RequestHash = "decision-paraphrase", "decision-paraphrase"
	reused.Evidence = rememberapp.RepositoryEvidenceInputs([]rememberapp.RememberEvidenceInput{{Content: "Decision shared evidence stores data in PostgreSQL.", SourceType: "manual"}})
	second, err := api.ProcessRemember(ctx, reused)
	require.NoError(t, err)
	require.Equal(t, first.Evidence[0].EvidenceID, second.Evidence[0].EvidenceID)
	require.False(t, unauthorized.Load())
	var normalized []byte
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT response_history->0->'normalized_response' FROM semantic_assessments WHERE team_id=?::uuid AND attempt_id=?::uuid`, team, second.SubmissionID).Row().Scan(&normalized)
	}))
	var assessment assessor.SemanticAssessmentResponse
	require.NoError(t, json.Unmarshal(normalized, &assessment))
	require.Len(t, assessment.DecisionStages, 2)
	for _, stage := range assessment.DecisionStages {
		require.Equal(t, frozen.Revision, stage.Settings.Revision)
		require.True(t, stage.Settings.Effective.SecurityReview)
	}
	require.NotEqual(t, initial.Revision, frozen.Revision)
	fixture.onDecision = nil
	for _, status := range []int64{401, 429, 529} {
		fixture.status.Store(status)
		failedInput := input
		failedInput.IdempotencyKey, failedInput.RequestHash = fmt.Sprintf("decision-failure-%d", status), fmt.Sprint(status)
		failedInput.Evidence = rememberapp.RepositoryEvidenceInputs([]rememberapp.RememberEvidenceInput{{Content: fmt.Sprintf("Decision failure %d", status), SourceType: "manual"}, {Content: "Second evidence cannot partially commit.", SourceType: "manual"}})
		failed, failure := api.ProcessRemember(ctx, failedInput)
		require.Error(t, failure)
		var processingFailure *rememberapp.RememberProcessError
		require.ErrorAs(t, failure, &processingFailure)
		failed = processingFailure.Status
		require.NotNil(t, failed)
		require.Equal(t, "failed", failed.ProcessingState)
		require.Zero(t, duplicateCount(t, admin, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id=?::uuid AND ingest_id=?::uuid`, team, failed.SubmissionID))
		require.Zero(t, duplicateCount(t, admin, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id=?::uuid AND ingest_id=?::uuid`, team, failed.SubmissionID))
	}
	fixture.status.Store(0)
	fixture.onDecision = func(string) {
		if err := rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
			return tx.Exec(`INSERT INTO evidence_quarantines(team_id,fragment_id,ingest_id,owner_profile_id,reason) SELECT team_id,fragment_id,ingest_id,owner_profile_id,'decision stale test' FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid ON CONFLICT DO NOTHING`, team, first.Evidence[0].EvidenceID).Error
		}); err != nil {
			t.Error(err)
		}
	}
	stale := reused
	stale.IdempotencyKey, stale.RequestHash = "decision-stale", "decision-stale"
	stale.Evidence = rememberapp.RepositoryEvidenceInputs([]rememberapp.RememberEvidenceInput{{Content: "Decision shared evidence uses the PostgreSQL database.", SourceType: "manual"}})
	staleResult, staleErr := api.ProcessRemember(ctx, stale)
	require.Error(t, staleErr)
	var staleFailure *rememberapp.RememberProcessError
	require.ErrorAs(t, staleErr, &staleFailure)
	staleResult = staleFailure.Status
	require.NotNil(t, staleResult)
	require.Equal(t, "failed", staleResult.ProcessingState)
	require.Zero(t, duplicateCount(t, admin, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id=?::uuid AND ingest_id=?::uuid`, team, staleResult.SubmissionID))
	fixture.onDecision = nil
	// A fresh authorized source proves Recall hydration filters foreign/private candidates before dispatch.
	visible := duplicateRememberInput(team, ownerB, "decision-recall-visible", "Decision recall visible evidence", false)
	visible.SpaceID, visible.SpaceGeneration = shared, generation
	commitDuplicateFixture(t, ctx, store, visible)
	search := searchpostgres.NewStore(app, rls)
	reader := func(readCtx context.Context, tx *gorm.DB, teamID string, knownAt *time.Time, hits []recallcontract.RecallEvidenceHit) ([]tracecontract.RelationshipConflictCaseRecord, error) {
		ids := []string{}
		for _, hit := range hits {
			ids = append(ids, hit.RelationshipIDs...)
		}
		return conflictpostgres.LoadRelationshipConflictRecordsInSpace(readCtx, tx, teamID, ids, knownAt, shared)
	}
	read := recallpostgres.NewStore(app, rls, search, reader, recallpostgres.LoadRecallEvidenceConflictRecords)
	recallAPI := recall.NewRecallService(recall.RecallDependencies{Search: read, Provider: &sessionFactsEmbedder{}, Decisions: decisions, DecisionSettings: controls, Logger: logger})
	fixture.onDecision = func(raw string) {
		if strings.Contains(raw, "PRIVATE DECISION CANARY") || strings.Contains(raw, "FOREIGN DECISION CANARY") {
			t.Error("unauthorized candidate dispatched to decision provider")
		}
	}
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: uuid.MustParse(team), OwnerID: uuid.MustParse(ownerA), AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(shared), Kind: domain.MemorySpaceTeamShared}}})
	zero := 0
	recalled, err := recallAPI.Recall(actorCtx, recall.RecallRequest{Query: "Decision", Limit: 1, RelationshipLimit: &zero, CommunityLimit: &zero})
	require.NoError(t, err)
	require.Len(t, recalled.Results, 1)
	require.NotEmpty(t, recalled.DecisionStages)
	require.Equal(t, []string{recalled.Results[0].EvidenceID}, recalled.DecisionStages[0].SelectedIDs)
}

type decisionConflictGenerative struct{ calls atomic.Int64 }

func (p *decisionConflictGenerative) ModelName() string { return "fixture-generative" }
func (p *decisionConflictGenerative) AssessRelationshipConflict(context.Context, conflictassessment.ConflictAssessmentRequest) (conflictassessment.ConflictAssessmentResponse, error) {
	p.calls.Add(1)
	return conflictassessment.ConflictAssessmentResponse{}, fmt.Errorf("unexpected generative call")
}

func TestDecisionConflictPostgresSelectionFailureAndStaleFence(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	installOwnerSearchContract(t, admin, rls, "decision-conflict", 3, 1)
	for _, mode := range []string{"select", "failure", "stale"} {
		t.Run(mode, func(t *testing.T) {
			team := createLedgerTeam(t, admin, rls, "decision-conflict-"+mode)
			a := createLedgerProfile(t, admin, rls, team, "decision-conflict-a")
			b := createLedgerProfile(t, admin, rls, team, "decision-conflict-b")
			ledger := NewStore(app, rls, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})
			subject := createSemanticEntity(t, ctx, ledger, team, a, "project", "Decision conflict")
			left := createSemanticEntity(t, ctx, ledger, team, a, "product", "PostgreSQL")
			right := createSemanticEntity(t, ctx, ledger, team, a, "product", "GraphDB")
			commitConflictRememberFixture(t, ctx, ledger, team, a, subject.EntityID, left.EntityID, "Decision conflict uses PostgreSQL.", "decision-left")
			commitConflictRememberFixture(t, ctx, ledger, team, b, subject.EntityID, right.EntityID, "Decision conflict uses GraphDB.", "decision-right")
			var id string
			var due time.Time
			require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
				return tx.Raw(`SELECT conflict_id::text,review_due_at FROM relationship_conflict_cases WHERE team_id=?::uuid`, team).Row().Scan(&id, &due)
			}))
			now := due.Add(time.Minute)
			run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{TeamID: team, WorkerID: "decision-worker", LocalRunDate: now, Timezone: "UTC", Lease: time.Minute})
			require.NoError(t, err)
			require.True(t, claimed)
			cases, err := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{TeamID: team, WorkerID: "decision-worker", ReviewRunID: run.ReviewRunID, Limit: 10, Lease: time.Minute, MaxAttempts: 5, Now: now})
			require.NoError(t, err)
			require.Len(t, cases, 1)
			fixture := &decisionWorkflowHTTP{}
			var versionChanged atomic.Bool
			if mode == "failure" {
				fixture.status.Store(401)
			}
			if mode == "stale" {
				fixture.onDecision = func(string) {
					if err := rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
						update := tx.Exec(`UPDATE relationship_conflict_cases SET version=version+1 WHERE team_id=?::uuid AND conflict_id=?::uuid`, team, id)
						if update.Error != nil {
							return update.Error
						}
						if update.RowsAffected != 1 {
							return fmt.Errorf("expected one changed conflict")
						}
						versionChanged.Store(true)
						return nil
					}); err != nil {
						t.Error(err)
					}
				}
			}
			server := httptest.NewServer(fixture)
			defer server.Close()
			logger := observability.New(slog.LevelError)
			decisions, err := decisionprovider.New(config.DecisionProviderConfig{Provider: "openai", APIURL: server.URL + "/v1", APIKey: "decision-fixture", Model: "gpt-6-luna", MaxInputTokens: 32000, MaxQuestionInputTokens: 32000, MaxConcurrency: 1, TimeoutSeconds: 10}, "o200k_base", server.Client(), logger, observability.NewCredentialProtector("decision-fixture"), nil)
			require.NoError(t, err)
			controls := settings.NewAppConfigServiceWithDecisions(settingspostgres.NewAppConfigRepository(app, rls), nil, settings.DecisionSettingsOptions{Provider: domain.DecisionProviderStatus{Configured: true, Provider: "openai", Model: "gpt-6-luna"}})
			generative := &decisionConflictGenerative{}
			service, err := conflictreview.New(conflictreview.Dependencies{Repository: ledger, Provider: generative, Embeddings: &sessionFactsEmbedder{}, EmbeddingTimeout: time.Second, Timezone: "UTC", Limits: conflictassessment.DefaultSemanticAssessmentLimits(), Decisions: decisions, DecisionSettings: controls, Logger: logger})
			require.NoError(t, err)
			result, err := service.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{TeamID: team, WorkerID: "decision-worker", ReviewRunID: run.ReviewRunID, ConflictID: id, Now: now})
			require.Zero(t, generative.calls.Load())
			require.EqualValues(t, 1, fixture.calls.Load())
			if mode == "failure" {
				require.Error(t, err)
				for day := 1; day <= ConflictAssessmentMaxFailedDays+1; day++ {
					reviewAt := now.AddDate(0, 0, day)
					dailyRun, claimed, runErr := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{TeamID: team, WorkerID: "decision-worker", LocalRunDate: reviewAt, Timezone: "UTC", Lease: time.Minute})
					require.NoError(t, runErr)
					require.True(t, claimed)
					dailyCases, claimErr := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{TeamID: team, WorkerID: "decision-worker", ReviewRunID: dailyRun.ReviewRunID, Limit: 10, Lease: time.Minute, MaxAttempts: 5, Now: reviewAt})
					require.NoError(t, claimErr)
					require.Len(t, dailyCases, 1)
					recovering := day == ConflictAssessmentMaxFailedDays+1
					if recovering {
						fixture.status.Store(0)
					}
					dailyResult, dailyErr := service.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{TeamID: team, WorkerID: "decision-worker", ReviewRunID: dailyRun.ReviewRunID, ConflictID: id, Now: reviewAt})
					require.EqualValues(t, day+1, fixture.calls.Load(), "every day must attempt the required provider, including after legacy failure exhaustion")
					if recovering {
						require.NoError(t, dailyErr)
						require.Equal(t, ConflictReviewOutcomeResolve, dailyResult.Outcome)
						require.Equal(t, "ai", dailyResult.ResolutionMethod)
					} else {
						require.Error(t, dailyErr)
						require.Equal(t, "overdue_assessment_failed", dailyResult.Stage)
						require.Zero(t, duplicateCount(t, admin, rls, `SELECT count(*) FROM relationship_records WHERE team_id=?::uuid AND status='superseded'`, team))
					}
				}
				require.Zero(t, generative.calls.Load())
				return
			} else if mode == "stale" {
				require.True(t, versionChanged.Load(), "the source version must change successfully during provider work")
				if err != nil {
					require.ErrorIs(t, err, ErrConflictAssessmentStale)
				} else {
					require.Equal(t, "overdue_assessment_stale", result.Stage)
				}
			} else {
				require.NoError(t, err)
			}
			if mode == "select" {
				require.Equal(t, ConflictReviewOutcomeResolve, result.Outcome)
				require.NotEmpty(t, result.PreferredPositionID)
				var metadata []byte
				require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
					return tx.Raw(`SELECT metadata FROM relationship_conflict_ai_assessment_events WHERE team_id=?::uuid AND metadata ? 'decision_stage' ORDER BY created_at DESC LIMIT 1`, team).Row().Scan(&metadata)
				}))
				require.Contains(t, string(metadata), "decision_position_probability")
				require.Contains(t, string(metadata), "select_typed_position")
			} else {
				require.Zero(t, duplicateCount(t, admin, rls, `SELECT count(*) FROM relationship_records WHERE team_id=?::uuid AND status='superseded'`, team))
			}
		})
	}
}
