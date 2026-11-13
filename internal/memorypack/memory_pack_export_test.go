package memorypack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type exportSemanticStub struct {
	trace  *tracecontract.EvidenceTraceResult
	err    error
	inputs []tracecontract.Input
}

func (s *exportSemanticStub) TraceEvidence(_ context.Context, input tracecontract.Input) (*tracecontract.EvidenceTraceResult, error) {
	s.inputs = append(s.inputs, input)
	return s.trace, s.err
}
func exportFixture() (context.Context, *exportSemanticStub, MemoryPackService, string) {
	team, owner := uuid.New(), uuid.New()
	id := uuid.NewString()
	content := "  exact café 🧭 Evidence.\n"
	hash := sha256.Sum256([]byte(content))
	reader := &exportSemanticStub{trace: &tracecontract.EvidenceTraceResult{Evidence: &tracecontract.TraceEvidenceFragment{TeamID: team.String(), FragmentID: id, Content: content, ContentHash: hex.EncodeToString(hash[:]), OwnerProfileID: owner.String()}, Occurrences: []tracecontract.TraceEvidenceFragment{}, Omissions: []string{}}}
	return requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: team, OwnerID: owner}), reader, NewMemoryPackService(MemoryPackDependencies{Semantic: reader, Now: func() time.Time { return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) }}), id
}

func TestEvidenceOnlyUnitMemoryPackRejectsInvalidRootsAndUnavailableEvidence(t *testing.T) {
	ctx, reader, service, id := exportFixture()
	for _, req := range []ExportRequest{{EvidenceIDs: []string{id}}, {Name: "pack"}, {Name: "pack", EvidenceIDs: []string{id, id}}, {Name: "pack", EvidenceIDs: []string{"invalid"}}, {Name: "pack", EvidenceIDs: make([]string, 501)}} {
		_, err := service.Export(ctx, req)
		require.Error(t, err)
	}
	require.Empty(t, reader.inputs)
	_, err := service.Export(context.Background(), ExportRequest{Name: "pack", EvidenceIDs: []string{id}})
	require.ErrorIs(t, err, ErrMemoryPackAuthContext)
	reader.trace.Evidence.TeamID = uuid.NewString()
	_, err = service.Export(ctx, ExportRequest{Name: "pack", EvidenceIDs: []string{id}})
	require.ErrorIs(t, err, tracecontract.ErrEvidenceNotFound)
	cause := errors.New("database unavailable")
	reader.err = cause
	_, err = service.Export(ctx, ExportRequest{Name: "pack", EvidenceIDs: []string{id}})
	require.ErrorIs(t, err, cause)
}

func TestEvidenceOnlyUnitMemoryPackExportsExactEvidenceProvenanceAndCanonicalHash(t *testing.T) {
	ctx, reader, service, id := exportFixture()
	reader.trace.Omissions = []string{"decisions_max_events"}
	reader.trace.Truncated = true
	result, err := service.Export(ctx, ExportRequest{Name: " Team Pack ", EvidenceIDs: []string{id}, IncludeEvidence: new(true)})
	require.NoError(t, err)
	require.Equal(t, MemoryPackFormat, result.Artifact.Format)
	require.Equal(t, 1, result.ItemCount)
	require.Equal(t, "team-pack.memory-pack.json", result.Filename)
	require.Equal(t, reader.trace.Evidence.Content, result.Artifact.Evidence[0].Content)
	require.Equal(t, reader.trace.Evidence.OwnerProfileID, result.Artifact.Evidence[0].OwnerProfileID)
	var wire MemoryPackArtifact
	require.NoError(t, json.Unmarshal([]byte(result.CanonicalJSON), &wire))
	require.Equal(t, result.SHA256, wire.ContentSHA256)
	canonical, hash, err := canonicalMemoryPackArtifact(wire)
	require.NoError(t, err)
	require.Equal(t, result.SHA256, hash)
	require.NotContains(t, string(canonical), result.SHA256)
	require.Equal(t, []string{id + ":decisions_max_events"}, result.Omissions)
	require.Equal(t, []string{"decisions_max_events"}, wire.Evidence[0].Omissions)
	require.Equal(t, 100, reader.inputs[0].MaxEvents)
	require.Equal(t, 16384, reader.inputs[0].MaxFragmentContentRunes)
	require.True(t, *reader.inputs[0].IncludeEvidenceContent)
}

func TestEvidenceOnlyUnitMemoryPackCoalescesCanonicalEvidenceAfterAuthorizingEveryRoot(t *testing.T) {
	ctx, reader, service, canonical := exportFixture()
	alias := uuid.NewString()
	result, err := service.Export(ctx, ExportRequest{Name: "Historical aliases", EvidenceIDs: []string{canonical, alias}})
	require.NoError(t, err)
	require.Equal(t, 1, result.ItemCount)
	require.Equal(t, canonical, result.Artifact.Evidence[0].FragmentID)
	require.Equal(t, reader.trace.Evidence.Content, result.Artifact.Evidence[0].Content)
	require.Len(t, reader.inputs, 2)
	require.ElementsMatch(t, []string{canonical, alias}, []string{reader.inputs[0].EvidenceID, reader.inputs[1].EvidenceID})
}
