package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) ClaimTopicConsolidation(ctx context.Context, turn topic.MaintenanceTurn, windowID string, now time.Time) ([]topic.TopicConsolidationPair, *topic.MaintenanceClaim, error) {
	claim := &topic.MaintenanceClaim{ID: uuid.NewString(), Kind: "consolidation", WindowID: windowID, TeamID: turn.TeamID,
		SpaceID: turn.SpaceID, Generation: turn.Generation, LeaseToken: turn.LeaseToken, LeaseUntil: turn.LeaseUntil,
		Sources: []topic.SourceHandle{}, Revisions: map[string]int64{}}
	var pairs []topic.TopicConsolidationPair
	admitted := false
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		fence, err := sharedScope(ctx, tx, turn.TeamID)
		if err != nil {
			return err
		}
		if fence.SpaceID != turn.SpaceID || fence.Generation != turn.Generation {
			return topic.ErrLeaseLost
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		type seed struct {
			id, vector string
			version    int64
			active     assignmentSearchContract
		}
		var seeds []seed
		rows, err := tx.Raw(`SELECT embedding.topic_id::text,embedding.topic_version,embedding.embedding::text,
			contract.embedding_contract_id::text,generation.search_index_generation_id::text,contract.dimensions,
			generation.ann_strategy,generation.candidate_limit,generation.query_ef_search
			FROM topic_embeddings AS embedding JOIN topics AS current USING(team_id,shared_space_id,space_generation,topic_id)
			JOIN embedding_contracts AS contract ON contract.embedding_contract_id=embedding.embedding_contract_id
			AND contract.dimensions=embedding.embedding_dimensions AND contract.lifecycle_state='active'
			JOIN search_index_generations AS generation ON generation.embedding_contract_id=contract.embedding_contract_id
			AND generation.activation_state='active'
			WHERE embedding.team_id=?::uuid AND embedding.shared_space_id=?::uuid AND embedding.space_generation=?
			AND NOT current.retired AND current.definition_version=embedding.topic_version AND embedding.consolidation_pending
			AND (embedding.consolidation_lease_until IS NULL OR embedding.consolidation_lease_until<=statement_timestamp())
			ORDER BY embedding.topic_id LIMIT 2 FOR UPDATE OF embedding SKIP LOCKED`, fence.TeamID, fence.SpaceID, fence.Generation).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			var item seed
			if err := rows.Scan(&item.id, &item.version, &item.vector, &item.active.ID, &item.active.GenerationID,
				&item.active.Dimensions, &item.active.Strategy, &item.active.CandidateLimit, &item.active.EFSearch); err != nil {
				rows.Close()
				return err
			}
			seeds = append(seeds, item)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(seeds) == 0 {
			return nil
		}
		seen := map[string]bool{}
		for _, seed := range seeds {
			claim.Revisions[seed.id] = seed.version
			ids, err := s.assignmentVectorCandidates(tx, fence, seed.active, seed.vector)
			if err != nil {
				return err
			}
			for _, other := range ids {
				if other == seed.id {
					continue
				}
				left, right := seed.id, other
				if left > right {
					left, right = right, left
				}
				key := left + ":" + right
				if seen[key] {
					continue
				}
				seen[key] = true
				var pair topic.TopicConsolidationPair
				err = tx.Raw(`SELECT left_topic.topic_id::text,left_topic.definition_version,left_topic.label,left_topic.description,
					right_topic.topic_id::text,right_topic.definition_version,right_topic.label,right_topic.description,
					1-(left_vector.embedding <=> right_vector.embedding)
					FROM topics AS left_topic JOIN topics AS right_topic ON
					(right_topic.team_id,right_topic.shared_space_id,right_topic.space_generation)=
					(left_topic.team_id,left_topic.shared_space_id,left_topic.space_generation)
					JOIN topic_embeddings AS left_vector ON
					(left_vector.team_id,left_vector.shared_space_id,left_vector.space_generation,left_vector.topic_id,left_vector.topic_version)=
					(left_topic.team_id,left_topic.shared_space_id,left_topic.space_generation,left_topic.topic_id,left_topic.definition_version)
					JOIN topic_embeddings AS right_vector ON
					(right_vector.team_id,right_vector.shared_space_id,right_vector.space_generation,right_vector.topic_id,right_vector.topic_version,right_vector.embedding_contract_id)=
					(right_topic.team_id,right_topic.shared_space_id,right_topic.space_generation,right_topic.topic_id,right_topic.definition_version,left_vector.embedding_contract_id)
					WHERE left_topic.team_id=?::uuid AND left_topic.shared_space_id=?::uuid AND left_topic.space_generation=?
					AND left_topic.topic_id=?::uuid AND right_topic.topic_id=?::uuid AND NOT left_topic.retired AND NOT right_topic.retired
					AND left_vector.embedding_contract_id=?::uuid AND NOT EXISTS(SELECT 1 FROM topic_consolidation_assessments AS done
					WHERE (done.team_id,done.shared_space_id,done.space_generation,done.left_topic_id,done.left_version,done.right_topic_id,done.right_version,done.embedding_contract_id)=
					(left_topic.team_id,left_topic.shared_space_id,left_topic.space_generation,left_topic.topic_id,left_topic.definition_version,right_topic.topic_id,right_topic.definition_version,left_vector.embedding_contract_id))`,
					fence.TeamID, fence.SpaceID, fence.Generation, left, right, seed.active.ID).Row().Scan(
					&pair.Left.ID, &pair.Left.Version, &pair.Left.Name, &pair.Left.Description,
					&pair.Right.ID, &pair.Right.Version, &pair.Right.Name, &pair.Right.Description, &pair.Similarity)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				pair.EmbeddingContractID = seed.active.ID
				pairs = append(pairs, pair)
			}
		}
		ids := make([]string, 0, len(seeds))
		for _, seed := range seeds {
			ids = append(ids, seed.id)
		}
		if len(pairs) == 0 {
			return tx.Exec(`UPDATE topic_embeddings AS embedding SET consolidation_pending=false
				FROM topics AS current WHERE (current.team_id,current.shared_space_id,current.space_generation,current.topic_id,current.definition_version)=
				(embedding.team_id,embedding.shared_space_id,embedding.space_generation,embedding.topic_id,embedding.topic_version)
				AND embedding.team_id=?::uuid AND embedding.shared_space_id=?::uuid AND embedding.space_generation=?
				AND embedding.topic_id=ANY(?::uuid[]) AND embedding.embedding_contract_id=?::uuid AND NOT current.retired`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids), seeds[0].active.ID).Error
		}
		admitted, err = admitMaintenanceBatchTx(tx, turn, claim, nil, now)
		if err != nil || !admitted {
			return err
		}
		updated := tx.Exec(`UPDATE topic_embeddings SET consolidation_batch_id=?::uuid,consolidation_lease_until=?
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND topic_id=ANY(?::uuid[]) AND embedding_contract_id=?::uuid`,
			claim.ID, claim.LeaseUntil, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids), seeds[0].active.ID)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != int64(len(ids)) {
			return topic.ErrSourceStale
		}
		return nil
	})
	if err != nil || !admitted {
		return nil, nil, err
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Similarity != pairs[j].Similarity {
			return pairs[i].Similarity > pairs[j].Similarity
		}
		if pairs[i].Left.ID != pairs[j].Left.ID {
			return pairs[i].Left.ID < pairs[j].Left.ID
		}
		return pairs[i].Right.ID < pairs[j].Right.ID
	})
	return pairs, claim, nil
}

