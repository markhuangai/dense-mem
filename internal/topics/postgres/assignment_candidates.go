package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

type assignmentSearchContract struct {
	ID, GenerationID, Strategy           string
	Dimensions, CandidateLimit, EFSearch int
}

type assignmentCandidateStats struct {
	VectorCount, FullTextCount, UnembeddedTopicCount int
	EmbeddingContractID                              string
	VectorUnavailable                                bool
}

func (s *Store) assignmentCandidates(tx *gorm.DB, fence scope, input assignment.Slice, forced []string) ([]topic.RecordView, assignmentCandidateStats, error) {
	return s.assignmentCandidatesWithVector(tx, fence, input, forced, "", "")
}

func (s *Store) assignmentCandidatesWithVector(tx *gorm.DB, fence scope, input assignment.Slice, forced []string, vectorContract, suppliedVector string) ([]topic.RecordView, assignmentCandidateStats, error) {
	var stats assignmentCandidateStats
	var active assignmentSearchContract
	err := tx.Raw(`SELECT contract.embedding_contract_id::text,generation.search_index_generation_id::text,
		contract.dimensions,generation.ann_strategy,generation.candidate_limit,generation.query_ef_search
		FROM search_index_generations AS generation JOIN embedding_contracts AS contract
		ON contract.embedding_contract_id=generation.embedding_contract_id AND contract.dimensions=generation.embedding_dimensions
		WHERE generation.activation_state='active' AND contract.lifecycle_state='active' AND contract.distance_metric='cosine'
		ORDER BY contract.version DESC,generation.generation DESC,generation.created_at DESC LIMIT 1`).Row().Scan(
		&active.ID, &active.GenerationID, &active.Dimensions, &active.Strategy, &active.CandidateLimit, &active.EFSearch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, stats, err
	}
	stats.EmbeddingContractID = active.ID
	vector := ""
	if vectorContract != "" {
		if vectorContract == active.ID {
			vector = suppliedVector
		}
	} else {
		vector, err = assignmentEvidenceVector(tx, fence, input, active)
		if err != nil {
			return nil, stats, err
		}
	}
	stats.VectorUnavailable = vector == ""
	_, words := topic.VocabularyQuery([]topic.SourceSnapshot{{State: map[string]string{"content": input.Text}}})
	query := topic.LooseVocabularyQuery(words)
	rows, err := tx.Raw(`SELECT current.topic_id::text,NOT EXISTS (
		SELECT 1 FROM topic_embeddings AS embedding WHERE
		(embedding.team_id,embedding.shared_space_id,embedding.space_generation,embedding.topic_id)=
		(current.team_id,current.shared_space_id,current.space_generation,current.topic_id)
		AND embedding.embedding_contract_id=NULLIF(?,'')::uuid AND embedding.topic_version=current.definition_version)
		FROM topics AS current
		WHERE current.team_id=?::uuid AND current.shared_space_id=?::uuid AND current.space_generation=?
		AND NOT current.retired AND current.search_tsv @@ websearch_to_tsquery('simple',?)
		ORDER BY ts_rank_cd(current.search_tsv,websearch_to_tsquery('simple',?)) DESC,current.topic_id LIMIT ?`,
		active.ID, fence.TeamID, fence.SpaceID, fence.Generation, query, query, assignment.MaxTopics).Rows()
	if err != nil {
		return nil, stats, err
	}
	lexical := []string{}
	for rows.Next() {
		var id string
		var missing bool
		if err := rows.Scan(&id, &missing); err != nil {
			rows.Close()
			return nil, stats, err
		}
		lexical = append(lexical, id)
		if missing {
			stats.UnembeddedTopicCount++
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, stats, err
	}
	stats.FullTextCount = len(lexical)
	var semantic []string
	if vector != "" {
		semantic, err = assignmentVectorCandidates(tx, fence, active, vector)
		if err != nil {
			return nil, stats, err
		}
	}
	stats.VectorCount = len(semantic)
	type ranked struct {
		id     string
		forced bool
		score  float64
	}
	candidates := map[string]*ranked{}
	get := func(id string) *ranked {
		candidate := candidates[id]
		if candidate == nil {
			candidate = &ranked{id: id}
			candidates[id] = candidate
		}
		return candidate
	}
	for _, id := range forced {
		get(id).forced = true
	}
	for index, id := range lexical {
		get(id).score += 1 / float64(61+index)
	}
	for index, id := range semantic {
		get(id).score += 1 / float64(61+index)
	}
	ordered := make([]ranked, 0, len(candidates))
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, *candidate)
		ids = append(ids, candidate.id)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].forced != ordered[j].forced {
			return ordered[i].forced
		}
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		return ordered[i].id < ordered[j].id
	})
	if len(ids) == 0 {
		return nil, stats, nil
	}
	heads, err := loadHeads(tx, fence, ids, nil)
	if err != nil {
		return nil, stats, err
	}
	records := make([]topic.Record, 0, len(ordered))
	for _, candidate := range ordered {
		if record, exists := heads[candidate.id]; exists && !record.Retired {
			records = append(records, record)
		}
	}
	views, err := s.organizationViews(tx, fence, records)
	if err != nil {
		return nil, stats, err
	}
	result := make([]topic.RecordView, 0, assignment.MaxTopics)
	for _, view := range views {
		if view.Current {
			result = append(result, view)
			if len(result) == assignment.MaxTopics {
				break
			}
		}
	}
	return result, stats, nil
}

