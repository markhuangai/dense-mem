package recallservice

import (
	"strings"
	"time"
)

func applyCurrentnessAdjustments(query string, entries []rrfEntry) {
	if !isCurrentnessQuery(query) {
		return
	}
	temporalFrame := currentnessTemporalFrameFor(query, entries)
	for i := range entries {
		lexicalAdjustment := currentnessAdjustment(query, entries[i].Content)
		if temporalFrame.hasContentDate && lexicalAdjustment > 0 && latestCurrentnessTemporalDateInEntry(entries[i]).IsZero() {
			lexicalAdjustment = 0
		}
		entries[i].FinalScore += lexicalAdjustment
		entries[i].FinalScore += currentnessTemporalAdjustment(query, entries[i], temporalFrame)
		entries[i].FinalScore += expiredValidityAdjustment(query, entries[i], temporalFrame)
		if entries[i].FinalScore < 0 {
			entries[i].FinalScore = 0
		}
	}
}

func applyCueAdjustments(query string, entries []rrfEntry) {
	if !isSelectionRecallQuery(query) {
		return
	}
	frame := selectionCueFrameFor(query, entries)
	for i := range entries {
		entries[i].FinalScore += cueAdjustment(query, entries[i].Content)
		entries[i].FinalScore += historicalSelectionAdjustment(query, entries[i].Content, frame)
		if entries[i].FinalScore < 0 {
			entries[i].FinalScore = 0
		}
	}
}

func applyAuthorityAdjustments(query string, entries []rrfEntry) {
	if !isAuthorityRecallQuery(query) {
		return
	}
	for i := range entries {
		entries[i].FinalScore += authorityAdjustment(query, entries[i].Content)
		if entries[i].FinalScore < 0 {
			entries[i].FinalScore = 0
		}
	}
}

func currentnessAdjustment(query, content string) float64 {
	queryText := rerankText(query)
	contentText := rerankText(content)
	if queryText == "" || contentText == "" {
		return 0
	}

	adjustment := 0.0
	hasCurrentCue := containsAnyRerankCue(contentText, currentnessPositiveCues)
	if hasCurrentCue && rerankMatchesQueryIdentifiers(queryText, contentText) {
		adjustment += 0.024
	}
	if containsAnyRerankCue(contentText, currentnessStrongStaleCues) {
		if hasCurrentCue {
			adjustment -= 0.006
		} else {
			adjustment -= 0.024
		}
	}
	if containsAnyRerankCue(contentText, currentnessWeakStaleCues) {
		adjustment -= 0.012
	}

	if adjustment > 0.028 {
		return 0.028
	}
	if adjustment < -0.028 {
		return -0.028
	}
	return adjustment
}

func cueAdjustment(query, content string) float64 {
	queryText := rerankText(query)
	contentText := rerankText(content)
	if queryText == "" || contentText == "" {
		return 0
	}

	adjustment := 0.0
	boostAllowed := matchesQueryIdentifiers(queryText, contentText)
	if boostAllowed {
		if containsAnyCue(contentText, directiveCues) {
			adjustment += 0.022
		}
		if containsAnyCue(contentText, canonicalCues) {
			adjustment += 0.004
		}
	}
	if containsAnyCue(contentText, strongDisqualifierCues) {
		adjustment -= 0.022
	}
	if containsAnyCue(contentText, weakDisqualifierCues) {
		adjustment -= 0.012
	}
	if boostAllowed && containsAnyCue(queryText, conditionalQueryCues) && containsAnyCue(contentText, conditionalQueryCues) && containsAnyCue(contentText, directiveCues) {
		adjustment += 0.006
	}

	if adjustment > 0.026 {
		return 0.026
	}
	if adjustment < -0.026 {
		return -0.026
	}
	return adjustment
}

type selectionCueFrame struct {
	hasDirectiveMatch bool
}

