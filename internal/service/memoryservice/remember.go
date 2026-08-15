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
	rememberStatusTool        = "get_submission_status"
)

var (
	ErrRememberAuthContext = errors.New("remember: authenticated actor context is required")
	ErrRememberCredential  = errors.New("remember: authenticated credential context is required")
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
}

type rememberService struct {
	ledger  repository.LedgerRepository
	auditor SecurityRejectionAuditor
	metrics observability.DiscoverabilityMetrics
}

func NewRememberService(deps RememberDependencies) *rememberService {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &rememberService{ledger: deps.Ledger, auditor: deps.Auditor, metrics: metrics}
}

type RememberRequest struct {
	Evidence             []RememberEvidenceInput `json:"evidence"`
	EntityHints          []map[string]any        `json:"entity_hints,omitempty"`
	RelationshipHints    []map[string]any        `json:"relationship_hints,omitempty"`
	IdempotencyKey       string                  `json:"idempotency_key,omitempty"`
	ReplacesSubmissionID string                  `json:"replaces_submission_id,omitempty"`
}

type GetSubmissionStatusRequest struct {
	SubmissionID string `json:"submission_id"`
}

// Keep the marker used by persisted pre-v2.4 idempotency hashes during replay.
const legacyRequestHashContractVersion = "dense-mem.v2.4"

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
	// IngestID is retained for internal compatibility and is never serialized.
	IngestID          string `json:"-"`
	SubmissionID      string `json:"submission_id"`
	SubmissionKind    string `json:"submission_kind"`
	ProcessingState   string `json:"processing_state"`
	CheckAfterSeconds int    `json:"check_after_seconds"`
	StatusTool        string `json:"status_tool"`
	CorrelationID     string `json:"correlation_id"`
}

type SubmissionStatusResult struct {
	SubmissionID               string                                   `json:"submission_id"`
	SubmissionKind             string                                   `json:"submission_kind"`
	ProcessingState            string                                   `json:"processing_state"`
	SearchState                string                                   `json:"search_state"`
	CheckAfterSeconds          int                                      `json:"check_after_seconds"`
	Evidence                   []SubmissionEvidenceStatus               `json:"evidence"`
	Errors                     []SubmissionStatusError                  `json:"errors"`
	QuarantineExpiresAt        *time.Time                               `json:"quarantine_expires_at,omitempty"`
	ReplacementWindowExpiresAt *time.Time                               `json:"replacement_window_expires_at,omitempty"`
	AwaitingConfirmation       *SubmissionAwaitingConfirmation          `json:"awaiting_confirmation,omitempty"`
	CorrectionResult           *repository.RelationshipCorrectionResult `json:"correction_result,omitempty"`
}

type SubmissionAwaitingConfirmation struct {
	ConfirmationToken string                                       `json:"confirmation_token"`
	ExpiresAt         time.Time                                    `json:"expires_at"`
	Candidates        []repository.RelationshipCorrectionCandidate `json:"candidates"`
}

type SubmissionEvidenceStatus struct {
	EvidenceID            string                 `json:"evidence_id"`
	EvidenceIndex         int                    `json:"evidence_index"`
	SupersededEvidenceIDs []string               `json:"superseded_evidence_ids"`
	SearchState           string                 `json:"search_state"`
	Error                 *SubmissionStatusError `json:"error,omitempty"`
}

// SubmissionErrorCode is the closed public vocabulary for terminal submission
// failures. Internal/provider/database reasons must be translated into this
// set before they cross the status projection boundary.
type SubmissionErrorCode string

