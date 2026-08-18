package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/postgrescompat"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	CommunityAlgorithmKind    = "louvain"
	CommunityAlgorithmVersion = "v2"
	CommunityProfileVersion   = "postgres-v2"
)

var (
	ErrCommunityRunAlreadyClaimed = errors.New("community run already claimed")
	ErrCommunityNotFound          = errors.New("community not found")
	ErrCommunitySourceStale       = errors.New("community source is stale")
)

type CommunityRepository interface {
	ClaimCommunityRun(ctx context.Context, input CommunityRunClaimInput) (*CommunityRun, error)
	RenewCommunityRunLease(ctx context.Context, input CommunityRunLeaseInput) error
	CompleteCommunityRun(ctx context.Context, input CommunityRunCompleteInput) error
	ListCommunityInputs(ctx context.Context, input CommunityInputListInput) ([]CommunityInput, error)
	PublishCommunitySnapshot(ctx context.Context, input CommunitySnapshotPublishInput) error
	RefreshCommunityStaleness(ctx context.Context, input CommunityStalenessInput) (int, error)
	ListCommunities(ctx context.Context, input CommunityListInput) ([]CommunityRecord, error)
	CountCurrentCommunities(ctx context.Context, teamID string) (int, error)
	GetCommunity(ctx context.Context, input CommunityGetInput) (*CommunityRecord, error)
	RecallCommunityDiscovery(ctx context.Context, input CommunityDiscoveryInput) ([]CommunityDiscoveryPath, error)
	LatestCommunityRun(ctx context.Context, teamID string) (*CommunityRun, error)
	ListCurrentCommunityLineage(ctx context.Context, teamID string) ([]CommunityLineageRecord, error)
	ListCommunitySemanticGroups(ctx context.Context, input CommunityCoverageInput) ([]string, error)
	RecallCommunities(ctx context.Context, input CommunityRecallInput) ([]CommunityRecallRecord, error)
	RecordCommunitySummaryAttempt(ctx context.Context, input CommunitySummaryAttemptInput) error
}

type CommunityRunClaimInput struct {
	TeamID            string
	WindowKey         string
	LeaseUntil        time.Time
	AlgorithmKind     string
	AlgorithmVersion  string
	ProfileVersion    string
	ConfigurationHash string
	SourceFingerprint string
	MaxNodes          int
	MaxEdges          int
}

type CommunityRunLeaseInput struct {
	TeamID     string
	RunID      string
	LeaseUntil time.Time
}

type CommunityRunCompleteInput struct {
	TeamID         string
	RunID          string
	Status         string
	NodeCount      int
	EdgeCount      int
	CommunityCount int
	Error          string
}

type CommunityRun struct {
	TeamID            string
	RunID             string
	WindowKey         string
	Status            string
	AlgorithmKind     string
	AlgorithmVersion  string
	ProfileVersion    string
	ConfigurationHash string
	SourceFingerprint string
	NodeCount         int
	EdgeCount         int
	CommunityCount    int
	MaxNodes          int
	MaxEdges          int
	Error             string
	StartedAt         time.Time
	CompletedAt       *time.Time
	Claimed           bool
}

type CommunityInputListInput struct {
	TeamID string
	Limit  int
}

type CommunityInput struct {
	RelationshipID   string
	OwnerProfileID   string
	Version          int
	SubjectEntityID  string
	SubjectName      string
	PredicateKey     string
	PredicateVersion int
	ObjectEntityID   string
	ObjectName       string
	ObjectValueID    string
	ObjectValueType  string
	ObjectValue      string
	SemanticGroupKey string
	EvidenceIDs      []string
	EvidenceQuotes   []domain.CommunitySummarySupportQuote
}

type CommunitySnapshotPublishInput struct {
	TeamID            string
	RunID             string
	AlgorithmKind     string
	AlgorithmVersion  string
	ProfileVersion    string
	ConfigurationHash string
	SourceFingerprint string
	SourceSnapshot    []map[string]any
	NodeCount         int
	EdgeCount         int
	Communities       []CommunityPublishRecord
}

type CommunityPublishRecord struct {
	CommunityID          string
	LogicalCommunityID   string
	Ordinal              int
	Summary              string
	SummaryVersion       string
	MemberCount          int
	SourceCount          int
	TopEntities          []string
	TopPredicates        []string
	SourceFingerprint    string
	SummaryInputHash     string
	SummaryProviderModel string
	SummaryPromptHash    string
	SummaryResponseHash  string
	Memberships          []CommunityMembershipInput
	Sources              []CommunitySourceInput
}

