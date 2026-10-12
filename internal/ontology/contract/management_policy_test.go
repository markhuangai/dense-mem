package contract

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestManagementDefinitionEditsPinAndValidate(t *testing.T) {
	context := ManagementContext{DraftID: uuid.NewString(), Records: map[string]Record{}, Sources: map[string]SourceSnapshot{}}
	input := SaveDraftInput{OperationKey: "definition", Reason: "Correct catalog label", Edits: []ManagementEdit{{Action: "define", Kind: Topic, Definition: &Definition{Key: "storage", Label: "Storage", Description: "Durable data"}}}}
	publication, err := CompileManagementEdits(input, context)
	require.NoError(t, err)
	require.Len(t, publication.Changes, 2)
	var definition, pin Record
	for _, change := range publication.Changes {
		if change.Record.Definition != nil {
			definition = change.Record
		} else {
			pin = change.Record
		}
	}
	require.Equal(t, definition.ID, pin.Override.TargetID)
	require.Equal(t, PinDefinition, pin.Override.Action)
	replay, err := CompileManagementEdits(input, context)
	require.NoError(t, err)
	require.Equal(t, publication, replay)
	context.Records[definition.ID] = Record{ID: definition.ID, Version: 1, Kind: Topic, Definition: definition.Definition}
	_, err = CompileManagementEdits(input, context)
	require.ErrorIs(t, err, ErrConflict)
	context.Revision = 1
	_, err = CompileManagementEdits(input, context)
	require.ErrorIs(t, err, ErrConflict)
}

func TestManagementCommandsRejectAmbiguousShapes(t *testing.T) {
	id := uuid.NewString()
	handle := SourceHandle{Kind: EvidenceSource, ID: id, Version: 1}
	for _, edit := range []ManagementEdit{
		{Action: "unknown"}, {Action: "define", Kind: Topic, Definition: &Definition{Key: "valid", Label: "Valid"}, Members: []SourceHandle{handle}},
		{Action: "classify", Source: &handle, DefinitionID: id, Definition: &Definition{Key: "extra", Label: "Extra"}},
		{Action: "group_together", Members: []SourceHandle{handle, handle}},
		{Action: "remove_override", RecordID: id}, {Action: "pin_definition", DefinitionID: id, Kind: Topic},
	} {
		require.ErrorIs(t, ValidateSaveDraft(SaveDraftInput{OperationKey: "test", Reason: "Explain correction", Edits: []ManagementEdit{edit}}), ErrInvalid)
	}
}

func TestManagementGroupingContradictionsAreTransitive(t *testing.T) {
	a, b, c := SourceHandle{Kind: EvidenceSource, ID: uuid.NewString(), Version: 1}, SourceHandle{Kind: EvidenceSource, ID: uuid.NewString(), Version: 1}, SourceHandle{Kind: EvidenceSource, ID: uuid.NewString(), Version: 1}
	record := func(action OverrideAction, members ...SourceHandle) Record {
		return Record{ID: uuid.NewString(), Kind: OverrideKind, Override: &Override{Action: action, Members: members}}
	}
	ab, bc, separate := record(GroupTogether, a, b), record(GroupTogether, b, c), record(KeepSeparate, a, c)
	current := map[string]Record{ab.ID: ab, bc.ID: bc}
	err := ValidateManagementOverrides(current, Publication{Changes: []Change{{Record: separate}}})
	require.ErrorIs(t, err, ErrInvalid)
	ab.Retired = true
	bc.Retired = true
	require.NoError(t, ValidateManagementOverrides(current, Publication{Changes: []Change{{Record: ab}, {Record: bc}, {Record: separate}}}))
}

func TestManagementDefinitionEditHonorsExplicitUnpin(t *testing.T) {
	id, pinID := uuid.NewString(), uuid.NewString()
	definition := Record{ID: id, Version: 1, Kind: Topic, Definition: &Definition{Key: "storage", Label: "Storage"}}
	pin := Record{ID: pinID, Version: 1, Kind: OverrideKind, Override: &Override{Action: PinDefinition, TargetID: id}}
	current := ManagementContext{DraftID: uuid.NewString(), Revision: 1, Records: map[string]Record{id: definition, pinID: pin}, Sources: map[string]SourceSnapshot{}}
	input := SaveDraftInput{OperationKey: "edit-unpin", ExpectedRevision: 1, Reason: "Edit and explicitly unpin", Edits: []ManagementEdit{
		{Action: "define", RecordID: id, ExpectedVersion: 1, Kind: Topic, Definition: &Definition{Key: "storage", Label: "Storage revised"}},
		{Action: "remove_override", RecordID: pinID, ExpectedVersion: 1},
	}}
	publication, err := CompileManagementEdits(input, current)
	require.NoError(t, err)
	require.Len(t, publication.Changes, 2)
	for _, change := range publication.Changes {
		if change.Record.Override != nil {
			require.Equal(t, pinID, change.Record.ID)
			require.True(t, change.Record.Retired)
		}
	}
}
