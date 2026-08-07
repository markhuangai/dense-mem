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
)

const (
	rememberCheckAfterSeconds = 60
	rememberStatusTool        = "get_memory_placement"
)

var (
	ErrRememberAuthContext = errors.New("remember: authenticated actor context is required")
	ErrRememberCredential  = errors.New("remember: authenticated credential context is required")
	ErrRememberConflict    = errors.New("remember: conflict")
	ErrRememberPersistence = errors.New("remember: persistence failed")
)

type RememberService interface {
	Remember(ctx context.Context, req RememberRequest) (*RememberResult, error)
	GetMemoryPlacement(ctx context.Context, req GetMemoryPlacementRequest) (*PlacementRunResult, error)
}

type RememberDependencies struct {
	Ledger  repository.LedgerRepository
	Auditor SecurityRejectionAuditor
	Metrics observability.DiscoverabilityMetrics
}

type rememberService struct {
	ledger  repository.LedgerRepository
	auditor SecurityRejectionAuditor
	metrics observability.DiscoverabilityMetrics
}

func NewRememberService(deps RememberDependencies) RememberService {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &rememberService{ledger: deps.Ledger, auditor: deps.Auditor, metrics: metrics}
}

type RememberRequest struct {
	ContractVersion      string                  `json:"contract_version"`
	Evidence             []RememberEvidenceInput `json:"evidence"`
	EntityHints          []map[string]any        `json:"entity_hints,omitempty"`
	RelationshipHints    []map[string]any        `json:"relationship_hints,omitempty"`
	IdempotencyKey       string                  `json:"idempotency_key,omitempty"`
	ReplacesSubmissionID string                  `json:"replaces_submission_id,omitempty"`
}