type CommunityMembershipInput struct {
	EntityID        string
	Rank            int
	MembershipScore float64
	SourceCount     int
}

type CommunitySourceInput struct {
	RelationshipID      string
	OwnerProfileID      string
	RelationshipVersion int
	SourceRank          int
	SemanticGroupKey    string
	SourceStateHash     string
}

type CommunityLineageRecord struct {
	CommunityID          string
	LogicalCommunityID   string
	GroupKeys            []string
	SummaryInputHash     string
	Summary              string
	SummaryVersion       string
	SummaryProviderModel string
	SummaryPromptHash    string
	SummaryResponseHash  string
}

type CommunityRecallInput struct {
	TeamID               string
	Query                string
	ValidAt              *time.Time
	KnownAt              *time.Time
	ReturnedEvidenceIDs  []string
	KnownEvidenceIDs     []string
	KnownRelationshipIDs []string
	SeedRelationshipIDs  []string
	ExpandFromEntityIDs  []string
	ExcludedGroupKeys    []string
	CoveredGroupKeys     []string
	Limit                int
	RelationshipLimit    int
}

type CommunityCoverageInput struct {
	TeamID          string
	EvidenceIDs     []string
	RelationshipIDs []string
}

type CommunityRecallTopEntity struct {
	EntityID string
	Name     string
}

type CommunityRecallRecord struct {
	CommunityID            string
	LogicalCommunityID     string
	Rank                   int
	Summary                string
	TopEntities            []CommunityRecallTopEntity
	TopPredicates          []string
	EntityCount            int
	RelationshipCount      int
	Relationships          []RecallRelationshipHit
	RelationshipsTruncated bool
}

type CommunitySummaryAttemptInput struct {
	TeamID                  string
	RunID                   string
	CommunityID             string
	Attempt                 int
	ProviderModel           string
	PromptHash              string
	ResponseHash            string
	InputHash               string
	AdmittedRelationshipIDs []string
	AdmittedEvidenceIDs     []string
	AdmittedSupportQuotes   []domain.CommunitySummarySupportQuote
	ResponseSummary         string
	Valid                   bool
	ErrorCode               string
}

type CommunityStalenessInput struct {
	TeamID string
	Limit  int
}

type CommunityListInput struct {
	TeamID string
	Status string
	Limit  int
}

type CommunityGetInput struct {
	TeamID      string
	CommunityID string
}

type CommunityDiscoveryInput struct {
	TeamID               string
	Query                string
	ValidAt              *time.Time
	KnownAt              *time.Time
	KnownRelationshipIDs []string
	ExpandFromEntityIDs  []string
	Limit                int
}

type CommunityDiscoveryPath struct {
	CommunityID   string
	Relationship  CommunityDiscoveryRelationship
	EvidenceIDs   []string
	SourceRank    int
	CommunityRank int
}

type CommunityDiscoveryRelationship struct {
	RelationshipID  string
	SubjectEntityID string
	SubjectName     string
	PredicateKey    string
	ObjectEntityID  string
	ObjectName      string
	Polarity        string
}

type CommunityRecord struct {
	TeamID             string
	CommunityID        string
	LogicalCommunityID string
	RunID              string
	Ordinal            int
	Status             string
	Summary            string
	SummaryVersion     string
	MemberCount        int
	SourceCount        int
	TopEntities        []string
	TopPredicates      []string
	SourceFingerprint  string
	StaleReason        string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	SupersededAt       *time.Time
	GroupKeys          []string
}

var _ CommunityRepository = (*SemanticRepositoryImpl)(nil)

