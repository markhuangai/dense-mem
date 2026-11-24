package recall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/observability"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"github.com/stretchr/testify/require"
)

type bridgeReadFixture struct {
	*retrievalReadFixture
	paths       []recallcontract.EvidenceBridge
	bridgeError error
	seedIDs     []string
	hydratedIDs []string
}

func (f *bridgeReadFixture) ReadEvidenceBridges(_ context.Context, _ recallcontract.RecallEvidenceInput, _ *searchcontract.ActiveSearchContract, seeds []string) ([]recallcontract.EvidenceBridge, error) {
	f.seedIDs = seeds
	return f.paths, f.bridgeError
}

func (f *bridgeReadFixture) HydrateEvidence(_ context.Context, _ recallcontract.RecallEvidenceInput, _ *searchcontract.ActiveSearchContract, candidates []string) (map[string]recallcontract.RecallEvidenceHit, error) {
	f.hydratedIDs = candidates
	hits := make(map[string]recallcontract.RecallEvidenceHit)
	for _, id := range candidates {
		if hit, authorized := f.evidence[id]; authorized {
			hits[id] = hit
		}
	}
	return hits, nil
}

func TestRecallExpansionReservesHydrationForAuthorizedPeers(t *testing.T) {
	team, peer, hidden, known := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	contextText := strings.Repeat("é", 600) + " zirconium " + strings.Repeat("λ", 600)
	batch := &recallcontract.RecallCandidateBatch{SearchState: "current"}
	f := &bridgeReadFixture{retrievalReadFixture: &retrievalReadFixture{
		evidenceBatch: batch,
		evidence: map[string]recallcontract.RecallEvidenceHit{
			peer:  {EvidenceID: peer, TeamID: team, Context: "The zirconium recovery test failed."},
			known: {EvidenceID: known, TeamID: team, Context: "Previously seen."},
		},
	}}
	for index := 0; index < recallcontract.MaxRecallCandidateCount; index++ {
		textID, vectorID := uuid.NewString(), uuid.NewString()
		batch.TextHits = append(batch.TextHits, searchcontract.SearchHit{SourceKind: "evidence", SourceID: textID})
		batch.VectorHits = append(batch.VectorHits, searchcontract.SearchHit{SourceKind: "evidence", SourceID: vectorID})
		for _, id := range []string{textID, vectorID} {
			f.evidence[id] = recallcontract.RecallEvidenceHit{EvidenceID: id, TeamID: team, Context: contextText}
		}
	}
	ordinary := fuseRecallCandidates(batch, "evidence", nil)
	seedID := ordinary[0].ID
	f.paths = []recallcontract.EvidenceBridge{
		{SeedID: seedID, EvidenceID: peer, Term: "zirconium", Fanout: 1},
		{SeedID: seedID, EvidenceID: hidden, Term: "zirconium", Fanout: 2},
		{SeedID: seedID, EvidenceID: known, Term: "zirconium", Fanout: 3},
	}
	input := recallcontract.RecallEvidenceInput{TeamID: team, Query: "recovery", Limit: maxRecallResultLimit, ExpansionEnabled: true, KnownEvidenceIDs: []string{known}}
	retrieval := NewRetrieval(f)
	var logs bytes.Buffer
	protector := observability.NewCredentialProtector(hidden)
	retrieval.protector = protector
	retrieval.logger = observability.NewConsoleWithProtector(slog.LevelDebug, protector, &logs)
	result, err := retrieval.RecallEvidence(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, recallCandidateIDs(ordinary[:5]), f.seedIDs)
	require.Len(t, f.hydratedIDs, recallcontract.MaxRecallCandidateCount)
	require.Contains(t, f.hydratedIDs, peer)
	require.Contains(t, f.hydratedIDs, hidden)
	require.NotContains(t, f.hydratedIDs, known)
	require.Len(t, result.Results, maxRecallResultLimit)
	require.Equal(t, peer, result.Results[4].EvidenceID)
	require.Equal(t, "The zirconium recovery test failed.", result.Results[4].Context)
	bridge := result.Results[4].Bridge
	require.NotNil(t, bridge)
	require.Equal(t, seedID, bridge.SeedID)
	require.Contains(t, bridge.SeedExcerpt, "zirconium")
	require.Contains(t, contextText, bridge.SeedExcerpt)
	require.Len(t, []rune(bridge.SeedExcerpt), 512)
	require.Contains(t, logs.String(), "recall_expansion_candidates")
	require.Contains(t, logs.String(), seedID)
	require.Contains(t, logs.String(), peer)
	require.NotContains(t, logs.String(), hidden)
	for _, hit := range result.Results {
		require.NotEqual(t, hidden, hit.EvidenceID)
		require.NotEqual(t, known, hit.EvidenceID)
	}

	batch.ExpansionHits = nil
	f.paths = nil
	result, err = retrieval.RecallEvidence(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, seedID, result.Results[0].EvidenceID)
	require.Len(t, f.hydratedIDs, recallcontract.MaxRecallCandidateCount)
	require.NotContains(t, f.hydratedIDs, peer)

	f.bridgeError = errors.New("bridge read failed")
	result, err = retrieval.RecallEvidence(t.Context(), input)
	require.ErrorIs(t, err, f.bridgeError)
	require.Nil(t, result)
	_, err = NewRetrieval(f.retrievalReadFixture).RecallEvidence(t.Context(), input)
	require.ErrorContains(t, err, "evidence expansion reader is required")
}

