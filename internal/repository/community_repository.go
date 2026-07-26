package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const (
	CommunityAlgorithmKind    = "connected_components"
	CommunityAlgorithmVersion = "v1"
	CommunityProfileVersion   = "postgres"
)

var (
	ErrCommunityRunAlreadyClaimed = errors.New("community run already claimed")
	ErrCommunityNotFound          = errors.New("community not found")
	ErrCommunitySourceStale       = errors.New("community source is stale")
)

type CommunityRepository interface {
	ClaimCommunityRun(ctx context.Context, input CommunityRunClaimInput) (*CommunityRun, error)
	CompleteCommunityRun(ctx context.Context, input CommunityRunCompleteInput) error
	ListCommunityInputs(ctx context.Context, input CommunityInputListInput) ([]CommunityInput, error)
	PublishCommunitySnapshot(ctx context.Context, input CommunitySnapshotPublishInput) error
	RefreshCommunityStaleness(ctx context.Context, input CommunityStalenessInput) (int, error)
	ListCommunities(ctx context.Context, input CommunityListInput) ([]CommunityRecord, error)
	GetCommunity(ctx context.Context, input CommunityGetInput) (*CommunityRecord, error)
	RecallCommunityDiscovery(ctx context.Context, input CommunityDiscoveryInput) ([]CommunityDiscoveryPath, error)
	LatestCommunityRun(ctx context.Context, teamID string) (*CommunityRun, error)
}

