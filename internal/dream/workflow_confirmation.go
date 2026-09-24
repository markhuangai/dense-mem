package dream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

const dreamConfirmationFinalizationTimeout = 5 * time.Second

type deferredHypothesisDiagnostic struct {
	record  *dreamcontract.HypothesisRecord
	phase   string
	outcome string
	cause   string
	details map[string]any
}

func (d *deferredHypothesisDiagnostic) capture(record *dreamcontract.HypothesisRecord, phase, outcome, cause string, details map[string]any) {
	if d == nil {
		return
	}
	*d = deferredHypothesisDiagnostic{
		record: record, phase: phase, outcome: outcome, cause: cause, details: details,
	}
}

func (d *deferredHypothesisDiagnostic) recordAfterLock(s *service, ctx context.Context) {
	if d == nil || d.record == nil {
		return
	}
	s.recordHypothesisDiagnostic(context.WithoutCancel(ctx), d.record, d.phase, d.outcome, d.cause, d.details)
}

func isDreamConfirmationDecision(decision string) bool {
	switch decision {
	case "confirm_true", "confirm_false", "promote_candidate":
		return true
	default:
		return false
	}
}

func isDreamLifecycleDecision(decision string) bool {
	switch decision {
	case "reject", "stale", "reinforce":
		return true
	default:
		return false
	}
}