func TestBridgeExcerptWithoutMatchingTermUsesExactPrefix(t *testing.T) {
	prefix := strings.Repeat("é", 512)
	require.Equal(t, prefix, bridgeExcerpt(prefix+" extra source text", "absent"))
}

func TestBridgeRankingPrefersIndependentSeedsAndReservesTwoPlaces(t *testing.T) {
	paths := []recallcontract.EvidenceBridge{
		{SeedID: "1", EvidenceID: "peer-a", Term: "zirconium", Fanout: 3},
		{SeedID: "1", EvidenceID: "peer-b", TopicID: "topic", Fanout: 2},
		{SeedID: "1", EvidenceID: "peer-a", TopicID: "topic", Fanout: 3},
		{SeedID: "2", EvidenceID: "peer-a", TopicID: "other", Fanout: 2},
		{SeedID: "foreign", EvidenceID: "forbidden", Term: "other", Fanout: 1},
	}
	hits, bridges := rankEvidenceBridges([]string{"1", "2"}, paths)
	require.Len(t, hits, 2)
	require.Equal(t, "peer-a", hits[0].SourceID)
	require.InDelta(t, 1.0/3+1.0/4, hits[0].TextRank, 1e-10)
	candidates := []recallCandidate{}
	hydrated := &recallcontract.EvidenceHydration{Hits: map[string]recallcontract.RecallEvidenceHit{}}
	for i := 1; i <= 55; i++ {
		id := fmt.Sprint(i)
		hydrated.Hits[id] = recallcontract.RecallEvidenceHit{EvidenceID: id, Context: "source"}
		candidates = append(candidates, recallCandidate{ID: id})
	}
	for _, hit := range hits {
		hydrated.Hits[hit.SourceID] = recallcontract.RecallEvidenceHit{EvidenceID: hit.SourceID}
		candidates = append(candidates, recallCandidate{ID: hit.SourceID})
	}
	ranked := reserveBridgeResults(candidates, bridges, hydrated, nil)
	require.Equal(t, "peer-a", ranked[4].ID)
	require.Equal(t, "peer-b", ranked[9].ID)
	require.Equal(t, "1", ranked[0].ID)
	require.Equal(t, "source", hydrated.Hits["peer-a"].Bridge.SeedExcerpt)
}