type CommunityRunClaimInput struct {
	TeamID            string
	WindowKey         string
	LeaseUntil        time.Time
	AlgorithmKind     string
	AlgorithmVersion  string
	ProfileVersion    string
	ConfigurationHash string
	MaxNodes          int
	MaxEdges          int
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
	Tier             string
	SubjectEntityID  string
	SubjectName      string
	PredicateKey     string
	PredicateVersion int
	ObjectEntityID   string
	ObjectName       string
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
	CommunityID       string
	Ordinal           int
	Summary           string
	SummaryVersion    string
	MemberCount       int
	SourceCount       int
	TopEntities       []string
	TopPredicates     []string
	SourceFingerprint string
	Memberships       []CommunityMembershipInput
	Sources           []CommunitySourceInput
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
	TeamID            string
	CommunityID       string
	RunID             string
	Ordinal           int
	Status            string
	Summary           string
	SummaryVersion    string
	MemberCount       int
	SourceCount       int
	TopEntities       []string
	TopPredicates     []string
	SourceFingerprint string
	StaleReason       string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	SupersededAt      *time.Time
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
					profile_version, configuration_hash, max_nodes, max_edges, lease_until
				) VALUES (
					?::uuid, ?::uuid, ?, 'running', ?, ?, ?, ?, ?, ?, ?::timestamptz
				)
				ON CONFLICT ON CONSTRAINT community_snapshot_runs_window_unique DO UPDATE
				SET run_id = EXCLUDED.run_id,
				    status = 'running',
				    algorithm_kind = EXCLUDED.algorithm_kind,
				    algorithm_version = EXCLUDED.algorithm_version,
				    profile_version = EXCLUDED.profile_version,
				    configuration_hash = EXCLUDED.configuration_hash,
				    max_nodes = EXCLUDED.max_nodes,
				    max_edges = EXCLUDED.max_edges,
				    lease_until = EXCLUDED.lease_until,
				    source_fingerprint = '',
				    source_snapshot = '[]'::jsonb,
				    node_count = 0,
				    edge_count = 0,
				    community_count = 0,
				    error = '',
				    started_at = now(),
				    completed_at = NULL,
				    updated_at = now()
				WHERE community_snapshot_runs.status IN ('failed', 'skipped', 'too_large', 'cancelled')
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
			input.MaxNodes, input.MaxEdges, input.LeaseUntil, input.TeamID, input.WindowKey).Rows()
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
			)
			SELECT relationship.relationship_id::text,
			       relationship.owner_profile_id::text,
			       relationship.version,
			       relationship.tier,
			       relationship.subject_entity_id::text,
			       COALESCE(subject_name.display_name, relationship.subject_entity_id::text) AS subject_name,
			       relationship.predicate_key,
			       relationship.predicate_version,
			       relationship.object_entity_id::text,
			       COALESCE(object_name.display_name, relationship.object_entity_id::text) AS object_name
			FROM relationship_records AS relationship
			JOIN entity_records AS subject
			  ON subject.team_id = relationship.team_id
			 AND subject.entity_id = relationship.subject_entity_id
			 AND subject.status = 'active'
			JOIN entity_records AS object
			  ON object.team_id = relationship.team_id
			 AND object.entity_id = relationship.object_entity_id
			 AND object.status = 'active'
			LEFT JOIN canonical_names AS subject_name
			  ON subject_name.team_id = relationship.team_id
			 AND subject_name.entity_id = relationship.subject_entity_id
			LEFT JOIN canonical_names AS object_name
			  ON object_name.team_id = relationship.team_id
			 AND object_name.entity_id = relationship.object_entity_id
			WHERE relationship.team_id = ?::uuid
			  AND relationship.status = 'active'
			  AND relationship.tier IN ('validated_claim', 'fact')
			  AND relationship.object_entity_id IS NOT NULL
			ORDER BY relationship.updated_at ASC, relationship.relationship_id ASC
			LIMIT ?
		`, input.TeamID, input.TeamID, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item := CommunityInput{}
			if err := rows.Scan(&item.RelationshipID, &item.OwnerProfileID, &item.Version,
				&item.Tier, &item.SubjectEntityID, &item.SubjectName,
				&item.PredicateKey, &item.PredicateVersion, &item.ObjectEntityID,
				&item.ObjectName); err != nil {
				return err
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
				                OR relationship.tier NOT IN ('validated_claim', 'fact')
				                OR relationship.object_entity_id IS NULL
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
			SELECT team_id::text, community_id::text, run_id::text, ordinal, status,
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

func (r *SemanticRepositoryImpl) GetCommunity(ctx context.Context, input CommunityGetInput) (*CommunityRecord, error) {
	input = normalizeCommunityGetInput(input)
	if err := validateCommunityGetInput(input); err != nil {
		return nil, err
	}
	var record *CommunityRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, community_id::text, run_id::text, ordinal, status,
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

func (r *SemanticRepositoryImpl) RecallCommunityDiscovery(ctx context.Context, input CommunityDiscoveryInput) ([]CommunityDiscoveryPath, error) {
	input = normalizeCommunityDiscoveryInput(input)
	if err := validateCommunityDiscoveryInput(input); err != nil {
		return nil, err
	}
	if input.Query == "" && len(input.ExpandFromEntityIDs) == 0 {
		return []CommunityDiscoveryPath{}, nil
	}
	paths := []CommunityDiscoveryPath{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH matched_communities AS (
				SELECT record.team_id, record.community_id,
				       row_number() OVER (ORDER BY record.member_count DESC, record.community_id ASC)::int AS community_rank
				FROM community_records AS record
				WHERE record.team_id = ?::uuid
				  AND record.status = 'current'
				  AND (
				      (
				          ? <> ''
				          AND to_tsvector(
				              'simple',
				              concat_ws(' ', record.summary, array_to_string(record.top_entities, ' '), array_to_string(record.top_predicates, ' '))
				          ) @@ plainto_tsquery('simple', ?)
				      )
				      OR (
				          cardinality(?::uuid[]) > 0
				          AND EXISTS (
				              SELECT 1
				              FROM community_memberships AS membership
				              WHERE membership.team_id = record.team_id
				                AND membership.community_id = record.community_id
				                AND membership.entity_id = ANY(?::uuid[])
				          )
				      )
				  )
				ORDER BY record.member_count DESC, record.community_id ASC
				LIMIT ?
			),
			latest_support_decision AS (
				SELECT DISTINCT ON (team_id, support_id)
				       team_id, support_id, decision
				FROM relationship_support_decision_events
				WHERE team_id = ?::uuid
				ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
			),
			canonical_names AS (
				SELECT DISTINCT ON (team_id, entity_id)
				       team_id, entity_id, display_name
				FROM entity_names
				WHERE team_id = ?::uuid
				  AND name_kind = 'canonical'
				  AND valid_to IS NULL
				ORDER BY team_id, entity_id, created_at DESC, entity_name_id DESC
			)
			SELECT community.community_id::text,
			       community.community_rank,
			       community_source.source_rank,
			       relationship.relationship_id::text,
			       relationship.subject_entity_id::text,
			       COALESCE(subject_name.display_name, relationship.subject_entity_id::text) AS subject_name,
			       relationship.predicate_key,
			       relationship.object_entity_id::text,
			       COALESCE(object_name.display_name, relationship.object_entity_id::text) AS object_name,
			       relationship.polarity,
			       array_agg(DISTINCT support.fragment_id::text ORDER BY support.fragment_id::text) AS evidence_ids
			FROM matched_communities AS community
			JOIN community_sources AS community_source
			  ON community_source.team_id = community.team_id
			 AND community_source.community_id = community.community_id
			JOIN relationship_records AS relationship
			  ON relationship.team_id = community_source.team_id
			 AND relationship.relationship_id = community_source.relationship_id
			 AND relationship.version = community_source.relationship_version
			 AND relationship.status = 'active'
			 AND relationship.tier IN ('validated_claim', 'fact')
			 AND relationship.object_entity_id IS NOT NULL
			LEFT JOIN canonical_names AS subject_name
			  ON subject_name.team_id = relationship.team_id
			 AND subject_name.entity_id = relationship.subject_entity_id
			LEFT JOIN canonical_names AS object_name
			  ON object_name.team_id = relationship.team_id
			 AND object_name.entity_id = relationship.object_entity_id
			JOIN relationship_evidence_supports AS support
			  ON support.team_id = relationship.team_id
			 AND support.relationship_id = relationship.relationship_id
			JOIN latest_support_decision AS latest
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
			WHERE community.team_id = ?::uuid
			  AND quarantine.quarantine_id IS NULL
			  AND (
			      support.source_id IS NULL
			      OR source.current_revision_id = support.source_revision_id
			  )
			  AND (
			      ?::timestamptz IS NULL
			      OR ((relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
			          AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
			  )
			  AND (
			      ?::timestamptz IS NULL
			      OR (relationship.created_at <= ?::timestamptz
			          AND support.created_at <= ?::timestamptz
			          AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
			  )
			  AND (
			      cardinality(?::uuid[]) = 0
			      OR relationship.relationship_id <> ALL(?::uuid[])
			  )
			GROUP BY community.community_id, community.community_rank, community_source.source_rank,
			         relationship.relationship_id, relationship.subject_entity_id, subject_name.display_name,
			         relationship.predicate_key, relationship.object_entity_id, object_name.display_name,
			         relationship.polarity
			ORDER BY community.community_rank ASC, community_source.source_rank ASC, relationship.relationship_id ASC
			LIMIT ?
		`, input.TeamID, input.Query, input.Query,
			pq.Array(input.ExpandFromEntityIDs), pq.Array(input.ExpandFromEntityIDs),
			input.Limit, input.TeamID, input.TeamID, input.TeamID,
			input.ValidAt, input.ValidAt, input.ValidAt,
			input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt,
			pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs),
			input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			path := CommunityDiscoveryPath{}
			var evidenceIDs pq.StringArray
			if err := rows.Scan(
				&path.CommunityID,
				&path.CommunityRank,
				&path.SourceRank,
				&path.Relationship.RelationshipID,
				&path.Relationship.SubjectEntityID,
				&path.Relationship.SubjectName,
				&path.Relationship.PredicateKey,
				&path.Relationship.ObjectEntityID,
				&path.Relationship.ObjectName,
				&path.Relationship.Polarity,
				&evidenceIDs,
			); err != nil {
				return err
			}
			path.EvidenceIDs = []string(evidenceIDs)
			paths = append(paths, path)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: recall discovery: %w", err)
	}
	return paths, nil
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