type GetMemoryPlacementRequest struct {
	ContractVersion string `json:"contract_version"`
	IngestID        string `json:"ingest_id"`
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
	IdempotencyKey         string         `json:"idempotency_key,omitempty"`
	Labels                 []string       `json:"labels,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
}

type RememberResult struct {
	IngestID          string `json:"ingest_id"`
	ProcessingState   string `json:"processing_state"`
	CheckAfterSeconds int    `json:"check_after_seconds"`
	StatusTool        string `json:"status_tool"`
	CorrelationID     string `json:"correlation_id"`
}

type PlacementRunResult struct {
	IngestID        string                `json:"ingest_id"`
	ProcessingState string                `json:"processing_state"`
	SearchState     string                `json:"search_state"`
	Items           []PlacementItemResult `json:"items"`
	Errors          []PlacementError      `json:"errors"`
}

type PlacementItemResult struct {
	ItemID                string                   `json:"item_id"`
	EvidenceID            string                   `json:"evidence_id"`
	SupersededEvidenceIDs []string                 `json:"superseded_evidence_ids"`
	Version               int                      `json:"version"`
	EvidenceIndex         int                      `json:"evidence_index"`
	Category              string                   `json:"category"`
	SearchState           string                   `json:"search_state"`
	RelationshipOutcomes  []RelationshipOutcomeRef `json:"relationship_outcomes"`
	ReviewTasks           []PlacementReviewTaskRef `json:"review_tasks"`
	Errors                []PlacementError         `json:"errors"`
}

type PlacementReviewTaskRef struct {
	ReviewTaskID string           `json:"review_task_id"`
	Version      int              `json:"version"`
	Kind         string           `json:"kind"`
	Status       string           `json:"status"`
	Question     string           `json:"question"`
	Options      []map[string]any `json:"options"`
	Guidance     string           `json:"guidance"`
	ExpiresAt    *time.Time       `json:"expires_at"`
}

type RelationshipOutcomeRef struct {
	ProposalID         string `json:"proposal_id"`
	ObservationID      string `json:"observation_id"`
	RelationshipID     string `json:"relationship_id,omitempty"`
	OwnerProfileID     string `json:"owner_profile_id"`
	RelationshipStatus string `json:"relationship_status,omitempty"`
	Category           string `json:"category"`
	Reason             string `json:"reason"`
	ReviewTask         string `json:"review_task,omitempty"`
	ConfidenceGate     string `json:"confidence_gate,omitempty"`
	PolicyVersion      string `json:"policy_version,omitempty"`
}

type PlacementError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (s *rememberService) Remember(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	if s.ledger == nil {
		return nil, errors.New("remember: ledger repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("remember: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrRememberAuthContext
	}
	credential, ok := requestctx.ActorCredentialFromContext(ctx)
	credentialOK := ok && credential.KeyID != uuid.Nil
	if !credentialOK {
		return nil, ErrRememberCredential
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("remember: evidence is required")
	}
	if err := validateRememberRelationshipCoverage(len(req.Evidence), req.RelationshipHints); err != nil {
		return nil, err
	}
	replacementID := strings.TrimSpace(req.ReplacesSubmissionID)
	if replacementID != "" {
		if _, err := uuid.Parse(replacementID); err != nil {
			return nil, translateRememberLedgerError(fmt.Errorf("%w: invalid submission id: %v", repository.ErrSubmissionReplacementNotFound, err))
		}
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

	normalized := s.normalizeEvidence(req.Evidence)
	requestHash, err := canonicalRequestHash(req)
	if err != nil {
		return nil, err
	}
	correlationID := correlation.FromContext(ctx)
	actorMetadata := map[string]any{
		"team_id":        actor.TeamID.String(),
		"profile_id":     actor.ProfileID.String(),
		"correlation_id": correlationID,
	}
	if credentialOK {
		actorMetadata["role"] = credential.Role
		actorMetadata["credential_id"] = credential.KeyID.String()
		actorMetadata["auth_method"] = credential.AuthMethod
	}
	metadata := map[string]any{
		"contract_version": domain.ContractVersion,
		"actor":            actorMetadata,
	}
	created, err := s.ledger.CreateIngest(ctx, repository.CreateIngestInput{
		TeamID:               actor.TeamID.String(),
		OwnerProfileID:       actor.ProfileID.String(),
		IdempotencyKey:       strings.TrimSpace(req.IdempotencyKey),
		RequestHash:          requestHash,
		SourceSummary:        sourceSummary(req.Evidence),
		Status:               string(domain.PlacementRunQueued),
		TelemetryRemember:    true,
		Proposal:             proposal,
		Metadata:             metadata,
		Evidence:             normalized,
		ReplacesSubmissionID: replacementID,
	})
	if err != nil {
		observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
		return nil, translateRememberLedgerError(err)
	}
	observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "ok")
	if disposition := created.FirstDisposition; disposition != nil && disposition.IsRemember {
		observability.RecordRememberFirstDisposition(ctx, s.metrics, disposition.CompletedAt.Sub(disposition.CreatedAt), disposition.Status)
	}
	return rememberResultFromLedger(created, correlationID), nil
}

func validateRememberRelationshipCoverage(evidenceCount int, relationships []map[string]any) error {
	covered := make([]bool, evidenceCount)
	for _, relationship := range relationships {
		for _, rawSupport := range rememberArrayValues(relationship["supports"]) {
			support, ok := rawSupport.(map[string]any)
			if !ok {
				continue
			}
			index, ok := rememberEvidenceIndex(support["evidence_index"])
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
		return fmt.Errorf("remember: relationship hints must cover every evidence item; missing evidence indexes: %v", missing)
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

func (s *rememberService) GetMemoryPlacement(
	ctx context.Context,
	req GetMemoryPlacementRequest,
) (*PlacementRunResult, error) {
	if s.ledger == nil {
		return nil, errors.New("memory placement: ledger repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("memory placement: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrRememberAuthContext
	}
	ingestID := strings.TrimSpace(req.IngestID)
	if ingestID == "" {
		return nil, errors.New("memory placement: ingest_id is required")
	}
	placement, err := s.ledger.GetPlacementRun(ctx, repository.GetPlacementRunInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.ProfileID.String(),
		IngestID:       ingestID,
	})
	if err != nil {
		return nil, err
	}
	return placementRunResultFromLedger(placement), nil
}

func (s *rememberService) normalizeEvidence(evidence []RememberEvidenceInput) []repository.EvidenceInput {
	out := make([]repository.EvidenceInput, 0, len(evidence))
	sourceRevisionHashes := sourceRevisionContentHashes(evidence)
	for _, item := range evidence {
		event := submissionSecurityPassEvent()
		authority, metadata := ledgerAuthorityAndMetadata(item.Authority, item.Metadata)
		metadata = evidenceProcessingIntentMetadata(metadata, item)
		out = append(out, repository.EvidenceInput{
			Content:                       item.Content,
			SourceType:                    evidenceSourceType(item.SourceType),
			Authority:                     authority,
			SourceRef:                     strings.TrimSpace(item.Source),
			SourceKey:                     strings.TrimSpace(item.SourceKey),
			SourceRevisionToken:           strings.TrimSpace(item.SourceRevision),
			ExpectedPreviousRevisionToken: strings.TrimSpace(item.PreviousSourceRevision),
			SourceRevisionContentHash:     sourceRevisionHashes[sourceRevisionBatchKey(item)],
			SourceRevisionEnvelope:        sourceRevisionEnvelope(item),
			SupersedesEvidenceIDs:         append([]string(nil), item.SupersedesEvidenceIDs...),
			IdempotencyKey:                strings.TrimSpace(item.IdempotencyKey),
			Labels:                        append([]string(nil), item.Labels...),
			Metadata:                      metadata,
			InitialEvent:                  &event,
		})
	}
	return out
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
	payload := map[string]any{
		"contract_version":       req.ContractVersion,
		"evidence":               req.Evidence,
		"entity_hints":           req.EntityHints,
		"relationship_hints":     req.RelationshipHints,
		"idempotency_key":        strings.TrimSpace(req.IdempotencyKey),
		"replaces_submission_id": strings.TrimSpace(req.ReplacesSubmissionID),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("remember: canonical request hash: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func rememberResultFromLedger(created *repository.CreateIngestResult, correlationID string) *RememberResult {
	return &RememberResult{
		IngestID:          created.IngestID,
		ProcessingState:   created.Status,
		CheckAfterSeconds: rememberCheckAfterSeconds,
		StatusTool:        rememberStatusTool,
		CorrelationID:     correlationID,
	}
}

func placementRunResultFromLedger(created *repository.CreateIngestResult) *PlacementRunResult {
	items := make([]PlacementItemResult, 0, len(created.Items))
	topLevelErrors := make([]PlacementError, 0)
	seenErrors := make(map[string]struct{})
	lineage := make(map[string][]string, len(created.Evidence))
	for _, evidence := range created.Evidence {
		lineage[evidence.FragmentID] = append([]string(nil), evidence.SupersededEvidenceIDs...)
	}
	searchState := string(domain.SearchProjectionNotRequired)
	for _, item := range created.Items {
		version := item.Version
		if version == 0 {
			version = 1
		}
		itemSearchState := placementItemSearchState(item)
		searchState = placementCombinedSearchState(searchState, itemSearchState)
		supersededEvidenceIDs := lineage[item.FragmentID]
		if supersededEvidenceIDs == nil {
			supersededEvidenceIDs = []string{}
		}
		itemErrors := placementItemErrors(item)
		for _, placementError := range itemErrors {
			errorKey := placementError.Code + ":" + placementError.Message
			if _, exists := seenErrors[errorKey]; exists {
				continue
			}
			seenErrors[errorKey] = struct{}{}
			topLevelErrors = append(topLevelErrors, placementError)
		}
		items = append(items, PlacementItemResult{
			ItemID:                item.PlacementItemID,
			EvidenceID:            item.FragmentID,
			SupersededEvidenceIDs: supersededEvidenceIDs,
			Version:               version,
			EvidenceIndex:         item.EvidenceIndex,
			Category:              publicPlacementItemCategory(item),
			SearchState:           itemSearchState,
			RelationshipOutcomes:  placementRelationshipOutcomes(item.Result),
			ReviewTasks:           placementReviewTasks(item.ReviewTasks),
			Errors:                itemErrors,
		})
	}
	return &PlacementRunResult{
		IngestID:        created.IngestID,
		ProcessingState: created.Status,
		SearchState:     searchState,
		Items:           items,
		Errors:          topLevelErrors,
	}
}

func placementItemErrors(item repository.PlacementItem) []PlacementError {
	if item.Status != "failed" && item.Category != "failed" {
		return []PlacementError{}
	}
	if strings.EqualFold(resultString(item.Result, "status"), "superseded") {
		return []PlacementError{}
	}
	if _, ok := item.Result["failure_stage"]; !ok {
		return []PlacementError{}
	}
	stage := observability.NormalizeAssessorTerminalFailureStage(resultString(item.Result, "failure_stage"))
	if stage == "unknown" {
		return []PlacementError{}
	}
	return []PlacementError{{
		Code:      "semantic_assessment_terminal_failure",
		Message:   "semantic assessment failed at " + stage,
		Retryable: false,
	}}
}

func publicPlacementItemCategory(item repository.PlacementItem) string {
	if item.Category == "quarantined" || item.Status == "quarantined" {
		return string(domain.EvidenceQuarantined)
	}
	if item.Status == "failed" || item.Category == "failed" {
		return string(domain.EvidenceProcessingFailed)
	}
	return string(domain.EvidenceProcessed)
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

func placementRelationshipOutcomes(result map[string]any) []RelationshipOutcomeRef {
	values := resultArray(result, "relationship_outcomes")
	out := make([]RelationshipOutcomeRef, 0, len(values))
	for _, value := range values {
		fields, ok := value.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, RelationshipOutcomeRef{
			ProposalID:         resultString(fields, "proposal_id"),
			ObservationID:      resultString(fields, "observation_id"),
			RelationshipID:     resultString(fields, "relationship_id"),
			OwnerProfileID:     resultString(fields, "owner_profile_id"),
			RelationshipStatus: resultString(fields, "relationship_status"),
			Category:           resultString(fields, "category"),
			Reason:             resultString(fields, "reason"),
			ReviewTask:         resultString(fields, "review_task"),
			ConfidenceGate:     resultString(fields, "confidence_gate"),
			PolicyVersion:      resultString(fields, "policy_version"),
		})
	}
	return out
}

func placementReviewTasks(tasks []repository.PlacementReviewTask) []PlacementReviewTaskRef {
	result := make([]PlacementReviewTaskRef, 0, len(tasks))
	for _, task := range tasks {
		options := make([]map[string]any, 0, len(task.Options))
		for _, option := range task.Options {
			cloned := make(map[string]any, len(option))
			for key, value := range option {
				cloned[key] = value
			}
			options = append(options, cloned)
		}
		result = append(result, PlacementReviewTaskRef{
			ReviewTaskID: task.ReviewTaskID,
			Version:      task.Version,
			Kind:         task.Kind,
			Status:       task.Status,
			Question:     task.Question,
			Options:      options,
			Guidance:     task.Guidance,
			ExpiresAt:    task.ExpiresAt,
		})
	}
	return result
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

func resultString(fields map[string]any, key string) string {
	value, ok := fields[key]
	if !ok || value == nil {
		return ""
	}
	if str, ok := value.(string); ok {
		return strings.TrimSpace(str)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func translateRememberLedgerError(err error) error {
	switch {
	case errors.Is(err, repository.ErrSubmissionReplacementConflict):
		return httperr.New(httperr.CONFLICT, "submission replacement conflict")
	case errors.Is(err, repository.ErrIdempotencyConflict), errors.Is(err, repository.ErrSourceRevisionConflict):
		return fmt.Errorf("%w: duplicate or stale intake request", ErrRememberConflict)
	case errors.Is(err, repository.ErrSubmissionReplacementNotFound):
		return httperr.New(httperr.NOT_FOUND, "submission not found")
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
	out := make(map[string]any, len(metadata)+1)
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
	if value := strings.TrimSpace(item.IdempotencyKey); value != "" {
		metadata["evidence_idempotency_key"] = value
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
