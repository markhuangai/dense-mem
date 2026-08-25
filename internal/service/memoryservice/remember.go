package memoryservice

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

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

const (
	rememberCheckAfterSeconds = 60
)

var (
	ErrRememberAuthContext = errors.New("remember: authenticated actor context is required")
	ErrRememberConflict    = errors.New("remember: conflict")
	ErrRememberPersistence = errors.New("remember: persistence failed")
)

type RememberService interface {
	Remember(ctx context.Context, req RememberRequest) (*RememberResult, error)
}

// SubmissionStatusService exposes the bounded, owner-scoped status projection
// used by the public contract. Placement records remain internal worker state.
type SubmissionStatusService interface {
	GetSubmissionStatus(ctx context.Context, req GetSubmissionStatusRequest) (*SubmissionStatusResult, error)
}

type RememberDependencies struct {
	Ledger  repository.LedgerRepository
	Auditor SecurityRejectionAuditor
	Metrics observability.DiscoverabilityMetrics
	Logger  observability.LogProvider
}

type rememberService struct {
	ledger  repository.LedgerRepository
	auditor SecurityRejectionAuditor
	metrics observability.DiscoverabilityMetrics
	logger  observability.LogProvider
}

func NewRememberService(deps RememberDependencies) *rememberService {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &rememberService{ledger: deps.Ledger, auditor: deps.Auditor, metrics: metrics, logger: deps.Logger}
}

type RememberRequest struct {
	Evidence          []RememberEvidenceInput `json:"evidence"`
	EntityHints       []map[string]any        `json:"entity_hints,omitempty"`
	RelationshipHints []map[string]any        `json:"relationship_hints,omitempty"`
	IdempotencyKey    string                  `json:"idempotency_key,omitempty"`
}

type GetSubmissionStatusRequest struct {
	SubmissionID string `json:"submission_id"`
}

const requestHashContractVersion = "dense-mem.v2.6.1"

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
	// IngestID is retained for internal compatibility and is never serialized.
	ContractVersion      string                                   `json:"contract_version"`
	IngestID             string                                   `json:"-"`
	SubmissionID         string                                   `json:"submission_id"`
	SubmissionKind       string                                   `json:"submission_kind"`
	ProcessingState      string                                   `json:"processing_state"`
	CheckAfterSeconds    int                                      `json:"-"`
	StatusTool           string                                   `json:"-"`
	CorrelationID        string                                   `json:"correlation_id"`
	SearchState          string                                   `json:"search_state"`
	Evidence             []SubmissionEvidenceStatus               `json:"evidence"`
	Errors               []SubmissionStatusError                  `json:"errors"`
	Degradations         []SubmissionStatusDegradation            `json:"-"`
	RelationshipResults  []SubmissionRelationshipResult           `json:"relationship_results"`
	QuarantineExpiresAt  *time.Time                               `json:"quarantine_expires_at,omitempty"`
	AwaitingConfirmation *SubmissionAwaitingConfirmation          `json:"awaiting_confirmation,omitempty"`
	CorrectionResult     *repository.RelationshipCorrectionResult `json:"correction_result,omitempty"`
}

type SubmissionStatusResult struct {
	ContractVersion      string                                   `json:"contract_version"`
	SubmissionID         string                                   `json:"submission_id"`
	SubmissionKind       string                                   `json:"submission_kind"`
	ProcessingState      string                                   `json:"processing_state"`
	SearchState          string                                   `json:"search_state"`
	CheckAfterSeconds    int                                      `json:"check_after_seconds"`
	CorrelationID        string                                   `json:"correlation_id,omitempty"`
	Attempts             *int                                     `json:"attempts,omitempty"`
	MaxAttempts          *int                                     `json:"max_attempts,omitempty"`
	SubmittedAt          *time.Time                               `json:"submitted_at,omitempty"`
	NextAttemptAt        *time.Time                               `json:"next_attempt_at,omitempty"`
	StartedAt            *time.Time                               `json:"started_at,omitempty"`
	UpdatedAt            *time.Time                               `json:"updated_at,omitempty"`
	CompletedAt          *time.Time                               `json:"completed_at,omitempty"`
	Evidence             []SubmissionEvidenceStatus               `json:"evidence"`
	Errors               []SubmissionStatusError                  `json:"errors"`
	Degradations         []SubmissionStatusDegradation            `json:"degradations"`
	RelationshipResults  []SubmissionRelationshipResult           `json:"relationship_results,omitempty"`
	QuarantineExpiresAt  *time.Time                               `json:"quarantine_expires_at,omitempty"`
	AwaitingConfirmation *SubmissionAwaitingConfirmation          `json:"awaiting_confirmation,omitempty"`
	CorrectionResult     *repository.RelationshipCorrectionResult `json:"correction_result,omitempty"`
}

