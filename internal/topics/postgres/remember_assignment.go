package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/lib/pq"
	assignment "github.com/markhuangai/dense-mem/internal/topics/assignment"
	organization "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) ReadAssignmentCandidates(ctx context.Context, input assignment.Scope, slice assignment.Slice) (assignment.Candidates, error) {
	var candidates assignment.Candidates
	err := s.withScope(ctx, input.TeamID, true, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != input.SpaceID || fence.Generation != input.Generation {
			return assignment.ErrStale
		}
		var forced []string
		if slice.EvidenceID != "" {
			changed, retired, err := touchedAssignmentTopics(tx, fence, slice.EvidenceID, slice.SourceFingerprint)
			if err != nil {
				return err
			}
			forced = changed
			candidates.NeedsRepair = len(changed) > 0 || len(retired) > 0
		}
		source := organization.SourceSnapshot{State: map[string]string{"content": slice.Text}}
		views, err := s.organizationVocabulary(tx, fence, []organization.SourceSnapshot{source}, forced)
		if err != nil {
			return err
		}
		for _, v := range views {
			d := v.Definition
			candidates.Topics = append(candidates.Topics, assignment.Topic{ID: v.ID, Version: v.Version, Label: d.Label, Description: d.Description, Aliases: append([]string{d.Key}, d.Aliases...)})
		}
		return nil
	})
	return candidates, err
}

// A linked definition can fall out of lexical search after a rename. Include its current
// revision when either the definition or the Evidence changed since the membership was accepted.
func touchedAssignmentTopics(tx *gorm.DB, fence scope, evidenceID, fingerprint string) ([]string, map[string]bool, error) {
	rows, err := tx.Raw(`SELECT target.topic_id::text,target.retired
		FROM topic_memberships AS membership
		JOIN topics AS target USING(team_id,shared_space_id,space_generation,topic_id)
		WHERE membership.team_id=?::uuid AND membership.shared_space_id=?::uuid AND membership.space_generation=?
		  AND membership.evidence_id=?::uuid AND NOT membership.retired
		  AND (target.retired OR membership.topic_version<>target.definition_version OR membership.source_fingerprint<>?)
		ORDER BY target.topic_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, evidenceID, fingerprint, organization.MaxDependencyRecords+1).Rows()
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var changed []string
	retired := map[string]bool{}
	for rows.Next() {
		var id string
		var isRetired bool
		if err := rows.Scan(&id, &isRetired); err != nil {
			return nil, nil, err
		}
		if isRetired {
			retired[id] = true
		} else {
			changed = append(changed, id)
		}
		if len(changed) > assignment.MaxTopics || len(changed)+len(retired) > organization.MaxDependencyRecords {
			return nil, nil, organization.ErrContextBound
		}
	}
	return changed, retired, rows.Err()
}

// PublishAssignmentsTx shares the caller's authenticated transaction with its Evidence writes.
func (s *Store) PublishAssignmentsTx(ctx context.Context, tx *gorm.DB, input assignment.Scope, items []assignment.Publication) error {
	if len(items) == 0 {
		return nil
	}
	if tx == nil || len(items) > 100 {
		return organization.ErrInvalid
	}
	if _, err := uuid.Parse(input.OperationID); err != nil {
		return organization.ErrInvalid
	}
	fence, err := sharedScope(ctx, tx, input.TeamID)
	if err != nil {
		return err
	}
	if fence.SpaceID != input.SpaceID || fence.Generation != input.Generation {
		return assignment.ErrStale
	}
	var ids []string
	var proposalNames []string
	for _, item := range items {
		if item.Outcome.Unchanged {
			continue
		}
		ids = append(ids, item.EvidenceID)
		for _, proposal := range item.Outcome.Proposals {
			proposalNames = append(proposalNames, organization.NormalizeName(proposal.Label))
		}
	}
	if err := lockTopicNames(tx, fence, proposalNames); err != nil {
		return err
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?::text,25022))`, fence.SpaceID+":"+id).Error; err != nil {
			return err
		}
	}
	for _, item := range items {
		if item.Outcome.Unchanged {
			continue
		}
		if err = s.publishAssignment(tx, fence, input.OperationID, item); err != nil {
			if errors.Is(err, organization.ErrSourceStale) || errors.Is(err, organization.ErrConflict) {
				return fmt.Errorf("%w: %w", assignment.ErrStale, err)
			}
			return err
		}
	}
	return nil
}

