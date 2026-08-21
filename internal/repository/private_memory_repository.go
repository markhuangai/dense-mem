package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var (
	ErrPrivateMemoryNotFound          = errors.New("private memory target not found")
	ErrPrivateMemoryLegalHold         = errors.New("private memory is under legal hold")
	ErrPrivateMemoryIdempotency       = errors.New("private memory idempotency conflict")
	ErrPrivateMemoryOperationConflict = errors.New("private memory erasure is already in progress")
	ErrPrivateMemoryManifest          = errors.New("private memory erasure manifest mismatch")
	ErrPrivateMemoryClaimLost         = errors.New("private memory erasure claim lost")
	ErrPrivateMemoryRetentionDisabled = errors.New("private memory retention is disabled")
	ErrPrivateMemoryHoldConflict      = errors.New("private memory legal hold conflict")
	ErrPrivateMemoryInternal          = errors.New("private memory storage operation failed")
)

const (
	defaultPrivateMemoryLease      = 5 * time.Minute
	privateMemoryMaximumAttempts   = 5
	privateMemoryRetryBaseDelay    = time.Second
	privateMemoryRetryMaximumDelay = time.Minute
	defaultPrivateMemoryListLimit  = 100
	maximumPrivateMemoryListLimit  = 500
	defaultPrivateRetentionBatch   = 100
	maximumPrivateRetentionBatch   = 500
	privateContentAgeBackfillBatch = 500
	privateMemoryAuditMetadataJSON = `{"private_content_erased": true}`
)

// privateMemoryErasureManifest is the closed set of final-schema semantic
// tables owned by a memory space. Prepare compares this list to the live
// catalog before any erasure worker starts.
var privateMemoryErasureManifest = []string{
	"knowledge_ingests", "evidence_sources", "evidence_source_revisions",
	"evidence_fragments", "evidence_security_events", "evidence_security_signals",
	"evidence_quarantines", "evidence_lifecycle_operations", "evidence_lifecycle_events",
	"placement_runs", "placement_items", "placement_outcomes", "placement_assessments", "predicate_registration_events",
	"entity_records", "entity_names", "entity_resolution_events",
	"entity_correction_plans", "entity_correction_events", "value_records",
	"relationship_records", "relationship_observations", "relationship_evidence_supports",
	"relationship_support_decision_events", "relationship_transition_events",
	"relationship_cross_references", "relationship_correction_submissions",
	"relationship_correction_events", "verification_events", "review_tasks",
	"hypotheses", "hypothesis_derivation_sources", "hypothesis_feedback_events",
	"submission_holds", "submission_quarantine_payloads", "submission_quarantine_tombstones", "relationship_conflict_cases",
	"relationship_conflict_positions", "relationship_conflict_position_members",
	"relationship_conflict_events", "relationship_conflict_review_runs",
	"relationship_conflict_derived_evidence_tasks", "relationship_conflict_evidence_derivations",
	"relationship_conflict_resolution_plans", "relationship_conflict_ai_assessment_attempts",
	"relationship_conflict_ai_assessment_events", "search_documents", "embedding_jobs",
	"community_snapshot_runs", "community_records", "community_memberships",
	"community_sources", "community_summary_attempts", "dream_cycle_runs",
	"dream_path_evaluations", "recall_feedback_events",
}

var privateMemoryCatalogExclusions = []string{
	"private_memory_erasure_operations",
	"private_memory_legal_holds",
}

var privateMemoryExternalDependencies = map[string]struct {
	child  string
	parent string
}{
	"v2_migration_corpus_items_team_id_ingest_id_fkey": {
		child: "v2_migration_corpus_items", parent: "knowledge_ingests",
	},
	"v2_migration_corpus_items_team_id_placement_item_id_fkey": {
		child: "v2_migration_corpus_items", parent: "placement_items",
	},
}

type PrivateMemoryErasureRequest struct {
	TeamID                    uuid.UUID
	OwnerID                   uuid.UUID
	CredentialID              uuid.UUID
	IdempotencyScopeHash      string
	RequestHash               string
	ReasonCode                string
	CredentialRevocationAudit *PrivateMemoryCredentialRevocationAudit
}

type PrivateMemoryCredentialRevocationAudit struct {
	ActorProfileID    *string
	ActorCredentialID *string
	ActorRole         string
	ClientIP          string
	CorrelationID     string
}

type PrivateMemoryRetentionRequest struct {
	ActorClass           domain.PrivateMemoryActorClass
	IdempotencyScopeHash string
	RequestHash          string
	RetentionDays        int
	BatchSize            int
	Now                  time.Time
}

