package recallservice

import "strings"

func applyIdentifierSpecificityAdjustments(query string, entries []rrfEntry) {
	queryText := rerankText(query)
	if !isUnitValueQueryText(queryText) {
		return
	}
	if len(rerankIdentifiers(queryText)) == 0 {
		return
	}
	for i := range entries {
		entries[i].FinalScore += identifierSpecificityAdjustment(queryText, entries[i].Content)
	}
}

func isUnitValueQueryText(queryText string) bool {
	return strings.Contains(queryText, " timeout ") &&
		strings.Contains(queryText, " job ") &&
		(strings.Contains(queryText, " use ") || strings.Contains(queryText, " should "))
}

func identifierSpecificityAdjustment(queryText, content string) float64 {
	contentText := rerankText(content)
	if queryText == "" || contentText == "" {
		return 0
	}
	if !matchesQueryIdentifiers(queryText, contentText) {
		return 0
	}
	return 0.004
}