type SubmissionRelationshipSplit struct {
	SplitIndex          int    `json:"split_index"`
	RelationshipID      string `json:"relationship_id"`
	RelationshipVersion int    `json:"relationship_version"`
	Status              string `json:"status"`
}

type SubmissionRelationshipResult struct {
	RelationshipRef string                        `json:"ref"`
	Disposition     string                        `json:"disposition"`
	Reason          string                        `json:"reason,omitempty"`
	Splits          []SubmissionRelationshipSplit `json:"splits"`
}

type SubmissionAwaitingConfirmation struct {
	ConfirmationToken string                                       `json:"confirmation_token"`
	ExpiresAt         time.Time                                    `json:"expires_at"`
	Candidates        []repository.RelationshipCorrectionCandidate `json:"candidates"`
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

func (s *rememberService) Remember(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	if s.ledger == nil {
		return nil, errors.New("remember: ledger repository is required")
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
	started := time.Now()
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
		if err := recordSubmissionSecurityRejection(ctx, s.auditor, actor, "remember", scan, scanErr); err != nil {
			observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
			return nil, ErrRememberPersistence
		}
		observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
		return nil, scanErr
	}

	normalized := repositoryEvidenceInputs(req.Evidence)
	requestHash, err := canonicalRequestHash(req)
	if err != nil {
		return nil, err
	}
	correlationID := correlation.FromContext(ctx)
	actorMetadata := map[string]any{
		"team_id":        actor.TeamID.String(),
		"owner_id":       actor.OwnerID.String(),
		"correlation_id": correlationID,
	}
	actorMetadata["role"] = actor.Role
	actorMetadata["auth_method"] = actor.AuthMethod
	if actor.CredentialID != nil {
		actorMetadata["credential_id"] = actor.CredentialID.String()
	}
	metadata := map[string]any{
		"contract_version": domain.ContractVersion,
		"actor":            actorMetadata,
	}
	created, err := s.ledger.CreateIngest(ctx, repository.CreateIngestInput{
		TeamID:            actor.TeamID.String(),
		OwnerProfileID:    actor.OwnerID.String(),
		SpaceID:           rememberSpaceID(space),
		SpaceGeneration:   space.Generation,
		IdempotencyKey:    strings.TrimSpace(req.IdempotencyKey),
		RequestHash:       requestHash,
		SourceSummary:     sourceSummary(req.Evidence),
		Status:            string(domain.PlacementRunQueued),
		TelemetryRemember: true,
		Proposal:          proposal,
		Metadata:          metadata,
		Evidence:          normalized,
	})
	if err != nil {
		observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
		return nil, translateRememberLedgerError(err)
	}
	observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "ok")
	if !created.Existing {
		logSubmissionLifecycle(s.logger, submissionLifecycleEvent{
			Event:         "submission_accepted",
			TeamID:        actor.TeamID.String(),
			ProfileID:     actor.OwnerID.String(),
			CorrelationID: correlationID,
			SubmissionID:  created.IngestID,
			From:          "none",
			To:            publicSubmissionProcessingState(created.Status),
			Stage:         "intake",
			ReasonCode:    "durably_staged",
			Attempts:      created.Attempts,
			MaxAttempts:   created.MaxAttempts,
		})
	}
	if disposition := created.FirstDisposition; disposition != nil && disposition.IsRemember {
		observability.RecordRememberFirstDisposition(ctx, s.metrics, disposition.CompletedAt.Sub(disposition.CreatedAt), disposition.Status)
	}
	return rememberResultFromLedger(created, correlationID), nil
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

// GetSubmissionStatus is the public status projection. It deliberately
// omits placement IDs, review tasks, provider output, and database details.
func (s *rememberService) GetSubmissionStatus(
	ctx context.Context,
	req GetSubmissionStatusRequest,
) (*SubmissionStatusResult, error) {
	if s.ledger == nil {
		return nil, errors.New("submission status: ledger repository is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrRememberAuthContext
	}
	submissionID := strings.TrimSpace(req.SubmissionID)
	if submissionID == "" {
		return nil, errors.New("submission status: submission_id is required")
	}
	if _, err := uuid.Parse(submissionID); err != nil {
		return nil, httperr.New(httperr.NOT_FOUND, "submission not found")
	}
	placement, err := s.ledger.GetPlacementRun(ctx, repository.GetPlacementRunInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.OwnerID.String(),
		IngestID:       submissionID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrPlacementNotFound) {
			return nil, httperr.New(httperr.NOT_FOUND, "submission not found")
		}
		return nil, ErrRememberPersistence
	}
	return submissionStatusResultFromLedger(placement), nil
}

func publicSubmissionProcessingState(status string) string {
	switch strings.TrimSpace(status) {
	case string(domain.PlacementRunQueued), string(domain.PlacementRunGuarded):
		return "queued"
	case string(domain.PlacementRunProcessing):
		return "processing"
	case string(domain.PlacementRunCompleted):
		return "completed"
	case string(domain.PlacementRunRejected):
		return "rejected"
	case string(domain.PlacementRunQuarantined):
		return "quarantined"
	case string(domain.PlacementRunFailed):
		return "failed"
	default:
		return "failed"
	}
}

func sourceRevisionContentHashes(evidence []RememberEvidenceInput) map[string]string {
	groups := make(map[string][]string)
	for _, item := range evidence {
		key := sourceRevisionBatchKey(item)
		if key == "" {
			continue
		}
		groups[key] = append(groups[key], item.Content)
	}
	hashes := make(map[string]string, len(groups))
	for key, items := range groups {
		hashes[key] = sourceRevisionBatchHash(items)
	}
	return hashes
}

func sourceRevisionBatchKey(item RememberEvidenceInput) string {
	sourceKey := strings.TrimSpace(item.SourceKey)
	revision := strings.TrimSpace(item.SourceRevision)
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
	hash, err := rememberapp.CanonicalRequestBodyHash(req.Evidence, req.EntityHints, req.RelationshipHints)
	if err != nil {
		return "", fmt.Errorf("remember: canonical request hash: %w", err)
	}
	return hash, nil
}

func rememberResultFromLedger(created *repository.CreateIngestResult, correlationID string) *RememberResult {
	if created.Existing && strings.TrimSpace(created.CorrelationID) != "" {
		correlationID = created.CorrelationID
	}
	return &RememberResult{
		ContractVersion:   domain.ContractVersion,
		IngestID:          created.IngestID,
		SubmissionID:      created.IngestID,
		SubmissionKind:    "remember",
		ProcessingState:   publicSubmissionProcessingState(created.Status),
		CheckAfterSeconds: rememberCheckAfterSeconds,
		StatusTool:        "",
		CorrelationID:     correlationID,
	}
}

func placementCombinedSearchState(left, right string) string {
	if left == string(domain.SearchProjectionFailed) || right == string(domain.SearchProjectionFailed) {
		return string(domain.SearchProjectionFailed)
	}
	if left == string(domain.SearchProjectionPending) || right == string(domain.SearchProjectionPending) {
		return string(domain.SearchProjectionPending)
	}
	if left == string(domain.SearchProjectionCurrent) || right == string(domain.SearchProjectionCurrent) {
		return string(domain.SearchProjectionCurrent)
	}
	return string(domain.SearchProjectionNotRequired)
}

func placementItemSearchState(item repository.PlacementItem) string {
	if state := placementSearchStateFromStates(resultArray(item.Result, "search_document_states")); state != "" {
		return state
	}
	if len(resultArray(item.Result, "embedding_job_ids")) > 0 {
		return string(domain.SearchProjectionPending)
	}
	if len(resultArray(item.Result, "search_document_ids")) > 0 {
		return string(domain.SearchProjectionCurrent)
	}
	return string(domain.SearchProjectionNotRequired)
}

func placementSearchStateFromStates(values []any) string {
	if len(values) == 0 {
		return ""
	}
	hasCurrent := false
	for _, value := range values {
		state := strings.TrimSpace(fmt.Sprint(value))
		switch state {
		case string(domain.SearchProjectionFailed):
			return string(domain.SearchProjectionFailed)
		case string(domain.SearchProjectionPending):
			return string(domain.SearchProjectionPending)
		case string(domain.SearchProjectionCurrent):
			hasCurrent = true
		}
	}
	if hasCurrent {
		return string(domain.SearchProjectionCurrent)
	}
	return ""
}

func resultArray(result map[string]any, key string) []any {
	if len(result) == 0 {
		return nil
	}
	switch values := result[key].(type) {
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

func translateRememberLedgerError(err error) error {
	var preflight *repository.RememberPreflightError
	if errors.As(err, &preflight) {
		translated := &rememberapp.RememberValidationError{IssuesTruncated: preflight.IssuesTruncated}
		for _, issue := range preflight.Issues {
			translated.Issues = append(translated.Issues, rememberapp.RememberValidationIssue{
				Path: issue.Path, Code: issue.Code, Message: issue.Message,
			})
		}
		return translated
	}
	switch {
	case errors.Is(err, repository.ErrIdempotencyConflict), errors.Is(err, repository.ErrSourceRevisionConflict):
		return fmt.Errorf("%w: duplicate or stale intake request", ErrRememberConflict)
	case errors.Is(err, repository.ErrTeamInactive):
		return httperr.New(httperr.NOT_FOUND, "team not found")
	default:
		return ErrRememberPersistence
	}
}

func evidenceSourceType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
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
	if domain.Authority(authority).IsValid() {
		return authority, out
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
	return map[string]any{
		"source_type":  item.SourceType,
		"source":       item.Source,
		"source_group": item.SourceGroup,
		"metadata":     item.Metadata,
	}
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