type PrivateMemoryRepository interface {
	Prepare(ctx context.Context) error
	RequestProfileErasure(ctx context.Context, input PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error)
	RequestCredentialErasure(ctx context.Context, input PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error)
	RequestControlErasure(ctx context.Context, spaceID uuid.UUID, idempotencyScopeHash, requestHash, reasonCode string) (*domain.PrivateMemoryErasureOperation, bool, error)
	DisableSSOCredential(ctx context.Context, input PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error)
	GetOwnerOperation(ctx context.Context, teamID, operationID uuid.UUID, identityID, credentialID *uuid.UUID) (*domain.PrivateMemoryErasureOperation, error)
	GetOperation(ctx context.Context, operationID uuid.UUID) (*domain.PrivateMemoryErasureOperation, error)
	ListOperations(ctx context.Context, limit, offset int) ([]domain.PrivateMemoryErasureOperation, error)
	ListSpaces(ctx context.Context, limit, offset int) ([]domain.PrivateMemorySpaceMetadata, error)
	PlaceLegalHold(ctx context.Context, spaceID uuid.UUID, reasonCode string) (*domain.PrivateMemoryLegalHold, bool, error)
	ReleaseLegalHold(ctx context.Context, spaceID uuid.UUID) (*domain.PrivateMemoryLegalHold, bool, error)
	RunRetention(ctx context.Context, input PrivateMemoryRetentionRequest) (*domain.PrivateMemoryRetentionRun, bool, error)
	ListRetentionRuns(ctx context.Context, limit, offset int) ([]domain.PrivateMemoryRetentionRun, error)
	ClaimNext(ctx context.Context, workerID string, lease time.Duration) (*domain.PrivateMemoryErasureOperation, error)
	ExecuteClaim(ctx context.Context, operationID uuid.UUID, workerID string, fence int64) (*domain.PrivateMemoryErasureOperation, error)
	ReleaseClaim(ctx context.Context, operationID uuid.UUID, workerID string, fence int64, errorCode string) error
}

type PrivateMemoryRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
	now func() time.Time

	manifestMu sync.RWMutex
	ordered    []string
}

var _ PrivateMemoryRepository = (*PrivateMemoryRepositoryImpl)(nil)

func NewPrivateMemoryRepository(db *gorm.DB, rls postgres.RLSHelper) *PrivateMemoryRepositoryImpl {
	return &PrivateMemoryRepositoryImpl{
		db:  db,
		rls: rls,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func PrivateMemoryErasureManifest() []string {
	manifest := append([]string(nil), privateMemoryErasureManifest...)
	sort.Strings(manifest)
	return manifest
}

func (r *PrivateMemoryRepositoryImpl) Prepare(ctx context.Context) error {
	if r == nil || r.db == nil || r.rls == nil {
		return fmt.Errorf("%w: repository is unavailable", ErrPrivateMemoryManifest)
	}
	var ordered []string
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		ordered, err = validatePrivateMemoryManifestTx(ctx, tx)
		return err
	})
	if err != nil {
		return err
	}
	r.manifestMu.Lock()
	r.ordered = append([]string(nil), ordered...)
	r.manifestMu.Unlock()
	return nil
}