func (r *SemanticRepositoryImpl) ClaimCommunityRun(ctx context.Context, input CommunityRunClaimInput) (*CommunityRun, error) {
	input = normalizeCommunityRunClaimInput(input)
	if err := validateCommunityRunClaimInput(input); err != nil {
		return nil, err
	}
	runID := uuid.NewString()
	var run *CommunityRun
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH attempted AS (
				INSERT INTO community_snapshot_runs (
					team_id, run_id, window_key, status, algorithm_kind, algorithm_version,
					profile_version, configuration_hash, source_fingerprint, max_nodes, max_edges, lease_until
				) VALUES (
					?::uuid, ?::uuid, ?, 'running', ?, ?, ?, ?, ?, ?, ?, ?::timestamptz
				)
				ON CONFLICT ON CONSTRAINT community_snapshot_runs_window_unique DO UPDATE
				SET status = 'running',
				    algorithm_kind = EXCLUDED.algorithm_kind,
				    algorithm_version = EXCLUDED.algorithm_version,
				    profile_version = EXCLUDED.profile_version,
				    configuration_hash = EXCLUDED.configuration_hash,
				    source_fingerprint = EXCLUDED.source_fingerprint,
				    max_nodes = EXCLUDED.max_nodes,
				    max_edges = EXCLUDED.max_edges,
				    lease_until = EXCLUDED.lease_until,
				    source_snapshot = '[]'::jsonb,
				    node_count = 0,
				    edge_count = 0,
				    community_count = 0,
				    error = '',
				    started_at = now(),
				    completed_at = NULL,
				    updated_at = now()
				WHERE (community_snapshot_runs.status = 'completed'
				       AND community_snapshot_runs.source_fingerprint <> EXCLUDED.source_fingerprint)
				   OR community_snapshot_runs.status IN ('failed', 'skipped', 'too_large', 'cancelled')
				   OR community_snapshot_runs.lease_until IS NULL
				   OR community_snapshot_runs.lease_until <= now()
				RETURNING team_id::text, run_id::text, window_key, status, algorithm_kind,
				          algorithm_version, profile_version, configuration_hash,
				          source_fingerprint, node_count, edge_count, community_count,
				          max_nodes, max_edges, error, started_at, completed_at,
				          true AS claimed
			)
			SELECT * FROM attempted
			UNION ALL
			SELECT team_id::text, run_id::text, window_key, status, algorithm_kind,
			       algorithm_version, profile_version, configuration_hash,
			       source_fingerprint, node_count, edge_count, community_count,
			       max_nodes, max_edges, error, started_at, completed_at,
			       false AS claimed
			FROM community_snapshot_runs
			WHERE team_id = ?::uuid
			  AND window_key = ?
			  AND NOT EXISTS (SELECT 1 FROM attempted)
		`, input.TeamID, runID, input.WindowKey, input.AlgorithmKind,
			input.AlgorithmVersion, input.ProfileVersion, input.ConfigurationHash,
			input.SourceFingerprint, input.MaxNodes, input.MaxEdges, input.LeaseUntil, input.TeamID, input.WindowKey).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return ErrCommunityRunAlreadyClaimed
		}
		scanned, err := scanCommunityRun(rows)
		if err != nil {
			return err
		}
		run = scanned
		if err := rows.Close(); err != nil {
			return err
		}
		if run.Claimed && input.SourceFingerprint != "" {
			if err := tx.WithContext(ctx).Exec(`
				UPDATE community_records
				SET status = 'stale', stale_reason = 'source_changed', updated_at = now()
				WHERE team_id = ?::uuid
				  AND status = 'current'
				  AND source_fingerprint <> ?
			`, input.TeamID, input.SourceFingerprint).Error; err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: claim run: %w", err)
	}
	return run, nil
}

func (r *SemanticRepositoryImpl) CompleteCommunityRun(ctx context.Context, input CommunityRunCompleteInput) error {
	input = normalizeCommunityRunCompleteInput(input)
	if err := validateCommunityRunCompleteInput(input); err != nil {
		return err
	}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE community_snapshot_runs
			SET status = ?,
			    node_count = ?,
			    edge_count = ?,
			    community_count = ?,
			    error = ?,
			    completed_at = now(),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND run_id = ?::uuid
			  AND status = 'running'
		`, input.Status, input.NodeCount, input.EdgeCount, input.CommunityCount,
			truncateCommunityError(input.Error), input.TeamID, input.RunID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrCommunityRunAlreadyClaimed
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("community: complete run: %w", err)
	}
	return nil
}

