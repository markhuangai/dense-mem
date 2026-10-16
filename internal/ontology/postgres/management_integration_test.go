//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/httperr"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyManagementHTTPPersistedWorkflow(t *testing.T) {
	f := newOntologyFixture(t)
	repository := access.NewCredentialRepository(f.app, f.rls, nil)
	keys := make([]string, 2)
	for index := range keys {
		key, err := crypto.GenerateRawKey()
		require.NoError(t, err)
		hash, err := crypto.HashKey(key)
		require.NoError(t, err)
		credential := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "http-manager-" + uuid.NewString(), Role: "manager", Scopes: []string{"read", "write"}, KeyHash: hash, KeyPrefix: crypto.GetKeyPrefix(key), KeySuffix: crypto.GetKeySuffix(key)}
		require.NoError(t, repository.CreateCredential(context.Background(), credential))
		keys[index] = key
	}
	evidence := f.evidence(t, 0, "Atlas uses PostgreSQL.")
	before := f.canonicalSnapshot(t)
	router := echo.New()
	router.HTTPErrorHandler = httperr.ErrorHandler
	densehttp.RegisterUserPortal(router, densehttp.UserPortalDeps{
		OntologyManagement: managementService(f), CredentialRepo: repository,
		CredentialVerifier: crypto.NewArgon2Verifier(0), CredentialLookupPrefixes: crypto.GetLookupPrefixes,
		RateLimitSvc: accessservice.NewRateLimitService(inmem.NewInMemoryRateLimitStore()),
		Config:       &config.Config{RateLimitPerMinute: 100},
	})
	request := func(owner int, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded := []byte{}
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req := httptest.NewRequest(method, "/ui/api/ontology"+path, strings.NewReader(string(encoded)))
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+keys[owner])
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder, target any) {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "no-store", rec.Header().Get(echo.HeaderCacheControl))
		var envelope struct{ Data json.RawMessage }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		require.NoError(t, json.Unmarshal(envelope.Data, target))
	}
	var status ontology.TeamOntologyStatus
	decode(request(0, http.MethodGet, "/status", nil), &status)
	require.Zero(t, status.Revision)
	var sources ontology.ManagementSourcePage
	decode(request(0, http.MethodGet, "/sources?q="+evidence.Evidence[0].FragmentID, nil), &sources)
	require.Len(t, sources.Sources, 1)
	require.Equal(t, evidence.Evidence[0].FragmentID, sources.Sources[0].ID)
	input := managementInput(status.Revision, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "http-storage", Label: "Storage", Description: "Durable storage"}})
	var draft ontology.ManagementDraft
	decode(request(0, http.MethodPost, "/drafts", input), &draft)
	var drafts ontology.DraftPage
	decode(request(0, http.MethodGet, "/drafts", nil), &drafts)
	require.Equal(t, []ontology.ManagementDraft{draft}, drafts.Drafts)
	var stored ontology.ManagementDraft
	decode(request(0, http.MethodGet, "/drafts/"+draft.ID, nil), &stored)
	require.Equal(t, draft, stored)
	require.Equal(t, http.StatusNotFound, request(1, http.MethodGet, "/drafts/"+draft.ID, nil).Code)
	input.ExpectedVersion, input.OperationKey, input.Reason = draft.Version, uuid.NewString(), "Reviewed HTTP correction"
	decode(request(0, http.MethodPut, "/drafts/"+draft.ID, input), &draft)
	require.Equal(t, int64(2), draft.Version)
	input.OperationKey = uuid.NewString()
	require.Equal(t, http.StatusConflict, request(0, http.MethodPut, "/drafts/"+draft.ID, input).Code)
	decode(request(0, http.MethodGet, "/drafts/"+draft.ID, nil), &stored)
	require.Equal(t, draft, stored)
	var preview ontology.ImpactPreview
	decode(request(0, http.MethodPost, "/drafts/"+draft.ID+"/preview", map[string]any{"expected_version": draft.Version}), &preview)
	require.True(t, preview.Complete)
	require.Equal(t, 2, preview.ChangedRecords)
	decode(request(0, http.MethodPost, "/drafts/"+draft.ID+"/publish", managementCommand(draft)), &draft)
	require.Equal(t, "published", draft.State)
	require.NotNil(t, draft.Publication)
	var publications ontology.ManagementPublicationPage
	decode(request(0, http.MethodGet, "/publications", nil), &publications)
	require.Len(t, publications.Publications, 1)
	var publication ontology.ManagementPublication
	decode(request(0, http.MethodGet, "/publications/"+draft.Publication.ID, nil), &publication)
	require.Len(t, publication.RecordsView, 2)
	var records ontology.ManagementRecordPage
	decode(request(0, http.MethodGet, "/records?kind=topic", nil), &records)
	require.Len(t, records.Records, 1)
	var record ontology.RecordView
	decode(request(0, http.MethodGet, "/records/"+records.Records[0].ID+"?version=1", nil), &record)
	require.Equal(t, "http-storage", record.Definition.Key)
	var rollback ontology.PublicationResult
	decode(request(0, http.MethodPost, "/publications/"+publication.ID+"/rollback", map[string]any{"operation_key": uuid.NewString(), "expected_revision": publication.Revision, "reason": "Reverse HTTP correction"}), &rollback)
	require.NotEqual(t, publication.ID, rollback.ID)
	decode(request(0, http.MethodGet, "/publications/"+rollback.ID, nil), &publication)
	require.Equal(t, draft.Publication.ID, publication.RollbackOf)
	input = managementInput(rollback.Revision, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "discard-http", Label: "Discarded"}})
	var discarded ontology.ManagementDraft
	decode(request(0, http.MethodPost, "/drafts", input), &discarded)
	decode(request(0, http.MethodPost, "/drafts/"+discarded.ID+"/discard", managementCommand(discarded)), &discarded)
	require.Equal(t, "discarded", discarded.State)
	decode(request(0, http.MethodGet, "/drafts/"+discarded.ID, nil), &stored)
	require.Equal(t, discarded, stored)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyManagementDraftIsolationReplayAndRLS(t *testing.T) {
	f := newOntologyFixture(t)
	managementOwnerC(t, f)
	service := managementService(f)
	ctx := f.actor(0, "manager")
	before := f.canonicalSnapshot(t)
	input := managementInput(0, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "storage", Label: "Storage", Description: "Durable storage"}})
	draft, err := service.SaveDraft(ctx, "", input)
	require.NoError(t, err)
	stored, err := managementService(f).Draft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, draft, stored)
	replayed, err := service.SaveDraft(ctx, "", input)
	require.NoError(t, err)
	require.Equal(t, draft, replayed)
	changed := input
	changed.Reason = "Changed retry"
	_, err = service.SaveDraft(ctx, "", changed)
	require.ErrorIs(t, err, ontology.ErrConflict)
	_, err = service.Draft(f.actor(1, "manager"), draft.ID)
	require.ErrorIs(t, err, ontology.ErrNotFound)
	_, err = service.Draft(f.actor(2, "member"), draft.ID)
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	actor, _ := requestctx.ActorFromContext(ctx)
	actor.Grants = []string{"read"}
	readOnly := requestctx.WithActor(context.Background(), actor)
	_, err = service.Draft(readOnly, draft.ID)
	require.NoError(t, err)
	_, err = service.CommandDraft(readOnly, draft.ID, "publish", managementCommand(draft))
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	foreign := maintenanceOtherTeam(t, f)
	_, err = service.Draft(foreign.actor(0, "manager"), draft.ID)
	require.ErrorIs(t, err, ontology.ErrNotFound)
	var count, forced int
	require.NoError(t, f.rls.WithTeamProfileReadOnlyRepeatableTx(ctx, f.app, f.team, f.owners[1], func(tx *gorm.DB) error { return tx.Raw(`SELECT count(*) FROM ontology_draft_heads`).Row().Scan(&count) }))
	require.Zero(t, count)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM pg_class WHERE relname IN ('ontology_draft_heads','ontology_draft_revisions') AND relforcerowsecurity AND relrowsecurity`).Row().Scan(&forced))
	require.Equal(t, 2, forced)
	require.Error(t, f.admin.Exec(`UPDATE ontology_draft_revisions SET version=version+1`).Error)
	migrator := ontologyMigrationProvider(t, f.admin)
	require.NoError(t, rollbackOntologySchemaTo(t, f.admin, 20261011220000))
	_, err = migrator.ApplyVersion(context.Background(), 20261011220000, false)
	require.ErrorContains(t, err, "populated ontology draft history")
	preview, err := service.Preview(ctx, draft.ID, draft.Version)
	require.NoError(t, err)
	require.Equal(t, 2, preview.ChangedRecords)
	require.Empty(t, preview.Examples)
	published, err := service.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	require.Equal(t, "published", published.State)
	require.NotNil(t, published.Publication)
	input.ExpectedVersion = draft.Version
	input.OperationKey = uuid.NewString()
	_, err = service.SaveDraft(ctx, draft.ID, input)
	require.ErrorIs(t, err, ontology.ErrConflict)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyManagementAuditFailureIsAtomic(t *testing.T) {
	f := newOntologyFixture(t)
	service := managementService(f)
	ctx := f.actor(0, "manager")
	draft, err := service.SaveDraft(ctx, "", managementInput(0, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "atomic", Label: "Atomic", Description: "Audit atomicity"}}))
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	require.NoError(t, f.admin.Exec(`CREATE FUNCTION reject_management_publication_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation='ONTOLOGY_PUBLISH' THEN RAISE EXCEPTION 'fixture audit rejection'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_management_publication_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_management_publication_audit()`).Error)
	command := managementCommand(draft)
	_, err = service.CommandDraft(ctx, draft.ID, "publish", command)
	require.ErrorIs(t, err, ontology.ErrAuditUnavailable)
	stored, err := service.Draft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, draft, stored)
	page, err := service.Publications(ctx, 0, 20)
	require.NoError(t, err)
	require.Empty(t, page.Publications)
	require.NoError(t, f.admin.Exec(`DROP TRIGGER reject_management_publication_audit ON audit_log`).Error)
	published, err := service.CommandDraft(ctx, draft.ID, "publish", command)
	require.NoError(t, err)
	replayed, err := service.CommandDraft(ctx, draft.ID, "publish", command)
	require.NoError(t, err)
	require.Equal(t, published, replayed)
	var count int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE team_id=?::uuid AND operation='ONTOLOGY_PUBLISH'`, f.team).Row().Scan(&count))
	require.Equal(t, 1, count)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyManagementCompetingDraftsAndPublications(t *testing.T) {
	f := newOntologyFixture(t)
	service := managementService(f)
	ctx := f.actor(0, "manager")
	input := managementInput(0, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "races", Label: "Races"}})
	var group sync.WaitGroup
	drafts := make([]ontology.ManagementDraft, 2)
	errs := make([]error, 2)
	for i := range 2 {
		group.Add(1)
		go func(index int) { defer group.Done(); drafts[index], errs[index] = service.SaveDraft(ctx, "", input) }(i)
	}
	group.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, drafts[0], drafts[1])
	input2 := managementInput(0, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "other", Label: "Other"}})
	second, err := service.SaveDraft(f.actor(1, "manager"), "", input2)
	require.NoError(t, err)
	drafts[1] = second
	for i := range 2 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, errs[index] = service.CommandDraft(f.actor(index, "manager"), drafts[index].ID, "publish", managementCommand(drafts[index]))
		}(i)
	}
	group.Wait()
	if errs[0] == nil {
		require.ErrorIs(t, errs[1], ontology.ErrConflict)
	} else {
		require.ErrorIs(t, errs[0], ontology.ErrConflict)
		require.NoError(t, errs[1])
	}
	page, err := service.Publications(ctx, 0, 20)
	require.NoError(t, err)
	require.Len(t, page.Publications, 1)
}

