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

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	rememberCheckAfterSeconds  = 60
	rememberStatusTool         = "get_submission_status"
	requestHashContractVersion = "dense-mem.v2.6"
)

var (
	ErrRememberAuthContext = errors.New("remember: authenticated actor context is required")
	ErrRememberConflict    = errors.New("remember: conflict")
	ErrRememberPersistence = errors.New("remember: persistence failed")
)

// Service is the public application boundary for Remember and submission
// status. Its only durable dependency is the intake port above.
type Service interface {
	Remember(context.Context, RememberRequest) (*RememberResult, error)
	GetSubmissionStatus(context.Context, GetSubmissionStatusRequest) (*SubmissionStatusResult, error)
}

type RememberService = Service

type Dependencies struct {
	Intake IntakePort
	// Synchronous is installed only by an explicit composition owner. Leaving it
	// nil preserves the release intake-and-worker path.
	Synchronous SynchronousProcessor
	// Auditor is required for security-rejected submissions. A nil auditor
	// fails closed as ErrRememberPersistence instead of staging the input.
	Auditor SecurityRejectionAuditor
	Metrics observability.DiscoverabilityMetrics
	Logger  observability.LogProvider
}

type service struct {
	intake      IntakePort
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
	return &service{intake: deps.Intake, synchronous: deps.Synchronous, auditor: deps.Auditor, metrics: metrics, logger: deps.Logger}
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
	ContractVersion   string                  `json:"contract_version"`
	IngestID          string                  `json:"-"`
	SubmissionID      string                  `json:"submission_id"`
	SubmissionKind    string                  `json:"submission_kind"`
	ProcessingState   string                  `json:"processing_state"`
	CheckAfterSeconds int                     `json:"check_after_seconds"`
	StatusTool        string                  `json:"status_tool"`
	CorrelationID     string                  `json:"correlation_id"`
	Kind              ResultKind              `json:"-"`
	Terminal          *TerminalRememberResult `json:"-"`
}

type SubmissionStatusResult struct {
	ContractVersion      string                          `json:"contract_version"`
	SubmissionID         string                          `json:"submission_id"`
	SubmissionKind       string                          `json:"submission_kind"`
	ProcessingState      string                          `json:"processing_state"`
	SearchState          string                          `json:"search_state"`
	CheckAfterSeconds    int                             `json:"check_after_seconds"`
	CorrelationID        string                          `json:"correlation_id,omitempty"`
	Attempts             *int                            `json:"attempts,omitempty"`
	MaxAttempts          *int                            `json:"max_attempts,omitempty"`
	SubmittedAt          *time.Time                      `json:"submitted_at,omitempty"`
	NextAttemptAt        *time.Time                      `json:"next_attempt_at,omitempty"`
	StartedAt            *time.Time                      `json:"started_at,omitempty"`
	UpdatedAt            *time.Time                      `json:"updated_at,omitempty"`
	CompletedAt          *time.Time                      `json:"completed_at,omitempty"`
	Evidence             []SubmissionEvidenceStatus      `json:"evidence"`
	Errors               []SubmissionStatusError         `json:"errors"`
	Degradations         []SubmissionStatusDegradation   `json:"degradations"`
	RelationshipResults  []SubmissionRelationshipResult  `json:"relationship_results,omitempty"`
	QuarantineExpiresAt  *time.Time                      `json:"quarantine_expires_at,omitempty"`
	AwaitingConfirmation *SubmissionAwaitingConfirmation `json:"awaiting_confirmation,omitempty"`
	CorrectionResult     *RelationshipCorrectionResult   `json:"correction_result,omitempty"`
	Kind                 ResultKind                      `json:"-"`
	Terminal             *TerminalRememberResult         `json:"-"`
}

type SubmissionStatusDegradation struct {
	Frontier string `json:"frontier"`
	Optional bool   `json:"optional"`
	Code     string `json:"code"`
	Message  string `json:"message"`
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
	EvidenceID            string                 `json:"evidence_id"`
	EvidenceIndex         int                    `json:"evidence_index"`
	SupersededEvidenceIDs []string               `json:"superseded_evidence_ids"`
	SearchState           string                 `json:"search_state"`
	Error                 *SubmissionStatusError `json:"error,omitempty"`
}

