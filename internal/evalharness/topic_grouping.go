package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/lib/pq"
)

type judgedTopicSource struct {
	ID     string   `json:"source_id"`
	Title  string   `json:"title"`
	Labels []string `json:"labels"`
}

type topicGroupingLock struct {
	Version    int                 `json:"version"`
	Dataset    string              `json:"dataset"`
	CorpusHash string              `json:"corpus_sha256"`
	Rubric     string              `json:"rubric"`
	Sources    []judgedTopicSource `json:"sources"`
}

type topicGroupingMeasurement struct {
	LockHash          string              `json:"judgments_sha256"`
	CorpusHash        string              `json:"corpus_sha256"`
	Sources           int                 `json:"sources"`
	UnassignedSources int                 `json:"sources_without_direct_topics"`
	Pairs             int                 `json:"pairs"`
	TruePositive      int                 `json:"true_positive"`
	FalsePositive     int                 `json:"false_positive"`
	TrueNegative      int                 `json:"true_negative"`
	FalseNegative     int                 `json:"false_negative"`
	Precision         *float64            `json:"pairwise_precision"`
	Recall            *float64            `json:"pairwise_recall"`
	DirectTopics      map[string][]string `json:"observed_direct_topics_by_source"`
}

func measureTopicGrouping(ctx context.Context, path string, corpusHash string, corpus []CorpusItem, mapping KnowledgeMapping, n *Native) (topicGroupingMeasurement, error) {
	var result topicGroupingMeasurement
	data, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	var lock topicGroupingLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return result, err
	}
	if lock.Dataset != "hotpotqa" || lock.Version != 1 || lock.CorpusHash != corpusHash || lock.Rubric == "" || len(lock.Sources) < 2 {
		return result, errors.New("invalid paired topic judgments or corpus digest")
	}
	index := make(map[string]CorpusItem, len(corpus))
	for _, row := range corpus {
		index[row.SourceDocID] = row
	}
	sourceByEvidence := map[string][]string{}
	observed := map[string][]string{}
	for _, source := range lock.Sources {
		item, found := index[source.ID]
		_, duplicate := observed[source.ID]
		parts := mapping.Partitions[source.ID]
		if !found || duplicate || len(parts) == 0 || item.Title != source.Title || len(source.Labels) == 0 || slices.Contains(source.Labels, "") {
			return result, fmt.Errorf("invalid paired topic source %q", source.ID)
		}
		observed[source.ID] = []string{}
		for _, part := range parts {
			sourceByEvidence[part.EvidenceID] = append(sourceByEvidence[part.EvidenceID], source.ID)
		}
	}
	ids := make([]string, 0, len(sourceByEvidence))
	for id := range sourceByEvidence {
		ids = append(ids, id)
	}
	tx, err := n.systemTx(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	query := `SELECT a.evidence_id::text,t.record_id::text FROM topic_record_heads a JOIN topic_record_heads t
		ON t.team_id=a.team_id AND t.shared_space_id=a.shared_space_id AND t.space_generation=a.space_generation AND t.record_id=a.topic_id
		WHERE a.team_id=$1::uuid AND a.shared_space_id=$2::uuid AND a.space_generation=$3 AND a.kind='assignment' AND NOT a.retired
		AND t.kind='topic' AND NOT t.retired AND a.evidence_id=ANY($4::uuid[])`
	if n.legacyOrganization {
		query = `SELECT r.body#>>'{assignment,source,id}',t.record_id::text FROM ontology_record_heads a
			JOIN ontology_revision_records r USING(team_id,shared_space_id,space_generation,record_id,version)
			JOIN ontology_record_heads t ON t.team_id=a.team_id AND t.shared_space_id=a.shared_space_id
			AND t.space_generation=a.space_generation AND t.record_id::text=r.body#>>'{assignment,definition_id}'
			WHERE a.team_id=$1::uuid AND a.shared_space_id=$2::uuid AND a.space_generation=$3 AND a.kind='assignment' AND NOT a.retired
			AND t.kind='topic' AND NOT t.retired AND r.body#>>'{assignment,source,kind}'='evidence' AND r.body#>>'{assignment,source,id}'=ANY($4::text[])`
	}
	rows, err := tx.QueryContext(ctx, query, n.Scope.TeamID, n.Scope.SpaceID, n.Scope.Generation, pq.Array(ids))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var evidence, topic string
		if err := rows.Scan(&evidence, &topic); err != nil {
			return result, err
		}
		for _, source := range sourceByEvidence[evidence] {
			observed[source] = append(observed[source], topic)
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	result = scoreTopicPairs(lock.Sources, observed)
	result.LockHash, result.CorpusHash = bytesHash(data), corpusHash
	return result, nil
}

func scoreTopicPairs(judgments []judgedTopicSource, observed map[string][]string) topicGroupingMeasurement {
	result := topicGroupingMeasurement{Sources: len(judgments), DirectTopics: observed}
	for i, left := range judgments {
		slices.Sort(observed[left.ID])
		observed[left.ID] = slices.Compact(observed[left.ID])
		if len(observed[left.ID]) == 0 {
			result.UnassignedSources++
		}
		for _, right := range judgments[i+1:] {
			gold := slices.ContainsFunc(left.Labels, func(label string) bool { return slices.Contains(right.Labels, label) })
			predicted := slices.ContainsFunc(observed[left.ID], func(topic string) bool { return slices.Contains(observed[right.ID], topic) })
			result.Pairs++
			switch {
			case gold && predicted:
				result.TruePositive++
			case predicted:
				result.FalsePositive++
			case gold:
				result.FalseNegative++
			default:
				result.TrueNegative++
			}
		}
	}
	if predicted := result.TruePositive + result.FalsePositive; predicted > 0 {
		result.Precision = new(float64(result.TruePositive) / float64(predicted))
	}
	if positives := result.TruePositive + result.FalseNegative; positives > 0 {
		result.Recall = new(float64(result.TruePositive) / float64(positives))
	}
	return result
}