func (r *SemanticRepositoryImpl) RenewCommunityRunLease(ctx context.Context, input CommunityRunLeaseInput) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RunID = strings.TrimSpace(input.RunID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is required: %w", err)
	}
	if input.LeaseUntil.IsZero() {
		return errors.New("lease_until is required")
	}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE community_snapshot_runs
			SET lease_until = ?, updated_at = now()
			WHERE team_id = ?::uuid
			  AND run_id = ?::uuid
			  AND status = 'running'
		`, input.LeaseUntil, input.TeamID, input.RunID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrCommunityRunAlreadyClaimed
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("community: renew run lease: %w", err)
	}
	return nil
}

func (r *SemanticRepositoryImpl) ListCommunityInputs(ctx context.Context, input CommunityInputListInput) ([]CommunityInput, error) {
	input = normalizeCommunityInputListInput(input)
	if err := validateCommunityInputListInput(input); err != nil {
		return nil, err
	}
	var out []CommunityInput
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH canonical_names AS (
				SELECT DISTINCT ON (team_id, entity_id)
				       team_id, entity_id, display_name
				FROM entity_names
				WHERE team_id = ?::uuid
				  AND name_kind = 'canonical'
				  AND valid_to IS NULL
				ORDER BY team_id, entity_id, created_at DESC, entity_name_id DESC
			), latest_support AS (
				SELECT DISTINCT ON (team_id, support_id)
				       team_id, support_id, decision
				FROM relationship_support_decision_events
				WHERE team_id = ?::uuid
				ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
			), effective_support AS (
				SELECT support.team_id, support.relationship_id,
				       array_agg(DISTINCT support.fragment_id::text ORDER BY support.fragment_id::text) AS evidence_ids,
				       jsonb_agg(jsonb_build_object('evidence_id', support.fragment_id::text, 'quote', support.quote)
				                 ORDER BY support.fragment_id::text) AS evidence_quotes
				FROM relationship_evidence_supports AS support
				JOIN latest_support AS latest
				  ON latest.team_id = support.team_id
				 AND latest.support_id = support.support_id
				 AND latest.decision IN ('grant', 'reinstate')
				LEFT JOIN evidence_quarantines AS quarantine
				  ON quarantine.team_id = support.team_id
				 AND quarantine.fragment_id = support.fragment_id
				 AND quarantine.status = 'active'
				LEFT JOIN evidence_sources AS source
				  ON source.team_id = support.team_id
				 AND source.source_id = support.source_id
				LEFT JOIN evidence_lifecycle_events AS lifecycle
				  ON lifecycle.team_id = support.team_id
				 AND lifecycle.target_fragment_id = support.fragment_id
				WHERE support.team_id = ?::uuid
				  AND quarantine.quarantine_id IS NULL
				  AND lifecycle.lifecycle_event_id IS NULL
				  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
				GROUP BY support.team_id, support.relationship_id
			)
			SELECT relationship.relationship_id::text,
			       relationship.owner_profile_id::text,
			       relationship.version,
			       relationship.subject_entity_id::text,
			       COALESCE(subject_name.display_name, relationship.subject_entity_id::text) AS subject_name,
			       relationship.predicate_key,
			       relationship.predicate_version,
			       COALESCE(relationship.object_entity_id::text, ''),
			       COALESCE(object_name.display_name, value_record.display, value_record.canonical_value, relationship.object_value_id::text, '') AS object_name,
			       COALESCE(relationship.object_value_id::text, ''),
			       COALESCE(value_record.value_type, ''),
			       COALESCE(value_record.canonical_value, ''),
			       relationship.semantic_group_key,
			       effective_support.evidence_ids,
			       effective_support.evidence_quotes
			FROM relationship_records AS relationship
			JOIN entity_records AS subject
			  ON subject.team_id = relationship.team_id
			 AND subject.entity_id = relationship.subject_entity_id
			 AND subject.status = 'active'
			LEFT JOIN entity_records AS object
			  ON object.team_id = relationship.team_id
			 AND object.entity_id = relationship.object_entity_id
			 AND object.status = 'active'
			LEFT JOIN value_records AS value_record
			  ON value_record.team_id = relationship.team_id
			 AND value_record.value_id = relationship.object_value_id
			LEFT JOIN canonical_names AS subject_name
			  ON subject_name.team_id = relationship.team_id
			 AND subject_name.entity_id = relationship.subject_entity_id
			LEFT JOIN canonical_names AS object_name
				  ON object_name.team_id = relationship.team_id
				 AND object_name.entity_id = relationship.object_entity_id
			JOIN effective_support
			  ON effective_support.team_id = relationship.team_id
			 AND effective_support.relationship_id = relationship.relationship_id
			WHERE relationship.team_id = ?::uuid
			  AND relationship.identity_alias_of_relationship_id IS NULL
			  AND relationship.status = 'active'
			  AND (relationship.object_entity_id IS NULL OR object.entity_id IS NOT NULL)
			  AND (relationship.object_entity_id IS NOT NULL OR relationship.object_value_id IS NOT NULL)
			ORDER BY relationship.updated_at ASC, relationship.relationship_id ASC
			LIMIT ?
		`, input.TeamID, input.TeamID, input.TeamID, input.TeamID, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item := CommunityInput{}
			var evidenceIDs postgrescompat.StringArray
			var evidenceQuotesJSON []byte
			if err := rows.Scan(&item.RelationshipID, &item.OwnerProfileID, &item.Version,
				&item.SubjectEntityID, &item.SubjectName, &item.PredicateKey,
				&item.PredicateVersion, &item.ObjectEntityID,
				&item.ObjectName, &item.ObjectValueID, &item.ObjectValueType,
				&item.ObjectValue, &item.SemanticGroupKey, &evidenceIDs, &evidenceQuotesJSON); err != nil {
				return err
			}
			item.EvidenceIDs = []string(evidenceIDs)
			if len(evidenceQuotesJSON) > 0 {
				if err := json.Unmarshal(evidenceQuotesJSON, &item.EvidenceQuotes); err != nil {
					return err
				}
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: list inputs: %w", err)
	}
	return out, nil
}

