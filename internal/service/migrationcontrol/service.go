package migrationcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	DefaultMigrationContractVersion = "dense-mem.v2.1.migration-control.v1"
	DefaultCorpusVersion            = "dense-mem.v2.1.legacy-corpus.v1"
	DefaultCutoverMarkerVersion     = "dense-mem.v2.1.cutover.v1"
	defaultSourceKind               = "neo4j"
)

var (
	ErrIllegalTransition = errors.New("v2 migration control: illegal transition")
	ErrPreflightRequired = errors.New("v2 migration control: preflight approval is required")
	ErrAlreadyCutOver    = errors.New("v2 migration control: already cut over")
	ErrIncompatible      = errors.New("v2 migration control: incompatible marker")
	ErrCutoverBlocked    = errors.New("v2 migration control: cutover blocked")
	ErrCutoverGate       = errors.New("v2 migration control: cutover gate failed")
)

var defaultCutoverRequiredGates = []string{
	"backup_restore",
	"postgres_schema_topology",
	"tenant_integrity",
	"team_owner_reconciliation",
	"terminal_official_pipeline_outcomes",
	"exclusion_manifest",
	"rls_security",
	"relationship_identity_uniqueness",
	"active_endpoint_integrity",
	"relationship_owner_preservation",
	"inactive_candidate_review_boundary",
	"predicate_identity_hold_policy",
	"vector_contract_current",
	"recall_oracle_hnsw",
	"trace_lineage",
	"v2_smoke_lifecycle",
	"abc_visibility_mutation",
	"provider_readiness",
	"search_index_backlog",
	"worker_lease_health",
	"telemetry_audit",
	"release_gate_1k",
	"uat_mcp_http_browser",
	"compose_rehearsal",
	"restart_rehearsal",
	"rollback_compatibility",
	"no_neo4j_fallback",
}

type Store interface {
	GetLatestRun(ctx context.Context) (*domain.V2MigrationRun, error)
	CreateRun(ctx context.Context, input repository.V2CreateMigrationRunInput) (*domain.V2MigrationRun, error)
	UpdateRunState(ctx context.Context, input repository.V2UpdateMigrationRunStateInput) (*domain.V2MigrationRun, error)
	CommitCutover(ctx context.Context, input repository.V2CommitCutoverInput) (*domain.V2CompatibilityMarker, error)
	GetLatestMarker(ctx context.Context) (*domain.V2CompatibilityMarker, error)
	RecordOperatorAction(ctx context.Context, action domain.V2MigrationOperatorAction) error
	ListOperatorActions(ctx context.Context, runID string, limit int) ([]domain.V2MigrationOperatorAction, error)
}

type Service interface {
	Status(ctx context.Context) (*domain.V2MigrationControlStatus, error)
	ApprovePreflight(ctx context.Context, req OperatorRequest) (*domain.V2MigrationControlStatus, error)
	Start(ctx context.Context, req OperatorRequest) (*domain.V2MigrationControlStatus, error)
	Pause(ctx context.Context, req OperatorRequest) (*domain.V2MigrationControlStatus, error)
	Resume(ctx context.Context, req OperatorRequest) (*domain.V2MigrationControlStatus, error)
	Cutover(ctx context.Context, req CutoverRequest) (*domain.V2MigrationControlStatus, error)
}

type Config struct {
	Required                 bool
	MigrationContractVersion string
	CorpusVersion            string
	CutoverMarkerVersion     string
	CutoverRequiredGates     []string
	Now                      func() time.Time
}

type OperatorRequest struct {
	Actor           string         `json:"-"`
	RemoteIP        string         `json:"-"`
	Reason          string         `json:"reason,omitempty"`
	BackupReference string         `json:"backup_reference,omitempty"`
	PreflightChecks map[string]any `json:"preflight_checks,omitempty"`
}

type CutoverRequest struct {
	Actor       string                         `json:"-"`
	RemoteIP    string                         `json:"-"`
	Reason      string                         `json:"reason,omitempty"`
	CorpusHash  string                         `json:"corpus_hash,omitempty"`
	GateResults []domain.V2MigrationGateResult `json:"gate_results"`
	Metadata    map[string]any                 `json:"metadata,omitempty"`
}

type service struct {
	store Store
	cfg   Config
	now   func() time.Time
}