func selectionCueFrameFor(query string, entries []rrfEntry) selectionCueFrame {
	queryText := rerankText(query)
	var frame selectionCueFrame
	for _, entry := range entries {
		contentText := rerankText(entry.Content)
		if queryText == "" || contentText == "" || !matchesQueryIdentifiers(queryText, contentText) {
			continue
		}
		if containsAnyCue(contentText, directiveCues) {
			frame.hasDirectiveMatch = true
			return frame
		}
	}
	return frame
}

func historicalSelectionAdjustment(query, content string, frame selectionCueFrame) float64 {
	if !frame.hasDirectiveMatch {
		return 0
	}
	queryText := rerankText(query)
	contentText := rerankText(content)
	if queryText == "" || contentText == "" || !matchesQueryIdentifiers(queryText, contentText) {
		return 0
	}
	if historicalSelectionActionCue(contentText) {
		return -0.034
	}
	return 0
}

func historicalSelectionActionCue(contentText string) bool {
	return strings.Contains(contentText, " before ") && strings.Contains(contentText, " used ")
}

func authorityAdjustment(query, content string) float64 {
	queryText := rerankText(query)
	contentText := rerankText(content)
	if queryText == "" || contentText == "" {
		return 0
	}

	adjustment := 0.0
	boostAllowed := authorityMatchesQueryIdentifiers(queryText, contentText)
	if boostAllowed {
		if containsAnyAuthorityCue(contentText, authorityPositiveCues) {
			adjustment += 0.026
		}
		if containsAnyAuthorityCue(contentText, authorityDirectiveCues) {
			adjustment += 0.006
		}
	}
	if containsAnyAuthorityCue(contentText, authorityStrongNegativeCues) {
		adjustment -= 0.028
	}
	if containsAnyAuthorityCue(contentText, authorityWeakNegativeCues) {
		adjustment -= 0.018
	}

	if adjustment > 0.034 {
		return 0.034
	}
	if adjustment < -0.034 {
		return -0.034
	}
	return adjustment
}

func isCurrentnessQuery(query string) bool {
	text := rerankText(query)
	return strings.Contains(text, " current ") ||
		strings.Contains(text, " as of ") ||
		strings.Contains(text, " now ") ||
		strings.Contains(text, " latest ") ||
		strings.Contains(text, " active ")
}

func isSelectionRecallQuery(query string) bool {
	text := rerankText(query)
	return strings.Contains(text, " which ") && (strings.Contains(text, " use ") || strings.Contains(text, " should "))
}

func rerankMatchesQueryIdentifiers(queryText, contentText string) bool {
	identifiers := rerankIdentifiers(queryText)
	return identifiersMatchContent(identifiers, contentText)
}

func matchesQueryIdentifiers(queryText, contentText string) bool {
	return rerankMatchesQueryIdentifiers(queryText, contentText)
}

func isAuthorityRecallQuery(query string) bool {
	text := rerankText(query)
	return strings.Contains(text, " authoritative ") ||
		strings.Contains(text, " canonical ") ||
		strings.Contains(text, " require ") ||
		strings.Contains(text, " requires ") ||
		strings.Contains(text, " required ")
}

func isEvidenceSourceQuery(query string) bool {
	text := rerankText(query)
	return strings.Contains(text, " which source ") ||
		strings.Contains(text, " what source ") ||
		strings.Contains(text, " source note ") ||
		strings.Contains(text, " source document ") ||
		strings.Contains(text, " supporting evidence ") ||
		strings.Contains(text, " raw evidence ") ||
		strings.Contains(text, " original evidence ") ||
		strings.Contains(text, " raw fragment ") ||
		strings.Contains(text, " source fragment ") ||
		strings.Contains(text, " which note ") ||
		strings.Contains(text, " what note ") ||
		strings.Contains(text, " note says ") ||
		strings.Contains(text, " note said ") ||
		strings.Contains(text, " mentioned ")
}

func authorityMatchesQueryIdentifiers(queryText, contentText string) bool {
	identifiers := rerankIdentifiers(queryText)
	return identifiersMatchContent(identifiers, contentText)
}

