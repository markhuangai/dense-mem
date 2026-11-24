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
			if edit.Definition == nil || edit.Kind != Topic {
				return ErrInvalid
			}
			if err := validateDefinition(edit.Kind, *edit.Definition); err != nil {
				return err
			}
			shape.Definition, shape.Kind = nil, ""
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
		return publication, PublicationVersionRejection("catalog_revision", "", input.ExpectedRevision, current.Revision)
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
				return publication, PublicationVersionRejection("record_version", id, edit.ExpectedVersion, previous.Version)
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
		}
		if err := add(record, edit.ExpectedVersion); err != nil {
			return publication, err
		}
	}
	publication.Changes = sortedManagementChanges(changes)
	if _, err := PreparePublication(current.Records, current.Sources, publication, false); err != nil {
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
