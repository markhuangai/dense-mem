package migrationcontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	GateBackupRestore             = "backup_restore"
	GateMigrationTerminalOutcomes = "migration_terminal_outcomes"
	GateSourceReconciliation      = "source_reconciliation"
	GateRLSSecurity               = "rls_security"
	GateSchemaIntegrity           = "schema_integrity"
	GateProviderReadiness         = "provider_readiness"
	GateSearchIndexReadiness      = "search_index_readiness"
	GateEmbeddingBacklog          = "embedding_backlog"
	GateWorkerLeaseHealth         = "worker_lease_health"
	GateTelemetryAudit            = "telemetry_audit"
	GateRelease1K                 = "release_gate_1k"
	GateUAT                       = "uat_mcp_http_browser_compose"
	GateRestartRehearsal          = "migration_restart_rehearsal"
)

var RequiredCutoverGateNames = []string{
	GateBackupRestore,
	GateMigrationTerminalOutcomes,
	GateSourceReconciliation,
	GateRLSSecurity,
	GateSchemaIntegrity,
	GateProviderReadiness,
	GateSearchIndexReadiness,
	GateEmbeddingBacklog,
	GateWorkerLeaseHealth,
	GateTelemetryAudit,
	GateRelease1K,
	GateUAT,
	GateRestartRehearsal,
}

type GateReportRequest struct {
	OperatorRequest
	RunID          string              `json:"run_id,omitempty"`
	CorpusHash     string              `json:"corpus_hash"`
	GateReportHash string              `json:"gate_report_hash"`
	Gates          []GateResultRequest `json:"gates"`
}

type GateResultRequest struct {
	GateName     string         `json:"gate_name"`
	Outcome      string         `json:"outcome"`
	EvidenceRef  string         `json:"evidence_ref"`
	EvidenceHash string         `json:"evidence_hash"`
	GateVersion  string         `json:"gate_version"`
	Message      string         `json:"message,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type CutoverRequest struct {
	OperatorRequest
	RunID          string         `json:"run_id,omitempty"`
	MarkerVersion  string         `json:"marker_version,omitempty"`
	CorpusHash     string         `json:"corpus_hash"`
	GateReportHash string         `json:"gate_report_hash"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

func (s *service) FinalizeGates(ctx context.Context, req GateReportRequest) (*domain.V2MigrationControlStatus, error) {
	if err := validateGateReportRequest(req); err != nil {
		return nil, err
	}
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
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = run.RunID
	}
	if runID != run.RunID {
		return nil, fmt.Errorf("%w: run_id %s is not the latest run", ErrCutoverNotReady, runID)
	}
	if run.State != domain.V2MigrationStateRunning && run.State != domain.V2MigrationStateVerifying {
		return nil, fmt.Errorf("%w: %s", ErrCutoverNotReady, run.State)
	}
	now := s.now().UTC()
	_, _, err = s.store.FinalizeMigrationGateReport(ctx, repository.V2FinalizeMigrationGateReportInput{
		RunID:             runID,
		FromState:         run.State,
		CorpusHash:        strings.TrimSpace(req.CorpusHash),
		GateReportHash:    strings.TrimSpace(req.GateReportHash),
		GateResults:       gateInputsFromRequest(req.Gates),
		RequiredGateNames: RequiredCutoverGateNames,
		Now:               now,
	})
	if err != nil {
		return nil, translateCutoverRepositoryError(err)
	}
	if err := s.recordActionWithMetadata(
		ctx,
		runID,
		domain.V2MigrationActionGatesFinalized,
		req.OperatorRequest,
		gateReportActionMetadata(req),
		now,
	); err != nil {
		return nil, err
	}
	return s.Status(ctx)
}

func (s *service) CommitCutover(ctx context.Context, req CutoverRequest) (*domain.V2MigrationControlStatus, error) {
	if err := validateCutoverRequest(req); err != nil {
		return nil, err
	}
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
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = run.RunID
	}
	if runID != run.RunID {
		return nil, fmt.Errorf("%w: run_id %s is not the latest run", ErrCutoverNotReady, runID)
	}
	if run.State != domain.V2MigrationStateReadyCutover {
		return nil, fmt.Errorf("%w: %s", ErrCutoverNotReady, run.State)
	}
	markerVersion := strings.TrimSpace(req.MarkerVersion)
	if markerVersion == "" {
		markerVersion = s.cfg.CutoverMarkerVersion
	}
	now := s.now().UTC()
	_, err = s.store.CommitMigrationCutover(ctx, repository.V2CommitMigrationCutoverInput{
		RunID:             runID,
		MarkerVersion:     markerVersion,
		CorpusHash:        strings.TrimSpace(req.CorpusHash),
		GateReportHash:    strings.TrimSpace(req.GateReportHash),
		RequiredGateNames: RequiredCutoverGateNames,
		Metadata:          cutoverMetadata(req.Metadata),
		Now:               now,
	})
	if err != nil {
		return nil, translateCutoverRepositoryError(err)
	}
	if err := s.recordActionWithMetadata(
		ctx,
		runID,
		domain.V2MigrationActionCutoverCommitted,
		req.OperatorRequest,
		cutoverActionMetadata(req, markerVersion),
		now,
	); err != nil {
		return nil, err
	}
	return s.Status(ctx)
}

