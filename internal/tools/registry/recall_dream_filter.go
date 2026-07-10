package registry

import (
	"sort"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/recallident"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

const (
	relatedDreamLimit     = 3
	relatedDreamOverfetch = 20
)

func rerankRelatedDreams(query string, hits []recallservice.RecallHit, dreams []*domain.Dream, limit int) []*domain.Dream {
	if len(dreams) == 0 {
		return nil
	}
	if limit <= 0 || limit > relatedDreamLimit {
		limit = relatedDreamLimit
	}
	queryIDs := recallident.Extract(query)
	queryTerms := significantTerms(query)
	anchors := recallHitAnchorRefs(hits)
	scored := make([]scoredDream, 0, len(dreams))
	for _, dream := range dreams {
		if dream == nil || excludedDreamStatus(dream.Status) {
			continue
		}
		sourceOverlap := dreamSourceOverlap(dream, anchors)
		identifierOverlap := recallident.Overlap(queryIDs, dream.IdentifierTokens) +
			recallident.OverlapText(queryIDs, dream.Hypothesis, dream.WhatIf, dream.PossibleOutcome, dream.Rationale, dream.RecallText, dream.CycleRunID, dream.GeneratorModel)
		lexicalOverlap := termOverlap(queryTerms, significantTerms(strings.Join([]string{
			dream.Hypothesis,
			dream.WhatIf,
			dream.PossibleOutcome,
			dream.Rationale,
			dream.RecallText,
		}, " ")))
		if len(queryIDs) > 0 && identifierOverlap == 0 && sourceOverlap == 0 {
			continue
		}
		if len(queryIDs) == 0 && sourceOverlap == 0 && lexicalOverlap < 2 {
			continue
		}
		if dream.Likelihood > 0 && dream.Likelihood < 0.4 && sourceOverlap == 0 && identifierOverlap == 0 {
			continue
		}
		score := dream.Likelihood*0.2 + dream.Confidence*0.2 + float64(sourceOverlap)*0.4 + float64(identifierOverlap)*0.3 + float64(lexicalOverlap)*0.05
		if dream.Status == domain.DreamStatusReinforced {
			score += 0.05
		}
		scored = append(scored, scoredDream{dream: dream, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].dream.DreamID < scored[j].dream.DreamID
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]*domain.Dream, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.dream)
	}
	return out
}

type scoredDream struct {
	dream *domain.Dream
	score float64
}

func excludedDreamStatus(status domain.DreamStatus) bool {
	return status == domain.DreamStatusRejected ||
		status == domain.DreamStatusStale ||
		status == domain.DreamStatusPromoted
}

func recallHitAnchorRefs(hits []recallservice.RecallHit) map[string]struct{} {
	out := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		if hit.Fragment != nil && hit.Fragment.FragmentID != "" {
			out["fragment:"+hit.Fragment.FragmentID] = struct{}{}
		}
		if hit.Claim != nil && hit.Claim.ClaimID != "" {
			out["claim:"+hit.Claim.ClaimID] = struct{}{}
		}
		if hit.Fact != nil && hit.Fact.FactID != "" {
			out["fact:"+hit.Fact.FactID] = struct{}{}
		}
	}
	return out
}

func dreamSourceOverlap(dream *domain.Dream, anchors map[string]struct{}) int {
	if dream == nil || len(anchors) == 0 {
		return 0
	}
	count := 0
	for _, ref := range dream.SourceRefs {
		if _, ok := anchors[ref.Type+":"+ref.ID]; ok {
			count++
		}
	}
	return count
}

func significantTerms(text string) map[string]struct{} {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if len(field) < 3 || dreamStopwords[field] {
			continue
		}
		out[field] = struct{}{}
	}
	return out
}

func termOverlap(left, right map[string]struct{}) int {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	count := 0
	for term := range left {
		if _, ok := right[term]; ok {
			count++
		}
	}
	return count
}

var dreamStopwords = map[string]bool{
	"about": true, "after": true, "and": true, "are": true, "before": true,
	"can": true, "for": true, "from": true, "how": true, "into": true,
	"not": true, "the": true, "this": true, "what": true, "when": true,
	"which": true, "with": true,
}
