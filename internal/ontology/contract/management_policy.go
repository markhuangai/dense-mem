package contract

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

func ManagementRequestHash(action, id string, input any) (string, error) {
	return hashJSON(struct {
		Action, ID string
		Input      any
	}{action, id, input})
}

func ValidateSaveDraft(input SaveDraftInput) error {
	if err := ValidateOperation(input.OperationKey, input.ExpectedRevision, input.Reason); err != nil {
		return err
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > MaxPublicationBytes || input.ExpectedVersion < 0 || len(input.Edits) < 1 || len(input.Edits) > MaxChanges {
		return ErrInvalid
	}
	for _, edit := range input.Edits {
		if edit.ExpectedVersion < 0 || (edit.RecordID != "" && !validID(edit.RecordID)) {
			return ErrInvalid
		}
		shape := edit
		shape.Action, shape.RecordID, shape.ExpectedVersion = "", "", 0
		switch edit.Action {
		case "define":
			if edit.Definition == nil || (edit.Kind != EntityClass && edit.Kind != PredicateConcept && edit.Kind != Topic) {
				return ErrInvalid
			}
			if err := validateDefinition(edit.Kind, *edit.Definition); err != nil {
				return err
			}
			shape.Definition, shape.Kind = nil, ""
		case "classify":
			if edit.Source == nil || !validID(edit.DefinitionID) {
				return ErrInvalid
			}
			if err := ValidateSourceHandle(*edit.Source); err != nil {
				return err
			}
			shape.Source, shape.DefinitionID = nil, ""
		case "group_together", "keep_separate":
			if err := validateMembers(edit.Members, 2); err != nil {
				return err
			}
			shape.Members = nil
		case "pin_definition":
			if !validID(edit.DefinitionID) {
				return ErrInvalid
			}
			shape.DefinitionID = ""
		case "remove_override":
			if edit.RecordID == "" || edit.ExpectedVersion < 1 {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if shape.Kind != "" || shape.Definition != nil || shape.Source != nil || shape.DefinitionID != "" || len(shape.Members) != 0 {
			return ErrInvalid
		}
	}
	return nil
}

func CompileManagementEdits(input SaveDraftInput, current ManagementContext) (Publication, error) {
	publication := Publication{OperationKey: input.OperationKey, ExpectedRevision: input.ExpectedRevision, Reason: input.Reason}
	if err := ValidateSaveDraft(input); err != nil {
		return publication, err
	}
	if current.Revision != input.ExpectedRevision {
		return publication, ErrConflict
	}
	changes := map[string]Change{}
	add := func(record Record, expected int64) error {
		if _, exists := changes[record.ID]; exists {
			return fmt.Errorf("%w: duplicate correction target", ErrInvalid)
		}
		changes[record.ID] = Change{ExpectedVersion: expected, Record: record}
		return nil
	}
	for index, edit := range input.Edits {
		id := edit.RecordID
		if id == "" {
			id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(current.DraftID+":"+fmt.Sprint(index)+":"+edit.Action)).String()
		}
		record := Record{ID: id}
		if edit.RecordID != "" {
			previous, exists := current.Records[id]
			if !exists {
				return publication, ErrNotFound
			}
			if previous.Version != edit.ExpectedVersion {
				return publication, ErrConflict
			}
			record = previous
		} else if edit.ExpectedVersion != 0 {
			return publication, ErrConflict
		}
		switch edit.Action {
		case "define":
			if record.Kind != "" && record.Definition == nil {
				return publication, ErrInvalid
			}
			record.Kind, record.Definition = edit.Kind, edit.Definition
		case "remove_override":
			if record.Override == nil || record.Retired {
				return publication, ErrInvalid
			}
			record.Retired = true
		default:
			if record.Kind != "" && record.Override == nil {
				return publication, ErrInvalid
			}
			record.Kind, record.Retired = OverrideKind, false
			record.Override = &Override{}
			switch edit.Action {
			case "classify":
				record.Override.Action, record.Override.DefinitionID, record.Override.Members = SetClassification, edit.DefinitionID, []SourceHandle{*edit.Source}
			case "pin_definition":
				record.Override.Action, record.Override.TargetID = PinDefinition, edit.DefinitionID
			default:
				record.Override.Action, record.Override.Members = OverrideAction(edit.Action), edit.Members
			}
			record.Sources, record.Dependencies, record.Fingerprint = nil, nil, ""
			for _, handle := range record.Override.Members {
				source, exists := current.Sources[SourceKey(handle)]
				if !exists || !source.Eligible || source.SourceHandle != handle {
					return publication, ErrSourceStale
				}
				fingerprint, err := SourceFingerprint(source)
				if err != nil {
					return publication, err
				}
				record.Sources = append(record.Sources, SourceDependency{SourceHandle: handle, Fingerprint: fingerprint})
			}
		}
		if err := add(record, edit.ExpectedVersion); err != nil {
			return publication, err
		}
	}
	for _, change := range sortedManagementChanges(changes) {
		if change.Record.Definition == nil {
			continue
		}
		pinned, explicitlyUnpinned := false, false
		for _, existing := range current.Records {
			if existing.Retired || existing.Override == nil || existing.Override.Action != PinDefinition || existing.Override.TargetID != change.Record.ID {
				continue
			}
			if changed, ok := changes[existing.ID]; ok && changed.Record.Retired {
				explicitlyUnpinned = true
				continue
			}
			pinned = true
		}
		for _, changed := range changes {
			pinned = pinned || (!changed.Record.Retired && changed.Record.Override != nil && changed.Record.Override.Action == PinDefinition && changed.Record.Override.TargetID == change.Record.ID)
		}
		if !pinned && !explicitlyUnpinned {
			pinID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(current.DraftID+":pin:"+change.Record.ID)).String()
			if err := add(Record{ID: pinID, Kind: OverrideKind, Override: &Override{Action: PinDefinition, TargetID: change.Record.ID}}, 0); err != nil {
				return publication, err
			}
		}
	}
	publication.Changes = sortedManagementChanges(changes)
	if _, err := PreparePublication(current.Records, current.Sources, publication, false); err != nil {
		return publication, err
	}
	if err := ValidateManagementOverrides(current.Records, publication); err != nil {
		return publication, err
	}
	return publication, nil
}

func sortedManagementChanges(changes map[string]Change) []Change {
	ids := make([]string, 0, len(changes))
	for id := range changes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]Change, 0, len(ids))
	for _, id := range ids {
		result = append(result, changes[id])
	}
	return result
}

