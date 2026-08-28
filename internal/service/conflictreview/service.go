package conflictreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

const AssessmentConfidenceThreshold = 0.70

type Repository interface {
	ReviewRelationshipConflictCase(context.Context, repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error)
	ResumePendingOverdueConflictResolution(context.Context, repository.ResumePendingOverdueConflictResolutionInput) (*repository.RelationshipConflictResolutionInput, bool, error)
	ReserveOverdueConflictAssessment(context.Context, repository.ReserveOverdueConflictAssessmentInput) (*repository.OverdueConflictAssessmentReservation, *repository.OverdueConflictAssessmentDossier, bool, error)
	CompleteOverdueConflictAssessment(context.Context, repository.CompleteOverdueConflictAssessmentInput) (*repository.CompleteOverdueConflictAssessmentResult, error)
	PlanRelationshipConflictResolution(context.Context, repository.RelationshipConflictResolutionInput) (*repository.RelationshipConflictResolutionPlan, error)
	CommitRelationshipConflictResolution(context.Context, repository.CommitRelationshipConflictResolutionInput) (*repository.ApplyOverdueConflictResolutionResult, error)
	ClaimConflictDerivedEvidenceTasks(context.Context, repository.ClaimConflictDerivedEvidenceTasksInput) ([]repository.ConflictDerivedEvidenceTarget, error)
	StageConflictDerivedEvidence(context.Context, repository.ConflictDerivedEvidenceTarget) (*repository.StageConflictDerivedEvidenceResult, error)
	RecordConflictDerivedEvidenceFailure(context.Context, repository.ConflictDerivedEvidenceTarget, string) error
}

type Provider interface {
	AssessRelationshipConflict(context.Context, conflictassessment.ConflictAssessmentRequest) (conflictassessment.ConflictAssessmentResponse, error)
	ModelName() string
}

type EmbeddingProvider interface {
	EmbedBatch(context.Context, []string) ([][]float32, string, error)
	ModelName() string
	Dimensions() int
	IsAvailable() bool
}

type embeddingExecutor interface {
	Execute(context.Context, semanticwrite.Plan) (semanticwrite.Result, error)
}

type metricsProvider interface {
	SetMetrics(observability.DiscoverabilityMetrics)
}

type Dependencies struct {
	Repository       Repository
	Provider         Provider
	Embeddings       EmbeddingProvider
	EmbeddingTimeout time.Duration
	Metrics          observability.DiscoverabilityMetrics
	Timezone         string
	Limits           conflictassessment.SemanticAssessmentLimits
	Now              func() time.Time
}

type Service struct {
	repository       Repository
	provider         Provider
	embeddings       embeddingExecutor
	embeddingTimeout time.Duration
	metrics          observability.DiscoverabilityMetrics
	location         *time.Location
	limits           conflictassessment.SemanticAssessmentLimits
	now              func() time.Time
}

type conflictEmbeddingBatchProvider struct {
	provider EmbeddingProvider
}

func (p conflictEmbeddingBatchProvider) EmbedBatch(ctx context.Context, texts []string) ([]semanticwrite.IndexedEmbedding, string, error) {
	vectors, model, err := p.provider.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, model, err
	}
	indexed := make([]semanticwrite.IndexedEmbedding, len(vectors))
	for index, vector := range vectors {
		indexed[index] = semanticwrite.IndexedEmbedding{Index: index, Vector: append([]float32(nil), vector...)}
	}
	return indexed, model, nil
}

func (p conflictEmbeddingBatchProvider) ModelName() string {
	return p.provider.ModelName()
}

func (p conflictEmbeddingBatchProvider) Dimensions() int {
	return p.provider.Dimensions()
}

func (p conflictEmbeddingBatchProvider) IsAvailable() bool {
	return p.provider.IsAvailable()
}

