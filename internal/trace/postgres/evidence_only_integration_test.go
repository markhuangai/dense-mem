//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/memorypack"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEvidenceOnlyPostgresMemoryPackMaximumOmissionCardinality(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := t.Context()
	team := createLedgerTeam(t, admin, rls, "maximum-evidence-export")
	owner := createLedgerProfile(t, admin, rls, team, "export owner")
	store := knowledge.NewStore(app, rls)
	ids := make([]string, 0, 500)
	var originalIngest string
	for batch := range 25 {
		evidence := make([]knowledge.EvidenceInput, 20)
		for index := range evidence {
			evidence[index] = knowledge.EvidenceInput{Content: fmt.Sprintf("Exact export Evidence %d.", batch*20+index), ForceInsert: true}
		}
		if batch == 0 {
			evidence[0].InitialEvent = &knowledge.SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass", Reason: "accepted"}
		}
		key := fmt.Sprintf("export-batch-%d", batch)
		ingest, err := store.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: team, OwnerProfileID: owner, IdempotencyKey: key, RequestHash: key, Evidence: evidence})
		require.NoError(t, err)
		if batch == 0 {
			originalIngest = ingest.IngestID
		}
		for _, item := range ingest.Evidence {
			ids = append(ids, item.FragmentID)
		}
	}
	var space string
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT space_id::text,space_generation FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, team, ids[0]).Row().Scan(&space, &generation)
	}))
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: uuid.MustParse(team), OwnerID: uuid.MustParse(owner), Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(space), Kind: domain.MemorySpaceTeamShared, Generation: generation}}})
	require.NoError(t, rls.WithTeamProfileTx(actorCtx, app, team, owner, func(tx *gorm.DB) error {
		for range 99 {
			ingestID := uuid.NewString()
			if err := tx.Exec(`INSERT INTO knowledge_ingests
 SELECT (jsonb_populate_record(NULL::knowledge_ingests,to_jsonb(ingest)||jsonb_build_object('ingest_id',?::uuid,'idempotency_key',?::text))).*
 FROM knowledge_ingests ingest WHERE team_id=?::uuid AND ingest_id=?::uuid`, ingestID, "export-history:"+ingestID, team, originalIngest).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO evidence_occurrences
 SELECT (jsonb_populate_record(NULL::evidence_occurrences,to_jsonb(occurrence)||jsonb_build_object('occurrence_id',?::uuid,'ingest_id',?::uuid))).*
 FROM evidence_occurrences occurrence WHERE team_id=?::uuid AND occurrence_id=?::uuid`, uuid.NewString(), ingestID, team, ids[0]).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	exporter := memorypack.NewMemoryPackService(memorypack.MemoryPackDependencies{Semantic: New(app, rls)})
	active, err := registry.BuildActive(registry.Dependencies{MemoryPackBindings: registry.MemoryPackBindings{Service: exporter}})
	require.NoError(t, err)
	tool, ok := active.Get(registry.ToolExportMemoryPack)
	require.True(t, ok)
	output, err := tool.Invoke(actorCtx, team, map[string]any{"name": "Maximum Evidence export", "evidence_ids": ids, "include_evidence": false})
	require.NoError(t, err)
	require.NoError(t, registry.ValidateInput(registry.Tool{InputSchema: tool.OutputSchema}, output))
	omissions := output["omissions"].([]map[string]any)
	require.Len(t, omissions, 500)
	projected := make(map[string]string, len(omissions))
	for _, omission := range omissions {
		projected[omission["item_id"].(string)] = omission["reason"].(string)
		require.Contains(t, ids, omission["item_id"])
		require.Contains(t, omission["reason"], "evidence_content_excluded_by_request")
		if omission["item_id"] == ids[0] {
			require.Contains(t, omission["reason"], "decisions_max_events")
		}
	}
	var artifact memorypack.MemoryPackArtifact
	require.NoError(t, json.Unmarshal([]byte(output["artifact_json"].(string)), &artifact))
	require.Len(t, artifact.Evidence, 500)
	require.Greater(t, len(artifact.Omissions), 500)
	require.Contains(t, artifact.Omissions, ids[0]+":decisions_max_events")
	for _, omission := range artifact.Omissions {
		id, reason, scoped := strings.Cut(omission, ":")
		require.True(t, scoped)
		require.Contains(t, projected[id], reason)
	}
	for _, item := range artifact.Evidence {
		require.True(t, item.ContentOmitted)
		if item.FragmentID == ids[0] {
			require.Len(t, item.Occurrences, 100)
			require.Contains(t, item.Omissions, "decisions_max_events")
		}
	}
	hash := artifact.ContentSHA256
	artifact.ContentSHA256 = ""
	canonical, err := json.Marshal(artifact)
	require.NoError(t, err)
	digest := sha256.Sum256(canonical)
	require.Equal(t, hex.EncodeToString(digest[:]), hash)
	require.Equal(t, hash, output["content_sha256"])
}

func TestEvidenceOnlyPostgresTraceBudgetsAndMemoryPackCanonicalProvenance(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := t.Context()
	team := createLedgerTeam(t, admin, rls, "evidence-only-trace")
	owner := createLedgerProfile(t, admin, rls, team, "owner A")
	peer := createLedgerProfile(t, admin, rls, team, "reader B")
	store := knowledge.NewStore(app, rls)
	content := strings.Repeat("界🙂", 4000) + "尾"
	ingest, err := store.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: team, OwnerProfileID: owner, IdempotencyKey: "retained-long-evidence", RequestHash: "retained-long-evidence", Evidence: []knowledge.EvidenceInput{{Content: content, SourceKey: "document:trace", SourceRevisionToken: "revision-1", SourceRef: "document:trace", SourceRevisionEnvelope: map[string]any{"exact": "Original source envelope"}}}})
	require.NoError(t, err)
	id := ingest.Evidence[0].FragmentID
	var spaceID string
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT space_id::text,space_generation FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, team, id).Row().Scan(&spaceID, &generation)
	}))
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: uuid.MustParse(team), OwnerID: uuid.MustParse(peer), Grants: []string{"read"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(spaceID), Kind: domain.MemorySpaceTeamShared, Generation: generation}}})
	adapter := New(app, rls)
	path := filepath.Join(t.TempDir(), "trace-export.jsonl")
	output, err := os.Create(path)
	require.NoError(t, err)
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	logger := observability.NewWithProtector(slog.LevelDebug, observability.NewCredentialProtector("trace-export-credential-canary"))
	os.Stdout = previous
	service := traceapp.NewSemantic(adapter, logger)
	active, err := registry.BuildActive(registry.Dependencies{TraceBindings: registry.TraceBindings{Service: service}})
	require.NoError(t, err)
	tool, present := active.Get(registry.ToolTraceMemory)
	require.True(t, present)
	wire, err := tool.Invoke(actorCtx, team, map[string]any{"evidence_id": id})
	require.NoError(t, err)
	require.NoError(t, registry.ValidateInput(registry.Tool{InputSchema: tool.OutputSchema}, wire))
	require.Equal(t, []any{}, wire["lifecycle"])
	for _, maximum := range []int{1, 2000, 8000, 99999} {
		request := traceapp.TraceRequest{EvidenceID: id, MaxFragmentContentRunes: maximum}
		result, err := service.Trace(actorCtx, "", request)
		require.NoError(t, err)
		expected := min(maximum, 8000)
		require.Equal(t, string([]rune(content)[:expected]), result.Semantic.Evidence.Content)
		require.True(t, result.Semantic.Truncated)
		require.Contains(t, result.Semantic.Omissions, "evidence_content_max_runes")
		require.Equal(t, owner, result.Semantic.Evidence.OwnerProfileID)
	}
	no := false
	omitted, err := adapter.TraceEvidence(actorCtx, tracecontract.Input{TeamID: team, EvidenceID: id, IncludeEvidenceContent: &no, IncludeTransitions: &no})
	require.NoError(t, err)
	require.Empty(t, omitted.Evidence.Content)
	require.True(t, omitted.Evidence.ContentOmitted)
	require.False(t, omitted.Evidence.ContentTruncated)
	require.Contains(t, omitted.Omissions, "evidence_content_excluded_by_request")
	require.Contains(t, omitted.Omissions, "lifecycle_excluded_by_request")
	bounded, err := adapter.TraceEvidence(actorCtx, tracecontract.Input{TeamID: team, EvidenceID: id, MaxEvents: 1})
	require.NoError(t, err)
	require.LessOrEqual(t, len(bounded.Occurrences)+len(bounded.SourceRevisions)+len(bounded.Decisions)+len(bounded.Lifecycle)+len(bounded.SearchDocuments), 1)
	require.True(t, bounded.Truncated)
	require.Contains(t, bounded.Omissions, "source_revisions_max_events")
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	exporter := memorypack.NewMemoryPackService(memorypack.MemoryPackDependencies{Semantic: adapter, Logger: logger, Now: func() time.Time { return now }})
	request := memorypack.ExportRequest{Name: "Retained Evidence", EvidenceIDs: []string{id}}
	pack, err := exporter.Export(actorCtx, request)
	require.NoError(t, err)
	require.Equal(t, memorypack.MemoryPackFormat, pack.Artifact.Format)
	require.Equal(t, 1, pack.ItemCount)
	require.Len(t, pack.Artifact.Evidence[0].SourceRevisions, 1)
	require.Equal(t, "revision-1", pack.Artifact.Evidence[0].SourceRevisions[0].RevisionToken)
	require.Equal(t, map[string]any{"exact": "Original source envelope"}, pack.Artifact.Evidence[0].SourceRevisions[0].Envelope)
	require.Equal(t, owner, pack.Artifact.Evidence[0].OwnerProfileID)
	require.Contains(t, pack.Omissions, id+":evidence_content_max_runes")
	canonical := pack.Artifact
	canonical.ContentSHA256 = ""
	encoded, err := json.Marshal(canonical)
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	require.Equal(t, hex.EncodeToString(digest[:]), pack.SHA256)
	replayed, err := exporter.Export(actorCtx, request)
	require.NoError(t, err)
	require.Equal(t, pack.CanonicalJSON, replayed.CanonicalJSON)
	foreign := createLedgerTeam(t, admin, rls, "foreign trace")
	foreignOwner := createLedgerProfile(t, admin, rls, foreign, "reader C")
	foreignCtx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.MustParse(foreign), OwnerID: uuid.MustParse(foreignOwner), Grants: []string{"read"}})
	_, err = exporter.Export(foreignCtx, request)
	require.ErrorIs(t, err, tracecontract.ErrEvidenceNotFound)
	require.NoError(t, output.Close())
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(body), "trace-export-credential-canary")
	traced, exported, failed := 0, 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		require.Equal(t, "DEBUG", record["level"])
		switch record["msg"] {
		case "trace_evidence_selection":
			traced++
			require.Equal(t, id, record["evidence_id"])
			require.Equal(t, team, record["team_id"])
			require.NotEmpty(t, record["limits"])
			require.NotEmpty(t, record["selection"])
			require.NotEmpty(t, record["omissions"])
			require.Equal(t, "completed", record["outcome"])
		case "memory_pack_evidence_selection":
			require.Equal(t, []any{id}, record["requested_evidence_ids"])
			require.Equal(t, float64(100), record["max_events"])
			require.Equal(t, float64(8000), record["max_content_runes"])
			if record["outcome"] == "failed" {
				failed++
				require.NotEmpty(t, record["reason"])
			} else {
				exported++
				require.NotEmpty(t, record["omissions"])
				require.Equal(t, pack.SHA256, record["content_sha256"])
			}
		}
	}
	require.Equal(t, 5, traced)
	require.Equal(t, 2, exported)
	require.Equal(t, 1, failed)

	t.Run("historical_alias_lifecycle", func(t *testing.T) {
		aliases := []string{uuid.NewString(), uuid.NewString()}
		credentials := accesspostgres.NewCredentialRepository(app, rls, nil)
		credential, err := credentials.GetByIDForTeam(ctx, uuid.MustParse(team), uuid.MustParse(owner))
		require.NoError(t, err)
		require.NotNil(t, credential)
		ownerCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: credential.TeamID, OwnerID: credential.OwnerID, IdentityID: credential.ActorIdentityID, MembershipID: credential.MembershipID, CredentialID: &credential.ID, Grants: credential.Scopes, AllowedSpaces: credential.MemorySpaces()})
		peerCredential, err := credentials.GetByIDForTeam(ctx, uuid.MustParse(team), uuid.MustParse(peer))
		require.NoError(t, err)
		require.NotNil(t, peerCredential)
		peerCtx := requestctx.WithActor(ctx, requestctx.Actor{TeamID: peerCredential.TeamID, OwnerID: peerCredential.OwnerID, IdentityID: peerCredential.ActorIdentityID, MembershipID: peerCredential.MembershipID, CredentialID: &peerCredential.ID, Grants: peerCredential.Scopes, AllowedSpaces: peerCredential.MemorySpaces()})
		require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
			for index, alias := range aliases {
				if err := tx.Exec(`INSERT INTO evidence_occurrences
 SELECT (jsonb_populate_record(NULL::evidence_occurrences,to_jsonb(occurrence)||jsonb_build_object('occurrence_id',?::uuid,'evidence_index',?::int))).*
 FROM evidence_occurrences occurrence WHERE team_id=?::uuid AND occurrence_id=?::uuid`, alias, index+1, team, id).Error; err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO evidence_fragments
 SELECT (jsonb_populate_record(NULL::evidence_fragments,to_jsonb(fragment)||jsonb_build_object('fragment_id',?::uuid,'evidence_index',?::int))).*
 FROM evidence_fragments fragment WHERE team_id=?::uuid AND fragment_id=?::uuid`, alias, index+1, team, id).Error; err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO evidence_exact_aliases(team_id,alias_fragment_id,alias_owner_profile_id,canonical_fragment_id,canonical_owner_profile_id,space_id,space_generation)
 SELECT team_id,?::uuid,owner_profile_id,fragment_id,owner_profile_id,space_id,space_generation
 FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, alias, team, id).Error; err != nil {
					return err
				}
			}
			return nil
		}))
		_, err = store.RetractEvidence(peerCtx, knowledge.RetractEvidenceInput{TeamID: team, OwnerProfileID: peer, EvidenceIDs: []string{aliases[0]}, Reason: "Foreign owner", IdempotencyKey: "alias-peer-retraction", RequestHash: "alias-peer-retraction"})
		require.Error(t, err)
		retracted, err := store.RetractEvidence(ownerCtx, knowledge.RetractEvidenceInput{TeamID: team, OwnerProfileID: owner, EvidenceIDs: []string{aliases[0]}, Reason: "Historical alias retraction", IdempotencyKey: "alias-retraction", RequestHash: "alias-retraction"})
		require.NoError(t, err)
		require.Equal(t, "completed", retracted.ProcessingState)
		replacement, err := store.CreateIngestForTest(ownerCtx, knowledge.CreateIngestInput{TeamID: team, OwnerProfileID: owner, SpaceID: spaceID, SpaceGeneration: generation, IdempotencyKey: "alias-replacement", RequestHash: "alias-replacement", Evidence: []knowledge.EvidenceInput{{Content: "Replacement for a historical alias.", SupersedesEvidenceIDs: []string{aliases[1]}}}})
		require.NoError(t, err)
		for _, evidenceID := range append([]string{id}, aliases...) {
			traced, err := adapter.TraceEvidence(actorCtx, tracecontract.Input{TeamID: team, EvidenceID: evidenceID})
			require.NoError(t, err)
			require.Equal(t, id, traced.Evidence.FragmentID)
			require.Len(t, traced.Lifecycle, 2)
			events := map[string]tracecontract.TraceEvidenceLifecycleEvent{}
			for _, event := range traced.Lifecycle {
				events[event.TargetFragmentID] = event
			}
			require.Equal(t, "retract", events[aliases[0]].Action)
			require.Equal(t, "supersede", events[aliases[1]].Action)
			require.Equal(t, replacement.Evidence[0].FragmentID, events[aliases[1]].ReplacementFragmentID)
			_, err = adapter.TraceEvidence(foreignCtx, tracecontract.Input{TeamID: foreign, EvidenceID: evidenceID})
			require.ErrorIs(t, err, tracecontract.ErrEvidenceNotFound)
		}
		withoutTransitions, err := adapter.TraceEvidence(actorCtx, tracecontract.Input{TeamID: team, EvidenceID: aliases[0], IncludeTransitions: &no})
		require.NoError(t, err)
		require.Empty(t, withoutTransitions.Lifecycle)
		require.Contains(t, withoutTransitions.Omissions, "lifecycle_excluded_by_request")
		limited, err := adapter.TraceEvidence(actorCtx, tracecontract.Input{TeamID: team, EvidenceID: aliases[0], MaxEvents: 1})
		require.NoError(t, err)
		require.LessOrEqual(t, len(limited.Occurrences)+len(limited.SourceRevisions)+len(limited.Decisions)+len(limited.Lifecycle)+len(limited.SearchDocuments), 1)
		require.True(t, limited.Truncated)
		aliasExporter := memorypack.NewMemoryPackService(memorypack.MemoryPackDependencies{Semantic: adapter})
		aliasPack, err := aliasExporter.Export(actorCtx, memorypack.ExportRequest{Name: "Historical alias history", EvidenceIDs: append([]string{id}, aliases...)})
		require.NoError(t, err)
		require.Len(t, aliasPack.Artifact.Evidence, 1)
		require.Len(t, aliasPack.Artifact.Evidence[0].Lifecycle, 2)
	})
}
