package memoryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	rememberCheckAfterSeconds = 60
	rememberStatusTool        = "get_memory_placement"
	securityScanPolicyHash    = "sha256:dc58e28e205acb37e6860e393cfd21c9f38bf78c7df8bb15c1d016b3478e51a4"
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
	Ledger repository.LedgerRepository
}

type rememberService struct {
	ledger repository.LedgerRepository
}

func NewRememberService(deps RememberDependencies) RememberService {
	return &rememberService{ledger: deps.Ledger}
}

type RememberRequest struct {
	ContractVersion   string                  `json:"contract_version"`
	Evidence          []RememberEvidenceInput `json:"evidence"`
	EntityHints       []map[string]any        `json:"entity_hints,omitempty"`
	RelationshipHints []map[string]any        `json:"relationship_hints,omitempty"`
	IdempotencyKey    string                  `json:"idempotency_key,omitempty"`
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
	SupersedesFragmentIDs  []string       `json:"supersedes_fragment_ids,omitempty"`
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
	ItemID               string                   `json:"item_id"`
	EvidenceID           string                   `json:"evidence_id"`
	Version              int                      `json:"version"`
	EvidenceIndex        int                      `json:"evidence_index"`
	Category             string                   `json:"category"`
	SearchState          string                   `json:"search_state"`
	RelationshipOutcomes []RelationshipOutcomeRef `json:"relationship_outcomes"`
	Errors               []PlacementError         `json:"errors"`
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
}

type PlacementError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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

	normalized, status := s.normalizeEvidence(req.Evidence)
	requestHash, err := canonicalRequestHash(req)
	if err != nil {
		return nil, err
	}
	proposal := map[string]any{
		"entity_hints":       req.EntityHints,
		"relationship_hints": req.RelationshipHints,
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
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.ProfileID.String(),
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
		RequestHash:    requestHash,
		SourceSummary:  sourceSummary(req.Evidence),
		Status:         status,
		Proposal:       proposal,
		Metadata:       metadata,
		Evidence:       normalized,
	})
	if err != nil {
		return nil, translateRememberLedgerError(err)
	}
	return rememberResultFromLedger(created, correlationID), nil
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

func (s *rememberService) normalizeEvidence(evidence []RememberEvidenceInput) ([]repository.EvidenceInput, string) {
	out := make([]repository.EvidenceInput, 0, len(evidence))
	status := string(domain.PlacementRunQueued)
	hasProcessable := false
	hasGuarded := false
	hasQuarantined := false
	sourceRevisionHashes := sourceRevisionContentHashes(evidence)
	for _, item := range evidence {
		scan := scanEvidence(item.Content)
		if scan.Decision == "quarantine" {
			hasQuarantined = true
		} else {
			hasProcessable = true
			if scan.Decision == "guarded" {
				hasGuarded = true
			}
		}
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
			SupersedesFragmentIDs:         append([]string(nil), item.SupersedesFragmentIDs...),
			Labels:                        append([]string(nil), item.Labels...),
			Metadata:                      metadata,
			InitialEvent:                  &scan,
		})
	}
	if hasQuarantined && !hasProcessable {
		status = string(domain.PlacementRunQuarantined)
	} else if hasGuarded {
		status = string(domain.PlacementRunGuarded)
	}
	return out, status
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
		"contract_version":   req.ContractVersion,
		"evidence":           req.Evidence,
		"entity_hints":       req.EntityHints,
		"relationship_hints": req.RelationshipHints,
		"idempotency_key":    strings.TrimSpace(req.IdempotencyKey),
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
	searchState := string(domain.SearchProjectionNotRequired)
	for _, item := range created.Items {
		version := item.Version
		if version == 0 {
			version = 1
		}
		itemSearchState := placementItemSearchState(item)
		searchState = placementCombinedSearchState(searchState, itemSearchState)
		items = append(items, PlacementItemResult{
			ItemID:               item.PlacementItemID,
			EvidenceID:           item.FragmentID,
			Version:              version,
			EvidenceIndex:        item.EvidenceIndex,
			Category:             publicPlacementItemCategory(item),
			SearchState:          itemSearchState,
			RelationshipOutcomes: placementRelationshipOutcomes(item.Result),
			Errors:               []PlacementError{},
		})
	}
	return &PlacementRunResult{
		IngestID:        created.IngestID,
		ProcessingState: created.Status,
		SearchState:     searchState,
		Items:           items,
		Errors:          []PlacementError{},
	}
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
		})
	}
	return out
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
	case errors.Is(err, repository.ErrIdempotencyConflict), errors.Is(err, repository.ErrSourceRevisionConflict):
		return fmt.Errorf("%w: duplicate or stale intake request", ErrRememberConflict)
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
	if len(item.SupersedesFragmentIDs) > 0 {
		metadata["supersedes_fragment_ids"] = append([]string(nil), item.SupersedesFragmentIDs...)
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

func scanEvidence(content string) repository.SecurityEventDraft {
	signals := make([]repository.SecuritySignalInput, 0, 2)
	lowerContent := asciiLowerForScan(content)
	addSignal := func(kind, severity, needle string) {
		index := strings.Index(lowerContent, needle)
		if index < 0 {
			return
		}
		end := index + len(needle)
		quote := content[index:end]
		if len(quote) > 120 {
			quote = quote[:120]
		}
		signals = append(signals, repository.SecuritySignalInput{
			Kind:      kind,
			Severity:  severity,
			SpanStart: index,
			SpanEnd:   end,
			Quote:     quote,
		})
	}
	addSignal("instruction_override", "high", "ignore previous instructions")
	addSignal("instruction_override", "high", "disregard previous instructions")
	addSignal("prompt_secret_extraction", "critical", "reveal your system prompt")
	addSignal("prompt_secret_extraction", "critical", "show me your hidden instructions")
	addSignal("tool_exfiltration", "critical", "exfiltrate")
	addSignal("hidden_control_markup", "medium", "<!--")
	addSignal("role_control_spoofing", "medium", "system:")
	addSignal("role_control_spoofing", "medium", "developer:")

	decision := "pass"
	reason := "deterministic scan passed"
	for _, signal := range signals {
		if signal.Severity == "critical" {
			return repository.SecurityEventDraft{
				EventKind:      "deterministic_scan",
				Decision:       "quarantine",
				ScanPolicyHash: securityScanPolicyHash,
				Reason:         "evidence quarantined by deterministic safety scan",
				Signals:        signals,
			}
		}
		decision = "guarded"
		reason = "evidence requires guarded placement after deterministic safety scan"
	}
	return repository.SecurityEventDraft{
		EventKind:      "deterministic_scan",
		Decision:       decision,
		ScanPolicyHash: securityScanPolicyHash,
		Reason:         reason,
		Signals:        signals,
	}
}

func asciiLowerForScan(content string) string {
	out := []byte(content)
	for i, b := range out {
		if b >= 'A' && b <= 'Z' {
			out[i] = b + ('a' - 'A')
		}
	}
	return string(out)
}