func validateGateReportRequest(req GateReportRequest) error {
	if strings.TrimSpace(req.CorpusHash) == "" {
		return errors.New("v2 migration control: corpus_hash is required")
	}
	if strings.TrimSpace(req.GateReportHash) == "" {
		return errors.New("v2 migration control: gate_report_hash is required")
	}
	if len(req.Gates) == 0 {
		return errors.New("v2 migration control: gates are required")
	}
	seen := map[string]struct{}{}
	for _, gate := range req.Gates {
		name := strings.TrimSpace(gate.GateName)
		if name == "" {
			return errors.New("v2 migration control: gate_name is required")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("v2 migration control: duplicate gate %s", name)
		}
		seen[name] = struct{}{}
		switch strings.TrimSpace(gate.Outcome) {
		case domain.V2MigrationGateOutcomePass, domain.V2MigrationGateOutcomeFail, domain.V2MigrationGateOutcomeWarning:
		default:
			return fmt.Errorf("v2 migration control: invalid gate outcome %q", gate.Outcome)
		}
		if strings.TrimSpace(gate.EvidenceRef) == "" {
			return fmt.Errorf("v2 migration control: gate %s evidence_ref is required", name)
		}
		if strings.TrimSpace(gate.EvidenceHash) == "" {
			return fmt.Errorf("v2 migration control: gate %s evidence_hash is required", name)
		}
		if strings.TrimSpace(gate.GateVersion) == "" {
			return fmt.Errorf("v2 migration control: gate %s gate_version is required", name)
		}
	}
	return nil
}

func validateCutoverRequest(req CutoverRequest) error {
	if strings.TrimSpace(req.CorpusHash) == "" {
		return errors.New("v2 migration control: corpus_hash is required")
	}
	if strings.TrimSpace(req.GateReportHash) == "" {
		return errors.New("v2 migration control: gate_report_hash is required")
	}
	return nil
}

func gateInputsFromRequest(gates []GateResultRequest) []repository.V2MigrationGateResultInput {
	out := make([]repository.V2MigrationGateResultInput, 0, len(gates))
	for _, gate := range gates {
		out = append(out, repository.V2MigrationGateResultInput{
			GateName:     strings.TrimSpace(gate.GateName),
			Outcome:      strings.TrimSpace(gate.Outcome),
			EvidenceRef:  strings.TrimSpace(gate.EvidenceRef),
			EvidenceHash: strings.TrimSpace(gate.EvidenceHash),
			GateVersion:  strings.TrimSpace(gate.GateVersion),
			Message:      strings.TrimSpace(gate.Message),
			Metadata:     gate.Metadata,
		})
	}
	return out
}

func cutoverMetadata(input map[string]any) map[string]any {
	out := map[string]any{
		"required_gates": RequiredCutoverGateNames,
	}
	for key, value := range input {
		out[key] = value
	}
	return out
}

func gateReportActionMetadata(req GateReportRequest) map[string]any {
	metadata := operatorMetadata(req.OperatorRequest)
	metadata["corpus_hash"] = strings.TrimSpace(req.CorpusHash)
	metadata["gate_report_hash"] = strings.TrimSpace(req.GateReportHash)
	metadata["gate_count"] = len(req.Gates)
	return metadata
}

func cutoverActionMetadata(req CutoverRequest, markerVersion string) map[string]any {
	metadata := operatorMetadata(req.OperatorRequest)
	metadata["marker_version"] = markerVersion
	metadata["corpus_hash"] = strings.TrimSpace(req.CorpusHash)
	metadata["gate_report_hash"] = strings.TrimSpace(req.GateReportHash)
	return metadata
}

func translateCutoverRepositoryError(err error) error {
	switch {
	case errors.Is(err, repository.ErrV2MigrationGateReportFailed):
		return fmt.Errorf("%w: %v", ErrGateReportFailed, err)
	case errors.Is(err, repository.ErrV2MigrationCutoverNotReady):
		return fmt.Errorf("%w: %v", ErrCutoverNotReady, err)
	case errors.Is(err, repository.ErrV2MigrationAlreadyCutOver):
		return fmt.Errorf("%w: %v", ErrAlreadyCutOver, err)
	default:
		return err
	}
}
