package remember

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	// Request hashes retain the v2.6.2 body contract so retries can replay
	// attempts created before the v2.6.3 public contract change.
	requestHashContractVersion = domain.PreviousContractVersion
)

var (
	ErrRememberAuthContext             = errors.New("remember: authenticated actor context is required")
	ErrRememberConflict                = errors.New("remember: conflict")
	ErrRememberPolicyRejected          = errors.New("remember: submission rejected by semantic policy")
	ErrRememberStaleInput              = errors.New("remember: stale input")
	ErrRememberPersistence             = errors.New("remember: persistence failed")
	ErrRememberProcessor               = errors.New("remember: synchronous processor is unavailable")
	ErrRememberProviderUnavailable     = errors.New("remember: provider unavailable")
	ErrRememberProviderResponseInvalid = errors.New("remember: provider response invalid")
	ErrRememberInputBudgetExceeded     = errors.New("remember: input budget exceeded")
	ErrRememberEmbeddingUnavailable    = errors.New("remember: embedding unavailable")
	ErrRememberEmbeddingInvalid        = errors.New("remember: embedding response invalid")
	ErrRememberCommitConflict          = errors.New("remember: commit conflict")
	ErrRememberDatabaseFailure         = errors.New("remember: database failure")
	ErrRememberRequestTimeout          = errors.New("remember: request timeout")
	ErrRememberRequestCancelled        = errors.New("remember: request cancelled")
)

const (
	maxRememberEvidenceItems = 20
	maxRememberEvidenceRunes = 999
	maxRememberRelationships = 200
)

// Service is the public application boundary for synchronous Remember.
type Service interface {
	Remember(context.Context, RememberRequest) (*RememberResult, error)
}

type RememberService = Service

type Dependencies struct {
	Synchronous SynchronousProcessor
	// Auditor is required for security-rejected submissions. A nil auditor
	// fails closed as ErrRememberPersistence instead of staging the input.
	Auditor SecurityRejectionAuditor
	Metrics observability.DiscoverabilityMetrics
	Logger  observability.LogProvider
}

type service struct {
	synchronous SynchronousProcessor
	auditor     SecurityRejectionAuditor
	metrics     observability.DiscoverabilityMetrics
	logger      observability.LogProvider
}

func NewService(deps Dependencies) *service {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	synchronous := deps.Synchronous
	return &service{synchronous: synchronous, auditor: deps.Auditor, metrics: metrics, logger: deps.Logger}
}

type RememberRequest struct {
	Evidence          []RememberEvidenceInput `json:"evidence"`
	EntityHints       []map[string]any        `json:"entity_hints,omitempty"`
	RelationshipHints []map[string]any        `json:"relationship_hints,omitempty"`
	IdempotencyKey    string                  `json:"idempotency_key,omitempty"`
}

