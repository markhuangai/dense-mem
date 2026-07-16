package recallservice

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func parseSemanticRecallQuery(query string) domain.SemanticRecallQueryFeatures {
	query = strings.TrimSpace(query)
	terms := semanticRecallTerms(query, 16)
	phrases := semanticRecallEntityPhrases(query)
	anchors := semanticRecallHardAnchors(query)
	lower := strings.ToLower(query)
	return domain.SemanticRecallQueryFeatures{
		Query:             query,
		ContentQuery:      query,
		RelaxedQuery:      semanticRecallRelaxedWebQuery(query),
		Terms:             terms,
		EntityPhrases:     phrases,
		HardAnchors:       anchors,
		TemporalIntent:    containsAnyTerm(lower, "when", "before", "after", "during", "date", "time", "latest", "current", "currently", "recent"),
		CurrentnessIntent: containsAnyTerm(lower, "latest", "current", "currently", "recent", "now", "today"),
	}
}

func semanticRecallTerms(query string, maxTerms int) []string {
	stopWords := semanticRecallStopWords()
	seen := map[string]struct{}{}
	terms := make([]string, 0, maxTerms)
	for _, term := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len([]rune(term)) < 2 {
			continue
		}
		if _, skip := stopWords[term]; skip {
			continue
		}
		if _, duplicate := seen[term]; duplicate {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
		if len(terms) == maxTerms {
			break
		}
	}
	return terms
}

func semanticRecallRelaxedWebQuery(query string) string {
	terms := semanticRecallTerms(query, 12)
	if len(terms) == 0 {
		return query
	}
	return strings.Join(terms, " OR ")
}

func semanticRecallEntityPhrases(query string) []string {
	seen := map[string]struct{}{}
	var phrases []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if len([]rune(value)) < 2 {
			return
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		phrases = append(phrases, value)
	}
	var quoted strings.Builder
	inQuote := false
	for _, r := range query {
		if r == '"' || r == '\'' {
			if inQuote {
				add(quoted.String())
				quoted.Reset()
				inQuote = false
			} else {
				inQuote = true
			}
			continue
		}
		if inQuote {
			quoted.WriteRune(r)
		}
	}
	terms := semanticRecallTerms(query, 8)
	if len(terms) >= 2 {
		for i := 0; i < len(terms)-1 && i < 6; i++ {
			add(terms[i] + " " + terms[i+1])
		}
	}
	for _, term := range terms {
		add(term)
	}
	return phrases
}

func semanticRecallHardAnchors(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;()[]{}<>\"'", r)
	})
	seen := map[string]struct{}{}
	anchors := make([]string, 0, 4)
	for _, field := range fields {
		value := strings.Trim(field, ".,:!?")
		if value == "" {
			continue
		}
		if _, err := uuid.Parse(value); err == nil {
			key := strings.ToLower(value)
			if _, duplicate := seen[key]; !duplicate {
				seen[key] = struct{}{}
				anchors = append(anchors, value)
			}
			continue
		}
		if semanticRecallLooksLikeExternalID(value) {
			key := strings.ToLower(value)
			if _, duplicate := seen[key]; !duplicate {
				seen[key] = struct{}{}
				anchors = append(anchors, value)
			}
		}
	}
	return anchors
}

func semanticRecallLooksLikeExternalID(value string) bool {
	if len(value) < 4 || len(value) > 80 {
		return false
	}
	var hasLetter, hasDigit, hasSeparator bool
	for _, r := range value {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		case r == '-' || r == '_' || r == '/' || r == ':' || r == '.':
			hasSeparator = true
		default:
			return false
		}
	}
	return hasLetter && hasDigit && hasSeparator
}

func semanticRecallStopWords() map[string]struct{} {
	return map[string]struct{}{
		"a": {}, "an": {}, "are": {}, "about": {}, "and": {}, "as": {}, "at": {},
		"be": {}, "by": {}, "do": {}, "does": {}, "for": {}, "from": {}, "how": {},
		"in": {}, "is": {}, "it": {}, "me": {}, "of": {}, "on": {}, "or": {},
		"the": {}, "to": {}, "was": {}, "were": {}, "what": {}, "when": {},
		"which": {}, "who": {}, "why": {}, "with": {},
	}
}

func containsAnyTerm(value string, terms ...string) bool {
	tokens := stringSet(semanticRecallTerms(value, 64))
	for _, term := range terms {
		if _, ok := tokens[term]; ok {
			return true
		}
	}
	return false
}

func semanticRecallBranchLimit(limit int, ranking SemanticRecallRankingProfile) int {
	ranking = NormalizeSemanticRecallRankingProfile(ranking)
	limit = clampLimit(limit)
	branchLimit := limit * ranking.BranchLimitMultiplier
	if branchLimit < ranking.BranchLimitFloor {
		branchLimit = ranking.BranchLimitFloor
	}
	if branchLimit > ranking.BranchLimitMax {
		branchLimit = ranking.BranchLimitMax
	}
	return branchLimit
}

func semanticRecallEmbeddingContractID(model string, dimensions int) string {
	model = strings.TrimSpace(model)
	if model == "" || dimensions <= 0 {
		return ""
	}
	return model + ":" + strconv.Itoa(dimensions) + ":semantic_search_document_v1"
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func normalizeUUIDList(field string, values []string, maxItems int) ([]string, error) {
	if len(values) > maxItems {
		return nil, fmt.Errorf("semantic recall: %s exceeds %d items", field, maxItems)
	}
	seen := make(map[uuid.UUID]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("semantic recall: %s contains an invalid uuid", field)
		}
		if _, ok := seen[parsed]; ok {
			continue
		}
		seen[parsed] = struct{}{}
		out = append(out, parsed.String())
	}
	return out, nil
}

func semanticRecallTeamID(ctx context.Context, fallback string) string {
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		return actor.TeamID.String()
	}
	return strings.TrimSpace(fallback)
}
