package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
)

type Dependencies struct {
	Repository          session.Repository
	Preparer            session.Preparer
	Extractor           session.Extractor
	Enabled             bool
	Tokenizer           string
	Auditor             remember.SecurityRejectionAuditor
	Logger              observability.LogProvider
	DiagnosticProtector observability.DiagnosticProtector
	DiagnosticRecorder  func() modelprovider.SnapshotRecorder
}

type Service struct{ deps Dependencies }

func NewService(deps Dependencies) *Service { return &Service{deps: deps} }

func PrivateScope(ctx context.Context) (session.Scope, error) {
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return session.Scope{}, session.ErrUnauthorized
	}
	write := false
	for _, grant := range actor.Grants {
		if grant == "write" {
			write = true
			break
		}
	}
	if !write {
		return session.Scope{}, session.ErrUnauthorized
	}
	var selected *domain.MemorySpaceAccess
	for _, space := range actor.AllowedSpaces {
		if space.Kind != domain.MemorySpaceProfilePrivate && space.Kind != domain.MemorySpaceCredentialPrivate {
			continue
		}
		if space.ID == uuid.Nil || space.Generation < 1 || selected != nil {
			return session.Scope{}, session.ErrUnauthorized
		}
		copy := space
		selected = &copy
	}
	if selected == nil {
		return session.Scope{}, session.ErrUnauthorized
	}
	return session.Scope{TeamID: actor.TeamID.String(), OwnerProfileID: actor.OwnerID.String(), SpaceID: selected.ID.String(), SpaceGeneration: selected.Generation}, nil
}

func (s *Service) Available(ctx context.Context) bool {
	if s == nil || !s.deps.Enabled || s.deps.Repository == nil || s.deps.Preparer == nil || s.deps.Extractor == nil {
		return false
	}
	_, err := PrivateScope(ctx)
	return err == nil
}

func (s *Service) Ingest(ctx context.Context, req session.Request) (*session.Result, error) {
	started := time.Now()
	ctx = correlation.WithID(ctx, remember.NormalizeTerminalCorrelationID(correlation.FromContext(ctx)))
	ctx = remember.WithRememberDeadlines(ctx, started)
	ctx, cancel := context.WithDeadline(ctx, started.Add(remember.RememberTotalBudget))
	defer cancel()
	if !s.Available(ctx) {
		return nil, session.ErrUnauthorized
	}
	scope, err := PrivateScope(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidateRequest(req); err != nil {
		return nil, err
	}
	hash, err := RequestHash(req)
	if err != nil {
		return nil, err
	}
	intake := session.Intake{Scope: scope, Request: req, RequestHash: hash, ExtractionVersion: session.ExtractionVersion, Tokenizer: s.deps.Tokenizer}
	var result *session.Result
	err = s.deps.Repository.WithSessionLock(ctx, scope, req, func() error {
		submission, err := s.deps.Repository.LookupSession(ctx, scope, req.IdempotencyKey)
		if errors.Is(err, session.ErrNotFound) {
			windows, windowErr := BuildWindows(req, s.deps.Tokenizer)
			if windowErr != nil {
				return windowErr
			}
			contents := make([]string, 0, len(req.Events))
			for _, event := range req.Events {
				contents = append(contents, event.Text)
			}
			scan, scanErr := remember.ScanSubmissionBatch(contents)
			if scanErr != nil {
				actor, _ := requestctx.ActorFromContext(ctx)
				if err := remember.RecordSubmissionSecurityRejection(ctx, s.deps.Auditor, s.deps.Logger, actor, "ingest_session", scan, scanErr); err != nil {
					return err
				}
				return session.ErrSecurity
			}
			intake.Windows = windows
			submission, err = s.deps.Repository.StageSession(ctx, intake)
		}
		if err != nil {
			return err
		}
		if submission.Intake.RequestHash != hash || submission.Intake.Scope != scope {
			return session.ErrRequestConflict
		}

		if submission.Intake.ExtractionVersion != session.ExtractionVersion {
			return session.ErrStale
		}
		if submission.Result != nil && (submission.Result.ProcessingState == "completed" || !retryableResult(submission.Result)) {
			result = submission.Result
			return nil
		}
		processCtx := ctx
		var recorder modelprovider.SnapshotRecorder
		if s.deps.DiagnosticRecorder != nil {
			recorder = s.deps.DiagnosticRecorder()
			processCtx = modelprovider.WithExchangeRecorder(ctx, recorder)
		}
		result, err = s.process(processCtx, submission)
		s.recordDiagnostics(ctx, submission, req, result, recorder)
		return err
	})
	return result, err
}

func retryableResult(result *session.Result) bool {
	for _, failure := range result.Errors {
		if failure.Retryable {
			return true
		}
	}
	return false
}

func (s *Service) process(ctx context.Context, submission *session.Submission) (*session.Result, error) {
	result := baseSessionResult(ctx, submission)
	fail := func(cause error) (*session.Result, error) {
		if ctx.Err() != nil {
			cause = errors.Join(ctx.Err(), cause)
		}
		result.ProcessingState = "failed"
		result.SearchState = "not_required"
		result.Errors = []session.Error{classifySessionError(cause)}
		persistenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remember.RememberFailurePersistenceBudget)
		err := s.deps.Repository.RecordSessionFailure(persistenceCtx, submission.Intake.Scope, submission.ID, result)
		cancel()
		if err != nil {
			return &result, errors.Join(cause, err)
		}
		return &result, cause
	}
	responses, warnings, err := s.extractWindows(ctx, submission)
	if err != nil {
		return fail(err)
	}
	result.Warnings = warnings
	var prepared *session.Prepared
	if len(responses) > 0 {
		linkRequest, err := buildLinkingRequest(submission, responses)
		if err != nil {
			return fail(err)
		}
		if len(linkRequest.Relationships) > 0 {
			linked, err := s.linkEntities(ctx, submission, linkRequest)
			if err != nil {
				return fail(err)
			}
			evidence, proposal, err := buildSessionAssessment(submission, linkRequest, linked)
			if err != nil {
				return fail(err)
			}
			actor, _ := requestctx.ActorFromContext(ctx)
			metadata := map[string]any{"contract_version": domain.ContractVersion, "actor": map[string]any{
				"team_id": actor.TeamID.String(), "owner_id": actor.OwnerID.String(), "correlation_id": correlation.FromContext(ctx), "role": actor.Role, "auth_method": actor.AuthMethod,
			}, "session_submission_id": submission.ID, "extraction_version": session.ExtractionVersion}
			prepared, err = s.deps.Preparer.PrepareSession(ctx, session.PrepareInput{
				Scope: submission.Intake.Scope, SubmissionID: submission.ID, RequestHash: submission.Intake.RequestHash,
				Evidence: evidence, Proposal: proposal, Metadata: metadata,
			})
			if err != nil {
				return fail(err)
			}
		}
	}
	result.ProcessingState = "completed"
	for index := range result.Events {
		result.Events[index].ProcessingState = "completed"
	}
	commitCtx, cancel := remember.ContextForPhase(ctx, remember.RememberPhaseCommit)
	committed, err := s.deps.Repository.CommitSession(commitCtx, submission.Intake.Scope, submission.ID, prepared, result)
	cancel()
	if err != nil {
		for index := range result.Events {
			if _, exists := submission.Intake.DuplicateResults[result.Events[index].EventID]; !exists {
				result.Events[index].ProcessingState = "failed"
			}
		}
		return fail(err)
	}
	return committed, nil
}