func New(store Store, cfg Config) Service {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if strings.TrimSpace(cfg.MigrationContractVersion) == "" {
		cfg.MigrationContractVersion = DefaultMigrationContractVersion
	}
	if strings.TrimSpace(cfg.CorpusVersion) == "" {
		cfg.CorpusVersion = DefaultCorpusVersion
	}
	if strings.TrimSpace(cfg.CutoverMarkerVersion) == "" {
		cfg.CutoverMarkerVersion = DefaultCutoverMarkerVersion
	}
	if len(cfg.CutoverRequiredGates) == 0 {
		cfg.CutoverRequiredGates = append([]string(nil), defaultCutoverRequiredGates...)
	}
	return &service{store: store, cfg: cfg, now: now}
}

func (s *service) Status(ctx context.Context) (*domain.V2MigrationControlStatus, error) {
	if s.store == nil {
		return statusFromRunMarker(s.cfg.Required, nil, nil, nil), nil
	}
	marker, err := s.store.GetLatestMarker(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.store.GetLatestRun(ctx)
	if err != nil {
		return nil, err
	}
	actions, err := s.actions(ctx, run)
	if err != nil {
		return nil, err
	}
	return statusFromRunMarker(s.cfg.Required, run, marker, actions), nil
}

func (s *service) ApprovePreflight(ctx context.Context, req OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	if err := validatePreflight(req); err != nil {
		return nil, err
	}
	run, marker, err := s.latestRunAndMarker(ctx)
	if err != nil {
		return nil, err
	}
	if err := ensureMutableMarker(marker); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if run == nil {
		run, err = s.store.CreateRun(ctx, repository.V2CreateMigrationRunInput{
			MigrationContractVersion: s.cfg.MigrationContractVersion,
			CorpusVersion:            s.cfg.CorpusVersion,
			SourceKind:               defaultSourceKind,
			State:                    domain.V2MigrationStateReady,
			Phase:                    "preflight",
			Required:                 true,
			PreflightApproved:        true,
			BackupReference:          strings.TrimSpace(req.BackupReference),
			PreflightChecks:          req.PreflightChecks,
			Now:                      now,
		})
	} else {
		if !canTransition(run.State, domain.V2MigrationStateReady) && run.State != domain.V2MigrationStateReady {
			return nil, fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, run.State, domain.V2MigrationStateReady)
		}
		run, err = s.store.UpdateRunState(ctx, repository.V2UpdateMigrationRunStateInput{
			RunID:             run.RunID,
			FromState:         run.State,
			ToState:           domain.V2MigrationStateReady,
			Phase:             "preflight",
			PreflightApproved: true,
			BackupReference:   strings.TrimSpace(req.BackupReference),
			PreflightChecks:   req.PreflightChecks,
			Retryable:         true,
			Now:               now,
		})
	}
	if err != nil {
		return nil, err
	}
	if err := s.recordAction(ctx, run.RunID, domain.V2MigrationActionPreflightApproved, req, now); err != nil {
		return nil, err
	}
	return s.Status(ctx)
}

func (s *service) Start(ctx context.Context, req OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	return s.transition(ctx, domain.V2MigrationStateReady, domain.V2MigrationStateRunning, "migration", domain.V2MigrationActionStarted, req)
}

func (s *service) Pause(ctx context.Context, req OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	return s.transition(ctx, domain.V2MigrationStateRunning, domain.V2MigrationStatePaused, "paused", domain.V2MigrationActionPaused, req)
}

func (s *service) Resume(ctx context.Context, req OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	return s.transition(ctx, domain.V2MigrationStatePaused, domain.V2MigrationStateRunning, "migration", domain.V2MigrationActionResumed, req)
}

