package postgres

import (
	"fmt"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func managementEditContext(tx *gorm.DB, fence scope, draftID string, input ontology.SaveDraftInput) (ontology.ManagementContext, error) {
	current := ontology.ManagementContext{DraftID: draftID, Sources: map[string]ontology.SourceSnapshot{}}
	var err error
	current.Revision, err = catalogRevision(tx, fence)
	if err != nil {
		return current, err
	}
	if current.Revision != input.ExpectedRevision {
		return current, ontology.ErrConflict
	}
	var records []ontology.Record
	var changedDefinitions []string
	var handles []ontology.SourceHandle
	for index, edit := range input.Edits {
		id := edit.RecordID
		if id == "" {
			id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(draftID+":"+fmt.Sprint(index)+":"+edit.Action)).String()
		}
		record := ontology.Record{ID: id}
		switch edit.Action {
		case "define":
			record.Kind, record.Definition = edit.Kind, edit.Definition
			changedDefinitions = append(changedDefinitions, id)
		case "classify":
			record.Override = &ontology.Override{Action: ontology.SetClassification, DefinitionID: edit.DefinitionID, Members: []ontology.SourceHandle{*edit.Source}}
		case "pin_definition":
			record.Override = &ontology.Override{Action: ontology.PinDefinition, TargetID: edit.DefinitionID}
		case "group_together", "keep_separate":
			record.Override = &ontology.Override{Action: ontology.OverrideAction(edit.Action), Members: edit.Members}
		}
		records = append(records, record)
		handles = append(handles, ontology.RequiredSources(record)...)
	}
	current.Records, err = validationCatalog(tx, fence, records, changedDefinitions)
	if err != nil {
		return current, err
	}
	combined := make(map[string]ontology.Record, len(current.Records)+len(records))
	for id, record := range current.Records {
		combined[id] = record
	}
	for index, proposed := range records {
		edit := input.Edits[index]
		if previous, exists := current.Records[proposed.ID]; exists {
			if edit.Action == "define" {
				previous.Kind, previous.Definition = proposed.Kind, proposed.Definition
				proposed = previous
			}
			if edit.Action == "remove_override" {
				proposed = previous
				proposed.Retired = true
			}
		}
		records[index], combined[proposed.ID] = proposed, proposed
	}
	dependencies, err := ontology.DependencyRecords(records, combined)
	if err != nil {
		return current, err
	}
	for _, record := range dependencies {
		if record.Retired {
			continue
		}
		handles = append(handles, ontology.RequiredSources(record)...)
		for _, source := range record.Sources {
			handles = append(handles, source.SourceHandle)
		}
	}
	if len(handles) > ontology.MaxDependencyRecords {
		return current, ontology.ErrContextBound
	}
	for _, handle := range handles {
		key := ontology.SourceKey(handle)
		if previous, exists := current.Sources[key]; exists {
			if previous.SourceHandle != handle {
				return current, ontology.ErrSourceStale
			}
			continue
		}
		source, err := readSource(tx, fence, handle)
		if err != nil {
			return current, err
		}
		current.Sources[key] = source
	}
	return current, nil
}

func validateManagementPublication(tx *gorm.DB, fence scope, input ontology.Publication) (map[string]ontology.Record, map[string]ontology.SourceSnapshot, error) {
	revision, err := catalogRevision(tx, fence)
	if err != nil {
		return nil, nil, err
	}
	if revision != input.ExpectedRevision {
		return nil, nil, ontology.ErrConflict
	}
	var records []ontology.Record
	var definitions []string
	for _, change := range input.Changes {
		records = append(records, change.Record)
		if change.Record.Definition != nil {
			definitions = append(definitions, change.Record.ID)
		}
	}
	catalog, err := validationCatalog(tx, fence, records, definitions)
	if err != nil {
		return nil, nil, err
	}
	combined := make(map[string]ontology.Record, len(catalog)+len(records))
	for id, record := range catalog {
		combined[id] = record
	}
	for _, record := range records {
		combined[record.ID] = record
	}
	sources, err := sourceSnapshots(tx, fence, records, combined)
	if err != nil {
		return nil, nil, err
	}
	if _, err = ontology.PreparePublication(catalog, sources, input, false); err != nil {
		return nil, nil, err
	}
	return catalog, sources, ontology.ValidateManagementOverrides(catalog, input)
}