func New(deps Dependencies) (*Service, error) {
	if deps.Repository == nil {
		return nil, errors.New("conflict review service: repository is required")
	}
	if deps.Provider == nil {
		return nil, errors.New("conflict review service: provider is required")
	}
	if deps.Embeddings == nil {
		return nil, errors.New("conflict review service: embedding provider is required")
	}
	if strings.TrimSpace(deps.Embeddings.ModelName()) == "" || deps.Embeddings.Dimensions() < 1 {
		return nil, errors.New("conflict review service: embedding provider contract is invalid")
	}
	if deps.EmbeddingTimeout <= 0 {
		return nil, errors.New("conflict review service: embedding timeout is required")
	}
	if strings.TrimSpace(deps.Provider.ModelName()) == "" {
		return nil, errors.New("conflict review service: provider model is required")
	}
	timezone := strings.TrimSpace(deps.Timezone)
	if timezone == "" {
		timezone = "Local"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("conflict review service: timezone is invalid: %w", err)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	if provider, ok := deps.Provider.(metricsProvider); ok {
		provider.SetMetrics(metrics)
	}
	return &Service{
		repository:       deps.Repository,
		provider:         deps.Provider,
		embeddings:       semanticwrite.NewExecutor(conflictEmbeddingBatchProvider{provider: deps.Embeddings}),
		embeddingTimeout: deps.EmbeddingTimeout,
		metrics:          metrics,
		location:         location,
		limits:           deps.Limits,
		now:              now,
	}, nil
}

func (s *Service) ReviewRelationshipConflictCase(
	ctx context.Context,
	input repository.ReviewRelationshipConflictCaseInput,
) (*repository.ReviewRelationshipConflictCaseResult, error) {
	if s == nil || s.repository == nil || s.provider == nil || s.embeddings == nil {
		return nil, errors.New("conflict review service is not configured")
	}
	if input.Now.IsZero() {
		input.Now = s.now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	result, err := s.repository.ReviewRelationshipConflictCase(ctx, input)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("conflict review service: deterministic review returned no result")
	}
	if result.Outcome != repository.ConflictReviewOutcomeOverdue {
		if result.Outcome == repository.ConflictReviewOutcomeResolve {
			if result.Resolution == nil {
				return nil, errors.New("conflict review service: deterministic resolution plan is required")
			}
			applied, err := s.executeResolution(ctx, *result.Resolution)
			if err != nil {
				return nil, err
			}
			if applied == nil || applied.Stale {
				result.Outcome = repository.ConflictReviewOutcomeNoop
				result.Stage = "resolution_stale"
				return result, nil
			}
			if applied.Pending || !applied.Resolved {
				result.Outcome = repository.ConflictReviewOutcomeNoop
				result.Stage = "resolution_pending"
				result.ResolutionPending = true
				return result, nil
			}
			result.UpdatedRelationships = append([]string(nil), applied.UpdatedRelationships...)
			s.observeConflictResolution(input.TeamID, "deterministic", "resolved")
		}
		return result, nil
	}

	pending, found, err := s.repository.ResumePendingOverdueConflictResolution(ctx, repository.ResumePendingOverdueConflictResolutionInput{
		TeamID:      input.TeamID,
		ConflictID:  input.ConflictID,
		ReviewRunID: input.ReviewRunID,
		WorkerID:    input.WorkerID,
		Now:         input.Now,
	})
	if err != nil {
		return nil, err
	}
	if found {
		applied, err := s.executeResolution(ctx, *pending)
		if err != nil {
			return nil, err
		}
		return s.applyResolutionResult(ctx, result, input, pending.AssessmentAttemptID, pending.Method, applied)
	}

	localNow := input.Now.In(s.location)
	reservation, dossier, reserved, err := s.repository.ReserveOverdueConflictAssessment(ctx, repository.ReserveOverdueConflictAssessmentInput{
		TeamID:              input.TeamID,
		ConflictID:          input.ConflictID,
		ReviewRunID:         input.ReviewRunID,
		WorkerID:            input.WorkerID,
		LocalAssessmentDate: localNow,
		Model:               s.provider.ModelName(),
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	if err != nil {
		return nil, err
	}
	if !reserved || reservation == nil || dossier == nil {
		result.Stage = "overdue_assessment_already_reserved"
		return result, nil
	}
	result.AssessmentAttemptID = reservation.AssessmentAttemptID
	if reservation.LastWriteWins {
		return s.applyLastWriteWins(ctx, result, input, reservation, dossier)
	}
	request, requestErr := conflictAssessmentRequest(reservation, dossier)
	if requestErr != nil {
		return s.recordAssessmentFailure(ctx, result, input, reservation, dossier, "dossier_invalid")
	}
	prepared, validationErrors := conflictassessment.PrepareConflictAssessmentRequest(request, s.limits)
	if len(validationErrors) > 0 {
		return s.recordAssessmentFailure(ctx, result, input, reservation, dossier, "dossier_bound_exceeded")
	}

	providerCtx := observability.WithMetricIdentity(ctx, input.TeamID, "")
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationConflictReview, 1)
	response, err := s.provider.AssessRelationshipConflict(providerCtx, prepared)
	if err != nil {
		return s.recordAssessmentFailure(ctx, result, input, reservation, dossier, conflictAssessmentFailureClass(err))
	}
	return s.applyAssessmentResponse(ctx, result, input, reservation, dossier, response)
}

func (s *Service) ProcessPendingConflictDerivedEvidence(
	ctx context.Context,
	input repository.ClaimConflictDerivedEvidenceTasksInput,
) (int, error) {
	if s == nil || s.repository == nil {
		return 0, errors.New("conflict review service is not configured")
	}
	staged := 0
	for {
		targets, err := s.repository.ClaimConflictDerivedEvidenceTasks(ctx, input)
		if err != nil {
			return staged, err
		}
		if len(targets) == 0 {
			return staged, nil
		}
		completed, err := s.stageConflictDerivedEvidence(ctx, targets)
		staged += completed
		if err != nil {
			return staged, err
		}
		if len(targets) < input.Limit {
			return staged, nil
		}
	}
}

func (s *Service) applyAssessmentResponse(
	ctx context.Context,
	result *repository.ReviewRelationshipConflictCaseResult,
	input repository.ReviewRelationshipConflictCaseInput,
	reservation *repository.OverdueConflictAssessmentReservation,
	dossier *repository.OverdueConflictAssessmentDossier,
	response conflictassessment.ConflictAssessmentResponse,
) (*repository.ReviewRelationshipConflictCaseResult, error) {
	response.Decision = strings.TrimSpace(response.Decision)
	if response.Decision == conflictassessment.ConflictAssessmentDecisionSelect {
		positionID, valid := validSelectedConflictPosition(dossier, response)
		if !valid {
			return s.recordAssessmentFailure(ctx, result, input, reservation, dossier, "invalid_response")
		}
		if response.Confidence < AssessmentConfidenceThreshold {
			return s.recordAssessmentFailure(ctx, result, input, reservation, dossier, "below_confidence_threshold")
		}
		confidence := response.Confidence
		if _, err := s.completeAssessment(ctx, input, reservation, repository.CompleteOverdueConflictAssessmentInput{
			Decision:           "selected",
			SelectedPositionID: positionID,
			Confidence:         &confidence,
			ProviderTurns:      response.ProviderTurns,
			ResponseHash:       conflictAssessmentResponseHash(response),
		}); err != nil {
			return s.handleAssessmentCompletionError(result, err)
		}
		s.observeConflictAssessment(input.TeamID, "selected", "none")
		applied, err := s.executeResolution(ctx, repository.RelationshipConflictResolutionInput{
			TeamID:              input.TeamID,
			ConflictID:          input.ConflictID,
			ReviewRunID:         input.ReviewRunID,
			WorkerID:            input.WorkerID,
			ExpectedCaseVersion: reservation.CaseVersion,
			PreferredPositionID: positionID,
			AssessmentAttemptID: reservation.AssessmentAttemptID,
			Method:              "ai",
			Now:                 input.Now,
		})
		if err != nil {
			return nil, err
		}
		return s.applyResolutionResult(ctx, result, input, reservation.AssessmentAttemptID, "ai", applied)
	}
	if response.Decision == conflictassessment.ConflictAssessmentDecisionAbstain && response.PositionID == nil && response.Confidence == 0 {
		confidence := float64(0)
		if _, err := s.completeAssessment(ctx, input, reservation, repository.CompleteOverdueConflictAssessmentInput{
			Decision:      "abstained",
			Confidence:    &confidence,
			ProviderTurns: response.ProviderTurns,
			ResponseHash:  conflictAssessmentResponseHash(response),
		}); err != nil {
			return s.handleAssessmentCompletionError(result, err)
		}
		s.observeConflictAssessment(input.TeamID, "abstained", "none")
		return s.applyLastWriteWins(ctx, result, input, reservation, dossier)
	}
	return s.recordAssessmentFailure(ctx, result, input, reservation, dossier, "invalid_response")
}

func (s *Service) recordAssessmentFailure(
	ctx context.Context,
	result *repository.ReviewRelationshipConflictCaseResult,
	input repository.ReviewRelationshipConflictCaseInput,
	reservation *repository.OverdueConflictAssessmentReservation,
	dossier *repository.OverdueConflictAssessmentDossier,
	failureClass string,
) (*repository.ReviewRelationshipConflictCaseResult, error) {
	completed, err := s.completeAssessment(ctx, input, reservation, repository.CompleteOverdueConflictAssessmentInput{
		Decision:     "failed",
		FailureClass: failureClass,
	})
	if err != nil {
		return s.handleAssessmentCompletionError(result, err)
	}
	s.observeConflictAssessment(input.TeamID, "failed", failureClass)
	if completed == nil || completed.FailureCount < repository.ConflictAssessmentMaxFailedDays {
		result.Stage = "overdue_assessment_failed"
		return result, nil
	}
	return s.applyLastWriteWins(ctx, result, input, reservation, dossier)
}

func (s *Service) completeAssessment(
	ctx context.Context,
	input repository.ReviewRelationshipConflictCaseInput,
	reservation *repository.OverdueConflictAssessmentReservation,
	completion repository.CompleteOverdueConflictAssessmentInput,
) (*repository.CompleteOverdueConflictAssessmentResult, error) {
	completion.TeamID = input.TeamID
	completion.ConflictID = input.ConflictID
	completion.AssessmentAttemptID = reservation.AssessmentAttemptID
	completion.CaseVersion = reservation.CaseVersion
	completion.ReviewRunID = input.ReviewRunID
	return s.repository.CompleteOverdueConflictAssessment(ctx, completion)
}

func (s *Service) applyLastWriteWins(
	ctx context.Context,
	result *repository.ReviewRelationshipConflictCaseResult,
	input repository.ReviewRelationshipConflictCaseInput,
	reservation *repository.OverdueConflictAssessmentReservation,
	dossier *repository.OverdueConflictAssessmentDossier,
) (*repository.ReviewRelationshipConflictCaseResult, error) {
	positions := make([]domain.ConflictResolutionPosition, 0, len(dossier.Positions))
	for _, position := range dossier.Positions {
		positions = append(positions, domain.ConflictResolutionPosition{
			PositionID: position.PositionID,
			Supports:   append([]domain.ConflictResolutionSupport(nil), position.Supports...),
		})
	}
	winner, ok := domain.SelectConflictLastWriteWinner(positions)
	if !ok {
		result.Stage = "overdue_last_write_wins_unavailable"
		return result, nil
	}
	applied, err := s.executeResolution(ctx, repository.RelationshipConflictResolutionInput{
		TeamID:              input.TeamID,
		ConflictID:          input.ConflictID,
		ReviewRunID:         input.ReviewRunID,
		WorkerID:            input.WorkerID,
		ExpectedCaseVersion: reservation.CaseVersion,
		PreferredPositionID: winner.PositionID,
		AssessmentAttemptID: reservation.AssessmentAttemptID,
		Method:              "last_write_wins",
		Now:                 input.Now,
	})
	if err != nil {
		return nil, err
	}
	return s.applyResolutionResult(ctx, result, input, reservation.AssessmentAttemptID, "last_write_wins", applied)
}

func (s *Service) executeResolution(
	ctx context.Context,
	input repository.RelationshipConflictResolutionInput,
) (*repository.ApplyOverdueConflictResolutionResult, error) {
	plan, err := s.repository.PlanRelationshipConflictResolution(ctx, input)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, errors.New("conflict review service: resolution planner returned no plan")
	}
	if plan.Stale || plan.Pending {
		return &repository.ApplyOverdueConflictResolutionResult{
			ConflictID: plan.Resolution.ConflictID, PreferredPositionID: plan.Resolution.PreferredPositionID,
			Method: plan.Resolution.Method, Stale: plan.Stale, Pending: plan.Pending,
			PendingTransitioned: plan.PendingTransitioned,
		}, nil
	}
	documents := make([]semanticwrite.Document, 0, len(plan.Documents))
	seen := make(map[string]struct{}, len(plan.Documents))
	for _, document := range plan.Documents {
		if _, exists := seen[document.DocumentHash]; exists {
			continue
		}
		seen[document.DocumentHash] = struct{}{}
		documents = append(documents, semanticwrite.Document{Hash: document.DocumentHash, Text: document.DocumentText})
	}
	providerCtx := observability.WithMetricIdentity(ctx, input.TeamID, "")
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationConflictReview, 1)
	embedded, err := s.embeddings.Execute(providerCtx, semanticwrite.Plan{
		Documents: documents,
		Fence: semanticwrite.Fence{
			Model: plan.Fence.EmbeddingModel, Dimensions: plan.Fence.EmbeddingDimensions,
			EmbeddingContractID:     plan.Fence.EmbeddingContractID,
			SearchGenerationID:      plan.Fence.SearchIndexGenerationID,
			SearchGenerationVersion: int64(plan.Fence.IndexGeneration),
		},
		Timeout: s.embeddingTimeout,
	})
	if err != nil {
		return nil, err
	}
	embeddings := make([]repository.RelationshipConflictResolutionEmbedding, 0, len(embedded.Embeddings))
	for _, value := range embedded.Embeddings {
		embeddings = append(embeddings, repository.RelationshipConflictResolutionEmbedding{
			DocumentHash: value.DocumentHash,
			Embedding:    append([]float32(nil), value.Vector...),
		})
	}
	return s.repository.CommitRelationshipConflictResolution(ctx, repository.CommitRelationshipConflictResolutionInput{
		Plan: *plan, Embeddings: embeddings,
	})
}

