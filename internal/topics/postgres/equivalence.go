package postgres

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) ClaimMaintenanceEquivalence(ctx context.Context, turn topic.MaintenanceTurn, windowID string, now time.Time) (*topic.MaintenanceClaim, error) {
	if err := requireAutomatic(ctx); err != nil {
		return nil, err
	}
	claim := &topic.MaintenanceClaim{ID: uuid.NewString(), Kind: "equivalence", WindowID: windowID,
		TeamID: turn.TeamID, SpaceID: turn.SpaceID, Generation: turn.Generation,
		LeaseToken: turn.LeaseToken, LeaseUntil: turn.LeaseUntil, Revisions: map[string]int64{}, Fingerprints: map[string]string{}}
	err := s.withScope(ctx, turn.TeamID, false, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != turn.SpaceID || fence.Generation != turn.Generation {
			return topic.ErrLeaseLost
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		busy, err := maintenanceBusySources(tx, fence)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(busy))
		for key := range busy {
			keys = append(keys, key)
		}
		rows, err := tx.Raw(`SELECT source_kind,source_id,source_version,fingerprint FROM topic_maintenance_sources AS source
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND eligible
			AND NOT (source_kind || ':' || source_id=ANY(?::text[])) AND
			((?<>'' AND equivalence_status='failed' AND equivalence_last_run_id=NULLIF(?,'')::uuid) OR
			 (?='' AND equivalence_status='pending')) ORDER BY source_kind,source_id LIMIT ?`,
			fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(keys), turn.RetryRunID, turn.RetryRunID, turn.RetryRunID, topic.MaxOrganizationSources).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var source topic.SourceHandle
			var fingerprint string
			if err := rows.Scan(&source.Kind, &source.ID, &source.Version, &fingerprint); err != nil {
				return err
			}
			claim.Sources = append(claim.Sources, source)
			claim.Fingerprints[topic.SourceKey(source)] = fingerprint
		}
		return rows.Err()
	})
	if err != nil || len(claim.Sources) == 0 {
		return nil, err
	}
	return s.admitMaintenanceBatch(ctx, turn, claim, nil, now)
}

func (s *Store) ReadEquivalenceNeighborhoods(ctx context.Context, claim topic.MaintenanceClaim) ([]topic.EquivalenceNeighborhood, error) {
	if err := requireAutomatic(ctx); err != nil {
		return nil, err
	}
	if _, err := topic.PrepareOrganizationInput(topic.OrganizationInput{OperationKey: "equivalence", Sources: claim.Sources}); err != nil {
		return nil, err
	}
	var result []topic.EquivalenceNeighborhood
	err := s.withScope(ctx, claim.TeamID, true, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != claim.SpaceID || fence.Generation != claim.Generation {
			return topic.ErrLeaseLost
		}
		seeds, err := organizationSources(tx, fence, claim.Sources)
		if err != nil {
			return err
		}
		sources := make(map[topic.SourceHandle]topic.SourceSnapshot, len(seeds))
		for _, seed := range seeds {
			sources[seed.SourceHandle] = seed
		}
		for _, seed := range seeds {
			if !seed.Eligible {
				if claim.Kind == "equivalence" {
					return topic.ErrSourceStale
				}
				continue
			}
			if claim.Kind == "equivalence" {
				fingerprint, err := topic.SourceFingerprint(seed)
				if err != nil {
					return err
				}
				if fingerprint != claim.Fingerprints[topic.SourceKey(seed.SourceHandle)] {
					return topic.ErrSourceStale
				}
			}
			var related []topic.SourceHandle
			if claim.Kind == "equivalence" {
				related, err = maintenanceVectorRelatedSources(tx, fence, seed)
			} else {
				related, err = maintenanceRelatedSources(tx, fence, seed)
			}
			if claim.Kind == "equivalence" && errors.Is(err, topic.ErrEvidenceEmbeddingUnavailable) {
				result = append(result, topic.EquivalenceNeighborhood{Seed: seed.SourceHandle, Failure: err})
				continue
			}
			if err != nil {
				return err
			}
			if len(related) == 0 && claim.Kind != "equivalence" {
				continue
			}
			var missing []topic.SourceHandle
			for _, handle := range related {
				if _, ok := sources[handle]; !ok {
					missing = append(missing, handle)
				}
			}
			if len(missing) > 0 {
				peers, err := organizationSources(tx, fence, missing)
				if err != nil {
					return err
				}
				for _, peer := range peers {
					sources[peer.SourceHandle] = peer
				}
			}
			selected := []topic.SourceHandle{seed.SourceHandle}
			for _, handle := range related {
				if topic.CompatibleMeaningContext(seed, sources[handle]) {
					selected = append(selected, handle)
				}
			}
			if claim.Kind != "equivalence" && slices.ContainsFunc(result, func(previous topic.EquivalenceNeighborhood) bool {
				for _, handle := range selected {
					if !slices.Contains(previous.Sources, handle) {
						return false
					}
				}
				return true
			}) {
				continue
			}
			for len(selected) > 1 {
				members, exceeded, err := s.maintenanceGroupClosure(tx, fence, selected)
				if err != nil {
					return err
				}
				if exceeded {
					selected = selected[:len(selected)-1]
					continue
				}
				duplicate := claim.Kind != "equivalence" && slices.ContainsFunc(result, func(previous topic.EquivalenceNeighborhood) bool {
					return slices.Equal(previous.Sources, members)
				})
				if !duplicate {
					result = append(result, topic.EquivalenceNeighborhood{Seed: seed.SourceHandle, Sources: members})
				}
				break
			}
			if claim.Kind == "equivalence" && len(selected) == 1 {
				members, exceeded, err := s.maintenanceGroupClosure(tx, fence, selected)
				if err != nil {
					return err
				}
				if exceeded {
					result = append(result, topic.EquivalenceNeighborhood{Seed: seed.SourceHandle, Failure: topic.ErrContextBound})
				} else {
					result = append(result, topic.EquivalenceNeighborhood{Seed: seed.SourceHandle, Sources: members})
				}
			}
		}
		return nil
	})
	return result, err
}