func (s *Store) publishAssignment(tx *gorm.DB, fence scope, operationID string, item assignment.Publication) error {
	handle := organization.SourceHandle{Kind: organization.EvidenceSource, ID: item.EvidenceID, Version: 1}
	source, err := readSource(tx, fence, handle)
	if err != nil {
		return err
	}
	if source.State["content"] != item.Outcome.Text {
		return assignment.ErrStale
	}
	fingerprint, err := organization.SourceFingerprint(source)
	if err != nil {
		return err
	}
	if item.ExpectedFingerprint != "" && fingerprint != item.ExpectedFingerprint {
		return assignment.ErrStale
	}
	dep := organization.SourceDependency{SourceHandle: handle, Fingerprint: fingerprint}
	targets, changes, err := s.assignmentDefinitions(tx, fence, item.Outcome, dep)
	if err != nil {
		return err
	}
	for _, change := range changes {
		if change.Record.Definition.ParentID == "" {
			continue
		}
		// The definition write can restore its declared parent. Other hierarchy writers take the catalog lock before locking names or rows.
		locked, err := tryLockOrganizationCatalog(tx, fence)
		if err != nil {
			return err
		}
		if !locked {
			return fmt.Errorf("%w: topic hierarchy is being updated", assignment.ErrStale)
		}
		break
	}
	memberships, err := assignmentMemberships(tx, fence, handle.ID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		previous, found := memberships[target.ID]
		if found && !previous.Retired && len(previous.Sources) == 1 && previous.Sources[0] == dep && len(previous.Dependencies) == 1 && previous.Dependencies[0] == target {
			continue
		}
		id := previous.ID
		if !found {
			id = organization.OrganizationRecordID(fence.TeamID, organization.AssignmentKind, organization.SourceKey(handle)+":"+target.ID)
		}
		record := organization.Record{Kind: organization.AssignmentKind, ID: id, Assignment: &organization.Assignment{Source: handle, DefinitionID: target.ID}, Sources: []organization.SourceDependency{dep}, Dependencies: []organization.RevisionRef{target}}
		changes = append(changes, organization.Change{ExpectedVersion: previous.Version, Record: record})
	}
	currentIDs := map[string]bool{}
	for _, ref := range targets {
		currentIDs[ref.ID] = true
	}
	reviewed := map[string]bool{}
	for _, candidate := range item.Outcome.Reviewed {
		reviewed[candidate.ID] = true
	}
	var remaining []organization.Record
	for topicID, record := range memberships {
		if !record.Retired && !currentIDs[topicID] {
			remaining = append(remaining, record)
		}
	}
	views, err := s.organizationViews(tx, fence, remaining)
	if err != nil {
		return err
	}
	var retiredTopics map[string]bool
	if len(remaining) > 0 && item.ExpectedFingerprint != "" {
		_, retiredTopics, err = touchedAssignmentTopics(tx, fence, handle.ID, fingerprint)
		if err != nil {
			return err
		}
	}
	for _, view := range views {
		topicID := view.Assignment.DefinitionID
		if view.Current {
			currentIDs[topicID] = true
		} else if (reviewed[topicID] || retiredTopics[topicID]) && item.ExpectedFingerprint != "" {
			record := view.Record
			record.Retired = true
			changes = append(changes, organization.Change{ExpectedVersion: view.Version, Record: record})
		}
	}
	if len(changes) > 0 {
		sort.Slice(changes, func(i, j int) bool { return changes[i].Record.ID < changes[j].Record.ID })
		publication := organization.Publication{OperationKey: "assign:" + operationID + ":" + item.EvidenceID, Reason: "Direct Topic reassessment retires stale reviewed memberships and memberships whose Topic was retired", Changes: changes}
		if err := organization.ValidatePublication(publication); err != nil {
			return err
		}
		hash, err := organization.RequestHash(publication, "automatic", "")
		if err != nil {
			return err
		}
		if _, err = s.writePublication(tx, fence, publication, "automatic", "", hash, "", false); err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(currentIDs))
	for id := range currentIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	status := "uncategorized"
	if len(ids) > 0 {
		status = "assigned"
	}
	return tx.Exec(`INSERT INTO topic_assignment_outcomes(team_id,shared_space_id,space_generation,evidence_id,input_hash,method,status,topic_ids,operation_id)
		VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?,?::uuid[],?::uuid)`, fence.TeamID, fence.SpaceID, fence.Generation, item.EvidenceID, item.Outcome.InputHash, item.Outcome.Method, status, pq.Array(ids), operationID).Error
}