func (s *service) Remember(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	if s == nil || (s.intake == nil && s.synchronous == nil) {
		return nil, errors.New("remember: intake port is required")
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
	if s.synchronous != nil {
		ctx = WithRememberDeadlines(ctx, started)
		operationCtx, cancel := context.WithDeadline(ctx, started.Add(RememberTotalBudget))
		defer cancel()
		ctx = operationCtx
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
	var securityAudit *SecurityRejectionAuditInput
	if scanErr != nil {
		if s.synchronous == nil {
			if err := recordSubmissionSecurityRejection(ctx, s.auditor, s.logger, actor, "remember", scan, scanErr); err != nil {
				observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
				return nil, ErrRememberPersistence
			}
			observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
			return nil, scanErr
		}
		input := securityRejectionAuditInputForIdempotency(ctx, actor, "remember", scan, scanErr, req.IdempotencyKey)
		securityAudit = &input
	}

	requestHash, err := canonicalRequestHash(req)
	if err != nil {
		return nil, err
	}
	migratedRequestHash, err := canonicalRequestHashForVersion(req, domain.MigratedRememberRequestHashVersion)
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
	if s.synchronous != nil {
		terminal, err := s.synchronous.ProcessRemember(ctx, RememberProcessRequest{
			TeamID: actor.TeamID.String(), OwnerProfileID: actor.OwnerID.String(), SpaceID: rememberSpaceID(space),
			SpaceGeneration: space.Generation, IdempotencyKey: strings.TrimSpace(req.IdempotencyKey), RequestHash: requestHash, MigratedRequestHash: migratedRequestHash,
			SourceSummary: sourceSummary(req.Evidence), Proposal: proposal, Metadata: metadata, Evidence: repositoryEvidenceInputs(req.Evidence),
			SecuritySignals:          append([]SubmissionSecurityBatchSignal(nil), scan.Signals...),
			SecuritySignalsTruncated: scan.SignalsTruncated, SecurityRejected: scanErr != nil, SecurityRejectionAudit: securityAudit,
		})
		if err != nil {
			observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
			return nil, err
		}
		if terminal == nil {
			observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
			return nil, ErrRememberPersistence
		}
		observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "ok")
		return rememberResultFromTerminal(terminal), nil
	}
	created, err := s.intake.Stage(ctx, StageRequest{
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
		Evidence:          repositoryEvidenceInputs(req.Evidence),
	})
	if err != nil {
		observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
		return nil, translateRememberLedgerError(err)
	}
	if created == nil {
		observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "error")
		return nil, ErrRememberPersistence
	}
	observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), "ok")
	if !created.Existing {
		logSubmissionLifecycle(s.logger, submissionLifecycleEvent{
			Event: "submission_accepted", TeamID: actor.TeamID.String(), ProfileID: actor.OwnerID.String(),
			CorrelationID: correlationID, SubmissionID: created.SubmissionID, From: "none",
			To: publicSubmissionProcessingState(created.Status), Stage: "intake", ReasonCode: "durably_staged",
			Attempts: created.Attempts, MaxAttempts: created.MaxAttempts,
		})
	}
	if disposition := created.FirstDisposition; disposition != nil && disposition.IsRemember {
		observability.RecordRememberFirstDisposition(ctx, s.metrics, disposition.CompletedAt.Sub(disposition.CreatedAt), disposition.Status)
	}
	return rememberResultFromLedger(created, correlationID), nil
}

func rememberResultFromTerminal(status *SubmissionStatusResult) *RememberResult {
	terminal := status.Terminal
	if terminal == nil {
		terminal = terminalRememberResultFromStatus(status)
	}
	return &RememberResult{
		ContractVersion: status.ContractVersion, IngestID: status.SubmissionID, SubmissionID: status.SubmissionID,
		SubmissionKind: status.SubmissionKind, ProcessingState: status.ProcessingState,
		CorrelationID: status.CorrelationID, Kind: ResultKindTerminal, Terminal: terminal,
	}
}

func terminalRememberResultFromStatus(status *SubmissionStatusResult) *TerminalRememberResult {
	if status == nil {
		return nil
	}
	terminal := &TerminalRememberResult{
		ContractVersion: status.ContractVersion, SubmissionID: status.SubmissionID,
		SubmissionKind: status.SubmissionKind, ProcessingState: status.ProcessingState,
		SearchState: status.SearchState, CorrelationID: status.CorrelationID, Kind: ResultKindTerminal,
		Evidence:            make([]TerminalEvidenceResult, 0, len(status.Evidence)),
		RelationshipResults: append([]SubmissionRelationshipResult(nil), status.RelationshipResults...),
		Errors:              append([]SubmissionStatusError(nil), status.Errors...),
	}
	for _, evidence := range status.Evidence {
		disposition := "stored"
		if evidence.Error != nil || evidence.SearchState == "not_required" {
			disposition = "not_stored"
		}
		terminal.Evidence = append(terminal.Evidence, TerminalEvidenceResult{
			Disposition: disposition, EvidenceID: evidence.EvidenceID, EvidenceIndex: evidence.EvidenceIndex,
			SupersededEvidenceIDs: append([]string(nil), evidence.SupersededEvidenceIDs...), SearchState: evidence.SearchState,
		})
	}
	return terminal
}

