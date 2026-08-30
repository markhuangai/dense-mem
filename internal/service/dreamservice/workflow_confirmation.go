package dreamservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func isDreamConfirmationDecision(decision string) bool {
	switch decision {
	case "confirm_true", "confirm_false", "promote_candidate":
		return true
	default:
		return false
	}
}

func (s *service) resolveConfirmationWithLock(
	ctx context.Context,
	teamID string,
	actorProfileID string,
	dreamID string,
	decision string,
	req ResolveFeedbackRequest,
) (*ResolveFeedbackResult, error) {
	var result *ResolveFeedbackResult
	err := s.deps.Store.WithHypothesisConfirmationLock(ctx, teamID, dreamID, func(store repository.DreamRepository) error {
		var err error
		result, err = s.resolveConfirmation(ctx, store, teamID, actorProfileID, dreamID, decision, req)
		return err
	})
	if errors.Is(err, repository.ErrDreamConfirmationBusy) {
		return nil, &ConfirmationBusyError{}
	}
	return result, err
}

func (s *service) resolveConfirmation(
	ctx context.Context,
	store repository.DreamRepository,
	teamID string,
	actorProfileID string,
	dreamID string,
	decision string,
	req ResolveFeedbackRequest,
) (*ResolveFeedbackResult, error) {
	record, err := store.GetHypothesis(ctx, repository.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		if errors.Is(err, repository.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	dream := dreamRecord(record)
	if s.deps.Remember == nil {
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, fmt.Errorf("resolve dream feedback: remember service is required")
	}
	if !dreamConfirmationReplayMatches(record, req, decision) {
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, ErrDreamNotFound
	}
	evidence, err := dreamSubmissionEvidence(req, record)
	if err != nil {
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, err
	}
	if replay, err := dreamSubmittedConfirmationReplay(record, req, evidence); err != nil {
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, err
	} else if replay {
		s.recordDreamFeedback(ctx, decision, dream, "ok")
		return &ResolveFeedbackResult{Dream: dream, Memory: dreamReplayRememberResult(record)}, nil
	}
	remember, err := s.deps.Remember.Remember(ctx, rememberapp.RememberRequest{
		Evidence:          evidence,
		EntityHints:       req.EntityHints,
		RelationshipHints: req.RelationshipHints,
		IdempotencyKey:    dreamFeedbackIdempotency(req, record.HypothesisID, decision),
	})
	if err != nil {
		var processErr *rememberapp.RememberProcessError
		if !errors.As(err, &processErr) || processErr.Result == nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		remember = rememberResultFromProcessError(processErr.Result)
	}
	completed, ingestID, err := dreamRememberCompletion(remember)
	if err != nil {
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, err
	}
	if !completed {
		applyDreamTerminalRetryGuidance(remember, record.HypothesisID, decision)
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return &ResolveFeedbackResult{Dream: dream, Memory: remember}, nil
	}
	updated, err := store.SubmitHypothesis(ctx, repository.SubmitHypothesisInput{
		TeamID:            teamID,
		ActorProfileID:    actorProfileID,
		HypothesisID:      dreamID,
		Decision:          decision,
		SubmittedIngestID: ingestID,
		InvalidatedReason: req.Feedback,
	})
	return s.feedbackResult(ctx, decision, dream, updated, remember, err)
}

func dreamSubmissionEvidence(
	req ResolveFeedbackRequest,
	record *repository.HypothesisRecord,
) ([]rememberapp.RememberEvidenceInput, error) {
	return dreamSubmissionEvidenceWithStatus(req, record, record.Status, false)
}

func dreamSubmissionEvidenceWithStatus(
	req ResolveFeedbackRequest,
	record *repository.HypothesisRecord,
	statusBefore string,
	legacy bool,
) ([]rememberapp.RememberEvidenceInput, error) {
	if len(req.Evidence) == 0 {
		return nil, errors.New("resolve dream feedback: independent evidence is required")
	}
	out := make([]rememberapp.RememberEvidenceInput, 0, len(req.Evidence))
	for i, item := range req.Evidence {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			return nil, fmt.Errorf("resolve dream feedback: evidence[%d].content is required", i)
		}
		if strings.EqualFold(content, strings.TrimSpace(record.Statement)) {
			return nil, errors.New("resolve dream feedback: hypothesis text cannot be submitted as its own evidence")
		}
		if item.SourceType == "" {
			item.SourceType = "manual"
		}
		if item.Source == "" {
			item.Source = "dream_feedback:" + record.HypothesisID
		}
		metadata := make(map[string]any)
		for key, value := range item.Metadata {
			metadata[key] = value
		}
		item.Metadata = metadata
		item.Metadata["hypothesis_id"] = record.HypothesisID
		if legacy {
			item.Metadata["hypothesis_status_before"] = strings.TrimSpace(statusBefore)
			delete(item.Metadata, "dream_feedback_reason")
		} else if feedback := strings.TrimSpace(req.Feedback); feedback != "" {
			item.Metadata["dream_feedback_reason"] = feedback
		} else {
			delete(item.Metadata, "dream_feedback_reason")
		}
		out = append(out, item)
	}
	return out, nil
}

func dreamSubmittedConfirmationReplay(
	record *repository.HypothesisRecord,
	req ResolveFeedbackRequest,
	evidence []rememberapp.RememberEvidenceInput,
) (bool, error) {
	if record == nil || record.Status != string(domain.DreamStatusSubmitted) ||
		strings.TrimSpace(record.SubmittedIngestID) == "" ||
		strings.TrimSpace(record.SubmittedIngestRequestHash) == "" ||
		strings.TrimSpace(record.InvalidatedReason) != strings.TrimSpace(req.Feedback) {
		return false, nil
	}
	requestHash, err := rememberapp.CanonicalRequestBodyHash(evidence, req.EntityHints, req.RelationshipHints)
	if err != nil {
		return false, fmt.Errorf("resolve dream feedback: replay request hash: %w", err)
	}
	if strings.TrimSpace(record.SubmittedIngestRequestHash) == requestHash {
		return true, nil
	}
	for _, statusBefore := range domain.HypothesisStatuses() {
		legacyEvidence, err := dreamSubmissionEvidenceWithStatus(req, record, statusBefore, true)
		if err != nil {
			return false, err
		}
		legacyHash, err := rememberapp.CanonicalRequestBodyHash(legacyEvidence, req.EntityHints, req.RelationshipHints)
		if err != nil {
			return false, fmt.Errorf("resolve dream feedback: legacy replay request hash: %w", err)
		}
		if strings.TrimSpace(record.SubmittedIngestRequestHash) == legacyHash {
			return true, nil
		}
	}
	return false, nil
}

func dreamReplayRememberResult(record *repository.HypothesisRecord) *rememberapp.RememberResult {
	if record == nil {
		return nil
	}
	return &rememberapp.RememberResult{
		ContractVersion: domain.ContractVersion,
		IngestID:        record.SubmittedIngestID,
		SubmissionID:    record.SubmittedIngestID,
		SubmissionKind:  "remember",
		ProcessingState: string(domain.PlacementRunQueued),
		StatusTool:      "get_submission_status",
		Kind:            rememberapp.ResultKindLegacyReceipt,
	}
}

func applyDreamTerminalRetryGuidance(result *rememberapp.RememberResult, dreamID, decision string) {
	if result == nil || result.Terminal == nil {
		return
	}
	retryKey := fmt.Sprintf("dream-feedback:%s:%s:retry:%s", dreamID, decision, result.Terminal.SubmissionID)
	if len([]rune(retryKey)) > 128 {
		retryKey = "dream-feedback:" + dreamID + ":" + decision + ":retry"
	}
	for index := range result.Terminal.Errors {
		if result.Terminal.Errors[index].NextAction != string(rememberapp.TerminalNextActionResubmitRemember) {
			continue
		}
		result.Terminal.Errors[index].NextAction = string(rememberapp.TerminalNextActionRetryDreamFeedback)
		result.Terminal.Errors[index].Remediation = rememberapp.DreamFeedbackRetryRemediation(retryKey)
	}
}

func rememberResultFromProcessError(terminal *rememberapp.TerminalRememberResult) *rememberapp.RememberResult {
	if terminal == nil {
		return nil
	}
	return &rememberapp.RememberResult{
		ContractVersion: terminal.ContractVersion,
		IngestID:        terminal.SubmissionID,
		SubmissionID:    terminal.SubmissionID,
		SubmissionKind:  terminal.SubmissionKind,
		ProcessingState: terminal.ProcessingState,
		CorrelationID:   terminal.CorrelationID,
		Kind:            rememberapp.ResultKindTerminal,
		Terminal:        terminal,
	}
}

func dreamRememberCompletion(result *rememberapp.RememberResult) (bool, string, error) {
	if result == nil {
		return false, "", errors.New("resolve dream feedback: Remember result is required")
	}
	switch result.Kind {
	// TODO(#307): remove legacy receipt compatibility when all callers use terminal outcomes.
	case rememberapp.ResultKindLegacyReceipt:
		ingestID := strings.TrimSpace(result.IngestID)
		if _, err := uuid.Parse(ingestID); err != nil {
			return false, "", errors.New("resolve dream feedback: legacy Remember result has no canonical ingest")
		}
		return true, ingestID, nil
	case rememberapp.ResultKindTerminal:
		if result.Terminal == nil {
			return false, "", errors.New("resolve dream feedback: terminal Remember result is required")
		}
		switch result.Terminal.ProcessingState {
		case string(rememberapp.TerminalProcessingCompleted):
			ingestID := strings.TrimSpace(result.Terminal.SubmissionID)
			if _, err := uuid.Parse(ingestID); err != nil {
				return false, "", errors.New("resolve dream feedback: completed Remember result has no canonical ingest")
			}
			return true, ingestID, nil
		case string(rememberapp.TerminalProcessingRejected),
			string(rememberapp.TerminalProcessingQuarantined),
			string(rememberapp.TerminalProcessingFailed):
			return false, "", nil
		default:
			return false, "", errors.New("resolve dream feedback: terminal Remember result has an unsupported processing state")
		}
	default:
		return false, "", errors.New("resolve dream feedback: Remember result kind is required")
	}
}

func dreamConfirmationReplayMatches(record *repository.HypothesisRecord, req ResolveFeedbackRequest, decision string) bool {
	if record == nil || record.Status != string(domain.DreamStatusSubmitted) || strings.TrimSpace(record.SubmittedIngestID) == "" {
		return true
	}
	return strings.TrimSpace(record.SubmittedIngestIdempotencyKey) != "" &&
		strings.TrimSpace(record.SubmittedIngestIdempotencyKey) == dreamFeedbackIdempotency(req, record.HypothesisID, decision) &&
		strings.TrimSpace(record.SubmittedDecision) == decision
}

func dreamFeedbackIdempotency(req ResolveFeedbackRequest, dreamID string, decision string) string {
	if value := strings.TrimSpace(req.IdempotencyKey); value != "" {
		return value
	}
	return "dream-feedback:" + dreamID + ":" + decision
}