func validatePrivateMemoryManifestTx(ctx context.Context, tx *gorm.DB) ([]string, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT DISTINCT columns.table_name
		FROM information_schema.columns AS columns
		JOIN information_schema.tables AS tables
		  ON tables.table_schema = columns.table_schema
		 AND tables.table_name = columns.table_name
		WHERE columns.table_schema = 'public'
		  AND columns.column_name = 'space_id'
		  AND tables.table_type = 'BASE TABLE'
		  AND NOT (columns.table_name = ANY($1::text[]))
		ORDER BY columns.table_name
	`, pq.Array(privateMemoryCatalogExclusions)).Rows()
	if err != nil {
		return nil, fmt.Errorf("%w: read catalog: %v", ErrPrivateMemoryManifest, err)
	}
	defer rows.Close()
	catalog := make([]string, 0, len(privateMemoryErasureManifest))
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("%w: scan catalog: %v", ErrPrivateMemoryManifest, err)
		}
		catalog = append(catalog, table)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read catalog rows: %v", ErrPrivateMemoryManifest, err)
	}
	expected := PrivateMemoryErasureManifest()
	if missing, unknown := stringSetDifference(expected, catalog), stringSetDifference(catalog, expected); len(missing) > 0 || len(unknown) > 0 {
		return nil, fmt.Errorf("%w: missing=%v unknown=%v", ErrPrivateMemoryManifest, missing, unknown)
	}

	type foreignKey struct {
		child      string
		parent     string
		name       string
		deleteType string
		deferrable bool
	}
	fkRows, err := tx.WithContext(ctx).Raw(`
		SELECT child.relname, parent.relname, constraint_row.conname,
		       constraint_row.confdeltype::text, constraint_row.condeferrable
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS child ON child.oid = constraint_row.conrelid
		JOIN pg_namespace AS child_namespace ON child_namespace.oid = child.relnamespace
		JOIN pg_class AS parent ON parent.oid = constraint_row.confrelid
		JOIN pg_namespace AS parent_namespace ON parent_namespace.oid = parent.relnamespace
		WHERE constraint_row.contype = 'f'
		  AND child_namespace.nspname = 'public'
		  AND parent_namespace.nspname = 'public'
		ORDER BY child.relname, parent.relname, constraint_row.conname
	`).Rows()
	if err != nil {
		return nil, fmt.Errorf("%w: read foreign keys: %v", ErrPrivateMemoryManifest, err)
	}
	defer fkRows.Close()
	fks := make([]foreignKey, 0)
	for fkRows.Next() {
		var item foreignKey
		if err := fkRows.Scan(&item.child, &item.parent, &item.name, &item.deleteType, &item.deferrable); err != nil {
			return nil, fmt.Errorf("%w: scan foreign keys: %v", ErrPrivateMemoryManifest, err)
		}
		fks = append(fks, item)
	}
	if err := fkRows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read foreign key rows: %v", ErrPrivateMemoryManifest, err)
	}

	manifestSet := make(map[string]struct{}, len(expected))
	for _, table := range expected {
		manifestSet[table] = struct{}{}
	}
	edges := make(map[string]map[string]struct{}, len(expected))
	indegree := make(map[string]int, len(expected))
	externalDependencies := make([]string, 0)
	seenExternalDependencies := make(map[string]struct{}, len(privateMemoryExternalDependencies))
	for _, table := range expected {
		edges[table] = make(map[string]struct{})
		indegree[table] = 0
	}
	for _, fk := range fks {
		_, parentOwned := manifestSet[fk.parent]
		if !parentOwned {
			continue
		}
		_, childOwned := manifestSet[fk.child]
		if !childOwned {
			if fk.deleteType != "c" {
				known, ok := privateMemoryExternalDependencies[fk.name]
				if !ok || known.child != fk.child || known.parent != fk.parent {
					externalDependencies = append(externalDependencies, fmt.Sprintf("%s->%s(%s)", fk.child, fk.parent, fk.name))
				} else {
					seenExternalDependencies[fk.name] = struct{}{}
				}
			}
			continue
		}
		if fk.child == fk.parent {
			continue
		}
		if fk.deferrable {
			continue
		}
		if _, exists := edges[fk.child][fk.parent]; exists {
			continue
		}
		edges[fk.child][fk.parent] = struct{}{}
		indegree[fk.parent]++
	}
	if len(externalDependencies) > 0 {
		return nil, fmt.Errorf("%w: external dependencies=%v", ErrPrivateMemoryManifest, externalDependencies)
	}
	missingExternalDependencies := make([]string, 0)
	for name := range privateMemoryExternalDependencies {
		if _, ok := seenExternalDependencies[name]; !ok {
			missingExternalDependencies = append(missingExternalDependencies, name)
		}
	}
	if len(missingExternalDependencies) > 0 {
		sort.Strings(missingExternalDependencies)
		return nil, fmt.Errorf("%w: missing external dependencies=%v", ErrPrivateMemoryManifest, missingExternalDependencies)
	}

	ready := make([]string, 0, len(expected))
	for table, count := range indegree {
		if count == 0 {
			ready = append(ready, table)
		}
	}
	sort.Strings(ready)
	ordered := make([]string, 0, len(expected))
	for len(ready) > 0 {
		table := ready[0]
		ready = ready[1:]
		ordered = append(ordered, table)
		parents := make([]string, 0, len(edges[table]))
		for parent := range edges[table] {
			parents = append(parents, parent)
		}
		sort.Strings(parents)
		for _, parent := range parents {
			indegree[parent]--
			if indegree[parent] == 0 {
				ready = append(ready, parent)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(expected) {
		cycleEdges := make([]string, 0)
		for child, parents := range edges {
			if indegree[child] == 0 {
				continue
			}
			for parent := range parents {
				if indegree[parent] > 0 {
					cycleEdges = append(cycleEdges, child+"->"+parent)
				}
			}
		}
		sort.Strings(cycleEdges)
		return nil, fmt.Errorf("%w: restrictive foreign-key cycle=%v", ErrPrivateMemoryManifest, cycleEdges)
	}
	return ordered, nil
}

func stringSetDifference(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	difference := make([]string, 0)
	for _, item := range left {
		if _, exists := rightSet[item]; !exists {
			difference = append(difference, item)
		}
	}
	sort.Strings(difference)
	return difference
}

func (r *PrivateMemoryRepositoryImpl) RequestProfileErasure(ctx context.Context, input PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	if err := validatePrivateMemoryErasureInput(input); err != nil {
		return nil, false, err
	}
	var operation *domain.PrivateMemoryErasureOperation
	created := false
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := lockPrivateMemoryIdempotencyScopeTx(ctx, tx, input.IdempotencyScopeHash); err != nil {
			return err
		}
		existing, err := existingPrivateMemoryOperationTx(ctx, tx, input.IdempotencyScopeHash, input.RequestHash)
		if err != nil || existing != nil {
			operation = existing
			return err
		}
		space, err := privateMemorySpaceForProfileTx(ctx, tx, input.TeamID, input.OwnerID)
		if err != nil {
			return err
		}
		operation, err = queuePrivateMemorySpaceTx(ctx, tx, space, queuePrivateMemoryInput{
			Action:               domain.PrivateMemoryEraseProfilePrivate,
			ActorClass:           domain.PrivateMemoryActorOwnerSSO,
			ReasonCode:           input.ReasonCode,
			IdempotencyScopeHash: input.IdempotencyScopeHash,
			RequestHash:          input.RequestHash,
		})
		created = err == nil
		return err
	})
	return operation, created, wrapPrivateMemoryError("request profile erasure", err)
}

func (r *PrivateMemoryRepositoryImpl) RequestCredentialErasure(ctx context.Context, input PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	if err := validatePrivateMemoryErasureInput(input); err != nil {
		return nil, false, err
	}
	if input.CredentialID == uuid.Nil {
		return nil, false, fmt.Errorf("%w: credential ID is required", ErrPrivateMemoryNotFound)
	}
	var operation *domain.PrivateMemoryErasureOperation
	created := false
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := lockPrivateMemoryIdempotencyScopeTx(ctx, tx, input.IdempotencyScopeHash); err != nil {
			return err
		}
		existing, err := existingPrivateMemoryOperationTx(ctx, tx, input.IdempotencyScopeHash, input.RequestHash)
		if err != nil || existing != nil {
			operation = existing
			return err
		}
		space, err := privateMemorySpaceForCredentialTx(ctx, tx, input.TeamID, input.CredentialID, true)
		if err != nil {
			return err
		}
		credentialID := input.CredentialID
		operation, err = queuePrivateMemorySpaceTx(ctx, tx, space, queuePrivateMemoryInput{
			Action:               domain.PrivateMemoryEraseCredentialPrivate,
			ActorClass:           domain.PrivateMemoryActorOwnerCredential,
			ReasonCode:           input.ReasonCode,
			TargetCredentialID:   &credentialID,
			IdempotencyScopeHash: input.IdempotencyScopeHash,
			RequestHash:          input.RequestHash,
		})
		created = err == nil
		return err
	})
	return operation, created, wrapPrivateMemoryError("request credential erasure", err)
}

func (r *PrivateMemoryRepositoryImpl) RequestControlErasure(ctx context.Context, spaceID uuid.UUID, idempotencyScopeHash, requestHash, reasonCode string) (*domain.PrivateMemoryErasureOperation, bool, error) {
	if spaceID == uuid.Nil || len(idempotencyScopeHash) != 64 || len(requestHash) != 64 || strings.TrimSpace(reasonCode) == "" {
		return nil, false, ErrPrivateMemoryNotFound
	}
	var operation *domain.PrivateMemoryErasureOperation
	created := false
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := lockPrivateMemoryIdempotencyScopeTx(ctx, tx, idempotencyScopeHash); err != nil {
			return err
		}
		existing, err := existingPrivateMemoryOperationTx(ctx, tx, idempotencyScopeHash, requestHash)
		if err != nil || existing != nil {
			operation = existing
			return err
		}
		space, err := privateMemorySpaceByIDTx(ctx, tx, spaceID)
		if err != nil {
			return err
		}
		action := domain.PrivateMemoryEraseProfilePrivate
		var targetCredentialID *uuid.UUID
		if space.Kind == domain.MemorySpaceCredentialPrivate {
			action = domain.PrivateMemoryEraseCredentialPrivate
			targetCredentialID = cloneUUIDPtr(space.OwnerCredentialID)
		}
		operation, err = queuePrivateMemorySpaceTx(ctx, tx, space, queuePrivateMemoryInput{
			Action: action, ActorClass: domain.PrivateMemoryActorControl, ReasonCode: strings.TrimSpace(reasonCode),
			TargetCredentialID: targetCredentialID, IdempotencyScopeHash: idempotencyScopeHash, RequestHash: requestHash,
		})
		created = err == nil
		return err
	})
	return operation, created, wrapPrivateMemoryError("request control erasure", err)
}

func (r *PrivateMemoryRepositoryImpl) DisableSSOCredential(ctx context.Context, input PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	if err := validatePrivateMemoryErasureInput(input); err != nil {
		return nil, false, err
	}
	if input.OwnerID == uuid.Nil || input.CredentialID == uuid.Nil {
		return nil, false, fmt.Errorf("%w: owner and credential IDs are required", ErrPrivateMemoryNotFound)
	}
	var operation *domain.PrivateMemoryErasureOperation
	created := false
	now := r.now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := lockPrivateMemoryIdempotencyScopeTx(ctx, tx, input.IdempotencyScopeHash); err != nil {
			return err
		}
		existing, err := existingPrivateMemoryOperationTx(ctx, tx, input.IdempotencyScopeHash, input.RequestHash)
		if err != nil || existing != nil {
			operation = existing
			return err
		}

		var binding string
		var memorySpaceID sql.NullString
		var actorIdentityID uuid.UUID
		err = tx.WithContext(ctx).Raw(`
			SELECT memory_binding, memory_space_id::text, actor_identity_id
			FROM credentials
			WHERE id = $1
			  AND team_id = $2
			  AND owner_identity_id = $3
			  AND kind = 'api_key'
			  AND status <> 'disabled'
			FOR UPDATE
		`, input.CredentialID, input.TeamID, input.OwnerID).Row().Scan(&binding, &memorySpaceID, &actorIdentityID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPrivateMemoryNotFound
		}
		if err != nil {
			return err
		}

		var space *domain.MemorySpace
		if binding == string(domain.CredentialBindingCredentialPrivate) {
			space, err = privateMemorySpaceForCredentialTx(ctx, tx, input.TeamID, input.CredentialID, false)
			if err != nil {
				return err
			}
			if !memorySpaceID.Valid || memorySpaceID.String != space.ID.String() {
				return ErrPrivateMemoryNotFound
			}
		}

		if err := tx.WithContext(ctx).Exec(`
			UPDATE credentials
			SET status = 'disabled', revoked_at = COALESCE(revoked_at, $1), updated_at = $1
			WHERE id = $2 AND team_id = $3 AND owner_identity_id = $4 AND status <> 'disabled'
		`, now, input.CredentialID, input.TeamID, input.OwnerID).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			UPDATE team_memberships
			SET status = 'revoked', updated_at = $1
			WHERE team_id = $2 AND actor_identity_id = $3
			  AND NOT EXISTS (
				SELECT 1
				FROM credentials AS other
				WHERE other.team_id = team_memberships.team_id
				  AND other.actor_identity_id = team_memberships.actor_identity_id
				  AND other.id <> $4
				  AND other.status = 'active'
			  )
		`, now, input.TeamID, actorIdentityID, input.CredentialID).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			UPDATE actor_identities AS actor
			SET active = false, updated_at = $1
			WHERE actor.id = $2
			  AND NOT EXISTS (
				SELECT 1 FROM team_memberships AS membership
				WHERE membership.actor_identity_id = actor.id AND membership.status = 'active'
			  )
		`, now, actorIdentityID).Error; err != nil {
			return err
		}
		if err := appendPrivateMemoryCredentialRevocationAuditTx(ctx, tx, input, memorySpaceID, now); err != nil {
			return err
		}

		credentialID := input.CredentialID
		if space != nil {
			operation, err = queuePrivateMemorySpaceTx(ctx, tx, space, queuePrivateMemoryInput{
				Action:               domain.PrivateMemoryRetireCredential,
				ActorClass:           domain.PrivateMemoryActorOwnerSSO,
				ReasonCode:           input.ReasonCode,
				TargetCredentialID:   &credentialID,
				RetireSpace:          true,
				QueueWhileHeld:       true,
				IdempotencyScopeHash: input.IdempotencyScopeHash,
				RequestHash:          input.RequestHash,
			})
			created = err == nil
			return err
		}

		operation = &domain.PrivateMemoryErasureOperation{
			ID:                 uuid.New(),
			TeamID:             input.TeamID,
			TargetCredentialID: &credentialID,
			Action:             domain.PrivateMemoryRetireCredential,
			ActorClass:         domain.PrivateMemoryActorOwnerSSO,
			ReasonCode:         input.ReasonCode,
			RetireSpace:        false,
			Status:             domain.PrivateMemoryErasureCompleted,
			DeletedCounts:      map[string]int64{},
			RequestedAt:        now,
			StartedAt:          &now,
			CompletedAt:        &now,
			UpdatedAt:          now,
		}
		if err := insertPrivateMemoryOperationTx(ctx, tx, operation, input.IdempotencyScopeHash, input.RequestHash); err != nil {
			return err
		}
		created = true
		return nil
	})
	return operation, created, wrapPrivateMemoryError("disable sso credential", err)
}

func appendPrivateMemoryCredentialRevocationAuditTx(ctx context.Context, tx *gorm.DB, input PrivateMemoryErasureRequest, memorySpaceID sql.NullString, now time.Time) error {
	if input.CredentialRevocationAudit == nil {
		return nil
	}
	// Keep the revocation event in DisableSSOCredential's transaction so a failed audit insert rolls back the irreversible credential change.
	beforePayload, err := json.Marshal(map[string]any{
		"id":                input.CredentialID.String(),
		"team_id":           input.TeamID.String(),
		"owner_identity_id": input.OwnerID.String(),
		"status":            "active",
		"revoked_at":        nil,
	})
	if err != nil {
		return fmt.Errorf("marshal credential revocation audit: %w", err)
	}

	var actorProfileID any
	if value := strings.TrimSpace(stringValue(input.CredentialRevocationAudit.ActorProfileID)); value != "" {
		actorProfileID = value
	}
	metadata := map[string]any{}
	if value := strings.TrimSpace(stringValue(input.CredentialRevocationAudit.ActorCredentialID)); value != "" {
		metadata["actor_credential_id"] = value
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal credential revocation audit metadata: %w", err)
	}
	var clientIP any
	if value := strings.TrimSpace(input.CredentialRevocationAudit.ClientIP); value != "" {
		clientIP = value
	}
	var auditMemorySpaceID any
	if memorySpaceID.Valid && strings.TrimSpace(memorySpaceID.String) != "" {
		auditMemorySpaceID = memorySpaceID.String
	}

	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO audit_log (
			id, team_id, timestamp, operation, entity_type, entity_id,
			before_payload, actor_profile_id, actor_role, client_ip,
			correlation_id, metadata, memory_space_id
		) VALUES (?, ?, ?, 'REVOKE', 'api_key', ?, ?, ?, ?, ?, ?, ?, ?)
	`, uuid.New(), input.TeamID, now, input.CredentialID.String(), beforePayload,
		actorProfileID, input.CredentialRevocationAudit.ActorRole, clientIP,
		input.CredentialRevocationAudit.CorrelationID, metadataJSON, auditMemorySpaceID).Error; err != nil {
		return fmt.Errorf("append credential revocation audit: %w", err)
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type queuePrivateMemoryInput struct {
	Action               domain.PrivateMemoryErasureAction
	ActorClass           domain.PrivateMemoryActorClass
	ReasonCode           string
	TargetCredentialID   *uuid.UUID
	RetireSpace          bool
	QueueWhileHeld       bool
	IdempotencyScopeHash string
	RequestHash          string
}

func queuePrivateMemorySpaceTx(ctx context.Context, tx *gorm.DB, space *domain.MemorySpace, input queuePrivateMemoryInput) (*domain.PrivateMemoryErasureOperation, error) {
	if space == nil || space.ID == uuid.Nil || space.TeamID == uuid.Nil || !isPrivateMemorySpaceKind(space.Kind) {
		return nil, ErrPrivateMemoryNotFound
	}
	if !input.QueueWhileHeld {
		if err := ensureNoPrivateMemoryLegalHoldTx(ctx, tx, space.ID); err != nil {
			return nil, err
		}
	}
	activeOperation, err := scanPrivateMemoryOperation(tx.WithContext(ctx).Raw(privateMemoryOperationSelect+`
		WHERE operation.space_id = $1 AND operation.status IN ('queued', 'processing')
		ORDER BY operation.requested_at, operation.id
		LIMIT 1
		FOR UPDATE
	`, space.ID).Row())
	if err == nil {
		if !input.RetireSpace {
			return nil, ErrPrivateMemoryOperationConflict
		}
		if input.TargetCredentialID == nil || activeOperation.TargetGeneration == nil || activeOperation.TargetCredentialID == nil {
			return nil, ErrPrivateMemoryOperationConflict
		}
		// Retirement supersedes an active erase without changing its target generation or worker fence.
		now := time.Now().UTC()
		result := tx.WithContext(ctx).Exec(`
			UPDATE private_memory_erasure_operations
			SET action = $1,
			    actor_class = $2,
			    reason_code = $3,
			    target_credential_id = $4,
			    retire_space = true,
			    updated_at = $5
			WHERE id = $6 AND status IN ('queued', 'processing')
		`, input.Action, input.ActorClass, input.ReasonCode, input.TargetCredentialID, now, activeOperation.ID)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, ErrPrivateMemoryOperationConflict
		}
		if err := insertPrivateMemoryIdempotencyKeyTx(ctx, tx, input.IdempotencyScopeHash, input.RequestHash, activeOperation.ID); err != nil {
			return nil, err
		}
		activeOperation.Action = input.Action
		activeOperation.ActorClass = input.ActorClass
		activeOperation.ReasonCode = input.ReasonCode
		activeOperation.TargetCredentialID = cloneUUIDPtr(input.TargetCredentialID)
		activeOperation.RetireSpace = true
		activeOperation.UpdatedAt = now
		return activeOperation, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	now := time.Now().UTC()
	action := input.Action
	targetCredentialID := cloneUUIDPtr(input.TargetCredentialID)
	targetGeneration := space.Generation
	retireSpace := input.RetireSpace
	switch space.LifecycleState {
	case domain.MemorySpaceActive:
		result := tx.WithContext(ctx).Exec(`
			UPDATE memory_spaces
			SET generation = generation + 1,
			    lifecycle_state = 'sealed',
			    sealed_at = $1,
			    retired_at = NULL,
			    updated_at = $1
			WHERE id = $2 AND team_id = $3 AND generation = $4 AND lifecycle_state = 'active'
		`, now, space.ID, space.TeamID, space.Generation)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, ErrPrivateMemoryOperationConflict
		}
	case domain.MemorySpaceSealed:
		failed, err := failedPrivateMemoryOperationForRecoveryTx(ctx, tx, space)
		if err != nil {
			return nil, err
		}
		if input.RetireSpace && !failed.RetireSpace {
			// A credential-retirement request is stronger than the failed
			// non-retiring erase it supersedes. Keep the failed generation as
			// the deletion target, but adopt the new retirement action and
			// credential target so revocation cannot be rolled back.
			action = input.Action
			targetCredentialID = cloneUUIDPtr(input.TargetCredentialID)
			retireSpace = true
		} else {
			action = failed.Action
			targetCredentialID = cloneUUIDPtr(failed.TargetCredentialID)
			retireSpace = failed.RetireSpace
		}
		targetGeneration = *failed.TargetGeneration
	default:
		return nil, ErrPrivateMemoryOperationConflict
	}
	spaceKind := space.Kind
	operation := &domain.PrivateMemoryErasureOperation{
		ID:                 uuid.New(),
		TeamID:             space.TeamID,
		SpaceID:            &space.ID,
		SpaceKind:          &spaceKind,
		TargetCredentialID: targetCredentialID,
		Action:             action,
		ActorClass:         input.ActorClass,
		ReasonCode:         input.ReasonCode,
		TargetGeneration:   &targetGeneration,
		RetireSpace:        retireSpace,
		Status:             domain.PrivateMemoryErasureQueued,
		DeletedCounts:      map[string]int64{},
		RequestedAt:        now,
		UpdatedAt:          now,
	}
	if err := insertPrivateMemoryOperationTx(ctx, tx, operation, input.IdempotencyScopeHash, input.RequestHash); err != nil {
		return nil, err
	}
	return operation, nil
}