func (s *service) GetSubmissionStatus(ctx context.Context, req GetSubmissionStatusRequest) (*SubmissionStatusResult, error) {
	if s == nil || s.intake == nil {
		return nil, errors.New("submission status: intake port is required")
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
	placement, err := s.intake.Status(ctx, StatusRequest{TeamID: actor.TeamID.String(), OwnerProfileID: actor.OwnerID.String(), SubmissionID: submissionID})
	if err != nil {
		if errors.Is(err, ErrPlacementNotFound) {
			return nil, httperr.New(httperr.NOT_FOUND, "submission not found")
		}
		return nil, ErrRememberPersistence
	}
	return submissionStatusResultFromLedger(placement), nil
}

var ErrPlacementNotFound = errors.New("remember: placement not found")

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

func publicSubmissionProcessingState(status string) string {
	switch strings.TrimSpace(status) {
	case string(domain.PlacementRunQueued), string(domain.PlacementRunGuarded):
		return "queued"
	case string(domain.PlacementRunProcessing):
		return "processing"
	case string(domain.PlacementRunCompleted):
		return "completed"
	case string(domain.PlacementRunQuarantined):
		return "quarantined"
	case string(domain.PlacementRunRejected):
		return "rejected"
	case string(domain.PlacementRunFailed):
		return "failed"
	default:
		return "failed"
	}
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

func rememberResultFromLedger(created *StageResult, correlationID string) *RememberResult {
	if created.Existing && strings.TrimSpace(created.CorrelationID) != "" {
		correlationID = created.CorrelationID
	}
	return &RememberResult{ContractVersion: domain.ContractVersion, IngestID: created.SubmissionID, SubmissionID: created.SubmissionID, SubmissionKind: "remember", ProcessingState: publicSubmissionProcessingState(created.Status), CheckAfterSeconds: rememberCheckAfterSeconds, StatusTool: rememberStatusTool, CorrelationID: correlationID, Kind: ResultKindLegacyReceipt}
}

func translateRememberLedgerError(err error) error {
	var validation *RememberValidationError
	if errors.As(err, &validation) {
		return validation
	}
	switch {
	case errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrSourceRevisionConflict):
		return fmt.Errorf("%w: duplicate or stale intake request", ErrRememberConflict)
	case errors.Is(err, ErrTeamInactive):
		return httperr.New(httperr.NOT_FOUND, "team not found")
	default:
		return ErrRememberPersistence
	}
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

// The lifecycle logger is deliberately kept local to this boundary. Worker
// lifecycle logging uses its own application package and does not depend on
// this intake service.
type submissionLifecycleEvent struct {
	Event, TeamID, ProfileID, CorrelationID, SubmissionID, From, To, Stage, ReasonCode string
	Attempts, MaxAttempts                                                              int
}

func logSubmissionLifecycle(logger observability.LogProvider, event submissionLifecycleEvent) {
	if logger == nil {
		return
	}
	attrs := []observability.LogAttr{
		observability.String("event", event.Event), observability.String("team_id", event.TeamID), observability.ProfileID(event.ProfileID),
		observability.String("reference_type", "submission"), observability.String("reference_id", event.SubmissionID),
		observability.String("from", event.From), observability.String("to", event.To), observability.Int("attempts", event.Attempts),
	}
	if event.MaxAttempts > 0 {
		attrs = append(attrs, observability.Int("max_attempts", event.MaxAttempts))
	}
	if event.CorrelationID != "" {
		attrs = append(attrs, observability.CorrelationID(event.CorrelationID))
	}
	if event.Stage != "" {
		attrs = append(attrs, observability.String("stage", event.Stage))
	}
	if event.ReasonCode != "" {
		attrs = append(attrs, observability.String("reason_code", event.ReasonCode))
	}
	logger.Info(event.Event, attrs...)
}