func assignmentMemberships(tx *gorm.DB, fence scope, id string) (map[string]organization.Record, error) {
	rows, err := tx.Raw(`SELECT revision.body FROM topic_memberships AS head JOIN topic_revision_records AS revision USING(team_id,shared_space_id,space_generation,record_id,version)
		WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=? AND head.evidence_id=?::uuid
		ORDER BY head.retired DESC,head.version,head.record_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, id, organization.MaxDependencyRecords+1).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]organization.Record{}
	count := 0
	for rows.Next() {
		count++
		if count > organization.MaxDependencyRecords {
			return nil, organization.ErrContextBound
		}
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		record, err := decodeRecord(raw)
		if err != nil {
			return nil, err
		}
		if record.Assignment == nil {
			return nil, organization.ErrInvalid
		}
		result[record.Assignment.DefinitionID] = record
	}
	return result, rows.Err()
}

func (s *Store) assignmentDefinitions(tx *gorm.DB, fence scope, outcome assignment.Outcome, source organization.SourceDependency) ([]organization.RevisionRef, []organization.Change, error) {
	ids := make([]string, 0, len(outcome.Matches)+len(outcome.Proposals))
	names := make([]string, 0, len(outcome.Proposals))
	for _, match := range outcome.Matches {
		ids = append(ids, match.ID)
	}
	for _, t := range outcome.Reviewed {
		ids = append(ids, t.ID)
	}
	for _, p := range outcome.Proposals {
		name := organization.NormalizeName(p.Label)
		ids = append(ids, organization.OrganizationRecordID(fence.TeamID, organization.Topic, name))
		names = append(names, name)
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	var locked []string
	if err := tx.Raw(`SELECT target.topic_id::text FROM topics AS target
		JOIN topic_record_heads AS head ON (head.team_id,head.shared_space_id,head.space_generation,head.record_id)=
			(target.team_id,target.shared_space_id,target.space_generation,target.topic_id)
		WHERE target.team_id=?::uuid AND target.shared_space_id=?::uuid AND target.space_generation=?
		  AND (target.topic_id=ANY(?::uuid[]) OR (NOT target.retired AND head.names && ?::text[]))
		ORDER BY target.topic_id FOR SHARE OF target`,
		fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids), pq.Array(names)).Scan(&locked).Error; err != nil {
		return nil, nil, err
	}
	heads, err := loadHeads(tx, fence, ids, names)
	if err != nil {
		return nil, nil, err
	}
	records := make([]organization.Record, 0, len(heads))
	for _, r := range heads {
		if r.Kind == organization.Topic {
			records = append(records, r)
		}
	}
	views, err := s.organizationViews(tx, fence, records)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]organization.RecordView{}
	byName := map[string]organization.RecordView{}
	for _, view := range views {
		byID[view.ID] = view
		for _, name := range organization.DefinitionNames(view.Record) {
			byName[name] = view
		}
	}
	for _, reviewed := range outcome.Reviewed {
		v := byID[reviewed.ID]
		if v.Retired || v.Version != reviewed.Version {
			return nil, nil, assignment.ErrStale
		}
	}
	targets := map[string]int64{}
	for _, m := range outcome.Matches {
		v := byID[m.ID]
		if !v.Current || v.Version != m.Version {
			return nil, nil, assignment.ErrStale
		}
		targets[m.ID] = m.Version
	}
	var changes []organization.Change
	for _, p := range outcome.Proposals {
		name := organization.NormalizeName(p.Label)
		if view, ok := byName[name]; ok {
			if !view.Current {
				if view.StaleReason != "source_changed" {
					return nil, nil, assignment.ErrStale
				}
				if _, already := targets[view.ID]; !already {
					record := view.Record
					record.Sources = []organization.SourceDependency{source}
					changes = append(changes, organization.Change{ExpectedVersion: view.Version, Record: record})
					targets[view.ID] = view.Version + 1
				}
				continue
			}
			targets[view.ID] = view.Version
			continue
		}
		id := organization.OrganizationRecordID(fence.TeamID, organization.Topic, name)
		if _, exists := heads[id]; exists {
			// A released name must not overwrite the history at its original ID.
			id = uuid.NewString()
		}
		topic := organization.Record{ID: id, Kind: organization.Topic, Definition: &organization.Definition{Key: name, Label: p.Label, Description: p.Description}}
		changes = append(changes, organization.Change{Record: topic})
		targets[id] = 1
	}
	refs := make([]organization.RevisionRef, 0, len(targets))
	for id, version := range targets {
		refs = append(refs, organization.RevisionRef{ID: id, Version: version})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs, changes, nil
}