func failedPrivateMemoryOperationForRecoveryTx(ctx context.Context, tx *gorm.DB, space *domain.MemorySpace) (*domain.PrivateMemoryErasureOperation, error) {
	if space == nil || space.Generation <= 1 {
		return nil, ErrPrivateMemoryOperationConflict
	}
	operation, err := scanPrivateMemoryOperation(tx.WithContext(ctx).Raw(privateMemoryOperationSelect+`
		WHERE team_id = $1
		  AND space_id = $2
		  AND space_kind = $3
		  AND target_generation = $4
		  AND status = 'failed'
		ORDER BY completed_at DESC NULLS LAST, requested_at DESC, id DESC
		LIMIT 1
		FOR UPDATE
	`, space.TeamID, space.ID, space.Kind, space.Generation-1).Row())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPrivateMemoryOperationConflict
	}
	if err != nil {
		return nil, err
	}
	if operation.TargetGeneration == nil {
		return nil, ErrPrivateMemoryOperationConflict
	}
	return operation, nil
}

func privateMemorySpaceForProfileTx(ctx context.Context, tx *gorm.DB, teamID, ownerID uuid.UUID) (*domain.MemorySpace, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT id, team_id, kind, owner_profile_id, owner_credential_id,
		       generation, lifecycle_state, private_content_at, sealed_at, retired_at, created_at, updated_at
		FROM memory_spaces
		WHERE team_id = $1 AND kind = 'profile_private' AND owner_profile_id = $2
		FOR UPDATE
	`, teamID, ownerID).Row()
	space, err := scanPrivateMemorySpace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPrivateMemoryNotFound
	}
	return space, err
}

func privateMemorySpaceForCredentialTx(ctx context.Context, tx *gorm.DB, teamID, credentialID uuid.UUID, requireActiveCredential bool) (*domain.MemorySpace, error) {
	statusPredicate := ""
	if requireActiveCredential {
		statusPredicate = "AND credential.status = 'active'"
	}
	row := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		SELECT space.id, space.team_id, space.kind, space.owner_profile_id, space.owner_credential_id,
		       space.generation, space.lifecycle_state, space.private_content_at, space.sealed_at, space.retired_at,
		       space.created_at, space.updated_at
		FROM credentials AS credential
		JOIN memory_spaces AS space
		  ON space.id = credential.memory_space_id
		 AND space.team_id = credential.team_id
		WHERE credential.id = $1
		  AND credential.team_id = $2
		  AND credential.kind = 'api_key'
		  AND credential.memory_binding = 'credential_private'
		  AND space.kind = 'credential_private'
		  AND space.owner_credential_id = credential.id
		  %s
		FOR UPDATE OF space
	`, statusPredicate), credentialID, teamID).Row()
	space, err := scanPrivateMemorySpace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPrivateMemoryNotFound
	}
	return space, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPrivateMemorySpace(row rowScanner) (*domain.MemorySpace, error) {
	space := &domain.MemorySpace{}
	err := row.Scan(
		&space.ID, &space.TeamID, &space.Kind, &space.OwnerProfileID, &space.OwnerCredentialID,
		&space.Generation, &space.LifecycleState, &space.PrivateContentAt, &space.SealedAt, &space.RetiredAt,
		&space.CreatedAt, &space.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return space, nil
}

const privateMemoryOperationSelect = `
	SELECT id, team_id, space_id::text, space_kind, target_credential_id::text,
	       action, actor_class, reason_code, target_generation, retire_space,
	       status, manifest_position, deleted_counts, attempt_count, fence,
	       worker_id, lease_until, next_attempt_at, last_error_code, requested_at, started_at,
	       completed_at, updated_at
	FROM private_memory_erasure_operations AS operation
`

func scanPrivateMemoryOperation(row rowScanner) (*domain.PrivateMemoryErasureOperation, error) {
	operation := &domain.PrivateMemoryErasureOperation{}
	var spaceID, spaceKind, credentialID sql.NullString
	var targetGeneration sql.NullInt64
	var leaseUntil, nextAttemptAt, startedAt, completedAt sql.NullTime
	var deletedCounts []byte
	err := row.Scan(
		&operation.ID, &operation.TeamID, &spaceID, &spaceKind, &credentialID,
		&operation.Action, &operation.ActorClass, &operation.ReasonCode, &targetGeneration, &operation.RetireSpace,
		&operation.Status, &operation.ManifestPosition, &deletedCounts, &operation.AttemptCount, &operation.Fence,
		&operation.WorkerID, &leaseUntil, &nextAttemptAt, &operation.LastErrorCode, &operation.RequestedAt, &startedAt,
		&completedAt, &operation.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if spaceID.Valid {
		id, err := uuid.Parse(spaceID.String)
		if err != nil {
			return nil, err
		}
		operation.SpaceID = &id
	}
	if spaceKind.Valid {
		kind := domain.MemorySpaceKind(spaceKind.String)
		operation.SpaceKind = &kind
	}
	if credentialID.Valid {
		id, err := uuid.Parse(credentialID.String)
		if err != nil {
			return nil, err
		}
		operation.TargetCredentialID = &id
	}
	if targetGeneration.Valid {
		value := targetGeneration.Int64
		operation.TargetGeneration = &value
	}
	if leaseUntil.Valid {
		value := leaseUntil.Time.UTC()
		operation.LeaseUntil = &value
	}
	if nextAttemptAt.Valid {
		value := nextAttemptAt.Time.UTC()
		operation.NextAttemptAt = &value
	}
	if startedAt.Valid {
		value := startedAt.Time.UTC()
		operation.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		operation.CompletedAt = &value
	}
	operation.DeletedCounts = make(map[string]int64)
	if len(deletedCounts) > 0 {
		if err := json.Unmarshal(deletedCounts, &operation.DeletedCounts); err != nil {
			return nil, err
		}
	}
	return operation, nil
}
