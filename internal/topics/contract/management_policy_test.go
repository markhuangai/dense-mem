package contract

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestManagementDefinitionEditsPublishOnlyTheTopic(t *testing.T) {
	context := ManagementContext{DraftID: uuid.NewString(), Records: map[string]Record{}, Sources: map[string]SourceSnapshot{}}
	input := SaveDraftInput{OperationKey: "definition", Reason: "Correct catalog label", Edits: []ManagementEdit{{Action: "define", Kind: Topic, Definition: &Definition{Key: "storage", Label: "Storage", Description: "Durable data"}}}}
	publication, err := CompileManagementEdits(input, context)
	require.NoError(t, err)
	require.Len(t, publication.Changes, 1)
	definition := publication.Changes[0].Record
	require.Nil(t, definition.Override)
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

func TestManagementRejectsRetiredOverrideActions(t *testing.T) {
	id := uuid.NewString()
	a := SourceHandle{Kind: EvidenceSource, ID: uuid.NewString(), Version: 1}
	b := SourceHandle{Kind: EvidenceSource, ID: uuid.NewString(), Version: 1}
	for _, edit := range []ManagementEdit{
		{Action: "pin_definition", DefinitionID: id},
		{Action: "remove_override", RecordID: id, ExpectedVersion: 1},
		{Action: "classify", Source: &a, DefinitionID: id},
		{Action: "group_together", Members: []SourceHandle{a, b}},
		{Action: "keep_separate", Members: []SourceHandle{a, b}},
	} {
		require.ErrorIs(t, ValidateSaveDraft(SaveDraftInput{OperationKey: "retired", Reason: "Historical correction", Edits: []ManagementEdit{edit}}), ErrInvalid)
	}
}
