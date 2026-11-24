package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpg "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	application "github.com/markhuangai/dense-mem/internal/topics"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func managementService(f *ontologyFixture) *application.ManagementService {
	return application.NewManagementService(f.store, auditapp.New(auditpg.NewStore(f.app, f.rls)))
}
func managementInput(revision int64, edits ...topic.ManagementEdit) topic.SaveDraftInput {
	return topic.SaveDraftInput{OperationKey: uuid.NewString(), ExpectedRevision: revision, Reason: "Correct shared ontology organization", Edits: edits}
}
func managementCommand(draft topic.ManagementDraft) topic.DraftCommand {
	return topic.DraftCommand{OperationKey: uuid.NewString(), ExpectedVersion: draft.Version, ExpectedRevision: draft.BaseRevision, Reason: draft.Reason}
}
func managementOwnerC(t *testing.T, f *ontologyFixture) {
	t.Helper()
	credential := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "management-reader-c", KeyHash: "synthetic-" + uuid.NewString(), KeyPrefix: uuid.NewString()[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), credential))
	f.owners = append(f.owners, credential.ID.String())
	f.credentials = append(f.credentials, credential)
}
