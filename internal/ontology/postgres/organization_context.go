package postgres

import (
	"sort"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) organizationDefinitionContext(tx *gorm.DB, fence scope, result *ontology.OrganizationContext, forced []string) error {
	definitions := map[string]ontology.Record{}
	pending := []string{}
	for _, view := range result.Candidates {
		definitions[view.ID] = view.Record
		pending = append(pending, view.ID)
	}
	pins := map[string]ontology.Record{}
	loaded := map[string]bool{}
	for len(pending) > 0 {
		for _, id := range pending {
			loaded[id] = true
		}
		heads, err := loadHeads(tx, fence, pending, nil, nil)
		if err != nil {
			return err
		}
		pending = nil
		for _, record := range heads {
			if record.Override != nil && record.Override.Action == ontology.PinDefinition && !record.Retired {
				pins[record.ID] = record
			}
			if record.Definition == nil || record.Retired {
				continue
			}
			definitions[record.ID] = record
			if record.Kind != ontology.Topic && record.Definition.ParentID != "" && !loaded[record.Definition.ParentID] {
				pending = append(pending, record.Definition.ParentID)
			}
		}
		if len(definitions)+len(pins) > ontology.MaxDependencyRecords {
			return ontology.ErrContextBound
		}
	}
	required := map[string]bool{}
	for _, id := range forced {
		required[id] = true
	}
	retained := map[string]bool{}
	ordered := []ontology.Record{}
	candidates := append([]ontology.RecordView(nil), result.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool { return required[candidates[i].ID] && !required[candidates[j].ID] })
	for _, view := range candidates {
		closure := []ontology.Record{}
		seen := map[string]bool{}
		for id := view.ID; id != "" && !retained[id]; {
			if seen[id] || len(seen) > ontology.MaxParentDepth {
				return ontology.ErrContextBound
			}
			seen[id] = true
			record, ok := definitions[id]
			if !ok {
				return ontology.ErrContextBound
			}
			closure = append(closure, record)
			id = ""
			if record.Kind != ontology.Topic {
				id = record.Definition.ParentID
			}
		}
		if len(ordered)+len(closure) > ontology.MaxVocabularyCandidates {
			if required[view.ID] {
				return ontology.ErrContextBound
			}
			continue
		}
		for _, record := range closure {
			ordered = append(ordered, record)
			retained[record.ID] = true
		}
	}
	var err error
	result.Candidates, err = s.organizationViews(tx, fence, ordered)
	if err != nil {
		return err
	}
	pinIDs := []string{}
	for id, pin := range pins {
		if retained[pin.Override.TargetID] {
			pinIDs = append(pinIDs, id)
		}
	}
	sort.Strings(pinIDs)
	for _, id := range pinIDs {
		view, err := s.currentView(tx, fence, pins[id])
		if err != nil {
			return err
		}
		result.Records = append(result.Records, view)
	}
	return nil
}