func (s *service) Cutover(ctx context.Context, req CutoverRequest) (*domain.V2MigrationControlStatus, error) {
	run, marker, err := s.latestRunAndMarker(ctx)
	if err != nil {
		return nil, err
	}
	if err := ensureMutableMarker(marker); err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrPreflightRequired
	}
	if run.State != domain.V2MigrationStateReadyCutover {
		return nil, fmt.Errorf("%w: run state %s", ErrCutoverBlocked, run.State)
	}
	if !run.PreflightApproved || strings.TrimSpace(run.BackupReference) == "" {
		return nil, fmt.Errorf("%w: approved preflight and backup reference are required", ErrPreflightRequired)
	}
	corpusHash := strings.TrimSpace(req.CorpusHash)
	if corpusHash == "" {
		corpusHash = strings.TrimSpace(run.CorpusHash)
	}
	if corpusHash == "" {
		return nil, fmt.Errorf("%w: corpus_hash is required", ErrCutoverGate)
	}
	gates, err := normalizeCutoverGates(req.GateResults, s.cfg.CutoverRequiredGates)
	if err != nil {
		return nil, err
	}
	gateReportHash, err := cutoverGateReportHash(gates)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	_, err = s.store.CommitCutover(ctx, repository.V2CommitCutoverInput{
		RunID:          run.RunID,
		FromState:      domain.V2MigrationStateReadyCutover,
		MarkerVersion:  s.cfg.CutoverMarkerVersion,
		CorpusHash:     corpusHash,
		GateReportHash: gateReportHash,
		GateResults:    gates,
		Metadata:       req.Metadata,
		OperatorAction: domain.V2MigrationOperatorAction{
			RunID:     run.RunID,
			Action:    domain.V2MigrationActionCutoverCommitted,
			Actor:     operatorActor(req.Actor),
			RemoteIP:  strings.TrimSpace(req.RemoteIP),
			Reason:    strings.TrimSpace(req.Reason),
			Metadata:  cutoverOperatorMetadata(req, gateReportHash, corpusHash, s.cfg.CutoverRequiredGates),
			CreatedAt: now,
		},
		Now: now,
	})
	if err != nil {
		if errors.Is(err, repository.ErrV2MigrationCutoverBlocked) {
			return nil, fmt.Errorf("%w: %v", ErrCutoverBlocked, err)
		}
		return nil, err
	}
	return s.Status(ctx)
}

func (s *service) transition(
	ctx context.Context,
	from string,
	to string,
	phase string,
	action string,
	req OperatorRequest,
) (*domain.V2MigrationControlStatus, error) {
	run, marker, err := s.latestRunAndMarker(ctx)
	if err != nil {
		return nil, err
	}
	if err := ensureMutableMarker(marker); err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrPreflightRequired
	}
	if run.State != from || !canTransition(run.State, to) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, run.State, to)
	}
	if !run.PreflightApproved {
		return nil, ErrPreflightRequired
	}
	now := s.now().UTC()
	updated, err := s.store.UpdateRunState(ctx, repository.V2UpdateMigrationRunStateInput{
		RunID:     run.RunID,
		FromState: from,
		ToState:   to,
		Phase:     phase,
		Retryable: true,
		Now:       now,
	})
	if err != nil {
		return nil, err
	}
	if err := s.recordAction(ctx, updated.RunID, action, req, now); err != nil {
		return nil, err
	}
	return s.Status(ctx)
}

func (s *service) latestRunAndMarker(ctx context.Context) (*domain.V2MigrationRun, *domain.V2CompatibilityMarker, error) {
	if s.store == nil {
		return nil, nil, errors.New("v2 migration control: store is required")
	}
	marker, err := s.store.GetLatestMarker(ctx)
	if err != nil {
		return nil, nil, err
	}
	run, err := s.store.GetLatestRun(ctx)
	if err != nil {
		return nil, nil, err
	}
	return run, marker, nil
}

func (s *service) actions(ctx context.Context, run *domain.V2MigrationRun) ([]domain.V2MigrationOperatorAction, error) {
	if run == nil || run.RunID == "" {
		return nil, nil
	}
	return s.store.ListOperatorActions(ctx, run.RunID, 20)
}

func (s *service) recordAction(ctx context.Context, runID, action string, req OperatorRequest, now time.Time) error {
	return s.store.RecordOperatorAction(ctx, domain.V2MigrationOperatorAction{
		RunID:     runID,
		Action:    action,
		Actor:     operatorActor(req.Actor),
		RemoteIP:  strings.TrimSpace(req.RemoteIP),
		Reason:    strings.TrimSpace(req.Reason),
		Metadata:  operatorMetadata(req),
		CreatedAt: now,
	})
}