const (
	SubmissionErrorSemanticHold          SubmissionErrorCode = "submission_semantic_hold"
	SubmissionErrorPolicyRejected        SubmissionErrorCode = "submission_policy_rejected"
	SubmissionErrorAssessorInvalid       SubmissionErrorCode = "assessor_response_invalid"
	SubmissionErrorAssessorUnavailable   SubmissionErrorCode = "assessor_unavailable"
	SubmissionErrorReplacementConflict   SubmissionErrorCode = "submission_replacement_conflict"
	SubmissionErrorProcessingFailed      SubmissionErrorCode = "submission_processing_failed"
	SubmissionErrorSearchIndexingDelayed SubmissionErrorCode = "search_indexing_delayed"

	SubmissionErrorRelationshipVersionStale      SubmissionErrorCode = "relationship_version_stale"
	SubmissionErrorRelationshipNotActive         SubmissionErrorCode = "relationship_not_active"
	SubmissionErrorObjectKindChangeForbidden     SubmissionErrorCode = "object_kind_change_forbidden"
	SubmissionErrorSupportSetMismatch            SubmissionErrorCode = "support_set_mismatch"
	SubmissionErrorEntityNotFound                SubmissionErrorCode = "entity_not_found"
	SubmissionErrorTooManyEntityCandidates       SubmissionErrorCode = "too_many_entity_candidates"
	SubmissionErrorPredicateNotFound             SubmissionErrorCode = "predicate_not_found"
	SubmissionErrorPredicateSubjectKindMismatch  SubmissionErrorCode = "predicate_subject_kind_mismatch"
	SubmissionErrorPredicateObjectKindMismatch   SubmissionErrorCode = "predicate_object_kind_mismatch"
	SubmissionErrorNoChange                      SubmissionErrorCode = "no_change"
	SubmissionErrorConfirmationExpired           SubmissionErrorCode = "confirmation_expired"
	SubmissionErrorRelationshipChanged           SubmissionErrorCode = "relationship_changed"
	SubmissionErrorSupportSetChanged             SubmissionErrorCode = "support_set_changed"
	SubmissionErrorPersistentAmbiguity           SubmissionErrorCode = "persistent_ambiguity"
	SubmissionErrorInactiveRelationshipCollision SubmissionErrorCode = "inactive_relationship_collision"
)

var submissionErrorCodes = []SubmissionErrorCode{
	SubmissionErrorSemanticHold,
	SubmissionErrorPolicyRejected,
	SubmissionErrorAssessorInvalid,
	SubmissionErrorAssessorUnavailable,
	SubmissionErrorReplacementConflict,
	SubmissionErrorProcessingFailed,
	SubmissionErrorSearchIndexingDelayed,
	SubmissionErrorRelationshipVersionStale,
	SubmissionErrorRelationshipNotActive,
	SubmissionErrorObjectKindChangeForbidden,
	SubmissionErrorSupportSetMismatch,
	SubmissionErrorEntityNotFound,
	SubmissionErrorTooManyEntityCandidates,
	SubmissionErrorPredicateNotFound,
	SubmissionErrorPredicateSubjectKindMismatch,
	SubmissionErrorPredicateObjectKindMismatch,
	SubmissionErrorNoChange,
	SubmissionErrorConfirmationExpired,
	SubmissionErrorRelationshipChanged,
	SubmissionErrorSupportSetChanged,
	SubmissionErrorPersistentAmbiguity,
	SubmissionErrorInactiveRelationshipCollision,
}

// SubmissionErrorCodes returns the public enum in deterministic schema order.
func SubmissionErrorCodes() []string {
	result := make([]string, 0, len(submissionErrorCodes))
	for _, code := range submissionErrorCodes {
		result = append(result, string(code))
	}
	return result
}