func (s *Service) applyResolutionResult(
	ctx context.Context,
	result *repository.ReviewRelationshipConflictCaseResult,
	input repository.ReviewRelationshipConflictCaseInput,
	assessmentAttemptID string,
	method string,
	applied *repository.ApplyOverdueConflictResolutionResult,
) (*repository.ReviewRelationshipConflictCaseResult, error) {
	if applied == nil || applied.Stale {
		result.Stage = "overdue_assessment_stale"
		return result, nil
	}
	if assessmentAttemptID != "" {
		result.AssessmentAttemptID = assessmentAttemptID
	}
	if method != "" {
		result.ResolutionMethod = method
	}
	if result.ResolutionMethod == "" {
		result.ResolutionMethod = applied.Method
	}
	if applied.PreferredPositionID != "" {
		result.PreferredPositionID = applied.PreferredPositionID
	}
	if applied.Pending {
		if applied.PendingTransitioned {
			s.observeConflictResolution(input.TeamID, result.ResolutionMethod, "pending")
		}
		result.Stage = "resolution_pending"
		result.ResolutionPending = true
		return result, nil
	}
	if !applied.Resolved {
		result.Stage = "overdue_assessment_stale"
		return result, nil
	}
	result.Outcome = repository.ConflictReviewOutcomeResolve
	result.Stage = "overdue_" + result.ResolutionMethod
	result.UpdatedRelationships = append([]string(nil), applied.UpdatedRelationships...)
	result.RetractedEvidenceIDs = append([]string(nil), applied.RetractedEvidenceIDs...)
	s.observeConflictResolution(input.TeamID, result.ResolutionMethod, "resolved")
	if _, err := s.stageConflictDerivedEvidence(ctx, applied.DerivedEvidence); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) observeConflictAssessment(teamID, decision, failureClass string) {
	if s == nil {
		return
	}
	if metrics, ok := s.metrics.(observability.ConflictQueueMetrics); ok {
		metrics.ObserveConflictAssessment(teamID, decision, failureClass)
	}
}

