package recallservice

import (
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

// rrfEntry is the internal accumulator keyed by fragment id.
type rrfEntry struct {
	id           string
	Content      string
	RecallText   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	SemanticRank int
	KeywordRank  int
	FinalScore   float64
}

// rrfMerge computes score(id) = Σ 1 / (RRFConstant + rank) across branches.
// Each branch contributes the 1-based rank of the id within that branch.
func rrfMerge(sem []semanticsearch.SearchHit, kw []keywordsearch.FragmentSearchResult) []rrfEntry {
	byID := make(map[string]*rrfEntry, len(sem)+len(kw))
	for i, h := range sem {
		rank := i + 1
		e, ok := byID[h.ID]
		if !ok {
			e = &rrfEntry{id: h.ID}
			byID[h.ID] = e
		}
		if e.Content == "" {
			e.Content = h.Content
		}
		e.RecallText = appendRecallText(e.RecallText, h.Content)
		mergeRRFEntryTimes(e, semanticHitTime(h.CreatedAt), semanticHitTime(h.UpdatedAt))
		if e.SemanticRank == 0 || rank < e.SemanticRank {
			e.SemanticRank = rank
		}
		e.FinalScore += 1.0 / float64(RRFConstant+rank)
	}
	for i, h := range kw {
		rank := i + 1
		e, ok := byID[h.FragmentID]
		if !ok {
			e = &rrfEntry{id: h.FragmentID}
			byID[h.FragmentID] = e
		}
		if e.Content == "" {
			e.Content = h.Content
		}
		e.RecallText = appendRecallText(e.RecallText, h.RecallText)
		mergeRRFEntryTimes(e, h.CreatedAt, h.UpdatedAt)
		if e.KeywordRank == 0 || rank < e.KeywordRank {
			e.KeywordRank = rank
		}
		e.FinalScore += 1.0 / float64(RRFConstant+rank)
	}
	out := make([]rrfEntry, 0, len(byID))
	for _, e := range byID {
		out = append(out, *e)
	}
	return out
}

func appendRecallText(existing, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return existing
	}
	if existing == "" {
		return value
	}
	return existing + " " + value
}

func semanticHitTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func filterNonPositiveRRFEntries(entries []rrfEntry) []rrfEntry {
	hasPositive := false
	for _, entry := range entries {
		if entry.FinalScore > 0 {
			hasPositive = true
			break
		}
	}
	if !hasPositive {
		return entries
	}

	out := entries[:0]
	for _, entry := range entries {
		if entry.FinalScore > 0 {
			out = append(out, entry)
		}
	}
	return out
}

func mergeRRFEntryTimes(entry *rrfEntry, createdAt, updatedAt time.Time) {
	if entry == nil {
		return
	}
	if !createdAt.IsZero() && (entry.CreatedAt.IsZero() || createdAt.Before(entry.CreatedAt)) {
		entry.CreatedAt = createdAt
	}
	if !updatedAt.IsZero() && (entry.UpdatedAt.IsZero() || updatedAt.After(entry.UpdatedAt)) {
		entry.UpdatedAt = updatedAt
	}
}