type SubmissionStatusError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var submissionErrorMessages = map[SubmissionErrorCode]string{
	SubmissionErrorSemanticHold:          "submission was rejected by semantic hold policy",
	SubmissionErrorPolicyRejected:        "submission was rejected by semantic placement policy",
	SubmissionErrorAssessorInvalid:       "submission assessment returned an invalid response",
	SubmissionErrorAssessorUnavailable:   "submission assessment was unavailable after bounded retries",
	SubmissionErrorReplacementConflict:   "submission replacement conflicted with current state",
	SubmissionErrorProcessingFailed:      "submission processing failed",
	SubmissionErrorSearchIndexingDelayed: "search indexing is delayed",

	SubmissionErrorRelationshipVersionStale:      "relationship version is stale",
	SubmissionErrorRelationshipNotActive:         "relationship must be active, supported, and canonical",
	SubmissionErrorObjectKindChangeForbidden:     "a Value object cannot be replaced with an Entity",
	SubmissionErrorSupportSetMismatch:            "supports must exactly match the relationship's effective evidence spans",
	SubmissionErrorEntityNotFound:                "corrected Entity is not active and available to the team",
	SubmissionErrorTooManyEntityCandidates:       "corrected Entity name has too many exact candidates",
	SubmissionErrorPredicateNotFound:             "predicate is not registered and active for the team",
	SubmissionErrorPredicateSubjectKindMismatch:  "predicate does not allow the corrected subject kind",
	SubmissionErrorPredicateObjectKindMismatch:   "predicate does not allow the corrected object kind",
	SubmissionErrorNoChange:                      "correction does not change the Relationship",
	SubmissionErrorConfirmationExpired:           "relationship correction confirmation expired",
	SubmissionErrorRelationshipChanged:           "relationship changed while confirmation was pending",
	SubmissionErrorSupportSetChanged:             "relationship supports changed while confirmation was pending",
	SubmissionErrorPersistentAmbiguity:           "selected Entity candidate is no longer available",
	SubmissionErrorInactiveRelationshipCollision: "corrected Relationship collides with inactive or unsupported history",
}

func submissionStatusError(code SubmissionErrorCode) SubmissionStatusError {
	message := submissionErrorMessages[code]
	if message == "" {
		code = SubmissionErrorProcessingFailed
		message = submissionErrorMessages[code]
	}
	return SubmissionStatusError{Code: string(code), Message: message}
}

func submissionStatusErrorForCode(rawCode string, fallbackState string) SubmissionStatusError {
	code := SubmissionErrorCode(strings.TrimSpace(rawCode))
	for _, known := range submissionErrorCodes {
		if code == known {
			return submissionStatusError(code)
		}
	}
	if fallbackState == "rejected" {
		return submissionStatusError(SubmissionErrorPolicyRejected)
	}
	return submissionStatusError(SubmissionErrorProcessingFailed)
}

func submissionFailureCode(stage, class string) SubmissionErrorCode {
	stage = strings.TrimSpace(stage)
	class = strings.TrimSpace(class)
	switch {
	case stage == "replacement_conflict":
		return SubmissionErrorReplacementConflict
	case class == "malformed_response", class == "validation_failed", class == "provider_protocol":
		return SubmissionErrorAssessorInvalid
	case class == "timeout", class == "rate_limited", class == "http_4xx", class == "http_5xx",
		class == "http_unexpected", class == "transport", class == "provider_unavailable":
		return SubmissionErrorAssessorUnavailable
	case stage == "policy_review", stage == "confidence_policy", stage == "security_signal":
		return SubmissionErrorPolicyRejected
	default:
		return SubmissionErrorProcessingFailed
	}
}

func submissionItemFailureError(item repository.PlacementItem, processing string) *SubmissionStatusError {
	if item.Status != string(domain.PlacementRunFailed) && item.Status != "failed" && item.Status != "rejected" && item.Status != "awaiting_review" {
		return nil
	}
	stage, _ := item.Result["failure_stage"].(string)
	class, _ := item.Result["failure_class"].(string)
	errorValue := submissionStatusError(submissionFailureCode(stage, class))
	return &errorValue
}