func (s *Service) observeConflictResolution(teamID, method, outcome string) {
	if s == nil {
		return
	}
	if metrics, ok := s.metrics.(observability.ConflictQueueMetrics); ok {
		metrics.ObserveConflictResolution(teamID, method, outcome)
	}
}

func (s *Service) stageConflictDerivedEvidence(ctx context.Context, targets []repository.ConflictDerivedEvidenceTarget) (int, error) {
	staged := 0
	for _, target := range targets {
		if _, err := s.repository.StageConflictDerivedEvidence(ctx, target); err != nil {
			failureErr := s.repository.RecordConflictDerivedEvidenceFailure(ctx, target, "staging_failed")
			if failureErr != nil {
				return staged, errors.Join(err, failureErr)
			}
			return staged, err
		}
		staged++
	}
	return staged, nil
}

func (s *Service) handleAssessmentCompletionError(result *repository.ReviewRelationshipConflictCaseResult, err error) (*repository.ReviewRelationshipConflictCaseResult, error) {
	if errors.Is(err, repository.ErrConflictAssessmentReserved) || errors.Is(err, repository.ErrConflictAssessmentStale) {
		result.Stage = "overdue_assessment_stale"
		return result, nil
	}
	return nil, err
}

func conflictAssessmentRequest(
	reservation *repository.OverdueConflictAssessmentReservation,
	dossier *repository.OverdueConflictAssessmentDossier,
) (conflictassessment.ConflictAssessmentRequest, error) {
	if reservation == nil || dossier == nil {
		return conflictassessment.ConflictAssessmentRequest{}, errors.New("assessment reservation and dossier are required")
	}
	request := conflictassessment.ConflictAssessmentRequest{
		RequestID: reservation.AssessmentAttemptID,
		CaseID:    dossier.ConflictID,
		Version:   dossier.CaseVersion,
		Question:  dossier.Question,
		Positions: make([]conflictassessment.ConflictAssessmentPosition, 0, len(dossier.Positions)),
		Evidence:  make([]conflictassessment.ConflictAssessmentEvidence, 0, len(dossier.Evidence)),
	}
	for _, position := range dossier.Positions {
		request.Positions = append(request.Positions, conflictassessment.ConflictAssessmentPosition{
			PositionID:     position.PositionID,
			PositionKey:    position.PositionKey,
			SupporterCount: position.SupporterCount,
		})
	}
	for _, evidence := range dossier.Evidence {
		request.Evidence = append(request.Evidence, conflictassessment.ConflictAssessmentEvidence{
			EvidenceID:   evidence.FragmentID,
			PositionID:   evidence.PositionID,
			SupportID:    evidence.SupportID,
			SupporterRef: evidence.SupporterRef,
			Authority:    evidence.Authority,
			AcceptedAt:   evidence.AcceptedAt,
			EffectiveAt:  evidence.EffectiveAt,
			Content:      evidence.Content,
		})
	}
	return request, nil
}

