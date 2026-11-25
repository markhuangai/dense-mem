package quality

import (
	"math"
	"sort"
	"strings"
)

// ResultRef identifies one ranked recall result without content.
type ResultRef struct {
	Type          string
	ID            string
	EquivalentIDs []string
}

func (ref ResultRef) IDs() []string {
	if resultKey(ref.Type, ref.ID) == "" {
		return nil
	}
	return append([]string{ref.ID}, ref.EquivalentIDs...)
}

// Judgment grades an expected relevant result. Grades must be positive.
type Judgment struct {
	Type  string
	ID    string
	Grade float64
}

// Metrics captures one query's offline ranking quality at K.
type Metrics struct {
	K             int     `json:"k"`
	RelevantAtK   int     `json:"relevant_at_k"`
	RelevantTotal int     `json:"relevant_total"`
	BadAtK        int     `json:"bad_at_k"`
	RecallAtK     float64 `json:"recall_at_k"`
	MRR           float64 `json:"mrr"`
	NDCGAtK       float64 `json:"ndcg_at_k"`
}

// ScoreAtK scores ranked results against relevant and judged-irrelevant refs.
func ScoreAtK(ranked []ResultRef, relevant []Judgment, irrelevant []ResultRef, k int) Metrics {
	if k < 0 {
		k = 0
	}
	if k > len(ranked) {
		k = len(ranked)
	}
	relevantGrades := make(map[string]float64, len(relevant))
	for _, judgment := range relevant {
		key := resultKey(judgment.Type, judgment.ID)
		if key == "" || judgment.Grade <= 0 {
			continue
		}
		if judgment.Grade > relevantGrades[key] {
			relevantGrades[key] = judgment.Grade
		}
	}
	badRefs := make(map[string]struct{}, len(irrelevant))
	for _, ref := range irrelevant {
		if key := resultKey(ref.Type, ref.ID); key != "" {
			badRefs[key] = struct{}{}
		}
	}

	metrics := Metrics{K: k, RelevantTotal: len(relevantGrades)}
	seenRelevant := map[string]struct{}{}
	firstRelevantRank := 0
	dcg := 0.0
	for i := 0; i < k; i++ {
		grade := 0.0
		badSlot := false
		for _, id := range ranked[i].IDs() {
			key := resultKey(ranked[i].Type, id)
			if key == "" {
				continue
			}
			if _, bad := badRefs[key]; bad {
				badSlot = true
			}
			if relevantGrades[key] <= 0 {
				continue
			}
			if _, seen := seenRelevant[key]; !seen {
				seenRelevant[key] = struct{}{}
				metrics.RelevantAtK++
				grade = math.Max(grade, relevantGrades[key])
			}
		}
		if badSlot {
			metrics.BadAtK++
		}
		if grade > 0 && firstRelevantRank == 0 {
			firstRelevantRank = i + 1
		}
		dcg += discountedGain(grade, i+1)
	}
	if metrics.RelevantTotal > 0 {
		metrics.RecallAtK = float64(metrics.RelevantAtK) / float64(metrics.RelevantTotal)
	}
	if firstRelevantRank > 0 {
		metrics.MRR = 1 / float64(firstRelevantRank)
	}
	idealGrades := make(map[string]float64, len(relevantGrades))
	for key, grade := range relevantGrades {
		idealGrades[key] = grade
	}
	for _, ref := range ranked {
		if len(ref.EquivalentIDs) == 0 {
			continue
		}
		grade := 0.0
		for _, id := range ref.IDs() {
			key := resultKey(ref.Type, id)
			grade = math.Max(grade, relevantGrades[key])
			delete(idealGrades, key)
		}
		if grade > 0 {
			idealGrades[resultKey(ref.Type, ref.ID)] = grade
		}
	}
	ideal := idealDCG(idealGrades, k)
	if ideal > 0 {
		metrics.NDCGAtK = dcg / ideal
	}
	return metrics
}

func resultKey(resultType string, id string) string {
	resultType = strings.TrimSpace(resultType)
	id = strings.TrimSpace(id)
	if resultType == "" || id == "" {
		return ""
	}
	return resultType + ":" + id
}

func discountedGain(grade float64, rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return (math.Pow(2, grade) - 1) / math.Log2(float64(rank+1))
}

func idealDCG(relevantGrades map[string]float64, k int) float64 {
	if k <= 0 || len(relevantGrades) == 0 {
		return 0
	}
	grades := make([]float64, 0, len(relevantGrades))
	for _, grade := range relevantGrades {
		grades = append(grades, grade)
	}
	sort.Slice(grades, func(i, j int) bool { return grades[i] > grades[j] })
	if k > len(grades) {
		k = len(grades)
	}
	score := 0.0
	for i := 0; i < k; i++ {
		score += discountedGain(grades[i], i+1)
	}
	return score
}