func assignmentEvidenceVector(tx *gorm.DB, fence scope, input assignment.Slice, active assignmentSearchContract) (string, error) {
	if active.ID == "" {
		return "", nil
	}
	if len(input.Embedding) > 0 && input.EmbeddingContractID == active.ID && input.SearchIndexGenerationID == active.GenerationID {
		if len(input.Embedding) != active.Dimensions {
			return "", fmt.Errorf("topic assignment embedding has %d dimensions, expected %d", len(input.Embedding), active.Dimensions)
		}
		var vector string
		err := tx.Raw(`SELECT (?::real[])::vector::text`, pq.Array(input.Embedding)).Row().Scan(&vector)
		return vector, err
	}
	id := input.CanonicalEvidenceID
	if id == "" {
		id = input.EvidenceID
	}
	if id == "" {
		return "", nil
	}
	snapshot, err := readSource(tx, fence, topic.SourceHandle{Kind: topic.EvidenceSource, ID: id, Version: 1})
	if err != nil {
		return "", err
	}
	if topic.SourceDisplayText(snapshot) != input.Text {
		return "", assignment.ErrStale
	}
	var vector string
	err = tx.Raw(`SELECT document.embedding::text FROM search_documents AS document
		WHERE document.team_id=?::uuid AND document.space_id=?::uuid AND document.space_generation=?
		AND document.source_kind='evidence' AND document.source_id=?::uuid AND document.source_version=1
		AND document.embedding_contract_id=?::uuid AND document.embedding_dimensions=?
		AND document.search_state='current' AND document.embedding IS NOT NULL AND document.document_text=? LIMIT 1`,
		fence.TeamID, fence.SpaceID, fence.Generation, id, active.ID, active.Dimensions, strings.TrimSpace(input.Text)).Row().Scan(&vector)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return vector, err
}

func assignmentVectorCandidates(tx *gorm.DB, fence scope, active assignmentSearchContract, vector string) ([]string, error) {
	if active.Dimensions < 1 || active.Dimensions > domain.MaxEmbeddingDimensions {
		return nil, errors.New("topic assignment search dimensions are invalid")
	}
	var distance string
	switch active.Strategy {
	case string(domain.VectorIndexVectorHNSW):
		if active.Dimensions > 2000 {
			return nil, errors.New("topic vector index dimensions are invalid")
		}
		distance = fmt.Sprintf("e.embedding::vector(%d) <=> ?::vector(%d)", active.Dimensions, active.Dimensions)
	case string(domain.VectorIndexHalfvecHNSW):
		if active.Dimensions > 4000 {
			return nil, errors.New("topic halfvec index dimensions are invalid")
		}
		distance = fmt.Sprintf("e.embedding::halfvec(%d) <=> ?::halfvec(%d)", active.Dimensions, active.Dimensions)
	case string(domain.VectorIndexBinaryHNSW):
		distance = fmt.Sprintf("binary_quantize(e.embedding)::bit(%d) <~> binary_quantize(?::vector)::bit(%d)", active.Dimensions, active.Dimensions)
	case string(domain.VectorIndexExact):
		distance = "e.embedding <=> ?::vector"
	default:
		return nil, fmt.Errorf("unsupported topic vector strategy %q", active.Strategy)
	}
	limit := min(max(active.CandidateLimit, assignment.MaxTopics), 200)
	if active.Strategy != string(domain.VectorIndexExact) {
		if err := tx.Exec(`SET LOCAL hnsw.iterative_scan='relaxed_order'`).Error; err != nil {
			return nil, err
		}
		if err := tx.Exec(`SELECT set_config('hnsw.ef_search',?,true)`, strconv.Itoa(max(active.EFSearch, limit))).Error; err != nil {
			return nil, err
		}
	}
	query := `WITH nearest AS MATERIALIZED (
		SELECT e.topic_id,e.embedding FROM topic_embeddings AS e JOIN topics AS current
		USING(team_id,shared_space_id,space_generation,topic_id)
		WHERE e.team_id=?::uuid AND e.shared_space_id=?::uuid AND e.space_generation=?
		AND e.embedding_contract_id=?::uuid AND e.embedding_dimensions=? AND e.embedding IS NOT NULL
		AND e.topic_version=current.definition_version AND NOT current.retired
		ORDER BY ` + distance + `,e.topic_id LIMIT ?
	) SELECT topic_id::text FROM nearest ORDER BY embedding <=> ?::vector,topic_id LIMIT ?`
	rows, err := tx.Raw(query, fence.TeamID, fence.SpaceID, fence.Generation, active.ID, active.Dimensions, vector, limit, vector, assignment.MaxTopics).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
