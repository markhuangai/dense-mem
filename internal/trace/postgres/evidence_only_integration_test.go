//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/memorypack"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
	require.Equal(t, 4, traced)
	require.Equal(t, 2, exported)
	require.Equal(t, 1, failed)

}