func insertCommunityRecord(ctx context.Context, tx *gorm.DB, input CommunitySnapshotPublishInput, community CommunityPublishRecord) error {
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO community_records (
			team_id, community_id, run_id, ordinal, status, summary, summary_version,
			member_count, source_count, top_entities, top_predicates, source_fingerprint
		) VALUES (
			?::uuid, ?::uuid, ?::uuid, ?, 'current', ?, ?, ?, ?, ?::text[], ?::text[], ?
		)
	`, input.TeamID, community.CommunityID, input.RunID, community.Ordinal,
		community.Summary, community.SummaryVersion, community.MemberCount,
		community.SourceCount, pq.Array(community.TopEntities),
		pq.Array(community.TopPredicates), community.SourceFingerprint).Error; err != nil {
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
				relationship_version, source_rank
			) VALUES (
				?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?
			)
		`, input.TeamID, community.CommunityID, source.RelationshipID,
			source.OwnerProfileID, source.RelationshipVersion, source.SourceRank).Error; err != nil {
			return err
		}
	}
	return nil
}

func ensureCommunitySourcesCurrent(ctx context.Context, tx *gorm.DB, teamID string, sources []CommunitySourceInput) error {
	if len(sources) == 0 {
		return ErrCommunitySourceStale
	}
	relationshipIDs := make([]string, 0, len(sources))
	versions := make([]int, 0, len(sources))
	for _, source := range sources {
		relationshipIDs = append(relationshipIDs, source.RelationshipID)
		versions = append(versions, source.RelationshipVersion)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH expected AS (
			SELECT *
			FROM unnest(?::uuid[], ?::int[]) AS e(relationship_id, relationship_version)
		)
		SELECT expected.relationship_id::text
		FROM expected
		LEFT JOIN relationship_records AS relationship
		  ON relationship.team_id = ?::uuid
		 AND relationship.relationship_id = expected.relationship_id
		WHERE relationship.relationship_id IS NULL
		   OR relationship.version <> expected.relationship_version
		   OR relationship.status <> 'active'
		   OR relationship.tier NOT IN ('validated_claim', 'fact')
		   OR relationship.object_entity_id IS NULL
		LIMIT 1
	`, pq.Array(relationshipIDs), pq.Array(versions), teamID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return ErrCommunitySourceStale
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func flattenCommunitySources(communities []CommunityPublishRecord) []CommunitySourceInput {
	seen := map[string]struct{}{}
	out := make([]CommunitySourceInput, 0)
	for _, community := range communities {
		for _, source := range community.Sources {
			if _, ok := seen[source.RelationshipID]; ok {
				continue
			}
			seen[source.RelationshipID] = struct{}{}
			out = append(out, source)
		}
	}
	return out
}
