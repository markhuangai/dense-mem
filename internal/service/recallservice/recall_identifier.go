package recallservice

import (
	"strings"

	"github.com/markhuangai/dense-mem/internal/recallident"
)

func applyIdentifierSpecificityAdjustments(query string, entries []rrfEntry) {
	queryAnchors := recallident.RankingAnchors(query)
	if len(queryAnchors) == 0 {
		return
	}
	for i := range entries {
		entries[i].FinalScore += identifierSpecificityAdjustment(queryAnchors, entries[i].Content, entries[i].RecallText)
	}
}

func isUnitValueQueryText(queryText string) bool {
	return strings.Contains(queryText, " timeout ") &&
		strings.Contains(queryText, " job ") &&
		(strings.Contains(queryText, " use ") || strings.Contains(queryText, " should "))
}

func identifierSpecificityAdjustment(queryIdentifiers []string, contentParts ...string) float64 {
	overlap := recallident.OverlapText(queryIdentifiers, contentParts...)
	if overlap == 0 {
		return 0
	}
	return 0.004 * float64(overlap)
}