func (r *SemanticRepositoryImpl) PublishCommunitySnapshot(ctx context.Context, input CommunitySnapshotPublishInput) error {
	input = normalizeCommunitySnapshotPublishInput(input)
	if err := validateCommunitySnapshotPublishInput(input); err != nil {
		return err
	}
	sourceSnapshot, err := marshalCommunitySnapshot(input.SourceSnapshot)
	if err != nil {
		return err
	}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		if err := ensureCommunitySourcesCurrent(ctx, tx, input.TeamID, flattenCommunitySources(input.Communities)); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			UPDATE community_records
			SET status = 'superseded',
			    superseded_at = now(),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND status = 'current'
		`, input.TeamID).Error; err != nil {
			return err
		}
		for _, community := range input.Communities {
			if err := insertCommunityRecord(ctx, tx, input, community); err != nil {
				return err
			}
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE community_snapshot_runs
			SET status = 'completed',
			    algorithm_kind = ?,
			    algorithm_version = ?,
			    profile_version = ?,
			    configuration_hash = ?,
			    source_fingerprint = ?,
			    source_snapshot = ?::jsonb,
			    node_count = ?,
			    edge_count = ?,
			    community_count = ?,
			    error = '',
			    completed_at = now(),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND run_id = ?::uuid
			  AND status = 'running'
		`, input.AlgorithmKind, input.AlgorithmVersion, input.ProfileVersion,
			input.ConfigurationHash, input.SourceFingerprint, string(sourceSnapshot),
			input.NodeCount, input.EdgeCount, len(input.Communities), input.TeamID, input.RunID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrCommunityRunAlreadyClaimed
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("community: publish snapshot: %w", err)
	}
	return nil
}