func baseSessionResult(ctx context.Context, submission *session.Submission) session.Result {
	result := session.Result{
		ContractVersion: domain.ContractVersion, SubmissionID: submission.ID, SubmissionKind: "session_ingest",
		CorrelationID: correlation.FromContext(ctx), AcceptedEventCount: submission.AcceptedEventCount,
		DuplicateEventCount: submission.DuplicateEventCount, ProcessingState: "failed", SearchState: "not_required",
		Events: []session.EventResult{}, RelationshipResults: []knowledge.SubmissionRelationshipResult{}, Errors: []session.Error{},
	}
	newIndices := map[int]bool{}
	for _, index := range submission.NewEventIndices {
		newIndices[index] = true
	}
	for index, event := range submission.Intake.Request.Events {
		outcome := session.EventResult{EventID: event.EventID, Disposition: "duplicate", ProcessingState: "failed", EvidenceIDs: []string{}}
		if newIndices[index] {
			outcome.Disposition = "accepted"
		}
		if duplicate, exists := submission.Intake.DuplicateResults[event.EventID]; exists {
			outcome.ProcessingState = duplicate.ProcessingState
			outcome.EvidenceIDs = append([]string{}, duplicate.EvidenceIDs...)
		}
		result.Events = append(result.Events, outcome)
	}
	return result
}

func classifySessionError(err error) session.Error {
	result := session.Error{Code: "database_failure", Message: "Session processing failed before semantic completion.", Retryable: true, NextAction: "retry_same_request", Remediation: "Resend the unchanged complete original request with its retained idempotency_key."}
	var preparation *session.ProcessingError
	switch {
	case errors.Is(err, context.Canceled):
		result.Code = "request_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		result.Code = "request_timeout"
	case errors.As(err, &preparation):
		result.Code = preparation.Code
		guidance := remember.StatusError(remember.SubmissionErrorCode(preparation.Code))
		result.Retryable = guidance.Retryable
		if !guidance.Retryable {
			result.NextAction = "contact_operator"
		}
	case errors.Is(err, modelprovider.ErrVerifierTimeout), errors.Is(err, modelprovider.ErrVerifierRateLimit), errors.Is(err, modelprovider.ErrVerifierProvider):
		result.Code = "provider_unavailable"
	case errors.Is(err, modelprovider.ErrVerifierMalformedResponse):
		result.Code = "provider_response_invalid"
	case errors.Is(err, session.ErrBudget):
		result.Code = "input_budget_exceeded"
		result.Retryable = false
		result.NextAction = "contact_operator"
		result.Remediation = "The complete request exceeds a server processing bound. Retain the original request and contact the operator."
	case errors.Is(err, session.ErrSecurity), errors.Is(err, remember.ErrEvidenceSecurityRejected), errors.Is(err, remember.ErrRememberPolicyRejected):
		result.Code = "submission_policy_rejected"
		result.Retryable = false
		result.NextAction = "none"
		result.Remediation = "The submitted content cannot be accepted under the evidence security policy."
	case errors.Is(err, session.ErrStale):
		result.Code = "stale_input"
		result.Retryable = false
		result.NextAction = "contact_operator"
		result.Remediation = "Private authorization or referenced evidence changed. Refresh authorization and inspect the original operation before resubmission."
	default:
		var provider *modelprovider.ProviderError
		var malformed *modelprovider.MalformedResponseError
		if errors.As(err, &provider) {
			result.Code = "provider_unavailable"
		}
		if errors.As(err, &malformed) {
			result.Code = "provider_response_invalid"
		}
	}
	return result
}

var _ session.API = (*Service)(nil)