func (s *rememberService) Remember(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	if s.ledger == nil {
		return nil, errors.New("remember: ledger repository is required")
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

	normalized := repositoryEvidenceInputs(req.Evidence)
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

// GetSubmissionStatus is the public status projection. It deliberately
// omits placement IDs, review tasks, provider output, and database details.
func (s *rememberService) GetSubmissionStatus(
	ctx context.Context,
	req GetSubmissionStatusRequest,
) (*SubmissionStatusResult, error) {
	if s.ledger == nil {
		return nil, errors.New("submission status: ledger repository is required")
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
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
		OwnerProfileID: actor.ProfileID.String(),
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

func submissionStatusResultFromLedger(placement *repository.CreateIngestResult) *SubmissionStatusResult {
	if placement == nil {
		return &SubmissionStatusResult{Evidence: []SubmissionEvidenceStatus{}, Errors: []SubmissionStatusError{}}
	}
	items := make([]SubmissionEvidenceStatus, 0, len(placement.Items))
	lineage := make(map[string][]string, len(placement.Evidence))
	for _, evidence := range placement.Evidence {
		lineage[evidence.FragmentID] = append([]string(nil), evidence.SupersededEvidenceIDs...)
	}
	searchState := string(domain.SearchProjectionNotRequired)
	searchErrorAdded := false
	processing := publicSubmissionProcessingState(placement.Status, placement.SemanticHoldState)
	statusErrors := make([]SubmissionStatusError, 0, 2)
	seenStatusErrors := make(map[string]struct{})
	appendStatusError := func(value SubmissionStatusError) {
		key := string(value.Code) + "\x00" + value.Message
		if _, exists := seenStatusErrors[key]; exists {
			return
		}
		seenStatusErrors[key] = struct{}{}
		statusErrors = append(statusErrors, value)
	}
	for _, item := range placement.Items {
		itemSearchState := placementItemSearchState(item)
		searchState = placementCombinedSearchState(searchState, itemSearchState)
		superseded := lineage[item.FragmentID]
		if superseded == nil {
			superseded = []string{}
		}
		var itemError *SubmissionStatusError
		if semanticError := submissionItemFailureError(item, processing); semanticError != nil {
			itemError = semanticError
			appendStatusError(*semanticError)
		} else if itemSearchState == string(domain.SearchProjectionFailed) {
			searchError := SubmissionStatusError{Code: string(SubmissionErrorSearchIndexingDelayed), Message: "Semantic search indexing is delayed."}
			itemError = &searchError
			searchErrorAdded = true
		}
		items = append(items, SubmissionEvidenceStatus{
			EvidenceID:            item.FragmentID,
			EvidenceIndex:         item.EvidenceIndex,
			SupersededEvidenceIDs: superseded,
			SearchState:           itemSearchState,
			Error:                 itemError,
		})
	}
	if processing == "rejected" && strings.TrimSpace(placement.SemanticHoldState) != "" {
		appendStatusError(submissionStatusError(SubmissionErrorSemanticHold))
	} else if processing == "rejected" {
		appendStatusError(submissionStatusError(SubmissionErrorPolicyRejected))
	} else if processing == "failed" && len(statusErrors) == 0 {
		appendStatusError(submissionStatusError(SubmissionErrorProcessingFailed))
	}
	if searchErrorAdded {
		appendStatusError(SubmissionStatusError{Code: string(SubmissionErrorSearchIndexingDelayed), Message: "Semantic search indexing is delayed; check the control portal for recovery guidance."})
	}
	return &SubmissionStatusResult{
		SubmissionID:               placement.IngestID,
		SubmissionKind:             "remember",
		ProcessingState:            processing,
		SearchState:                searchState,
		CheckAfterSeconds:          rememberCheckAfterSeconds,
		Evidence:                   items,
		Errors:                     statusErrors,
		QuarantineExpiresAt:        placement.QuarantineExpiresAt,
		ReplacementWindowExpiresAt: placement.ReplacementWindowExpiresAt,
	}
}

func publicSubmissionProcessingState(status, holdState string) string {
	if strings.TrimSpace(holdState) != "" {
		return "rejected"
	}
	switch strings.TrimSpace(status) {
	case string(domain.PlacementRunQueued), string(domain.PlacementRunGuarded):
		return "queued"
	case string(domain.PlacementRunProcessing):
		return "processing"
	case string(domain.PlacementRunCompleted):
		return "completed"
	case string(domain.PlacementRunAwaitingReview):
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
	payload := map[string]any{
		"contract_version":       legacyRequestHashContractVersion,
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
		SubmissionID:      created.IngestID,
		SubmissionKind:    "remember",
		ProcessingState:   publicSubmissionProcessingState(created.Status, created.SemanticHoldState),
		CheckAfterSeconds: rememberCheckAfterSeconds,
		StatusTool:        rememberStatusTool,
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
