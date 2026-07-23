package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var ErrFreshV2AuthorityBlocked = errors.New("authority repository: fresh V2 authority blocked")

type CommitFreshV2AuthorityInput struct {
	MarkerVersion string
	Metadata      map[string]any
	Now           time.Time
}

type AuthorityRepository struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

func NewAuthorityRepository(db *gorm.DB, rls postgres.RLSHelper) *AuthorityRepository {
	return &AuthorityRepository{db: db, rls: rls}
}

var freshAuthorityApplicationTables = []string{
	"teams",
	"team_profiles",
	"audit_log",
	"usage_metric_buckets",
	"operation_logs",
	"recall_feedback_events",
	"security_ip_failures",
	"security_ip_bans",
	"skill_pack_imports",
	"skill_pack_import_changes",
	"sso_providers",
	"sso_identities",
	"sso_group_mappings",
	"sso_entitlement_cache",
	"sso_oauth_states",
	"sso_sessions",
	"semantic_team_refs",
	"team_predicate_definitions",
	"semantic_profile_refs",
	"knowledge_ingests",
	"evidence_sources",
	"evidence_source_revisions",
	"evidence_fragments",
	"evidence_security_events",
	"evidence_security_signals",
	"evidence_quarantines",
	"placement_runs",
	"placement_items",
	"placement_outcomes",
	"entity_records",
	"entity_names",
	"value_records",
	"relationship_records",
	"relationship_observations",
	"verification_events",
	"relationship_evidence_supports",
	"relationship_support_decision_events",
	"relationship_transition_events",
	"entity_resolution_events",
	"entity_correction_events",
	"relationship_cross_references",
	"entity_correction_plans",
	"review_tasks",
	"hypotheses",
	"embedding_contracts",
	"search_index_generations",
	"search_documents",
	"embedding_jobs",
	"community_snapshot_runs",
	"community_records",
	"community_memberships",
	"community_sources",
	"dream_cycle_runs",
	"v2_migration_runs",
	"v2_migration_corpus_items",
	"v2_migration_source_maps",
	"v2_migration_checkpoints",
	"v2_migration_errors",
	"v2_migration_exclusions",
	"v2_migration_gate_results",
	"v2_migration_operator_actions",
}

func (r *AuthorityRepository) GetLatestMarker(ctx context.Context) (*domain.V2CompatibilityMarker, error) {
	var out *domain.V2CompatibilityMarker
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			SELECT marker_id::text, marker_kind, version, status,
			       COALESCE(run_id::text, ''), corpus_hash, gate_report_hash,
			       metadata::text, created_at
			FROM v2_compatibility_markers
			WHERE marker_kind = ?
			ORDER BY created_at DESC, marker_id DESC
			LIMIT 1
		`, domain.V2MigrationMarkerKindCutover).Row()
		record, err := scanV2CompatibilityMarker(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		if isAuthorityRowNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("authority repository: get marker: %w", err)
	}
	return out, nil
}

func (r *AuthorityRepository) CommitFreshV2Authority(
	ctx context.Context,
	input CommitFreshV2AuthorityInput,
) (*domain.V2CompatibilityMarker, error) {
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	metadata := map[string]any{"fresh_install": true}
	for key, value := range input.Metadata {
		metadata[key] = value
	}
	metadataJSON, err := marshalAuthorityJSON(metadata)
	if err != nil {
		return nil, err
	}
	var out *domain.V2CompatibilityMarker
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var existingMarkerCount int
		if err := tx.Raw(`
			SELECT count(*)::int
			FROM v2_compatibility_markers
			WHERE marker_kind = ?
		`, domain.V2MigrationMarkerKindCutover).Scan(&existingMarkerCount).Error; err != nil {
			return err
		}
		if existingMarkerCount > 0 {
			return fmt.Errorf("%w: cutover marker already exists", ErrFreshV2AuthorityBlocked)
		}
		nonempty, err := freshAuthorityNonemptyTables(tx)
		if err != nil {
			return err
		}
		if len(nonempty) > 0 {
			return fmt.Errorf("%w: nonempty application tables: %s", ErrFreshV2AuthorityBlocked, strings.Join(nonempty, ", "))
		}
		row := tx.Raw(`
			INSERT INTO v2_compatibility_markers (
			    marker_id, marker_kind, version, status, run_id,
			    corpus_hash, gate_report_hash, metadata, created_at
			) VALUES (
			    ?::uuid, ?, ?, ?, NULL, '', '', ?::jsonb, ?
			)
			RETURNING marker_id::text, marker_kind, version, status,
			          COALESCE(run_id::text, ''), corpus_hash, gate_report_hash,
			          metadata::text, created_at
		`, uuid.NewString(), domain.V2MigrationMarkerKindCutover, input.MarkerVersion,
			domain.V2MigrationMarkerCompatible, string(metadataJSON), now).Row()
		marker, err := scanV2CompatibilityMarker(row)
		if err != nil {
			return err
		}
		out = marker
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrFreshV2AuthorityBlocked) {
			return nil, err
		}
		return nil, fmt.Errorf("authority repository: commit fresh authority: %w", err)
	}
	return out, nil
}

func freshAuthorityNonemptyTables(tx *gorm.DB) ([]string, error) {
	nonempty := []string{}
	for _, table := range freshAuthorityApplicationTables {
		var exists bool
		if err := tx.Raw(`SELECT to_regclass(?) IS NOT NULL`, table).Scan(&exists).Error; err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		quoted := pqQuoteIdentifier(table)
		if err := tx.Exec("LOCK TABLE " + quoted + " IN SHARE MODE").Error; err != nil {
			return nil, err
		}
		var hasRows bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM " + quoted + " LIMIT 1)").Scan(&hasRows).Error; err != nil {
			return nil, err
		}
		if hasRows {
			nonempty = append(nonempty, table)
		}
	}
	return nonempty, nil
}

func pqQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (r *AuthorityRepository) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls == nil {
		return r.db.WithContext(ctx).Transaction(fn)
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

type authorityRowScanner interface {
	Scan(dest ...any) error
}

func scanV2CompatibilityMarker(row authorityRowScanner) (*domain.V2CompatibilityMarker, error) {
	var record domain.V2CompatibilityMarker
	var metadataJSON string
	if err := row.Scan(
		&record.MarkerID,
		&record.MarkerKind,
		&record.Version,
		&record.Status,
		&record.RunID,
		&record.CorpusHash,
		&record.GateReportHash,
		&metadataJSON,
		&record.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := unmarshalAuthorityJSON(metadataJSON, &record.Metadata); err != nil {
		return nil, err
	}
	return &record, nil
}

func marshalAuthorityJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("authority repository: encode json: %w", err)
	}
	return data, nil
}

func unmarshalAuthorityJSON(data string, target *map[string]any) error {
	if data == "" {
		*target = map[string]any{}
		return nil
	}
	if err := json.Unmarshal([]byte(data), target); err != nil {
		return fmt.Errorf("authority repository: decode json: %w", err)
	}
	if *target == nil {
		*target = map[string]any{}
	}
	return nil
}

func isAuthorityRowNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}