func maintenanceVectorRelatedSources(tx *gorm.DB, fence scope, seed topic.SourceSnapshot) ([]topic.SourceHandle, error) {
	var vector string
	var contractID string
	err := tx.Raw(`SELECT document.embedding::text,document.embedding_contract_id::text
		FROM search_documents AS document JOIN embedding_contracts AS contract
		ON contract.embedding_contract_id=document.embedding_contract_id AND contract.dimensions=document.embedding_dimensions
		JOIN search_index_generations AS generation ON generation.embedding_contract_id=contract.embedding_contract_id
		WHERE document.team_id=?::uuid AND document.space_id=?::uuid AND document.space_generation=?
		AND document.source_kind='evidence' AND document.source_id=?::uuid AND document.source_version=?
		AND document.search_state='current' AND document.embedding IS NOT NULL AND document.document_text=?
		AND contract.lifecycle_state='active' AND contract.distance_metric='cosine' AND generation.activation_state='active' LIMIT 1`,
		fence.TeamID, fence.SpaceID, fence.Generation, seed.ID, seed.Version, strings.TrimSpace(topic.SourceDisplayText(seed))).Row().Scan(&vector, &contractID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, topic.ErrEvidenceEmbeddingUnavailable
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Raw(`SELECT source.source_kind,source.source_id,source.source_version
		FROM topic_maintenance_sources AS source JOIN search_documents AS document
		ON document.team_id=source.team_id AND document.space_id=source.shared_space_id AND document.space_generation=source.space_generation
		AND document.source_kind=source.source_kind AND document.source_id::text=source.source_id AND document.source_version=source.source_version
		WHERE source.team_id=?::uuid AND source.shared_space_id=?::uuid AND source.space_generation=? AND source.eligible
		AND source.source_kind='evidence' AND source.source_id<>? AND document.embedding_contract_id=?::uuid
		AND document.search_state='current' AND document.embedding IS NOT NULL
		ORDER BY document.embedding <=> ?::vector,source.source_id LIMIT ?`,
		fence.TeamID, fence.SpaceID, fence.Generation, seed.ID, contractID, vector, topic.MaxOrganizationSources-1).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var related []topic.SourceHandle
	for rows.Next() {
		var source topic.SourceHandle
		if err := rows.Scan(&source.Kind, &source.ID, &source.Version); err != nil {
			return nil, err
		}
		related = append(related, source)
	}
	return related, rows.Err()
}
