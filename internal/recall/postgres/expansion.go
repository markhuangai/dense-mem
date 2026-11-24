package postgres

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topiccontract "github.com/markhuangai/dense-mem/internal/topics/contract"
)

// Five seeds, three terms and two topics per seed, and eight peers per path bound the round to 200 candidates.
const (
	maxExpansionSeeds = 5
	termsPerSeed      = 3
	topicsPerSeed     = topiccontract.MaxRecallPeerTopics
	peersPerBridge    = 8
)

func (r *Store) ReadEvidenceBridges(ctx context.Context, input RecallEvidenceInput, contract *ActiveSearchContract, seeds []string) ([]recallcontract.EvidenceBridge, error) {
	if len(seeds) == 0 {
		return nil, nil
	}
	if len(seeds) > maxExpansionSeeds {
		return nil, fmt.Errorf("recall: expansion seed limit exceeded")
	}
	var paths []recallcontract.EvidenceBridge
	err := r.withRecallTx(ctx, input.TeamID, true, func(tx *gorm.DB) error {
		var err error
		input.KnownEvidenceIDs, err = storage.ResolveMemoryHandlesTx(ctx, tx, input.TeamID, "evidence_fragments", input.KnownEvidenceIDs, input.SpaceID)
		if err != nil {
			return err
		}
		if err := r.resolveTopicFilter(ctx, tx, &input); err != nil {
			return err
		}
		authorized, err := hydrateRecallEvidence(ctx, tx, input, contract, seeds)
		if err != nil {
			return err
		}
		eligibleSeeds := make([]string, 0, len(seeds))
		for _, id := range seeds {
			if _, ok := authorized[id]; ok {
				eligibleSeeds = append(eligibleSeeds, id)
			}
		}
		if len(eligibleSeeds) == 0 {
			return nil
		}
		paths, err = readTermBridges(ctx, tx, input, contract, eligibleSeeds)
		if err != nil {
			return err
		}
		if !input.OrganizationEnabled || r.topics == nil || r.organizationFailure != "" || (input.SpaceKind != "" && input.SpaceKind != string(domain.MemorySpaceTeamShared)) || input.KnownAt != nil || input.ValidAt != nil {
			return nil
		}
		selection, err := r.topics(ctx, tx, input.TeamID, topiccontract.TopicReadInput{SpaceID: input.SpaceID, EvidenceIDs: eligibleSeeds, MaxMembers: peersPerBridge + 1})
		if err != nil {
			return &organizationReadError{cause: err}
		}
		peers := smallTopicBridges(eligibleSeeds, selection.Topics)
		ids := make([]string, 0, len(peers))
		for _, peer := range peers {
			ids = append(ids, peer.EvidenceID)
		}
		if len(ids) == 0 {
			return nil
		}
		eligible, err := hydrateRecallEvidence(ctx, tx, input, contract, domain.NormalizeReadIDList(ids))
		if err != nil {
			return err
		}
		for _, peer := range peers {
			if _, ok := eligible[peer.EvidenceID]; ok && !slices.Contains(input.KnownEvidenceIDs, peer.EvidenceID) {
				paths = append(paths, peer)
			}
		}
		return nil
	})
	return paths, err
}

func readTermBridges(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, contract *ActiveSearchContract, seeds []string) ([]recallcontract.EvidenceBridge, error) {
	filter, filterArgs := recallEvidenceEligibility(input, "fragment", true)
	args := []any{input.Query, pq.Array(topiccontract.VocabularyStopWords()), termsPerSeed, pq.Array(seeds), input.TeamID, input.TeamID, contract.EmbeddingContractID, recallEventAt(input.ValidAt, input.KnownAt), pq.Array(seeds)}
	args = append(args, filterArgs...)
	args = append(args, peersPerBridge+1, peersPerBridge)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH terms AS MATERIALIZED (
			SELECT seed.fragment_id AS seed_id, terms.term
			FROM evidence_fragments AS seed
			CROSS JOIN LATERAL (
				SELECT word AS term FROM unnest(tsvector_to_array(to_tsvector('simple',seed.content))) AS word
				WHERE length(word) BETWEEN 3 AND 64
				  AND NOT (word = ANY(tsvector_to_array(to_tsvector('simple',?))))
				  AND NOT (word = ANY(?::text[]))
				ORDER BY length(word) DESC,word
				LIMIT ?
			) AS terms
			WHERE seed.fragment_id=ANY(?::uuid[]) AND seed.team_id=?::uuid
		), peers AS (
			SELECT terms.seed_id, terms.term, peer.id,
			       count(*) OVER (PARTITION BY terms.seed_id, terms.term) AS fanout
			FROM terms
			CROSS JOIN LATERAL (
				SELECT fragment.fragment_id AS id FROM search_documents AS document
				JOIN evidence_fragments AS fragment ON fragment.team_id=document.team_id AND fragment.fragment_id=document.source_id
				WHERE document.team_id=?::uuid AND document.embedding_contract_id=?::uuid AND document.source_kind='evidence'
				  AND (document.search_state IN ('pending','current','failed') OR (?::timestamptz IS NOT NULL AND document.search_state='not_required'))
				  AND document.search_tsv @@ plainto_tsquery('simple',terms.term)
				  AND fragment.fragment_id<>ALL(?::uuid[])`+filter+`
				LIMIT ?
			) AS peer
		)
		SELECT seed_id::text,id::text,term,fanout FROM peers WHERE fanout<=?
		ORDER BY seed_id,id,term`, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []recallcontract.EvidenceBridge
	for rows.Next() {
		var path recallcontract.EvidenceBridge
		if err := rows.Scan(&path.SeedID, &path.EvidenceID, &path.Term, &path.Fanout); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func smallTopicBridges(seeds []string, topics []topiccontract.EvidenceTopic) []recallcontract.EvidenceBridge {
	sort.Slice(topics, func(i, j int) bool {
		if len(topics[i].EvidenceIDs) != len(topics[j].EvidenceIDs) {
			return len(topics[i].EvidenceIDs) < len(topics[j].EvidenceIDs)
		}
		return topics[i].ID < topics[j].ID
	})
	isSeed := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		isSeed[seed] = true
	}
	var paths []recallcontract.EvidenceBridge
	selected := map[string]int{}
	for _, topic := range topics {
		if len(topic.EvidenceIDs) < 2 || len(topic.EvidenceIDs) > peersPerBridge+1 {
			continue
		}
		for _, seed := range topic.EvidenceIDs {
			if !isSeed[seed] || selected[seed] == topicsPerSeed {
				continue
			}
			selected[seed]++
			for _, peer := range topic.EvidenceIDs {
				if !isSeed[peer] {
					paths = append(paths, recallcontract.EvidenceBridge{SeedID: seed, EvidenceID: peer, TopicID: topic.ID, TopicName: topic.Name, Fanout: len(topic.EvidenceIDs) - 1})
				}
			}
		}
	}
	return paths
}