func (s *Store) CompleteTopicConsolidationPair(ctx context.Context, claim topic.MaintenanceClaim, pair topic.TopicConsolidationPair, merge bool) (result topic.TopicConsolidationResult, err error) {
	if pair.Left.ID >= pair.Right.ID || pair.Left.Version < 1 || pair.Right.Version < 1 {
		return result, topic.ErrInvalid
	}
	err = s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		fence, err := sharedScope(ctx, tx, claim.TeamID)
		if err != nil {
			return err
		}
		if fence.SpaceID != claim.SpaceID || fence.Generation != claim.Generation {
			return topic.ErrLeaseLost
		}
		if err := maintenancePublicationFence(topic.WithMaintenanceClaim(ctx, claim), tx, fence); err != nil {
			return err
		}
		if merge {
			result, err = s.mergeTopicPair(tx, fence, claim, pair)
			if err != nil {
				return err
			}
			if result.ReassessmentRequired {
				return nil
			}
		} else {
			var count int
			if err := tx.Raw(`SELECT count(*) FROM topics AS current JOIN topic_embeddings AS embedding
				USING(team_id,shared_space_id,space_generation,topic_id)
				JOIN embedding_contracts AS contract ON contract.embedding_contract_id=embedding.embedding_contract_id
				WHERE current.team_id=?::uuid AND current.shared_space_id=?::uuid AND current.space_generation=?
				AND ((current.topic_id=?::uuid AND current.definition_version=?) OR (current.topic_id=?::uuid AND current.definition_version=?))
				AND NOT current.retired AND embedding.topic_version=current.definition_version
				AND embedding.embedding_contract_id=?::uuid AND contract.lifecycle_state='active'`,
				fence.TeamID, fence.SpaceID, fence.Generation, pair.Left.ID, pair.Left.Version, pair.Right.ID, pair.Right.Version, pair.EmbeddingContractID).Row().Scan(&count); err != nil {
				return err
			}
			if count != 2 {
				return topic.ErrSourceStale
			}
		}
		return tx.Exec(`INSERT INTO topic_consolidation_assessments(team_id,shared_space_id,space_generation,left_topic_id,left_version,right_topic_id,right_version,embedding_contract_id)
			VALUES(?::uuid,?::uuid,?,?::uuid,?,?::uuid,?,?::uuid) ON CONFLICT DO NOTHING`,
			fence.TeamID, fence.SpaceID, fence.Generation, pair.Left.ID, pair.Left.Version, pair.Right.ID, pair.Right.Version, pair.EmbeddingContractID).Error
	})
	return result, err
}

