package postgres

import (
	"errors"
	"sort"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) organizationDefinitionContext(tx *gorm.DB, fence scope, result *ontology.OrganizationContext, forced []string) error {
	required := map[string]bool{}
	for _, id := range forced {
		required[id] = true
	}
	retained := map[string]bool{}
	dependencies := map[string]bool{}
	for _, view := range result.Records {
		dependencies[view.ID] = true
	}
	pins := map[string]ontology.Record{}
	ordered := []ontology.Record{}
	candidates := append([]ontology.RecordView(nil), result.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool { return required[candidates[i].ID] && !required[candidates[j].ID] })
	for _, view := range candidates {
		closure, candidatePins, err := organizationDefinitionClosure(tx, fence, view.ID, retained)
		if err != nil && !errors.Is(err, ontology.ErrContextBound) {
			return err
		}
		additional := map[string]bool{}
		for _, record := range append(append([]ontology.Record(nil), closure...), candidatePins...) {
			if !dependencies[record.ID] {
				additional[record.ID] = true
			}
		}
		if err != nil || len(ordered)+len(closure) > ontology.MaxVocabularyCandidates || len(dependencies)+len(additional) > ontology.MaxDependencyRecords {
			if required[view.ID] || view.Kind == ontology.Topic {
				return ontology.ErrContextBound
			}
			continue
		}
		for _, record := range closure {
			ordered = append(ordered, record)
			retained[record.ID] = true
		}
		for _, pin := range candidatePins {
			pins[pin.ID] = pin
		}
		for id := range additional {
			dependencies[id] = true
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
	pinRecords := make([]ontology.Record, 0, len(pinIDs))
	for _, id := range pinIDs {
		pinRecords = append(pinRecords, pins[id])
	}
	views, err := s.organizationViews(tx, fence, pinRecords)
	if err != nil {
		return err
	}
	result.Records = append(result.Records, views...)
	return nil
}

func organizationDefinitionClosure(tx *gorm.DB, fence scope, id string, retained map[string]bool) ([]ontology.Record, []ontology.Record, error) {
	definitions := []ontology.Record{}
	pins := []ontology.Record{}
	seen := map[string]bool{}
	for id != "" && !retained[id] {
		if seen[id] || len(seen) > ontology.MaxParentDepth || len(definitions) == ontology.MaxVocabularyCandidates {
			return nil, nil, ontology.ErrContextBound
		}
		seen[id] = true
		heads, err := loadHeads(tx, fence, []string{id}, nil, nil)
		if err != nil {
			return nil, nil, err
		}
		record, ok := heads[id]
		if !ok || record.Definition == nil || record.Retired {
			return nil, nil, ontology.ErrContextBound
		}
		definitions = append(definitions, record)
		for _, head := range heads {
			if head.Override != nil && head.Override.Action == ontology.PinDefinition && !head.Retired {
				pins = append(pins, head)
			}
		}
		if len(definitions)+len(pins) > ontology.MaxDependencyRecords {
			return nil, nil, ontology.ErrContextBound
		}
		id = ""
		if record.Kind != ontology.Topic {
			id = record.Definition.ParentID
		}
	}
	return definitions, pins, nil
}