func statusFromRunMarker(
	required bool,
	run *domain.V2MigrationRun,
	marker *domain.V2CompatibilityMarker,
	actions []domain.V2MigrationOperatorAction,
) *domain.V2MigrationControlStatus {
	if marker != nil {
		switch marker.Status {
		case domain.V2MigrationMarkerCompatible:
			message := "compatible V2 migration marker present; neo4j_disconnect_required"
			if v2FreshInstallMarker(marker) {
				message = "fresh V2 authority marker present"
			}
			return &domain.V2MigrationControlStatus{
				State:            domain.V2MigrationStateCutOver,
				Required:         required,
				DataPlaneAllowed: true,
				ReadinessMessage: message,
				Run:              run,
				Marker:           marker,
				Actions:          actions,
			}
		case domain.V2MigrationMarkerIncompatible, domain.V2MigrationMarkerCorrupt:
			return &domain.V2MigrationControlStatus{
				State:            domain.V2MigrationStateIncompatible,
				Required:         true,
				DataPlaneAllowed: false,
				ReadinessMessage: "V2 migration marker is incompatible or corrupt",
				Run:              run,
				Marker:           marker,
				Actions:          actions,
			}
		}
	}
	if run != nil {
		return &domain.V2MigrationControlStatus{
			State:            run.State,
			Required:         run.Required,
			DataPlaneAllowed: run.State == domain.V2MigrationStateNotRequired || run.State == domain.V2MigrationStateCutOver,
			ReadinessMessage: readinessMessage(run.State),
			Run:              run,
			Actions:          actions,
		}
	}
	state := domain.V2MigrationStateNotRequired
	message := "legacy migration is not required"
	if required {
		state = domain.V2MigrationStateRequired
		message = "legacy migration is required; use the private control portal"
	}
	return &domain.V2MigrationControlStatus{
		State:            state,
		Required:         required,
		DataPlaneAllowed: !required,
		ReadinessMessage: message,
	}
}

func readinessMessage(state string) string {
	switch state {
	case domain.V2MigrationStateReady:
		return "migration preflight approved; start is allowed"
	case domain.V2MigrationStateRunning:
		return "migration is running"
	case domain.V2MigrationStatePaused:
		return "migration is paused at a durable checkpoint"
	case domain.V2MigrationStateFailed:
		return "migration failed; inspect errors and resume only if retryable"
	case domain.V2MigrationStateVerifying, domain.V2MigrationStateReadyCutover:
		return "migration is validating cutover gates"
	case domain.V2MigrationStateCutOver:
		return "migration cutover is complete"
	case domain.V2MigrationStateIncompatible:
		return "migration marker is incompatible"
	default:
		return "legacy migration is required; use the private control portal"
	}
}

