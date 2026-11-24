package recall

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/observability"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func recallRankingMethod(query string, vector, expanded bool) string {
	if query == "" {
		return "direct_topics"
	}
	method := "full_text"
	if vector {
		method = "hybrid_rrf"
	}
	if expanded {
		method += "_one_hop"
	}
	return method
}

func (r *Retrieval) expandEvidence(ctx context.Context, input recallcontract.RecallEvidenceInput, contract *searchcontract.ActiveSearchContract, batch *recallcontract.RecallCandidateBatch, ordinary []recallCandidate) ([]recallCandidate, map[string]recallcontract.EvidenceBridge, error) {
	reader, ok := r.repository.(recallcontract.EvidenceBridgeRepository)
	if !ok {
		return nil, nil, errors.New("recall: evidence expansion reader is required")
	}
	ctx, stage := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageExpansion)
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "recall_expansion"})
	seeds := make([]string, 0, 5)
	for _, hit := range ordinary {
		if hit.Score <= 0 || len(seeds) == cap(seeds) {
			break
		}
		seeds = append(seeds, hit.ID)
	}
	paths, err := reader.ReadEvidenceBridges(ctx, input, contract, seeds)
	stage.Finish(err, len(paths))
	if err != nil {
		return nil, nil, err
	}
	hits, bridges := rankEvidenceBridges(seeds, paths)
	if root, ok := r.logger.(interface{ ContextSlog() *slog.Logger }); ok {
		logger := root.ContextSlog()
		if logger != nil && logger.Enabled(ctx, slog.LevelDebug) {
			body, err := json.Marshal(map[string]any{"seed_ids": seeds, "paths": paths, "ranked_peers": hits})
			if err != nil {
				return nil, nil, err
			}
			observability.LogDiagnosticBody(ctx, logger, r.protector, "recall_expansion_candidates", body, modelprovider.MaxProviderDiagnosticBodyBytes,
				slog.String("team_id", input.TeamID), slog.String("space_id", input.SpaceID), slog.String("diagnostic_capture_id", uuid.NewString()))
		}
	}
	if len(paths) == 0 {
		return ordinary[:min(len(ordinary), recallcontract.MaxRecallCandidateCount)], nil, nil
	}
	batch.ExpansionHits = hits
	ranked := fuseRecallCandidates(batch, "evidence", input.KnownEvidenceIDs)
	if len(ranked) > recallcontract.MaxRecallCandidateCount {
		// Keep space for new peers before hydration, even when both ordinary channels fill their limits.
		graphSeats := min(len(hits), 10)
		pool := append([]recallCandidate(nil), ranked[:recallcontract.MaxRecallCandidateCount-graphSeats]...)
		inPool := make(map[string]bool, len(pool))
		candidatesByID := make(map[string]recallCandidate, len(ranked))
		for _, item := range ranked {
			candidatesByID[item.ID] = item
		}
		for _, item := range pool {
			inPool[item.ID] = true
		}
		for _, hit := range hits[:graphSeats] {
			if item, ok := candidatesByID[hit.SourceID]; ok && !inPool[item.ID] {
				pool = append(pool, item)
				inPool[item.ID] = true
			}
		}
		for _, item := range ranked[recallcontract.MaxRecallCandidateCount-graphSeats:] {
			if len(pool) == recallcontract.MaxRecallCandidateCount {
				break
			}
			if !inPool[item.ID] {
				pool = append(pool, item)
				inPool[item.ID] = true
			}
		}
		sortRecallCandidates(pool, "evidence")
		ranked = pool
	}
	return ranked, bridges, nil
}

func rankEvidenceBridges(seeds []string, paths []recallcontract.EvidenceBridge) ([]searchcontract.SearchHit, map[string]recallcontract.EvidenceBridge) {
	seedRanks := make(map[string]int, len(seeds))
	for i, seed := range seeds {
		seedRanks[seed] = i + 1
	}
	byPeer := make(map[string]map[string]recallcontract.EvidenceBridge)
	for _, path := range paths {
		if seedRanks[path.SeedID] == 0 || seedRanks[path.EvidenceID] != 0 || path.Fanout < 1 {
			continue
		}
		votes := byPeer[path.EvidenceID]
		if votes == nil {
			votes = make(map[string]recallcontract.EvidenceBridge)
			byPeer[path.EvidenceID] = votes
		}
		old, found := votes[path.SeedID]
		if !found || path.Fanout < old.Fanout || (path.Fanout == old.Fanout && (path.Term < old.Term || (path.Term == old.Term && path.TopicID < old.TopicID))) {
			votes[path.SeedID] = path
		}
	}
	hits := make([]searchcontract.SearchHit, 0, len(byPeer))
	bridges := make(map[string]recallcontract.EvidenceBridge, len(byPeer))
	for peer, votes := range byPeer {
		score, strongest := 0.0, 0.0
		for i, seed := range seeds {
			path, ok := votes[seed]
			if !ok {
				continue
			}
			vote := 1.0 / float64((i+1)*path.Fanout)
			score += vote
			if vote > strongest {
				strongest, bridges[peer] = vote, path
			}
		}
		hits = append(hits, searchcontract.SearchHit{SourceKind: "evidence", SourceID: peer, TextRank: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].TextRank != hits[j].TextRank {
			return hits[i].TextRank > hits[j].TextRank
		}
		return hits[i].SourceID < hits[j].SourceID
	})
	return hits, bridges
}

func reserveBridgeResults(candidates []recallCandidate, bridges map[string]recallcontract.EvidenceBridge, hydrated *recallcontract.EvidenceHydration, known []string) []recallCandidate {
	groups, suppressed, _ := eligibleEvidenceGroups(hydrated.Groups, hydrated.Hits, known)
	seenGroups := make(map[string]bool)
	ranked := make([]recallCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		hit, ok := hydrated.Hits[candidate.ID]
		if !ok || suppressed[candidate.ID] {
			continue
		}
		if group := groups[candidate.ID].ID; group != "" {
			if seenGroups[group] {
				continue
			}
			seenGroups[group] = true
		}
		if path, found := bridges[candidate.ID]; found {
			if seed, eligible := hydrated.Hits[path.SeedID]; eligible {
				path.SeedExcerpt = bridgeExcerpt(seed.Context, path.Term)
				hit.Bridge = &path
				hydrated.Hits[candidate.ID] = hit
			}
		}
		ranked = append(ranked, candidate)
	}
	seenBridges := 0
	for index := 0; index < min(10, len(ranked)); index++ {
		if hydrated.Hits[ranked[index].ID].Bridge != nil {
			seenBridges++
			continue
		}
		if (index+1)%5 != 0 || seenBridges >= (index+1)/5 {
			continue
		}
		for later := index + 1; later < len(ranked); later++ {
			if hydrated.Hits[ranked[later].ID].Bridge == nil {
				continue
			}
			promoted := ranked[later]
			copy(ranked[index+1:later+1], ranked[index:later])
			ranked[index] = promoted
			seenBridges++
			break
		}
	}
	return ranked
}

func bridgeExcerpt(content, term string) string {
	const limit = 512
	if utf8.RuneCountInString(content) <= limit {
		return content
	}
	start := 0
	if term != "" {
		text := strings.ToLower(content)
		if index := strings.Index(text, term); index >= 0 {
			start = max(0, utf8.RuneCountInString(text[:index])-limit/2)
		}
	}
	runes := []rune(content)
	start = min(start, len(runes)-limit)
	return string(runes[start : start+limit])
}
