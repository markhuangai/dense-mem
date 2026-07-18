package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrV2MigrationGateReportFailed = errors.New("v2 migration control: gate report failed")
	ErrV2MigrationCutoverNotReady  = errors.New("v2 migration control: cutover is not ready")
	ErrV2MigrationAlreadyCutOver   = errors.New("v2 migration control: already cut over")
)

type V2MigrationGateResultInput struct {
	GateName     string
	Outcome      string
	EvidenceRef  string
	EvidenceHash string
	GateVersion  string
	Message      string
	Metadata     map[string]any
}

type V2FinalizeMigrationGateReportInput struct {
	RunID             string
	FromState         string
	CorpusHash        string
	GateReportHash    string
	GateResults       []V2MigrationGateResultInput
	RequiredGateNames []string
	Now               time.Time
}

type V2CommitMigrationCutoverInput struct {
	RunID             string
	MarkerVersion     string
	CorpusHash        string
	GateReportHash    string
	RequiredGateNames []string
	Metadata          map[string]any
	Now               time.Time
}

func (r *V2MigrationControlRepositoryImpl) FinalizeMigrationGateReport(
	ctx context.Context,
	input V2FinalizeMigrationGateReportInput,
) (*domain.V2MigrationRun, []domain.V2MigrationGateResult, error) {
	if err := validateV2FinalizeGateInput(input); err != nil {
		return nil, nil, err
	}
	now := v2MigrationTime(input.Now)
	required := normalizeV2RequiredGates(input.RequiredGateNames)
	gates := normalizeV2GateInputs(input.GateResults)
	var out *domain.V2MigrationRun
	var results []domain.V2MigrationGateResult
	var gateErr error
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		run, err := lockV2MigrationRun(tx, input.RunID)
		if err != nil {
			return err
		}
		if !canFinalizeV2GateState(run.State) {
			return fmt.Errorf("%w: %s", ErrV2MigrationCutoverNotReady, run.State)
		}
		if strings.TrimSpace(input.FromState) != "" && run.State != strings.TrimSpace(input.FromState) {
			return fmt.Errorf("%w: stale state %s", ErrV2MigrationCutoverNotReady, run.State)
		}
		if _, err := updateV2MigrationRunFinalizing(tx, input.RunID, input.CorpusHash, input.GateReportHash, now); err != nil {
			return err
		}
		for _, gate := range gates {
			if err := upsertV2MigrationGateResult(tx, input.RunID, gate, now); err != nil {
				return err
			}
		}
		results, err = listV2MigrationGateResultsTx(tx, input.RunID, 0)
		if err != nil {
			return err
		}
		if err := validateV2CutoverPredicates(tx, input.RunID, required, results); err != nil {
			markErr := markV2MigrationRunGateFailure(tx, input.RunID, err.Error(), now)
			if markErr != nil {
				return markErr
			}
			gateErr = err
			return nil
		}
		ready, err := updateV2MigrationRunCutoverReady(tx, input.RunID, input.CorpusHash, input.GateReportHash, now)
		if err != nil {
			return err
		}
		out = ready
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("v2 migration control: finalize gate report: %w", err)
	}
	if gateErr != nil {
		return nil, nil, fmt.Errorf("v2 migration control: finalize gate report: %w", gateErr)
	}
	return out, results, nil
}