func v2FreshInstallMarker(marker *domain.V2CompatibilityMarker) bool {
	if marker == nil || len(marker.Metadata) == 0 {
		return false
	}
	value, ok := marker.Metadata["fresh_install"]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func ensureMutableMarker(marker *domain.V2CompatibilityMarker) error {
	if marker == nil {
		return nil
	}
	switch marker.Status {
	case domain.V2MigrationMarkerCompatible:
		return ErrAlreadyCutOver
	case domain.V2MigrationMarkerIncompatible, domain.V2MigrationMarkerCorrupt:
		return ErrIncompatible
	default:
		return nil
	}
}

func canTransition(from, to string) bool {
	allowed := map[string][]string{
		domain.V2MigrationStateRequired:     {domain.V2MigrationStatePreflight, domain.V2MigrationStateReady},
		domain.V2MigrationStatePreflight:    {domain.V2MigrationStateReady, domain.V2MigrationStateFailed},
		domain.V2MigrationStateReady:        {domain.V2MigrationStateRunning, domain.V2MigrationStateFailed},
		domain.V2MigrationStateRunning:      {domain.V2MigrationStatePaused, domain.V2MigrationStateFailed, domain.V2MigrationStateVerifying},
		domain.V2MigrationStatePaused:       {domain.V2MigrationStateRunning, domain.V2MigrationStateFailed},
		domain.V2MigrationStateFailed:       {domain.V2MigrationStateRunning},
		domain.V2MigrationStateVerifying:    {domain.V2MigrationStateReadyCutover, domain.V2MigrationStateFailed},
		domain.V2MigrationStateReadyCutover: {domain.V2MigrationStateCutOver},
	}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

func validatePreflight(req OperatorRequest) error {
	if strings.TrimSpace(req.BackupReference) == "" {
		return fmt.Errorf("%w: backup_reference is required", ErrPreflightRequired)
	}
	checks := req.PreflightChecks
	if !truthy(checks["postgres_restore_verified"]) {
		return fmt.Errorf("%w: postgres_restore_verified must be true", ErrPreflightRequired)
	}
	if !truthy(checks["neo4j_snapshot_verified"]) {
		return fmt.Errorf("%w: neo4j_snapshot_verified must be true", ErrPreflightRequired)
	}
	return nil
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func operatorActor(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "control"
	}
	return value
}

func operatorMetadata(req OperatorRequest) map[string]any {
	out := map[string]any{}
	if req.BackupReference != "" {
		out["backup_reference"] = strings.TrimSpace(req.BackupReference)
	}
	if len(req.PreflightChecks) > 0 {
		out["preflight_checks"] = req.PreflightChecks
	}
	return out
}

func normalizeCutoverGates(
	results []domain.V2MigrationGateResult,
	required []string,
) ([]domain.V2MigrationGateResult, error) {
	allowed := map[string]struct{}{}
	for _, name := range required {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		allowed[name] = struct{}{}
	}
	seen := map[string]domain.V2MigrationGateResult{}
	for _, gate := range results {
		gate.GateName = strings.TrimSpace(gate.GateName)
		gate.Outcome = strings.TrimSpace(gate.Outcome)
		gate.EvidenceRef = strings.TrimSpace(gate.EvidenceRef)
		gate.EvidenceHash = strings.TrimSpace(gate.EvidenceHash)
		gate.Message = strings.TrimSpace(gate.Message)
		if gate.GateName == "" {
			return nil, fmt.Errorf("%w: gate_name is required", ErrCutoverGate)
		}
		if _, ok := allowed[gate.GateName]; !ok {
			return nil, fmt.Errorf("%w: unknown gate %s", ErrCutoverGate, gate.GateName)
		}
		if gate.Outcome != domain.V2MigrationGateOutcomePass {
			return nil, fmt.Errorf("%w: %s outcome is %q", ErrCutoverGate, gate.GateName, gate.Outcome)
		}
		if gate.EvidenceRef == "" || gate.EvidenceHash == "" {
			return nil, fmt.Errorf("%w: %s evidence_ref and evidence_hash are required", ErrCutoverGate, gate.GateName)
		}
		if !validSHA256Ref(gate.EvidenceHash) {
			return nil, fmt.Errorf("%w: %s evidence_hash must be sha256:<64 hex chars>", ErrCutoverGate, gate.GateName)
		}
		if gate.Message == "" {
			return nil, fmt.Errorf("%w: %s message is required", ErrCutoverGate, gate.GateName)
		}
		if strings.TrimSpace(cutoverGateVersion(gate.Metadata)) == "" {
			return nil, fmt.Errorf("%w: %s metadata.version is required", ErrCutoverGate, gate.GateName)
		}
		if _, dup := seen[gate.GateName]; dup {
			return nil, fmt.Errorf("%w: duplicate gate %s", ErrCutoverGate, gate.GateName)
		}
		seen[gate.GateName] = gate
	}
	for name := range allowed {
		if _, ok := seen[name]; !ok {
			return nil, fmt.Errorf("%w: missing required gate %s", ErrCutoverGate, name)
		}
	}
	out := make([]domain.V2MigrationGateResult, 0, len(seen))
	for _, gate := range seen {
		out = append(out, gate)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GateName < out[j].GateName
	})
	return out, nil
}

func cutoverGateReportHash(results []domain.V2MigrationGateResult) (string, error) {
	payload := make([]map[string]any, 0, len(results))
	for _, gate := range results {
		payload = append(payload, map[string]any{
			"gate_name":     gate.GateName,
			"outcome":       gate.Outcome,
			"evidence_ref":  gate.EvidenceRef,
			"evidence_hash": gate.EvidenceHash,
			"message":       gate.Message,
			"metadata":      gate.Metadata,
		})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: encode gate report: %v", ErrCutoverGate, err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cutoverOperatorMetadata(
	req CutoverRequest,
	gateReportHash string,
	corpusHash string,
	requiredGates []string,
) map[string]any {
	out := map[string]any{
		"corpus_hash":      corpusHash,
		"gate_report_hash": gateReportHash,
		"required_gates":   append([]string(nil), requiredGates...),
	}
	if len(req.Metadata) > 0 {
		out["cutover_metadata"] = req.Metadata
	}
	return out
}

func cutoverGateVersion(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata["version"]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func validSHA256Ref(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	digest := value[len(prefix):]
	if len(digest) != 64 {
		return false
	}
	for _, ch := range digest {
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return false
	}
	return true
}