func (s *service) resolveFeedback(ctx context.Context, req ResolveFeedbackRequest) (*ResolveFeedbackResult, error) {
	teamID, actorProfileID, err := dreamActor(ctx)
	if err != nil {
		return nil, err
	}
	dreamID := strings.TrimSpace(req.DreamID)
	if dreamID == "" {
		return nil, fmt.Errorf("resolve dream feedback: dream_id is required")
	}
	decision := strings.TrimSpace(req.Decision)
	if isDreamConfirmationDecision(decision) {
		return s.resolveConfirmationWithLock(ctx, teamID, actorProfileID, dreamID, decision, req)
	}
	if isDreamLifecycleDecision(decision) {
		return s.resolveLifecycleFeedbackWithLock(ctx, teamID, actorProfileID, dreamID, decision, req)
	}
	record, err := s.deps.Store.GetHypothesis(ctx, dreamcontract.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		if errors.Is(err, dreamcontract.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	dream := dreamRecord(record)
	switch decision {
	case "ignore":
		s.recordDreamFeedback(ctx, decision, dream, "ok")
		return &ResolveFeedbackResult{Dream: dream}, nil
	default:
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, fmt.Errorf("%w: %s", ErrInvalidDreamStatus, decision)
	}
}

func (s *service) feedbackResult(
	ctx context.Context,
	decision string,
	original *domain.Dream,
	updated *dreamcontract.HypothesisRecord,
	remember *rememberapp.RememberResult,
	err error,
) (*ResolveFeedbackResult, error) {
	if err != nil {
		s.recordDreamFeedback(ctx, decision, original, "error")
		if errors.Is(err, dreamcontract.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	s.recordDreamFeedback(ctx, decision, original, "ok")
	return &ResolveFeedbackResult{Dream: dreamRecord(updated), Memory: remember}, nil
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
	var diagnostic deferredHypothesisDiagnostic
	callbackInvoked := false
	err := s.deps.Store.WithHypothesisConfirmationLock(ctx, teamID, dreamID, func(store dreamcontract.DreamRepository) error {
		callbackInvoked = true
		var err error
		result, err = s.resolveConfirmation(ctx, store, teamID, actorProfileID, dreamID, decision, req, &diagnostic)
		return err
	})
	if errors.Is(err, dreamcontract.ErrDreamConfirmationBusy) {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		return nil, &ConfirmationBusyError{Decision: decision}
	}
	if err != nil && !callbackInvoked {
		s.recordDreamFeedback(ctx, decision, nil, "error")
	}
	diagnostic.recordAfterLock(s, ctx)
	return result, err
}

func (s *service) resolveLifecycleFeedbackWithLock(
	ctx context.Context,
	teamID string,
	actorProfileID string,
	dreamID string,
	decision string,
	req ResolveFeedbackRequest,
) (*ResolveFeedbackResult, error) {
	var result *ResolveFeedbackResult
	var diagnostic deferredHypothesisDiagnostic
	callbackInvoked := false
	err := s.deps.Store.WithHypothesisConfirmationLock(ctx, teamID, dreamID, func(store dreamcontract.DreamRepository) error {
		callbackInvoked = true
		var err error
		result, err = s.resolveLifecycleFeedback(ctx, store, teamID, actorProfileID, dreamID, decision, req, &diagnostic)
		return err
	})
	if errors.Is(err, dreamcontract.ErrDreamConfirmationBusy) {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		return nil, &ConfirmationBusyError{Decision: decision}
	}
	if err != nil && !callbackInvoked {
		s.recordDreamFeedback(ctx, decision, nil, "error")
	}
	diagnostic.recordAfterLock(s, ctx)
	return result, err
}

func (s *service) resolveLifecycleFeedback(
	ctx context.Context,
	store dreamcontract.DreamRepository,
	teamID string,
	actorProfileID string,
	dreamID string,
	decision string,
	req ResolveFeedbackRequest,
	diagnostic *deferredHypothesisDiagnostic,
) (*ResolveFeedbackResult, error) {
	record, err := store.GetHypothesis(ctx, dreamcontract.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		if errors.Is(err, dreamcontract.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	updated, err := store.UpdateHypothesisStatus(ctx, dreamcontract.UpdateHypothesisStatusInput{
		TeamID:            teamID,
		ActorProfileID:    actorProfileID,
		HypothesisID:      dreamID,
		Status:            lifecycleStatus(decision),
		Decision:          decision,
		InvalidatedReason: req.Feedback,
	})
	result, feedbackErr := s.feedbackResult(ctx, decision, dreamRecord(record), updated, nil, err)
	status := ""
	if updated != nil {
		status = updated.Status
	}
	cause := ""
	if feedbackErr != nil {
		cause = feedbackErr.Error()
	}
	feedbackOutcome := decision
	if feedbackErr != nil {
		feedbackOutcome = "failed"
	}
	diagnostic.capture(record, "feedback", feedbackOutcome, cause, map[string]any{
		"status_before":    record.Status,
		"status_after":     status,
		"feedback_present": strings.TrimSpace(req.Feedback) != "",
	})
	return result, feedbackErr
}

func lifecycleStatus(decision string) string {
	switch decision {
	case "reject":
		return string(domain.DreamStatusRejected)
	case "stale":
		return string(domain.DreamStatusStale)
	case "reinforce":
		return string(domain.DreamStatusReinforced)
	default:
		return ""
	}
}

func (s *service) resolveConfirmation(
	ctx context.Context,
	store dreamcontract.DreamRepository,
	teamID string,
	actorProfileID string,
	dreamID string,
	decision string,
	req ResolveFeedbackRequest,
	diagnostic *deferredHypothesisDiagnostic,
) (*ResolveFeedbackResult, error) {
	record, err := store.GetHypothesis(ctx, dreamcontract.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		if errors.Is(err, dreamcontract.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	dream := dreamRecord(record)
	if record.Lane == domain.DreamLaneEvidenceDiscovery && !dreamEvidenceHypothesisOwnedBy(record, actorProfileID) {
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, ErrDreamNotFound
	}
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
	idempotencyKey, err := s.confirmationIdempotencyKey(ctx, teamID, actorProfileID, req, record, decision)
	if err != nil {
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, err
	}
	rememberRequest := rememberapp.RememberRequest{
		Evidence:          evidence,
		EntityHints:       req.EntityHints,
		RelationshipHints: req.RelationshipHints,
		IdempotencyKey:    idempotencyKey,
	}
	remember, err := s.deps.Remember.Remember(ctx, rememberRequest)
	if err != nil {
		var processErr *rememberapp.RememberProcessError
		if !errors.As(err, &processErr) || processErr.Result == nil {
			diagnostic.capture(record, "confirmation", "failed", err.Error(), map[string]any{
				"decision": decision,
			})
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		remember = rememberResultFromProcessError(processErr.Result)
	}
	completed, ingestID, err := dreamRememberCompletion(remember)
	if err != nil {
		diagnostic.capture(record, "confirmation", "failed", err.Error(), map[string]any{
			"decision": decision,
		})
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, err
	}
	if !completed {
		applyDreamTerminalRetryGuidance(remember, record.HypothesisID, decision)
		s.recordDreamFeedback(ctx, decision, dream, "error")
		diagnostic.capture(record, "confirmation", "failed", "remember did not complete", map[string]any{
			"decision": decision,
		})
		return &ResolveFeedbackResult{Dream: dream, Memory: remember}, nil
	}
	updated, err := s.submitDreamHypothesisWithRetry(ctx, store, dreamcontract.SubmitHypothesisInput{
		TeamID:            teamID,
		ActorProfileID:    actorProfileID,
		HypothesisID:      dreamID,
		Decision:          decision,
		SubmittedIngestID: ingestID,
		InvalidatedReason: req.Feedback,
	})
	result, feedbackErr := s.feedbackResult(ctx, decision, dream, updated, remember, err)
	cause := ""
	if feedbackErr != nil {
		cause = feedbackErr.Error()
	}
	confirmationOutcome := decision
	if feedbackErr != nil {
		confirmationOutcome = "failed"
	}
	relationshipResults, relationshipResultsTruncated := diagnosticRelationshipResults(func() []rememberapp.SubmissionRelationshipResult {
		if remember == nil || remember.Terminal == nil {
			return nil
		}
		return remember.Terminal.RelationshipResults
	}())
	details := map[string]any{
		"submitted_ingest_id":  ingestID,
		"decision":             decision,
		"relationship_results": relationshipResults,
	}
	if relationshipResultsTruncated {
		details["relationship_results_truncated"] = true
	}
	diagnostic.capture(record, "confirmation", confirmationOutcome, cause, details)
	return result, feedbackErr
}

func diagnosticRelationshipResults(results []rememberapp.SubmissionRelationshipResult) ([]map[string]any, bool) {
	const (
		maxDiagnosticRelationshipResults = 24
		maxDiagnosticRelationshipSplits  = 8
	)
	truncated := len(results) > maxDiagnosticRelationshipResults
	projected := make([]map[string]any, 0, min(len(results), maxDiagnosticRelationshipResults))
	for resultIndex, result := range results {
		if resultIndex >= maxDiagnosticRelationshipResults {
			break
		}
		if len(result.Splits) > maxDiagnosticRelationshipSplits {
			truncated = true
		}
		item := map[string]any{
			"disposition": result.Disposition,
			"splits":      make([]map[string]any, 0, min(len(result.Splits), maxDiagnosticRelationshipSplits)),
		}
		if diagnosticRelationshipReasonAllowed(result.Reason) {
			item["reason"] = result.Reason
		}
		splits := item["splits"].([]map[string]any)
		for splitIndex, split := range result.Splits {
			if splitIndex >= maxDiagnosticRelationshipSplits {
				break
			}
			splits = append(splits, map[string]any{
				"split_index":          split.SplitIndex,
				"relationship_id":      split.RelationshipID,
				"relationship_version": split.RelationshipVersion,
				"status":               split.Status,
			})
		}
		item["splits"] = splits
		projected = append(projected, item)
	}
	return projected, truncated
}

func diagnosticRelationshipReasonAllowed(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "not_supported_by_evidence", "stale_input", "submission_policy_rejected", "security_quarantine", "internal_failure":
		return true
	default:
		return false
	}
}

func dreamEvidenceHypothesisOwnedBy(record *dreamcontract.HypothesisRecord, profileID string) bool {
	if record == nil {
		return false
	}
	actorID, err := uuid.Parse(strings.TrimSpace(profileID))
	if err != nil {
		return false
	}
	for _, ownerID := range record.SourceOwnerProfileIDs {
		ownerUUID, err := uuid.Parse(strings.TrimSpace(ownerID))
		if err == nil && ownerUUID == actorID {
			return true
		}
	}
	return false
}

func (s *service) submitDreamHypothesisWithRetry(
	ctx context.Context,
	store dreamcontract.DreamRepository,
	input dreamcontract.SubmitHypothesisInput,
) (*dreamcontract.HypothesisRecord, error) {
	updated, err := store.SubmitHypothesis(ctx, input)
	if err == nil {
		return updated, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	retryCtx, cancel := context.WithTimeout(ctx, dreamConfirmationFinalizationTimeout)
	defer cancel()
	retried, retryErr := store.SubmitHypothesis(retryCtx, input)
	if retryErr == nil {
		return retried, nil
	}
	return nil, errors.Join(err, fmt.Errorf("resolve dream feedback: submit hypothesis retry: %w", retryErr))
}

func dreamSubmissionEvidence(
	req ResolveFeedbackRequest,
	record *dreamcontract.HypothesisRecord,
) ([]rememberapp.RememberEvidenceInput, error) {
	return dreamSubmissionEvidenceWithStatus(req, record, record.Status, false)
}

func dreamSubmissionEvidenceWithStatus(
	req ResolveFeedbackRequest,
	record *dreamcontract.HypothesisRecord,
	statusBefore string,
	legacy bool,
) ([]rememberapp.RememberEvidenceInput, error) {
	if len(req.Evidence) == 0 {
		return nil, fmt.Errorf("%w: independent evidence is required", ErrDreamFeedbackInvalidInput)
	}
	out := make([]rememberapp.RememberEvidenceInput, 0, len(req.Evidence))
	for i, item := range req.Evidence {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			return nil, fmt.Errorf("%w: evidence[%d].content is required", ErrDreamFeedbackInvalidInput, i)
		}
		if strings.EqualFold(content, strings.TrimSpace(record.Statement)) {
			return nil, fmt.Errorf("%w: hypothesis text cannot be submitted as its own evidence", ErrDreamFeedbackInvalidInput)
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
	record *dreamcontract.HypothesisRecord,
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
	legacyHash, err := rememberapp.CanonicalLegacyRequestBodyHash(evidence, req.EntityHints, req.RelationshipHints)
	if err != nil {
		return false, fmt.Errorf("resolve dream feedback: legacy replay request hash: %w", err)
	}
	if strings.TrimSpace(record.SubmittedIngestRequestHash) == legacyHash {
		return true, nil
	}
	for _, statusBefore := range domain.HypothesisStatuses() {
		legacyEvidence, err := dreamSubmissionEvidenceWithStatus(req, record, statusBefore, true)
		if err != nil {
			return false, err
		}
		legacyHash, err := rememberapp.CanonicalLegacyRequestBodyHash(legacyEvidence, req.EntityHints, req.RelationshipHints)
		if err != nil {
			return false, fmt.Errorf("resolve dream feedback: legacy replay request hash: %w", err)
		}
		if strings.TrimSpace(record.SubmittedIngestRequestHash) == legacyHash {
			return true, nil
		}
	}
	return false, nil
}

func dreamReplayRememberResult(record *dreamcontract.HypothesisRecord) *rememberapp.RememberResult {
	if record == nil {
		return nil
	}
	terminal := &rememberapp.TerminalRememberResult{
		ContractVersion:     domain.ContractVersion,
		SubmissionID:        record.SubmittedIngestID,
		SubmissionKind:      "remember",
		ProcessingState:     string(rememberapp.TerminalProcessingCompleted),
		SearchState:         string(rememberapp.TerminalSearchCurrent),
		Evidence:            []rememberapp.TerminalEvidenceResult{},
		RelationshipResults: []rememberapp.SubmissionRelationshipResult{},
		Errors:              []rememberapp.SubmissionStatusError{},
		Kind:                rememberapp.ResultKindTerminal,
	}
	return &rememberapp.RememberResult{
		ContractVersion: domain.ContractVersion,
		IngestID:        record.SubmittedIngestID,
		SubmissionID:    record.SubmittedIngestID,
		SubmissionKind:  "remember",
		ProcessingState: terminal.ProcessingState,
		SearchState:     terminal.SearchState,
		Kind:            rememberapp.ResultKindTerminal,
		Terminal:        terminal,
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

func applyDreamTerminalRetryGuidance(result *rememberapp.RememberResult, dreamID, decision string) {
	if result == nil || result.Terminal == nil {
		return
	}
	retryKey := fmt.Sprintf("dream-feedback:%s:%s:retry:%s", dreamID, decision, result.Terminal.SubmissionID)
	if len([]rune(retryKey)) > 128 {
		retryKey = "dream-feedback:" + dreamID + ":" + decision + ":retry"
	}
	for index := range result.Terminal.Errors {
		if !result.Terminal.Errors[index].Retryable || result.Terminal.Errors[index].NextAction != string(rememberapp.TerminalNextActionResubmitRemember) {
			continue
		}
		result.Terminal.Errors[index].NextAction = string(rememberapp.TerminalNextActionRetryDreamFeedback)
		result.Terminal.Errors[index].Remediation = rememberapp.DreamFeedbackRetryRemediation(retryKey)
	}
}

func dreamRememberCompletion(result *rememberapp.RememberResult) (bool, string, error) {
	if result == nil {
		return false, "", errors.New("resolve dream feedback: Remember result is required")
	}
	if result.Kind != rememberapp.ResultKindTerminal || result.Terminal == nil {
		return false, "", errors.New("resolve dream feedback: terminal Remember result is required")
	}
	switch result.Terminal.ProcessingState {
	case string(rememberapp.TerminalProcessingCompleted):
		ingestID := strings.TrimSpace(result.Terminal.SubmissionID)
		if _, err := uuid.Parse(ingestID); err != nil {
			return false, "", errors.New("resolve dream feedback: completed Remember result has no canonical ingest")
		}
		return true, ingestID, nil
	case string(rememberapp.TerminalProcessingFailed):
		return false, "", nil
	default:
		return false, "", errors.New("resolve dream feedback: terminal Remember result has an unsupported processing state")
	}
}

func dreamConfirmationReplayMatches(record *dreamcontract.HypothesisRecord, req ResolveFeedbackRequest, decision string) bool {
	if record == nil || record.Status != string(domain.DreamStatusSubmitted) || strings.TrimSpace(record.SubmittedIngestID) == "" {
		return true
	}
	storedKey := strings.TrimSpace(record.SubmittedIngestIdempotencyKey)
	if storedKey == "" || strings.TrimSpace(record.SubmittedDecision) != decision {
		return false
	}
	if storedKey == dreamFeedbackIdempotency(req, record.HypothesisID, decision) {
		return true
	}
	return strings.TrimSpace(req.IdempotencyKey) == "" &&
		storedKey == dreamFeedbackIdempotency(req, strings.TrimSpace(req.DreamID), decision)
}

func dreamFeedbackIdempotency(req ResolveFeedbackRequest, dreamID string, decision string) string {
	if value := strings.TrimSpace(req.IdempotencyKey); value != "" {
		return value
	}
	return dreamDefaultFeedbackIdempotency(dreamID, decision)
}

func dreamDefaultFeedbackIdempotency(dreamID, decision string) string {
	return "dream-feedback:" + strings.TrimSpace(dreamID) + ":" + strings.TrimSpace(decision)
}

func (s *service) confirmationIdempotencyKey(
	_ context.Context,
	_ string,
	_ string,
	req ResolveFeedbackRequest,
	record *dreamcontract.HypothesisRecord,
	decision string,
) (string, error) {
	if record == nil {
		return "", errors.New("resolve dream feedback: hypothesis record is required")
	}
	return dreamFeedbackIdempotency(req, record.HypothesisID, decision), nil
}