func (r *SemanticRepositoryImpl) RefreshCommunityStaleness(ctx context.Context, input CommunityStalenessInput) (int, error) {
	input = normalizeCommunityStalenessInput(input)
	if err := validateCommunityStalenessInput(input); err != nil {
		return 0, err
	}
	updated := 0
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH stale AS (
				SELECT record.community_id
				FROM community_records AS record
				WHERE record.team_id = ?::uuid
				  AND record.status = 'current'
				  AND (
				      NOT EXISTS (
				          SELECT 1
				          FROM community_sources AS source
				          WHERE source.team_id = record.team_id
				            AND source.community_id = record.community_id
				      )
				      OR EXISTS (
				          SELECT 1
				          FROM community_sources AS source
				          LEFT JOIN relationship_records AS relationship
				            ON relationship.team_id = source.team_id
				           AND relationship.relationship_id = source.relationship_id
				          WHERE source.team_id = record.team_id
				            AND source.community_id = record.community_id
				            AND (
					                relationship.relationship_id IS NULL
														OR relationship.version <> source.relationship_version
														OR relationship.status <> 'active'
														OR relationship.support_count = 0
								OR (relationship.object_entity_id IS NULL AND relationship.object_value_id IS NULL)
								OR NOT EXISTS (
									SELECT 1
									FROM relationship_evidence_supports AS support
									JOIN relationship_support_decision_events AS decision
									  ON decision.team_id = support.team_id
									 AND decision.support_id = support.support_id
									 AND decision.decision IN ('grant', 'reinstate')
									 AND decision.created_at = (
										SELECT max(latest.created_at)
										FROM relationship_support_decision_events AS latest
										WHERE latest.team_id = decision.team_id
										  AND latest.support_id = decision.support_id
									 )
									LEFT JOIN evidence_quarantines AS quarantine
									  ON quarantine.team_id = support.team_id
									 AND quarantine.fragment_id = support.fragment_id
									 AND quarantine.status = 'active'
															LEFT JOIN evidence_sources AS source
															  ON source.team_id = support.team_id
															 AND source.source_id = support.source_id
															LEFT JOIN evidence_lifecycle_events AS lifecycle
															  ON lifecycle.team_id = support.team_id
															 AND lifecycle.target_fragment_id = support.fragment_id
															WHERE support.team_id = relationship.team_id
															  AND support.relationship_id = relationship.relationship_id
															  AND quarantine.quarantine_id IS NULL
															  AND lifecycle.lifecycle_event_id IS NULL
									  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
								)
				            )
				      )
				  )
				ORDER BY record.updated_at ASC, record.community_id ASC
				LIMIT ?
			),
			updated AS (
				UPDATE community_records AS record
				SET status = 'stale',
				    stale_reason = 'source_changed',
				    updated_at = now()
				FROM stale
				WHERE record.team_id = ?::uuid
				  AND record.community_id = stale.community_id
				RETURNING 1
			)
			SELECT count(*)::int FROM updated
		`, input.TeamID, input.Limit, input.TeamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(&updated); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err != nil {
		return 0, fmt.Errorf("community: refresh staleness: %w", err)
	}
	return updated, nil
}

func (r *SemanticRepositoryImpl) ListCommunities(ctx context.Context, input CommunityListInput) ([]CommunityRecord, error) {
	input = normalizeCommunityListInput(input)
	if err := validateCommunityListInput(input); err != nil {
		return nil, err
	}
	var records []CommunityRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, community_id::text, COALESCE(logical_community_id, community_id)::text, run_id::text, ordinal, status,
			       summary, summary_version, member_count, source_count,
			       top_entities, top_predicates, source_fingerprint, stale_reason,
			       created_at, updated_at, superseded_at
			FROM community_records
			WHERE team_id = ?::uuid
			  AND status = ?
			ORDER BY member_count DESC, community_id ASC
			LIMIT ?
		`, input.TeamID, input.Status, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		var errScan error
		records, errScan = scanCommunityRecords(rows)
		return errScan
	})
	if err != nil {
		return nil, fmt.Errorf("community: list communities: %w", err)
	}
	return records, nil
}

func (r *SemanticRepositoryImpl) CountCurrentCommunities(ctx context.Context, teamID string) (int, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return 0, fmt.Errorf("team_id is required: %w", err)
	}
	var count int
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT count(*)::int
			FROM community_records
			WHERE team_id = ?::uuid
			  AND status = 'current'
		`, teamID).Scan(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("community: count current communities: %w", err)
	}
	return count, nil
}

func (r *SemanticRepositoryImpl) GetCommunity(ctx context.Context, input CommunityGetInput) (*CommunityRecord, error) {
	input = normalizeCommunityGetInput(input)
	if err := validateCommunityGetInput(input); err != nil {
		return nil, err
	}
	var record *CommunityRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, community_id::text, COALESCE(logical_community_id, community_id)::text, run_id::text, ordinal, status,
			       summary, summary_version, member_count, source_count,
			       top_entities, top_predicates, source_fingerprint, stale_reason,
			       created_at, updated_at, superseded_at
			FROM community_records
			WHERE team_id = ?::uuid
			  AND community_id = ?::uuid
		`, input.TeamID, input.CommunityID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		records, err := scanCommunityRecords(rows)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return ErrCommunityNotFound
		}
		record = &records[0]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("community: get community: %w", err)
	}
	return record, nil
}