type RememberEvidenceInput struct {
	Content                string         `json:"content"`
	ForceInsert            bool           `json:"force_insert,omitempty"`
	SourceType             string         `json:"source_type,omitempty"`
	Source                 string         `json:"source,omitempty"`
	SourceGroup            string         `json:"source_group,omitempty"`
	Authority              string         `json:"authority,omitempty"`
	SourceKey              string         `json:"source_key,omitempty"`
	SourceRevision         string         `json:"source_revision,omitempty"`
	PreviousSourceRevision string         `json:"previous_source_revision,omitempty"`
	SupersedesEvidenceIDs  []string       `json:"supersedes_evidence_ids,omitempty"`
	Labels                 []string       `json:"labels,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
}

type RememberResult struct {
	ContractVersion      string                          `json:"contract_version"`
	IngestID             string                          `json:"-"`
	SubmissionID         string                          `json:"submission_id"`
	SubmissionKind       string                          `json:"submission_kind"`
	ProcessingState      string                          `json:"processing_state"`
	SearchState          string                          `json:"search_state"`
	CorrelationID        string                          `json:"correlation_id,omitempty"`
	Evidence             []SubmissionEvidenceStatus      `json:"evidence"`
	Errors               []SubmissionStatusError         `json:"errors"`
	RelationshipResults  []SubmissionRelationshipResult  `json:"relationship_results"`
	Warnings             []string                        `json:"warnings,omitempty"`
	AwaitingConfirmation *SubmissionAwaitingConfirmation `json:"awaiting_confirmation,omitempty"`
	CorrectionResult     *RelationshipCorrectionResult   `json:"correction_result,omitempty"`
	Kind                 ResultKind                      `json:"-"`
	Terminal             *TerminalRememberResult         `json:"-"`
}

type SubmissionStatusResult struct {
	ContractVersion      string                          `json:"contract_version"`
	SubmissionID         string                          `json:"submission_id"`
	SubmissionKind       string                          `json:"submission_kind"`
	ProcessingState      string                          `json:"processing_state"`
	SearchState          string                          `json:"search_state"`
	CorrelationID        string                          `json:"correlation_id,omitempty"`
	Evidence             []SubmissionEvidenceStatus      `json:"evidence"`
	Errors               []SubmissionStatusError         `json:"errors"`
	RelationshipResults  []SubmissionRelationshipResult  `json:"relationship_results"`
	Warnings             []string                        `json:"warnings,omitempty"`
	QuarantineExpiresAt  *time.Time                      `json:"-"`
	AwaitingConfirmation *SubmissionAwaitingConfirmation `json:"awaiting_confirmation,omitempty"`
	CorrectionResult     *RelationshipCorrectionResult   `json:"correction_result,omitempty"`
	Kind                 ResultKind                      `json:"-"`
	Terminal             *TerminalRememberResult         `json:"-"`
}

type SubmissionAwaitingConfirmation struct {
	ConfirmationToken string                            `json:"confirmation_token"`
	ExpiresAt         time.Time                         `json:"expires_at"`
	Candidates        []RelationshipCorrectionCandidate `json:"candidates"`
}

type RelationshipCorrectionCandidate struct {
	Endpoint      string `json:"endpoint"`
	EntityID      string `json:"entity_id"`
	EntityKind    string `json:"entity_kind"`
	CanonicalName string `json:"canonical_name"`
}

type RelationshipCorrectionResult struct {
	OriginalRelationshipID  string `json:"original_relationship_id"`
	OriginalVersion         int    `json:"original_version"`
	SuccessorRelationshipID string `json:"successor_relationship_id"`
	SuccessorVersion        int    `json:"successor_version"`
	ReusedSuccessor         bool   `json:"reused_successor"`
}

type SubmissionEvidenceStatus struct {
	Disposition           string                 `json:"disposition"`
	EvidenceID            string                 `json:"evidence_id,omitempty"`
	ContentHash           string                 `json:"content_hash,omitempty"`
	EvidenceIndex         int                    `json:"evidence_index"`
	SupersededEvidenceIDs []string               `json:"superseded_evidence_ids"`
	SearchState           string                 `json:"search_state"`
	Reason                string                 `json:"reason,omitempty"`
	Error                 *SubmissionStatusError `json:"error,omitempty"`
}

func (s *service) Remember(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	started := time.Now()
	ctx = correlation.WithID(ctx, NormalizeTerminalCorrelationID(correlation.FromContext(ctx)))
	ctx = WithRememberDeadlines(ctx, started)
	operationCtx, cancel := context.WithDeadline(ctx, started.Add(RememberTotalBudget))
	defer cancel()
	ctx = operationCtx
	if s == nil {
		return nil, errors.New("remember: service is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrRememberAuthContext
	}
	space := rememberSpace(actor)
	if space.Kind != domain.MemorySpaceTeamShared && space.Kind != "" &&
		(space.ID == uuid.Nil || space.Generation < 1) {
		return nil, ErrRememberAuthContext
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("remember: evidence is required")
	}
	if len(req.Evidence) > maxRememberEvidenceItems {
		return nil, fmt.Errorf("remember: evidence must contain at most %d entries", maxRememberEvidenceItems)
	}
	if len(req.RelationshipHints) > maxRememberRelationships {
		return nil, fmt.Errorf("remember: relationships must contain at most %d entries", maxRememberRelationships)
	}
	for index, evidence := range req.Evidence {
		if len([]rune(evidence.Content)) == 0 || len([]rune(evidence.Content)) > maxRememberEvidenceRunes {
			return nil, fmt.Errorf("remember: evidence[%d].content must contain between 1 and %d characters", index, maxRememberEvidenceRunes)
		}
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("remember: idempotency_key is required")
	}
	contents := make([]string, 0, len(req.Evidence))
	for _, evidence := range req.Evidence {
		contents = append(contents, evidence.Content)
	}
	proposal := map[string]any{
		"entity_hints":       req.EntityHints,
		"relationship_hints": req.RelationshipHints,
	}
	scan, scanErr := scanSubmissionWithProviderProposal(contents, proposal)
	if scanErr != nil {
		if err := recordSubmissionSecurityRejection(ctx, s.auditor, s.logger, actor, "remember", scan, scanErr); err != nil {
			observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
			return nil, rememberSecurityAuditPersistenceError(req, correlation.FromContext(ctx))
		}
		if s.synchronous == nil {
			observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
			return nil, scanErr
		}
	}

	requestHash, err := canonicalRequestHash(req)
	if err != nil {
		return nil, err
	}
	correlationID := correlation.FromContext(ctx)
	actorMetadata := map[string]any{
		"team_id":        actor.TeamID.String(),
		"owner_id":       actor.OwnerID.String(),
		"correlation_id": correlationID,
		"role":           actor.Role,
		"auth_method":    actor.AuthMethod,
	}
	if actor.CredentialID != nil {
		actorMetadata["credential_id"] = actor.CredentialID.String()
	}
	metadata := map[string]any{"contract_version": domain.ContractVersion, "actor": actorMetadata}
	processInput := RememberProcessRequest{
		TeamID: actor.TeamID.String(), OwnerProfileID: actor.OwnerID.String(), SpaceID: rememberSpaceID(space),
		SpaceGeneration: space.Generation, IdempotencyKey: strings.TrimSpace(req.IdempotencyKey), RequestHash: requestHash,
		SourceSummary: sourceSummary(req.Evidence), Proposal: proposal, Metadata: metadata,
		Evidence: repositoryEvidenceInputs(req.Evidence),
	}
	if scanErr != nil {
		processInput.SecuritySignals = append([]SubmissionSecurityBatchSignal(nil), scan.Signals...)
		processInput.SecuritySignalsTruncated = scan.SignalsTruncated
		processInput.SecurityRejected = true
	}
	if s.synchronous != nil {
		terminal, err := s.synchronous.ProcessRemember(ctx, processInput)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				err = preserveProcessStatus(err, ErrRememberRequestTimeout)
			} else if errors.Is(err, context.Canceled) {
				err = preserveProcessStatus(err, ErrRememberRequestCancelled)
			}
			err = preserveProcessResult(err)
			observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
			return nil, err
		}
		if terminal == nil {
			observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
			return nil, ErrRememberProcessor
		}
		observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "ok")
		return rememberResultFromStatus(terminal, terminal.SubmissionID), nil
	}
	observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
	return nil, ErrRememberProcessor
}

func rememberSecurityAuditPersistenceError(req RememberRequest, correlationID string) *RememberProcessError {
	status := &SubmissionStatusResult{
		ContractVersion:     domain.ContractVersion,
		SubmissionID:        uuid.NewString(),
		SubmissionKind:      "remember",
		ProcessingState:     string(TerminalProcessingFailed),
		SearchState:         string(TerminalSearchNotRequired),
		CorrelationID:       correlationID,
		Evidence:            make([]SubmissionEvidenceStatus, len(req.Evidence)),
		RelationshipResults: make([]SubmissionRelationshipResult, len(req.RelationshipHints)),
		Errors: []SubmissionStatusError{StatusErrorWithDetails(
			SubmissionErrorDatabaseFailure,
			"security_audit_persistence",
			map[string]any{"component": "remember.security_audit", "server_owned": true},
		)},
	}
	for index := range status.Evidence {
		status.Evidence[index] = SubmissionEvidenceStatus{
			Disposition:           "not_stored",
			ContentHash:           evidenceContentHash(req.Evidence[index].Content),
			EvidenceIndex:         index,
			SupersededEvidenceIDs: []string{},
			SearchState:           string(TerminalSearchNotRequired),
			Reason:                "internal_failure",
		}
	}
	for index, hint := range req.RelationshipHints {
		ref, _ := hint["ref"].(string)
		status.RelationshipResults[index] = SubmissionRelationshipResult{
			RelationshipRef: strings.TrimSpace(ref),
			Disposition:     "not_stored",
			Reason:          "internal_failure",
			Splits:          []SubmissionRelationshipSplit{},
		}
	}
	result := rememberResultFromStatus(status, status.SubmissionID)
	return &RememberProcessError{Status: status, Result: result.Terminal, Err: ErrRememberPersistence}
}

func preserveProcessStatus(err error, mapped error) error {
	var processErr *RememberProcessError
	if errors.As(err, &processErr) && processErr.Status != nil {
		return &RememberProcessError{Status: processErr.Status, Result: processErr.Result, Err: mapped}
	}
	return mapped
}

func preserveProcessResult(err error) error {
	var processErr *RememberProcessError
	if !errors.As(err, &processErr) || processErr == nil || processErr.Result != nil || processErr.Status == nil {
		return err
	}
	result := rememberResultFromStatus(processErr.Status, processErr.Status.SubmissionID)
	if result != nil {
		processErr.Result = result.Terminal
	}
	return err
}

func rememberSpaceID(space domain.MemorySpaceAccess) string {
	if space.ID == uuid.Nil {
		return ""
	}
	return space.ID.String()
}

func rememberSpace(actor requestctx.Actor) domain.MemorySpaceAccess {
	var shared domain.MemorySpaceAccess
	for _, space := range actor.AllowedSpaces {
		if space.Kind == domain.MemorySpaceTeamShared || space.Kind == "" {
			shared = space
			continue
		}
		return space
	}
	return shared
}

func sourceRevisionContentHashes(evidence []RememberEvidenceInput) map[string]string {
	groups := make(map[string][]string)
	for _, item := range evidence {
		if key := sourceRevisionBatchKey(item); key != "" {
			groups[key] = append(groups[key], item.Content)
		}
	}
	hashes := make(map[string]string, len(groups))
	for key, items := range groups {
		hashes[key] = sourceRevisionBatchHash(items)
	}
	return hashes
}

func sourceRevisionBatchKey(item RememberEvidenceInput) string {
	sourceKey, revision := strings.TrimSpace(item.SourceKey), strings.TrimSpace(item.SourceRevision)
	if sourceKey == "" || revision == "" {
		return ""
	}
	return sourceKey + "\x00" + revision + "\x00" + strings.TrimSpace(item.PreviousSourceRevision)
}

func sourceRevisionBatchHash(contents []string) string {
	h := sha256.New()
	for _, content := range contents {
		_, _ = fmt.Fprintf(h, "%d:", len(content))
		_, _ = h.Write([]byte(content))
		_, _ = h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func canonicalRequestHash(req RememberRequest) (string, error) {
	hash, err := CanonicalRequestBodyHash(req.Evidence, req.EntityHints, req.RelationshipHints)
	if err != nil {
		return "", fmt.Errorf("remember: canonical request hash: %w", err)
	}
	return hash, nil
}

func rememberResultFromStatus(status *SubmissionStatusResult, ingestID string) *RememberResult {
	if status == nil {
		return nil
	}
	copyStatus := *status
	copyStatus.Evidence = append([]SubmissionEvidenceStatus(nil), status.Evidence...)
	copyStatus.RelationshipResults = append([]SubmissionRelationshipResult(nil), status.RelationshipResults...)
	copyStatus.Errors = append([]SubmissionStatusError(nil), status.Errors...)
	copyStatus.Warnings = append([]string(nil), status.Warnings...)
	if copyStatus.Evidence == nil {
		copyStatus.Evidence = []SubmissionEvidenceStatus{}
	}
	if copyStatus.RelationshipResults == nil {
		copyStatus.RelationshipResults = []SubmissionRelationshipResult{}
	}
	if copyStatus.Errors == nil {
		copyStatus.Errors = []SubmissionStatusError{}
	}
	for index := range copyStatus.Evidence {
		if copyStatus.Evidence[index].Disposition == "not_stored" {
			copyStatus.Evidence[index].EvidenceID = ""
		}
	}
	result := &RememberResult{
		ContractVersion:      copyStatus.ContractVersion,
		IngestID:             ingestID,
		SubmissionID:         copyStatus.SubmissionID,
		SubmissionKind:       copyStatus.SubmissionKind,
		ProcessingState:      copyStatus.ProcessingState,
		SearchState:          copyStatus.SearchState,
		CorrelationID:        copyStatus.CorrelationID,
		Evidence:             copyStatus.Evidence,
		Errors:               copyStatus.Errors,
		RelationshipResults:  copyStatus.RelationshipResults,
		Warnings:             copyStatus.Warnings,
		AwaitingConfirmation: copyStatus.AwaitingConfirmation,
		CorrectionResult:     copyStatus.CorrectionResult,
		Kind:                 copyStatus.Kind,
		Terminal:             copyStatus.Terminal,
	}
	if result.SubmissionID == "" {
		result.SubmissionID = ingestID
	}
	if result.SubmissionKind == "" {
		result.SubmissionKind = "remember"
	}
	if result.ContractVersion == "" {
		result.ContractVersion = domain.ContractVersion
	}
	if result.SearchState == "" {
		result.SearchState = string(domain.SearchProjectionNotRequired)
	}
	for index := range result.Evidence {
		if result.Evidence[index].SupersededEvidenceIDs == nil {
			result.Evidence[index].SupersededEvidenceIDs = []string{}
		}
		if result.Evidence[index].SearchState == "" {
			result.Evidence[index].SearchState = result.SearchState
		}
	}
	for index := range result.RelationshipResults {
		if result.RelationshipResults[index].Splits == nil {
			result.RelationshipResults[index].Splits = []SubmissionRelationshipSplit{}
		}
	}
	terminal := &TerminalRememberResult{
		ContractVersion:     result.ContractVersion,
		SubmissionID:        result.SubmissionID,
		SubmissionKind:      result.SubmissionKind,
		ProcessingState:     result.ProcessingState,
		SearchState:         result.SearchState,
		CorrelationID:       result.CorrelationID,
		Evidence:            make([]TerminalEvidenceResult, 0, len(result.Evidence)),
		RelationshipResults: append([]SubmissionRelationshipResult(nil), result.RelationshipResults...),
		Errors:              append([]SubmissionStatusError(nil), result.Errors...),
		Warnings:            append([]string(nil), result.Warnings...),
		Kind:                ResultKindTerminal,
	}
	for _, evidence := range result.Evidence {
		terminal.Evidence = append(terminal.Evidence, TerminalEvidenceResult{
			Disposition:           evidence.Disposition,
			EvidenceID:            evidence.EvidenceID,
			ContentHash:           evidence.ContentHash,
			EvidenceIndex:         evidence.EvidenceIndex,
			SupersededEvidenceIDs: append([]string(nil), evidence.SupersededEvidenceIDs...),
			SearchState:           evidence.SearchState,
			Reason:                evidence.Reason,
		})
	}
	if terminal.Evidence == nil {
		terminal.Evidence = []TerminalEvidenceResult{}
	}
	for index := range terminal.Evidence {
		if terminal.Evidence[index].SupersededEvidenceIDs == nil {
			terminal.Evidence[index].SupersededEvidenceIDs = []string{}
		}
	}
	if terminal.RelationshipResults == nil {
		terminal.RelationshipResults = []SubmissionRelationshipResult{}
	}
	if terminal.Errors == nil {
		terminal.Errors = []SubmissionStatusError{}
	}
	result.Kind = ResultKindTerminal
	result.Terminal = terminal
	return result
}

func evidenceSourceType(value string) string {
	if value = strings.TrimSpace(value); value == "" {
		return "conversation"
	}
	return value
}

func ledgerAuthorityAndMetadata(authority string, metadata map[string]any) (string, map[string]any) {
	out := make(map[string]any)
	for key, value := range metadata {
		out[key] = value
	}
	authority = strings.TrimSpace(authority)
	if authority != "" {
		out["contract_authority"] = authority
	}
	if authority == "" {
		return string(domain.AuthorityPrimary), out
	}
	return authority, out
}

func evidenceProcessingIntentMetadata(metadata map[string]any, item RememberEvidenceInput) map[string]any {
	if len(item.SupersedesEvidenceIDs) > 0 {
		metadata["supersedes_evidence_ids"] = append([]string(nil), item.SupersedesEvidenceIDs...)
	}
	if value := strings.TrimSpace(item.SourceGroup); value != "" {
		metadata["contract_source_group"] = value
	}
	return metadata
}

func sourceRevisionEnvelope(item RememberEvidenceInput) map[string]any {
	return map[string]any{"source_type": item.SourceType, "source": item.Source, "source_group": item.SourceGroup, "metadata": item.Metadata}
}

func sourceSummary(evidence []RememberEvidenceInput) string {
	for _, item := range evidence {
		if value := strings.TrimSpace(item.SourceKey); value != "" {
			return value
		}
		if value := strings.TrimSpace(item.Source); value != "" {
			return value
		}
	}
	return fmt.Sprintf("remember evidence_count=%d", len(evidence))
}
