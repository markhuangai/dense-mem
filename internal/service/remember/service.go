package remember

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	requestHashContractVersion = "dense-mem.v2.6.1"
)

var (
	ErrRememberAuthContext             = errors.New("remember: authenticated actor context is required")
	ErrRememberConflict                = errors.New("remember: conflict")
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
	EvidenceIndex         int                    `json:"evidence_index"`
	SupersededEvidenceIDs []string               `json:"superseded_evidence_ids"`
	SearchState           string                 `json:"search_state"`
	Reason                string                 `json:"reason,omitempty"`
	Error                 *SubmissionStatusError `json:"error,omitempty"`
}

func (s *service) Remember(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	started := time.Now()
	ctx = correlation.WithID(ctx, normalizeSynchronousCorrelationID(correlation.FromContext(ctx)))
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
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("remember: idempotency_key is required")
	}
	if err := validateRememberRelationshipCoverage(len(req.Evidence), req.RelationshipHints); err != nil {
		return nil, err
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
			return nil, ErrRememberPersistence
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

func normalizeSynchronousCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxTerminalCorrelationIDRunes {
		return uuid.NewString()
	}
	return value
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

func validateRememberRelationshipCoverage(evidenceCount int, relationships []map[string]any) error {
	covered := make([]bool, evidenceCount)
	for _, relationship := range relationships {
		for _, rawIndex := range rememberArrayValues(relationship["evidence_indices"]) {
			index, ok := rememberEvidenceIndex(rawIndex)
			if ok && index >= 0 && index < len(covered) {
				covered[index] = true
			}
		}
	}
	missing := make([]int, 0)
	for index, present := range covered {
		if !present {
			missing = append(missing, index)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("remember: relationship evidence_indices must cover every evidence item; missing evidence indexes: %v", missing)
	}
	return nil
}

func rememberArrayValues(raw any) []any {
	switch values := raw.(type) {
	case []any:
		return values
	case []map[string]any:
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out
	case []string:
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out
	default:
		return nil
	}
}

func rememberEvidenceIndex(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case uint:
		return int(value), true
	case uint8:
		return int(value), true
	case uint16:
		return int(value), true
	case uint32:
		return int(value), true
	case uint64:
		return int(value), true
	case float64:
		if value == float64(int(value)) {
			return int(value), true
		}
	case float32:
		if value == float32(int(value)) {
			return int(value), true
		}
	case json.Number:
		index, err := strconv.Atoi(string(value))
		return index, err == nil
	case string:
		index, err := strconv.Atoi(strings.TrimSpace(value))
		return index, err == nil
	}
	return 0, false
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
		Kind:                ResultKindTerminal,
	}
	for _, evidence := range result.Evidence {
		terminal.Evidence = append(terminal.Evidence, TerminalEvidenceResult{
			Disposition:           evidence.Disposition,
			EvidenceID:            evidence.EvidenceID,
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