func identifiersMatchContent(identifiers []string, contentText string) bool {
	if len(identifiers) == 0 {
		return true
	}
	for _, identifier := range identifiers {
		if !strings.Contains(contentText, " "+identifier+" ") {
			return false
		}
	}
	return true
}

func rerankIdentifiers(text string) []string {
	fields := strings.Fields(text)
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if _, ok := seen[field]; ok {
			continue
		}
		if rerankIdentifierToken(field) {
			out = append(out, field)
			seen[field] = struct{}{}
		}
	}
	return out
}

func rerankIdentifierToken(token string) bool {
	if _, err := time.Parse("2006-01-02", token); err == nil {
		return false
	}
	hasDigit := false
	hasLetter := false
	allAlnum := true
	first := rune(0)
	for _, r := range token {
		if first == 0 {
			first = r
		}
		if r >= '0' && r <= '9' {
			hasDigit = true
			continue
		}
		if r >= 'a' && r <= 'z' {
			hasLetter = true
			continue
		}
		allAlnum = false
	}
	if !hasDigit {
		return false
	}
	if strings.Contains(token, "-") {
		return true
	}
	return allAlnum && hasLetter && first >= 'a' && first <= 'z' && len(token) >= 4
}

func rerankText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"\n", " ",
		"\t", " ",
		".", " ",
		",", " ",
		":", " ",
		";", " ",
		"?", " ",
		"!", " ",
		"(", " ",
		")", " ",
	)
	return " " + strings.Join(strings.Fields(replacer.Replace(value)), " ") + " "
}

func containsAnyRerankCue(text string, cues []string) bool {
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func containsAnyCue(text string, cues []string) bool {
	return containsAnyRerankCue(text, cues)
}

func containsAnyAuthorityCue(text string, cues []string) bool {
	return containsAnyRerankCue(text, cues)
}

var currentnessPositiveCues = []string{
	" current ",
	" now ",
	" active ",
	" valid on ",
	" update dated ",
}

var currentnessStrongStaleCues = []string{
	" archived ",
	" obsolete ",
	" replaced ",
	" rejected ",
	" not active ",
	" not approved ",
}

var currentnessWeakStaleCues = []string{
	" legacy ",
	" previous ",
	" previously ",
	" before ",
	" older ",
	" old ",
	" draft ",
	" suggested ",
	" copied ",
	" rollback ",
	" incident review ",
	" proposed ",
	" once ",
	" future proposal ",
}

var directiveCues = []string{
	" must use ",
	" must be ",
	" should use ",
	" should be ",
	" is assigned ",
}

var canonicalCues = []string{
	" canonical ",
	" current ",
	" policy ",
	" rule ",
	" registry ",
}

var strongDisqualifierCues = []string{
	" does not apply ",
	" not about ",
	" rejected ",
	" unapproved ",
	" forbidden ",
	" false positive ",
	" false positives ",
}

var weakDisqualifierCues = []string{
	" legacy ",
	" previously ",
	" before ",
	" once ",
	" removed ",
	" fallback ",
	" draft ",
	" rumor ",
	" troubleshooting note ",
}

var conditionalQueryCues = []string{
	" enterprise ",
	" standard ",
	" tenant ",
	" exception ",
}

var authorityPositiveCues = []string{
	" authoritative ",
	" signed by ",
	" approved ",
	" official ",
	" canonical ",
	" source of truth ",
}

var authorityDirectiveCues = []string{
	" requires ",
	" require ",
	" required ",
	" must use ",
	" must be ",
}

var authorityStrongNegativeCues = []string{
	" informal chat ",
	" not approved ",
	" unapproved ",
	" personal checklist ",
	" meeting transcript ",
}

var authorityWeakNegativeCues = []string{
	" suggested ",
	" as an option ",
	" while testing ",
	" before ",
	" draft ",
	" note about ",
}
