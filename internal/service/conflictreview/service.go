package conflictreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const AssessmentConfidenceThreshold = 0.70

type Repository interface {
	ReviewRelationshipConflictCase(context.Context, repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error)
	ResumePendingOverdueConflictResolution(context.Context, repository.ResumePendingOverdueConflictResolutionInput) (*repository.ApplyOverdueConflictResolutionResult, bool, error)
	ReserveOverdueConflictAssessment(context.Context, repository.ReserveOverdueConflictAssessmentInput) (*repository.OverdueConflictAssessmentReservation, *repository.OverdueConflictAssessmentDossier, bool, error)
	CompleteOverdueConflictAssessment(context.Context, repository.CompleteOverdueConflictAssessmentInput) (*repository.CompleteOverdueConflictAssessmentResult, error)
	ApplyOverdueConflictResolution(context.Context, repository.ApplyOverdueConflictResolutionInput) (*repository.ApplyOverdueConflictResolutionResult, error)
	ClaimConflictDerivedEvidenceTasks(context.Context, repository.ClaimConflictDerivedEvidenceTasksInput) ([]repository.ConflictDerivedEvidenceTarget, error)
	StageConflictDerivedEvidence(context.Context, repository.ConflictDerivedEvidenceTarget) (*repository.StageConflictDerivedEvidenceResult, error)
	RecordConflictDerivedEvidenceFailure(context.Context, repository.ConflictDerivedEvidenceTarget, string) error
}

type Provider interface {
	AssessRelationshipConflict(context.Context, verifier.ConflictAssessmentRequest) (verifier.ConflictAssessmentResponse, error)
	ModelName() string
}

type metricsProvider interface {
	SetMetrics(observability.DiscoverabilityMetrics)
}

type Dependencies struct {
	Repository Repository
	Provider   Provider
	Metrics    observability.DiscoverabilityMetrics
	Timezone   string
	Limits     verifier.SemanticAssessmentLimits
	Now        func() time.Time
}

type Service struct {
	repository Repository
	provider   Provider
	metrics    observability.DiscoverabilityMetrics
	location   *time.Location
	limits     verifier.SemanticAssessmentLimits
	now        func() time.Time
}

func New(deps Dependencies) (*Service, error) {
	if deps.Repository == nil {
		return nil, errors.New("conflict review service: repository is required")
	}
	if deps.Provider == nil {
		return nil, errors.New("conflict review service: provider is required")
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
		repository: deps.Repository,
		provider:   deps.Provider,
		metrics:    metrics,
		location:   location,
		limits:     deps.Limits,
		now:        now,
	}, nil
}

func (s *Service) ReviewRelationshipConflictCase(
	ctx context.Context,
	input repository.ReviewRelationshipConflictCaseInput,
) (*repository.ReviewRelationshipConflictCaseResult, error) {
	if s == nil || s.repository == nil || s.provider == nil {
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
		return s.applyResolutionResult(ctx, result, input, "", "", pending)
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
	prepared, validationErrors := verifier.PrepareConflictAssessmentRequest(request, s.limits)
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
	response verifier.ConflictAssessmentResponse,
) (*repository.ReviewRelationshipConflictCaseResult, error) {
	response.Decision = strings.TrimSpace(response.Decision)
	if response.Decision == verifier.ConflictAssessmentDecisionSelect {
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
		applied, err := s.repository.ApplyOverdueConflictResolution(ctx, repository.ApplyOverdueConflictResolutionInput{
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
	if response.Decision == verifier.ConflictAssessmentDecisionAbstain && response.PositionID == nil && response.Confidence == 0 {
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
	applied, err := s.repository.ApplyOverdueConflictResolution(ctx, repository.ApplyOverdueConflictResolutionInput{
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
		s.observeConflictResolution(input.TeamID, result.ResolutionMethod, "pending")
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
) (verifier.ConflictAssessmentRequest, error) {
	if reservation == nil || dossier == nil {
		return verifier.ConflictAssessmentRequest{}, errors.New("assessment reservation and dossier are required")
	}
	request := verifier.ConflictAssessmentRequest{
		RequestID: reservation.AssessmentAttemptID,
		CaseID:    dossier.ConflictID,
		Version:   dossier.CaseVersion,
		Question:  dossier.Question,
		Positions: make([]verifier.ConflictAssessmentPosition, 0, len(dossier.Positions)),
		Evidence:  make([]verifier.ConflictAssessmentEvidence, 0, len(dossier.Evidence)),
	}
	for _, position := range dossier.Positions {
		request.Positions = append(request.Positions, verifier.ConflictAssessmentPosition{
			PositionID:              position.PositionID,
			PositionKey:             position.PositionKey,
			SupportGroupCount:       position.SupportGroupCount,
			AuthoritativeGroupCount: position.AuthoritativeGroupCount,
			OwnerProfileCount:       position.OwnerProfileCount,
		})
	}
	for _, evidence := range dossier.Evidence {
		request.Evidence = append(request.Evidence, verifier.ConflictAssessmentEvidence{
			EvidenceID:     evidence.FragmentID,
			PositionID:     evidence.PositionID,
			SupportID:      evidence.SupportID,
			SourceGroupKey: evidence.SourceGroupKey,
			Authority:      evidence.Authority,
			AcceptedAt:     evidence.AcceptedAt,
			EffectiveAt:    evidence.EffectiveAt,
			Content:        evidence.Content,
		})
	}
	return request, nil
}

func validSelectedConflictPosition(dossier *repository.OverdueConflictAssessmentDossier, response verifier.ConflictAssessmentResponse) (string, bool) {
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
	if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
		return "malformed_response"
	}
	class := strings.TrimSpace(verifier.ProviderFailureDetails(err).Class)
	if class == "" || len(class) > 128 {
		return "provider_unavailable"
	}
	return class
}

func conflictAssessmentResponseHash(response verifier.ConflictAssessmentResponse) string {
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