func (r *SemanticRepositoryImpl) LatestCommunityRun(ctx context.Context, teamID string) (*CommunityRun, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	var run *CommunityRun
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, run_id::text, window_key, status, algorithm_kind,
			       algorithm_version, profile_version, configuration_hash,
			       source_fingerprint, node_count, edge_count, community_count,
			       max_nodes, max_edges, error, started_at, completed_at,
			       false AS claimed
			FROM community_snapshot_runs
			WHERE team_id = ?::uuid
			ORDER BY started_at DESC, run_id DESC
			LIMIT 1
		`, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return nil
		}
		scanned, err := scanCommunityRun(rows)
		if err != nil {
			return err
		}
		run = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: latest run: %w", err)
	}
	return run, nil
}

func (r *SemanticRepositoryImpl) ListCurrentCommunityLineage(ctx context.Context, teamID string) ([]CommunityLineageRecord, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	lineage := []CommunityLineageRecord{}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT record.community_id::text,
			       COALESCE(record.logical_community_id, record.community_id)::text,
			       COALESCE(array_agg(DISTINCT source.semantic_group_key ORDER BY source.semantic_group_key)
			                FILTER (WHERE source.semantic_group_key <> ''), ARRAY[]::text[]),
			       record.summary_input_hash, record.summary, record.summary_version,
			       record.summary_provider_model, record.summary_prompt_hash, record.summary_response_hash
			FROM community_records AS record
			LEFT JOIN community_sources AS source
			  ON source.team_id = record.team_id
			 AND source.community_id = record.community_id
			WHERE record.team_id = ?::uuid
			  AND record.status = 'current'
			GROUP BY record.community_id, record.logical_community_id, record.updated_at,
			         record.summary_input_hash, record.summary, record.summary_version,
			         record.summary_provider_model, record.summary_prompt_hash, record.summary_response_hash
			ORDER BY record.updated_at DESC, record.community_id ASC
		`, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var record CommunityLineageRecord
			var groups postgrescompat.StringArray
			if err := rows.Scan(&record.CommunityID, &record.LogicalCommunityID, &groups,
				&record.SummaryInputHash, &record.Summary, &record.SummaryVersion,
				&record.SummaryProviderModel, &record.SummaryPromptHash, &record.SummaryResponseHash); err != nil {
				return err
			}
			record.GroupKeys = []string(groups)
			lineage = append(lineage, record)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: list lineage: %w", err)
	}
	return lineage, nil
}

func insertCommunityRecord(ctx context.Context, tx *gorm.DB, input CommunitySnapshotPublishInput, community CommunityPublishRecord) error {
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO community_records (
			team_id, community_id, run_id, ordinal, status, summary, summary_version,
			logical_community_id, member_count, source_count, top_entities, top_predicates, source_fingerprint,
			summary_input_hash, summary_provider_model, summary_prompt_hash, summary_response_hash, summary_generated_at
		) VALUES (
			?::uuid, ?::uuid, ?::uuid, ?, 'current', ?, ?, ?::uuid, ?, ?, ?::text[], ?::text[], ?, ?, ?, ?, ?, now()
		)
	`, input.TeamID, community.CommunityID, input.RunID, community.Ordinal,
		community.Summary, community.SummaryVersion, normalizeCommunityLogicalID(community), community.MemberCount,
		community.SourceCount, postgrescompat.Array(community.TopEntities),
		postgrescompat.Array(community.TopPredicates), community.SourceFingerprint, community.SummaryInputHash,
		community.SummaryProviderModel, community.SummaryPromptHash, community.SummaryResponseHash).Error; err != nil {
		return err
	}
	for _, membership := range community.Memberships {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO community_memberships (
				team_id, community_id, entity_id, rank, membership_score, source_count
			) VALUES (
				?::uuid, ?::uuid, ?::uuid, ?, ?, ?
			)
		`, input.TeamID, community.CommunityID, membership.EntityID,
			membership.Rank, membership.MembershipScore, membership.SourceCount).Error; err != nil {
			return err
		}
	}
	for _, source := range community.Sources {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO community_sources (
				team_id, community_id, relationship_id, owner_profile_id,
				relationship_version, source_rank, semantic_group_key, source_state_hash
			) VALUES (
				?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?
			)
		`, input.TeamID, community.CommunityID, source.RelationshipID,
			source.OwnerProfileID, source.RelationshipVersion, source.SourceRank,
			source.SemanticGroupKey, source.SourceStateHash).Error; err != nil {
			return err
		}
	}
	return nil
}