type consolidationChild struct {
	record            topic.Record
	editVersion       int64
	placement, parent string
}

func (s *Store) mergeTopicPair(tx *gorm.DB, fence scope, claim topic.MaintenanceClaim, pair topic.TopicConsolidationPair) (topic.TopicConsolidationResult, error) {
	var result topic.TopicConsolidationResult
	if err := lockOrganizationCatalog(tx, fence); err != nil {
		return result, err
	}
	type choice struct {
		id                   string
		version, publication int64
		manager              bool
	}
	var choices []choice
	rows, err := tx.Raw(`SELECT current.topic_id::text,current.definition_version,current.manager_authored,
		COALESCE((SELECT min(publication.revision) FROM topic_record_revisions AS revision JOIN topic_publications AS publication
		USING(team_id,shared_space_id,space_generation,publication_id) WHERE
		(revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id)=
		(current.team_id,current.shared_space_id,current.space_generation,current.topic_id)),0)
		FROM topics AS current WHERE current.team_id=?::uuid AND current.shared_space_id=?::uuid AND current.space_generation=?
		AND current.topic_id=ANY(?::uuid[]) AND NOT current.retired ORDER BY current.topic_id`,
		fence.TeamID, fence.SpaceID, fence.Generation, pq.Array([]string{pair.Left.ID, pair.Right.ID})).Rows()
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item choice
		if err := rows.Scan(&item.id, &item.version, &item.manager, &item.publication); err != nil {
			rows.Close()
			return result, err
		}
		choices = append(choices, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	if len(choices) != 2 || choices[0].id != pair.Left.ID || choices[0].version != pair.Left.Version || choices[1].id != pair.Right.ID || choices[1].version != pair.Right.Version {
		return result, topic.ErrSourceStale
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].manager != choices[j].manager {
			return choices[i].manager
		}
		if choices[i].publication != choices[j].publication {
			return choices[i].publication < choices[j].publication
		}
		return choices[i].id < choices[j].id
	})
	survivorID, loserID := choices[0].id, choices[1].id
	result.SurvivorID, result.RetiredID = survivorID, loserID
	heads, err := loadHeads(tx, fence, []string{survivorID, loserID}, nil)
	if err != nil {
		return result, err
	}
	survivor, loser := heads[survivorID], heads[loserID]
	if survivor.Definition == nil || loser.Definition == nil || survivor.Retired || loser.Retired {
		return result, topic.ErrSourceStale
	}
	var children []consolidationChild
	rows, err = tx.Raw(`SELECT revision.body,current.version,current.placement,COALESCE(current.parent_topic_id::text,'')
		FROM topics AS current JOIN topic_revision_records AS revision ON
		(revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id,revision.version)=
		(current.team_id,current.shared_space_id,current.space_generation,current.topic_id,current.definition_version)
		WHERE current.team_id=?::uuid AND current.shared_space_id=?::uuid AND current.space_generation=? AND NOT current.retired
		AND (current.parent_topic_id=?::uuid OR revision.body->'definition'->>'parent_id'=?) ORDER BY current.topic_id LIMIT ?`,
		fence.TeamID, fence.SpaceID, fence.Generation, loserID, loserID, topic.MaxChanges+1).Rows()
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var body []byte
		var child consolidationChild
		if err := rows.Scan(&body, &child.editVersion, &child.placement, &child.parent); err != nil {
			rows.Close()
			return result, err
		}
		child.record, err = decodeRecord(body)
		if err != nil {
			rows.Close()
			return result, err
		}
		if child.record.ID == survivorID {
			rows.Close()
			return result, topic.ErrConflict
		}
		children = append(children, child)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	if len(children)+2 > topic.MaxChanges {
		return result, topic.ErrContextBound
	}
	names := append(topic.DefinitionNames(survivor), topic.DefinitionNames(loser)...)
	ids := []string{survivorID, loserID}
	for _, child := range children {
		names = append(names, topic.DefinitionNames(child.record)...)
		ids = append(ids, child.record.ID)
	}
	if err := lockTopicNames(tx, fence, names); err != nil {
		return result, err
	}
	var locked []struct {
		ID                                                    string
		DefinitionVersion, MaterialDefinitionVersion, Version int64
	}
	if err := tx.Raw(`SELECT topic_id::text AS id,definition_version,material_definition_version,version FROM topics
		WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND topic_id=ANY(?::uuid[]) AND NOT retired
		ORDER BY topic_id FOR UPDATE`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids)).Scan(&locked).Error; err != nil {
		return result, err
	}
	versions := map[string]struct{ definition, material, edit int64 }{}
	for _, row := range locked {
		versions[row.ID] = struct{ definition, material, edit int64 }{row.DefinitionVersion, row.MaterialDefinitionVersion, row.Version}
	}
	if len(versions) != len(ids) || versions[survivorID].definition != choices[0].version || versions[loserID].definition != choices[1].version {
		return result, topic.ErrSourceStale
	}
	for _, child := range children {
		if versions[child.record.ID].definition != child.record.Version || versions[child.record.ID].edit != child.editVersion {
			return result, topic.ErrSourceStale
		}
	}
	var active bool
	if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM embedding_contracts WHERE embedding_contract_id=?::uuid AND lifecycle_state='active')`, pair.EmbeddingContractID).Row().Scan(&active); err != nil {
		return result, err
	}
	if !active {
		return result, topic.ErrSourceStale
	}
	rows, err = tx.Raw(`SELECT evidence_id::text,topic_version FROM topic_memberships WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
		AND topic_id=?::uuid AND NOT retired ORDER BY evidence_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, loserID, topic.MaxChanges+1).Rows()
	if err != nil {
		return result, err
	}
	var evidenceIDs []string
	acceptedVersions := map[string]int64{}
	for rows.Next() {
		var id string
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			rows.Close()
			return result, err
		}
		evidenceIDs = append(evidenceIDs, id)
		acceptedVersions[id] = version
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	if len(evidenceIDs)+len(children)+2 > topic.MaxChanges {
		return result, topic.ErrContextBound
	}
	for _, id := range evidenceIDs {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?::text,25022))`, fence.SpaceID+":"+id).Error; err != nil {
			return result, err
		}
	}
	definition := *survivor.Definition
	definition.Aliases = append([]string(nil), definition.Aliases...)
	seen := map[string]bool{}
	for _, name := range topic.DefinitionNames(survivor) {
		seen[name] = true
	}
	for _, name := range append([]string{loser.Definition.Key, loser.Definition.Label}, loser.Definition.Aliases...) {
		key := topic.NormalizeName(name)
		if key != "" && !seen[key] {
			definition.Aliases = append(definition.Aliases, strings.TrimSpace(name))
			seen[key] = true
		}
	}
	if len(definition.Aliases) > topic.MaxAliases {
		return result, topic.ErrContextBound
	}
	survivor.Definition = &definition
	loser.Retired = true
	changes := []topic.Change{{Record: survivor, ExpectedVersion: survivor.Version}, {Record: loser, ExpectedVersion: loser.Version}}
	for _, id := range evidenceIDs {
		memberships, err := assignmentMemberships(tx, fence, id)
		if err != nil {
			return result, err
		}
		old, exists := memberships[loserID]
		if !exists || old.Retired {
			return result, topic.ErrSourceStale
		}
		view, err := s.currentView(tx, fence, old)
		if err != nil {
			return result, err
		}
		handle := topic.SourceHandle{Kind: topic.EvidenceSource, ID: id, Version: 1}
		snapshot, sourceErr := readSource(tx, fence, handle)
		if sourceErr != nil && !errors.Is(sourceErr, topic.ErrSourceStale) {
			return result, sourceErr
		}
		if sourceErr == nil && (!view.Current || acceptedVersions[id] < versions[loserID].material) {
			if err := refreshMaintenanceSource(tx, fence, handle, true); err != nil {
				return result, err
			}
			if err := tx.Exec(`UPDATE topic_maintenance_sources SET force_reassessment=true WHERE team_id=?::uuid AND shared_space_id=?::uuid
				AND space_generation=? AND source_kind='evidence' AND source_id=? AND eligible`, fence.TeamID, fence.SpaceID, fence.Generation, id).Error; err != nil {
				return result, err
			}
			result.ReassessmentRequired = true
			continue
		}
		old.Retired = true
		changes = append(changes, topic.Change{Record: old, ExpectedVersion: old.Version})
		result.RetiredMemberships++
		if errors.Is(sourceErr, topic.ErrSourceStale) {
			if err := refreshMaintenanceSource(tx, fence, handle, false); err != nil {
				return result, err
			}
			continue
		}
		if existing, found := memberships[survivorID]; found && !existing.Retired {
			continue
		}
		fingerprint, err := topic.SourceFingerprint(snapshot)
		if err != nil {
			return result, err
		}
		newID := topic.OrganizationRecordID(fence.TeamID, topic.AssignmentKind, topic.SourceKey(handle)+":"+survivorID)
		previous, err := loadHeads(tx, fence, []string{newID}, nil)
		if err != nil {
			return result, err
		}
		expected := int64(0)
		if record, exists := previous[newID]; exists {
			if !record.Retired {
				return result, topic.ErrConflict
			}
			expected = record.Version
		}
		membership := topic.Record{ID: newID, Kind: topic.AssignmentKind,
			Assignment:   &topic.Assignment{Source: handle, DefinitionID: survivorID},
			Sources:      []topic.SourceDependency{{SourceHandle: handle, Fingerprint: fingerprint}},
			Dependencies: []topic.RevisionRef{{ID: survivorID, Version: survivor.Version + 1}}}
		changes = append(changes, topic.Change{Record: membership, ExpectedVersion: expected})
		result.MovedMemberships++
	}
	if result.ReassessmentRequired {
		result.RetiredID = ""
		result.MovedMemberships, result.RetiredMemberships = 0, 0
		return result, nil
	}
	placementOnly := 0
	for _, child := range children {
		if child.record.Definition.ParentID == loserID {
			definition := *child.record.Definition
			definition.ParentID = survivorID
			child.record.Definition = &definition
			child.record.Dependencies = nil
			changes = append(changes, topic.Change{Record: child.record, ExpectedVersion: child.record.Version})
		} else {
			placementOnly++
		}
	}
	if len(changes)+placementOnly > topic.MaxChanges {
		return result, topic.ErrContextBound
	}
	operation := "consolidation:" + uuid.NewSHA1(uuid.MustParse(claim.ID), []byte(pair.Left.ID+":"+pair.Right.ID)).String()
	publication := topic.Publication{OperationKey: operation, Reason: "Consolidate equivalent Topics", Changes: changes}
	if err := topic.ValidatePublication(publication); err != nil {
		return result, err
	}
	hash, err := topic.RequestHash(publication, "automatic", "")
	if err != nil {
		return result, err
	}
	if _, err = s.writePublication(tx, fence, publication, "automatic", "", hash, "", false, map[string]bool{survivorID: true}); err != nil {
		return result, err
	}
	if err := reparentConsolidationChildren(tx, fence, children, survivorID, loserID, survivor.Version+1, claim.ID); err != nil {
		return result, err
	}
	result.ReparentedChildren = len(children)
	if err := tx.Exec(`DELETE FROM topic_embeddings WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND topic_id=?::uuid`, fence.TeamID, fence.SpaceID, fence.Generation, loserID).Error; err != nil {
		return result, err
	}
	return result, nil
}

func reparentConsolidationChildren(tx *gorm.DB, fence scope, children []consolidationChild, survivorID, loserID string, survivorVersion int64, operationID string) error {
	ids := make([]string, 0, len(children))
	for _, child := range children {
		ids = append(ids, child.record.ID)
		declaredChanged := child.record.Definition.ParentID == loserID
		if child.placement == "declared" {
			if !declaredChanged {
				return topic.ErrConflict
			}
			continue
		}
		if child.placement != "placed" && child.placement != "standalone" {
			return topic.ErrInvalid
		}
		parent := child.parent
		if parent == loserID {
			parent = survivorID
		}
		if parent == child.record.ID {
			return topic.ErrConflict
		}
		var parentVersion any
		if parent != "" {
			if parent == survivorID {
				parentVersion = survivorVersion
			} else {
				var version int64
				err := tx.Raw(`SELECT definition_version FROM topics WHERE team_id=?::uuid AND shared_space_id=?::uuid
					AND space_generation=? AND topic_id=?::uuid AND NOT retired`, fence.TeamID, fence.SpaceID, fence.Generation, parent).Row().Scan(&version)
				if errors.Is(err, sql.ErrNoRows) {
					return topic.ErrSourceStale
				}
				if err != nil {
					return err
				}
				parentVersion = version
			}
		}
		version := child.record.Version
		if declaredChanged {
			version++
		}
		updated := tx.Exec(`UPDATE topics SET parent_topic_id=NULLIF(?,'')::uuid,placement=?,version=version+1
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND topic_id=?::uuid
			AND definition_version=? AND NOT retired`, parent, child.placement, fence.TeamID, fence.SpaceID, fence.Generation, child.record.ID, version)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return topic.ErrSourceStale
		}
		if err := tx.Exec(`INSERT INTO community_placement_outcomes(team_id,shared_space_id,space_generation,topic_id,topic_version,input_hash,method,status,parent_topic_id,parent_topic_version,operation_id)
			VALUES(?::uuid,?::uuid,?,?::uuid,?,?,?, ?,NULLIF(?,'')::uuid,?::bigint,?::uuid)`, fence.TeamID, fence.SpaceID, fence.Generation,
			child.record.ID, version, "consolidation:"+operationID, "consolidation", child.placement, parent, parentVersion, operationID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE topic_maintenance_community_work SET parent_topic_id=NULLIF(?,'')::uuid,parent_topic_version=?::bigint,
			ready_at=NULL,lease_batch_id=NULL,lease_until=NULL WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND topic_id=?::uuid`,
			parent, parentVersion, fence.TeamID, fence.SpaceID, fence.Generation, child.record.ID).Error; err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var conflict bool
	err := tx.Raw(`WITH RECURSIVE descendants(id) AS (
		SELECT unnest(?::uuid[]) UNION SELECT child.topic_id FROM topics AS child JOIN descendants AS parent ON child.parent_topic_id=parent.id
		WHERE child.team_id=?::uuid AND child.shared_space_id=?::uuid AND child.space_generation=? AND NOT child.retired
	), chain(id,depth,path) AS (
		SELECT current.parent_topic_id,1,ARRAY[current.topic_id] FROM topics AS current JOIN descendants ON descendants.id=current.topic_id
		WHERE current.team_id=?::uuid AND current.shared_space_id=?::uuid AND current.space_generation=? AND NOT current.retired
		UNION ALL SELECT parent.parent_topic_id,chain.depth+1,chain.path||parent.topic_id FROM chain JOIN topics AS parent ON parent.topic_id=chain.id
		WHERE parent.team_id=?::uuid AND parent.shared_space_id=?::uuid AND parent.space_generation=? AND NOT parent.retired
		AND NOT chain.id=ANY(chain.path) AND chain.depth<=?
	) SELECT EXISTS(SELECT 1 FROM chain WHERE id IS NOT NULL AND (id=ANY(path) OR depth>?))`, pq.Array(ids),
		fence.TeamID, fence.SpaceID, fence.Generation, fence.TeamID, fence.SpaceID, fence.Generation,
		fence.TeamID, fence.SpaceID, fence.Generation, topic.MaxParentDepth, topic.MaxParentDepth).Row().Scan(&conflict)
	if err != nil {
		return err
	}
	if conflict {
		return fmt.Errorf("%w: consolidation would create a cycle or exceed hierarchy depth", topic.ErrConflict)
	}
	return nil
}

func (s *Store) FinishTopicConsolidation(ctx context.Context, claim topic.MaintenanceClaim, completed bool) error {
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		fence := scope{TeamID: claim.TeamID, SpaceID: claim.SpaceID, Generation: claim.Generation}
		if err := maintenancePublicationFence(topic.WithMaintenanceClaim(ctx, claim), tx, fence); err != nil {
			return err
		}
		return tx.Exec(`UPDATE topic_embeddings AS embedding SET
			consolidation_pending=CASE WHEN ? AND EXISTS(SELECT 1 FROM topics AS current WHERE
			(current.team_id,current.shared_space_id,current.space_generation,current.topic_id,current.definition_version)=
			(embedding.team_id,embedding.shared_space_id,embedding.space_generation,embedding.topic_id,embedding.topic_version)
			AND NOT current.retired) THEN false ELSE consolidation_pending END,
			consolidation_batch_id=NULL,consolidation_lease_until=NULL
			WHERE embedding.team_id=?::uuid AND embedding.shared_space_id=?::uuid AND embedding.space_generation=? AND embedding.consolidation_batch_id=?::uuid`,
			completed, claim.TeamID, claim.SpaceID, claim.Generation, claim.ID).Error
	})
}