func ValidateManagementOverrides(current map[string]Record, input Publication) error {
	catalog := make(map[string]Record, len(current)+len(input.Changes))
	for id, record := range current {
		catalog[id] = record
	}
	for _, change := range input.Changes {
		catalog[change.Record.ID] = change.Record
	}
	classifications, pins := map[string]string{}, map[string]bool{}
	parent := map[string]string{}
	var root func(string) string
	root = func(key string) string {
		if parent[key] == "" {
			parent[key] = key
		}
		if parent[key] != key {
			parent[key] = root(parent[key])
		}
		return parent[key]
	}
	var separations [][]SourceHandle
	for _, record := range catalog {
		if record.Retired || record.Override == nil {
			continue
		}
		override := record.Override
		switch override.Action {
		case PinDefinition:
			if pins[override.TargetID] {
				return fmt.Errorf("%w: duplicate definition pin", ErrInvalid)
			}
			pins[override.TargetID] = true
		case SetClassification:
			key := SourceKey(override.Members[0])
			if _, exists := classifications[key]; exists {
				return fmt.Errorf("%w: replace the existing classification explicitly", ErrInvalid)
			}
			classifications[key] = override.DefinitionID
		case GroupTogether:
			first := root(SourceKey(override.Members[0]))
			for _, member := range override.Members[1:] {
				parent[root(SourceKey(member))] = first
			}
		case KeepSeparate:
			separations = append(separations, override.Members)
		}
	}
	for _, members := range separations {
		seen := map[string]bool{}
		for _, member := range members {
			key := root(SourceKey(member))
			if seen[key] {
				return fmt.Errorf("%w: grouping contradicts a separation", ErrInvalid)
			}
			seen[key] = true
		}
	}
	return nil
}