func validSelectedConflictPosition(dossier *repository.OverdueConflictAssessmentDossier, response conflictassessment.ConflictAssessmentResponse) (string, bool) {
	if dossier == nil || response.PositionID == nil || response.Confidence < 0 || response.Confidence > 1 {
		return "", false
	}
	positionID := strings.TrimSpace(*response.PositionID)
	if positionID == "" {
		return "", false
	}
	for _, position := range dossier.Positions {
		if position.PositionID == positionID {
			return positionID, true
		}
	}
	return "", false
}

func conflictAssessmentFailureClass(err error) string {
	if errors.Is(err, conflictassessment.ErrVerifierMalformedResponse) {
		return "malformed_response"
	}
	class := strings.TrimSpace(conflictassessment.ProviderFailureDetails(err).Class)
	if class == "" || len(class) > 128 {
		return "provider_unavailable"
	}
	return class
}

func conflictAssessmentResponseHash(response conflictassessment.ConflictAssessmentResponse) string {
	positionID := ""
	if response.PositionID != nil {
		positionID = strings.TrimSpace(*response.PositionID)
	}
	payload := strings.Join([]string{
		strings.TrimSpace(response.Decision),
		positionID,
		fmt.Sprintf("%.6f", response.Confidence),
		strings.TrimSpace(response.Rationale),
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return "sha256:" + hex.EncodeToString(sum[:])
}