func (r *V2MigrationControlRepositoryImpl) CommitMigrationCutover(
	ctx context.Context,
	input V2CommitMigrationCutoverInput,
) (*domain.V2CompatibilityMarker, error) {
	if err := validateV2CommitCutoverInput(input); err != nil {
		return nil, err
	}
	now := v2MigrationTime(input.Now)
	required := normalizeV2RequiredGates(input.RequiredGateNames)
	metadata, err := marshalV2MigrationJSON(input.Metadata)
	if err != nil {
		return nil, err
	}
	var marker *domain.V2CompatibilityMarker
	var cutoverErr error
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		run, err := lockV2MigrationRun(tx, input.RunID)
		if err != nil {
			return err
		}
		if run.State != domain.V2MigrationStateReadyCutover {
			return fmt.Errorf("%w: %s", ErrV2MigrationCutoverNotReady, run.State)
		}
		if run.CorpusHash != strings.TrimSpace(input.CorpusHash) {
			cutoverErr = fmt.Errorf("%w: corpus hash changed", ErrV2MigrationCutoverNotReady)
			return markV2MigrationRunGateFailure(tx, input.RunID, cutoverErr.Error(), now)
		}
		if run.GateReportHash != strings.TrimSpace(input.GateReportHash) {
			cutoverErr = fmt.Errorf("%w: gate report hash changed", ErrV2MigrationCutoverNotReady)
			return markV2MigrationRunGateFailure(tx, input.RunID, cutoverErr.Error(), now)
		}
		results, err := listV2MigrationGateResultsTx(tx, input.RunID, 0)
		if err != nil {
			return err
		}
		if err := validateV2CutoverPredicates(tx, input.RunID, required, results); err != nil {
			markErr := markV2MigrationRunGateFailure(tx, input.RunID, err.Error(), now)
			if markErr != nil {
				return markErr
			}
			cutoverErr = err
			return nil
		}
		var existing int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM v2_compatibility_markers
			WHERE marker_kind = ?
			  AND status = ?
		`, domain.V2MigrationMarkerKindCutover, domain.V2MigrationMarkerCompatible).Scan(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return ErrV2MigrationAlreadyCutOver
		}
		row := tx.Raw(`
			INSERT INTO v2_compatibility_markers (
			    marker_id, marker_kind, version, status, run_id,
			    corpus_hash, gate_report_hash, metadata, created_at
			) VALUES (
			    ?::uuid, ?, ?, ?, ?::uuid, ?, ?, ?::jsonb, ?
			)
			RETURNING marker_id::text, marker_kind, version, status,
			          COALESCE(run_id::text, ''), corpus_hash, gate_report_hash,
			          metadata::text, created_at
		`, uuid.NewString(), domain.V2MigrationMarkerKindCutover, strings.TrimSpace(input.MarkerVersion),
			domain.V2MigrationMarkerCompatible, input.RunID, strings.TrimSpace(input.CorpusHash),
			strings.TrimSpace(input.GateReportHash), string(metadata), now).Row()
		inserted, err := scanV2CompatibilityMarker(row)
		if err != nil {
			return translateV2CutoverMarkerError(err)
		}
		if _, err := updateV2MigrationRunCutOver(tx, input.RunID, now); err != nil {
			return err
		}
		marker = inserted
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration control: commit cutover: %w", err)
	}
	if cutoverErr != nil {
		return nil, fmt.Errorf("v2 migration control: commit cutover: %w", cutoverErr)
	}
	return marker, nil
}

func (r *V2MigrationControlRepositoryImpl) ListMigrationGateResults(
	ctx context.Context,
	runID string,
	limit int,
) ([]domain.V2MigrationGateResult, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, nil
	}
	out := []domain.V2MigrationGateResult{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var err error
		out, err = listV2MigrationGateResultsTx(tx, runID, limit)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration control: list gate results: %w", err)
	}
	return out, nil
}

func lockV2MigrationRun(tx *gorm.DB, runID string) (*domain.V2MigrationRun, error) {
	row := tx.Raw(`
		SELECT run_id::text, migration_contract_version, corpus_version, source_kind,
		       state, phase, required, preflight_approved, backup_reference,
		       preflight_checks::text, corpus_watermark, corpus_hash, gate_report_hash,
		       total_items, completed_items, failed_items, excluded_items,
		       last_error, retryable, lease_owner, checkpoint_key,
		       checkpoint_value::text, started_at, completed_at, cutover_at,
		       created_at, updated_at
		FROM v2_migration_runs
		WHERE run_id = ?::uuid
		FOR UPDATE
	`, strings.TrimSpace(runID)).Row()
	run, err := scanV2MigrationRun(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrV2MigrationCutoverNotReady
		}
		return nil, err
	}
	return run, nil
}

func updateV2MigrationRunFinalizing(
	tx *gorm.DB,
	runID string,
	corpusHash string,
	gateReportHash string,
	now time.Time,
) (*domain.V2MigrationRun, error) {
	row := tx.Raw(`
		UPDATE v2_migration_runs
		SET state = ?,
		    phase = ?,
		    corpus_hash = ?,
		    gate_report_hash = ?,
		    last_error = '',
		    retryable = true,
		    updated_at = ?
		WHERE run_id = ?::uuid
		RETURNING run_id::text, migration_contract_version, corpus_version, source_kind,
		          state, phase, required, preflight_approved, backup_reference,
		          preflight_checks::text, corpus_watermark, corpus_hash, gate_report_hash,
		          total_items, completed_items, failed_items, excluded_items,
		          last_error, retryable, lease_owner, checkpoint_key,
		          checkpoint_value::text, started_at, completed_at, cutover_at,
		          created_at, updated_at
	`, domain.V2MigrationStateVerifying, "finalizing", strings.TrimSpace(corpusHash),
		strings.TrimSpace(gateReportHash), now, strings.TrimSpace(runID)).Row()
	return scanV2MigrationRun(row)
}

func updateV2MigrationRunCutoverReady(
	tx *gorm.DB,
	runID string,
	corpusHash string,
	gateReportHash string,
	now time.Time,
) (*domain.V2MigrationRun, error) {
	row := tx.Raw(`
		UPDATE v2_migration_runs
		SET state = ?,
		    phase = ?,
		    corpus_hash = ?,
		    gate_report_hash = ?,
		    last_error = '',
		    retryable = true,
		    updated_at = ?
		WHERE run_id = ?::uuid
		  AND state = ?
		RETURNING run_id::text, migration_contract_version, corpus_version, source_kind,
		          state, phase, required, preflight_approved, backup_reference,
		          preflight_checks::text, corpus_watermark, corpus_hash, gate_report_hash,
		          total_items, completed_items, failed_items, excluded_items,
		          last_error, retryable, lease_owner, checkpoint_key,
		          checkpoint_value::text, started_at, completed_at, cutover_at,
		          created_at, updated_at
	`, domain.V2MigrationStateReadyCutover, "cutover_ready", strings.TrimSpace(corpusHash),
		strings.TrimSpace(gateReportHash), now, strings.TrimSpace(runID), domain.V2MigrationStateVerifying).Row()
	return scanV2MigrationRun(row)
}

func updateV2MigrationRunCutOver(tx *gorm.DB, runID string, now time.Time) (*domain.V2MigrationRun, error) {
	row := tx.Raw(`
		UPDATE v2_migration_runs
		SET state = ?,
		    phase = ?,
		    completed_at = COALESCE(completed_at, ?),
		    cutover_at = ?,
		    updated_at = ?
		WHERE run_id = ?::uuid
		  AND state = ?
		RETURNING run_id::text, migration_contract_version, corpus_version, source_kind,
		          state, phase, required, preflight_approved, backup_reference,
		          preflight_checks::text, corpus_watermark, corpus_hash, gate_report_hash,
		          total_items, completed_items, failed_items, excluded_items,
		          last_error, retryable, lease_owner, checkpoint_key,
		          checkpoint_value::text, started_at, completed_at, cutover_at,
		          created_at, updated_at
	`, domain.V2MigrationStateCutOver, "cutover", now, now, now,
		strings.TrimSpace(runID), domain.V2MigrationStateReadyCutover).Row()
	return scanV2MigrationRun(row)
}

func markV2MigrationRunGateFailure(tx *gorm.DB, runID string, message string, now time.Time) error {
	safeMessage := strings.TrimSpace(message)
	if len(safeMessage) > 1000 {
		safeMessage = safeMessage[:1000]
	}
	return tx.Exec(`
		UPDATE v2_migration_runs
		SET state = ?,
		    phase = ?,
		    last_error = ?,
		    retryable = true,
		    updated_at = ?
		WHERE run_id = ?::uuid
	`, domain.V2MigrationStateVerifying, "gate_failed", safeMessage, now, strings.TrimSpace(runID)).Error
}

func upsertV2MigrationGateResult(
	tx *gorm.DB,
	runID string,
	gate V2MigrationGateResultInput,
	now time.Time,
) error {
	metadata, err := marshalV2MigrationJSON(gate.Metadata)
	if err != nil {
		return err
	}
	return tx.Exec(`
		INSERT INTO v2_migration_gate_results (
		    gate_id, run_id, gate_name, outcome, evidence_ref, evidence_hash,
		    gate_version, message, metadata, created_at
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?::jsonb, ?
		)
		ON CONFLICT (run_id, gate_name) DO UPDATE
		SET outcome = EXCLUDED.outcome,
		    evidence_ref = EXCLUDED.evidence_ref,
		    evidence_hash = EXCLUDED.evidence_hash,
		    gate_version = EXCLUDED.gate_version,
		    message = EXCLUDED.message,
		    metadata = EXCLUDED.metadata,
		    created_at = EXCLUDED.created_at
	`, uuid.NewString(), strings.TrimSpace(runID), strings.TrimSpace(gate.GateName),
		strings.TrimSpace(gate.Outcome), strings.TrimSpace(gate.EvidenceRef),
		strings.TrimSpace(gate.EvidenceHash), strings.TrimSpace(gate.GateVersion),
		strings.TrimSpace(gate.Message), string(metadata), now).Error
}

func listV2MigrationGateResultsTx(tx *gorm.DB, runID string, limit int) ([]domain.V2MigrationGateResult, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := tx.Raw(`
		SELECT gate_id::text, run_id::text, gate_name, outcome,
		       evidence_ref, evidence_hash, gate_version, message,
		       metadata::text, created_at
		FROM v2_migration_gate_results
		WHERE run_id = ?::uuid
		ORDER BY gate_name ASC
		LIMIT ?
	`, strings.TrimSpace(runID), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.V2MigrationGateResult{}
	for rows.Next() {
		record, err := scanV2MigrationGateResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func scanV2MigrationGateResult(row v2MigrationRowScanner) (domain.V2MigrationGateResult, error) {
	var record domain.V2MigrationGateResult
	var metadataJSON string
	if err := row.Scan(
		&record.GateID,
		&record.RunID,
		&record.GateName,
		&record.Outcome,
		&record.EvidenceRef,
		&record.EvidenceHash,
		&record.GateVersion,
		&record.Message,
		&metadataJSON,
		&record.CreatedAt,
	); err != nil {
		return domain.V2MigrationGateResult{}, err
	}
	if err := unmarshalV2MigrationJSON(metadataJSON, &record.Metadata); err != nil {
		return domain.V2MigrationGateResult{}, err
	}
	return record, nil
}

func validateV2CutoverPredicates(
	tx *gorm.DB,
	runID string,
	required []string,
	results []domain.V2MigrationGateResult,
) error {
	blockers := validateV2RequiredGateResults(required, results)
	var pendingOrFailed int64
	if err := tx.Raw(`
		SELECT count(*)
		FROM v2_migration_corpus_items
		WHERE run_id = ?::uuid
		  AND outcome IN (?, ?)
	`, strings.TrimSpace(runID), domain.V2MigrationOutcomePending, domain.V2MigrationOutcomeFailed).
		Scan(&pendingOrFailed).Error; err != nil {
		return err
	}
	if pendingOrFailed > 0 {
		blockers = append(blockers, fmt.Sprintf("%d corpus items are pending or failed", pendingOrFailed))
	}
	var blockingExclusions int64
	if err := tx.Raw(`
		SELECT count(*)
		FROM v2_migration_exclusions
		WHERE run_id = ?::uuid
		  AND blocks_cutover = true
	`, strings.TrimSpace(runID)).Scan(&blockingExclusions).Error; err != nil {
		return err
	}
	if blockingExclusions > 0 {
		blockers = append(blockers, fmt.Sprintf("%d exclusions block cutover", blockingExclusions))
	}
	var missingManifest int64
	if err := tx.Raw(`
		SELECT count(*)
		FROM v2_migration_corpus_items c
		WHERE c.run_id = ?::uuid
		  AND c.outcome = ?
		  AND NOT EXISTS (
		      SELECT 1
		      FROM v2_migration_exclusions e
		      WHERE e.run_id = c.run_id
		        AND e.source_kind = c.source_kind
		        AND e.source_id = c.source_id
		  )
	`, strings.TrimSpace(runID), domain.V2MigrationOutcomeExcluded).Scan(&missingManifest).Error; err != nil {
		return err
	}
	if missingManifest > 0 {
		blockers = append(blockers, fmt.Sprintf("%d excluded corpus items are missing exclusion manifest rows", missingManifest))
	}
	if len(blockers) > 0 {
		return fmt.Errorf("%w: %s", ErrV2MigrationGateReportFailed, strings.Join(blockers, "; "))
	}
	return nil
}

func validateV2RequiredGateResults(required []string, results []domain.V2MigrationGateResult) []string {
	blockers := []string{}
	byName := make(map[string]domain.V2MigrationGateResult, len(results))
	for _, result := range results {
		byName[strings.TrimSpace(result.GateName)] = result
	}
	for _, name := range required {
		result, ok := byName[name]
		if !ok {
			blockers = append(blockers, "missing required gate "+name)
			continue
		}
		if result.Outcome != domain.V2MigrationGateOutcomePass {
			blockers = append(blockers, fmt.Sprintf("gate %s outcome is %s", name, result.Outcome))
			continue
		}
		if strings.TrimSpace(result.EvidenceRef) == "" ||
			strings.TrimSpace(result.EvidenceHash) == "" ||
			strings.TrimSpace(result.GateVersion) == "" {
			blockers = append(blockers, "gate "+name+" is missing evidence ref, hash, or version")
		}
	}
	return blockers
}

func validateV2FinalizeGateInput(input V2FinalizeMigrationGateReportInput) error {
	if strings.TrimSpace(input.RunID) == "" {
		return errors.New("v2 migration control: run_id is required")
	}
	if strings.TrimSpace(input.CorpusHash) == "" {
		return errors.New("v2 migration control: corpus_hash is required")
	}
	if strings.TrimSpace(input.GateReportHash) == "" {
		return errors.New("v2 migration control: gate_report_hash is required")
	}
	if len(normalizeV2RequiredGates(input.RequiredGateNames)) == 0 {
		return errors.New("v2 migration control: required gates are required")
	}
	if len(input.GateResults) == 0 {
		return errors.New("v2 migration control: gate results are required")
	}
	for _, gate := range normalizeV2GateInputs(input.GateResults) {
		if err := validateV2GateInput(gate); err != nil {
			return err
		}
	}
	return nil
}

func validateV2CommitCutoverInput(input V2CommitMigrationCutoverInput) error {
	if strings.TrimSpace(input.RunID) == "" {
		return errors.New("v2 migration control: run_id is required")
	}
	if strings.TrimSpace(input.MarkerVersion) == "" {
		return errors.New("v2 migration control: marker_version is required")
	}
	if strings.TrimSpace(input.CorpusHash) == "" {
		return errors.New("v2 migration control: corpus_hash is required")
	}
	if strings.TrimSpace(input.GateReportHash) == "" {
		return errors.New("v2 migration control: gate_report_hash is required")
	}
	if len(normalizeV2RequiredGates(input.RequiredGateNames)) == 0 {
		return errors.New("v2 migration control: required gates are required")
	}
	return nil
}

func validateV2GateInput(gate V2MigrationGateResultInput) error {
	if strings.TrimSpace(gate.GateName) == "" {
		return errors.New("v2 migration control: gate_name is required")
	}
	switch strings.TrimSpace(gate.Outcome) {
	case domain.V2MigrationGateOutcomePass, domain.V2MigrationGateOutcomeFail, domain.V2MigrationGateOutcomeWarning:
	default:
		return fmt.Errorf("v2 migration control: invalid gate outcome %q", gate.Outcome)
	}
	if strings.TrimSpace(gate.EvidenceRef) == "" {
		return errors.New("v2 migration control: evidence_ref is required")
	}
	if strings.TrimSpace(gate.EvidenceHash) == "" {
		return errors.New("v2 migration control: evidence_hash is required")
	}
	if strings.TrimSpace(gate.GateVersion) == "" {
		return errors.New("v2 migration control: gate_version is required")
	}
	return nil
}

func normalizeV2GateInputs(inputs []V2MigrationGateResultInput) []V2MigrationGateResultInput {
	out := make([]V2MigrationGateResultInput, 0, len(inputs))
	for _, input := range inputs {
		input.GateName = strings.TrimSpace(input.GateName)
		input.Outcome = strings.TrimSpace(input.Outcome)
		input.EvidenceRef = strings.TrimSpace(input.EvidenceRef)
		input.EvidenceHash = strings.TrimSpace(input.EvidenceHash)
		input.GateVersion = strings.TrimSpace(input.GateVersion)
		input.Message = strings.TrimSpace(input.Message)
		out = append(out, input)
	}
	return out
}

func normalizeV2RequiredGates(names []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func canFinalizeV2GateState(state string) bool {
	return state == domain.V2MigrationStateRunning || state == domain.V2MigrationStateVerifying
}

func translateV2CutoverMarkerError(err error) error {
	if isPostgresUniqueConstraint(err, "idx_v2_compatibility_markers_single_compatible_cutover") {
		return ErrV2MigrationAlreadyCutOver
	}
	return err
}
