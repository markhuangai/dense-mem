package postgres

import (
	"errors"
	"sort"

	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) organizationDefinitionContext(tx *gorm.DB, fence scope, result *topic.OrganizationContext, forced []string) error {
	required := map[string]bool{}
	for _, id := range forced {
		required[id] = true
	}
	retained := map[string]bool{}
	dependencies := map[string]bool{}
	for _, view := range result.Records {
		dependencies[view.ID] = true
	}
	pins := map[string]topic.Record{}
	ordered := []topic.Record{}
	candidates := append([]topic.RecordView(nil), result.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool { return required[candidates[i].ID] && !required[candidates[j].ID] })
	for _, view := range candidates {
		closure, candidatePins, err := organizationDefinitionClosure(tx, fence, view.ID, retained)
		if err != nil && !errors.Is(err, topic.ErrContextBound) {
			return err
		}
		additional := map[string]bool{}
		for _, record := range append(append([]topic.Record(nil), closure...), candidatePins...) {
			if !dependencies[record.ID] {
				additional[record.ID] = true
			}
		}
		if err != nil || len(ordered)+len(closure) > topic.MaxVocabularyCandidates || len(dependencies)+len(additional) > topic.MaxDependencyRecords {
			if required[view.ID] || view.Kind == topic.Topic {
				return topic.ErrContextBound
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
	pinRecords := make([]topic.Record, 0, len(pinIDs))
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

func organizationDefinitionClosure(tx *gorm.DB, fence scope, id string, retained map[string]bool) ([]topic.Record, []topic.Record, error) {
	definitions := []topic.Record{}
	pins := []topic.Record{}
	seen := map[string]bool{}
	for id != "" && !retained[id] {
		if seen[id] || len(seen) > topic.MaxParentDepth || len(definitions) == topic.MaxVocabularyCandidates {
			return nil, nil, topic.ErrContextBound
		}
		seen[id] = true
		heads, err := loadHeads(tx, fence, []string{id}, nil)
		if err != nil {
			return nil, nil, err
		}
		record, ok := heads[id]
		if !ok || record.Definition == nil || record.Retired {
			return nil, nil, topic.ErrContextBound
		}
		definitions = append(definitions, record)
		for _, head := range heads {
			if head.Override != nil && head.Override.Action == topic.PinDefinition && !head.Retired {
				pins = append(pins, head)
			}
		}
		if len(definitions)+len(pins) > topic.MaxDependencyRecords {
			return nil, nil, topic.ErrContextBound
		}
		id = ""
		if record.Kind != topic.Topic {
			id = record.Definition.ParentID
		}
	}
	return definitions, pins, nil
}