func TestOntologyManagementPrivateForeignAndStaleSources(t *testing.T) {
	f := newOntologyFixture(t)
	service := managementService(f)
	ctx := f.actor(0, "manager")
	private := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private", KeyHash: "synthetic-private", KeyPrefix: strings.ReplaceAll(uuid.NewString(), "-", "")[:24], KeySuffix: "privat", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), private))
	privateContext := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: private.MemorySpaceGeneration}})
	privateEvidence, err := f.knowledge.CreateIngestForTest(privateContext, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private canary"}}})
	require.NoError(t, err)
	foreign := maintenanceOtherTeam(t, f)
	foreignEvidence := foreign.evidence(t, 0, "Foreign canary")
	shared := f.evidence(t, 1, "Atlas uses PostgreSQL.")
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: shared.Evidence[0].FragmentID, Version: 1}
	topic := testTopic("storage")
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("seed", 0, ontology.Change{Record: topic}))
	require.NoError(t, err)
	for _, id := range []string{privateEvidence.Evidence[0].FragmentID, foreignEvidence.Evidence[0].FragmentID} {
		source := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: id, Version: 1}
		_, err = service.SaveDraft(ctx, "", managementInput(1, ontology.ManagementEdit{Action: "classify", Source: &source, DefinitionID: topic.ID}))
		require.ErrorIs(t, err, ontology.ErrSourceStale)
		page, err := service.Sources(ctx, ontology.EvidenceSource, "", id, 50)
		require.NoError(t, err)
		require.Empty(t, page.Sources)
	}
	draft, err := service.SaveDraft(ctx, "", managementInput(1, ontology.ManagementEdit{Action: "classify", Source: &handle, DefinitionID: topic.ID}))
	require.NoError(t, err)
	_, err = f.knowledge.RetractEvidence(f.actor(1, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[1], EvidenceIDs: []string{handle.ID}, IdempotencyKey: uuid.NewString(), RequestHash: testHash("retract"), Reason: "Source retired"})
	require.NoError(t, err)
	_, err = service.Preview(ctx, draft.ID, draft.Version)
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	_, err = service.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.ErrorIs(t, err, ontology.ErrSourceStale)
}
