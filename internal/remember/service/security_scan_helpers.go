package service

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	prohibitionPrefixPattern       = regexp.MustCompile(`(?is)^(?:please[[:space:]]+)?(?:never|do[[:space:]]+not|don't|must[[:space:]]+not|should[[:space:]]+not)[[:space:]]+(?:ever[[:space:]]+)?$`)
	prohibitionClausePattern       = regexp.MustCompile(`(?is)^(?:please[[:space:]]+)?(?:never|do[[:space:]]+not|don't|must[[:space:]]+not|should[[:space:]]+not)[[:space:]]+(?:ever[[:space:]]+)?(?:reveal|show|send|dump|print|exfiltrate|return|output|transmit|upload|post|ignore|disregard|forget|override)\b`)
	prohibitionJoinPattern         = regexp.MustCompile(`(?is)\b(?:and|or|nor)[[:space:]]+$`)
	prohibitionBreakPattern        = regexp.MustCompile(`(?i)\b(?:but|instead|then|however|except|unless|nevertheless)\b`)
	reportedInstructionPattern     = regexp.MustCompile(`(?is)^[[:alpha:]][[:alpha:]'[:space:]-]{1,100}[[:space:]]+(?:quoted|quotes|reported|reports|described|describes|cited|cites|gives|gave|uses|used)\b[[:space:]]*`)
	attackExamplePattern           = regexp.MustCompile(`(?is)^[^,.!?;\n]{0,80}\bas[[:space:]]+(?:an?[[:space:]]+)?(?:example|attack|malicious|prohibited)\b`)
	reportedExecutionClausePattern = regexp.MustCompile(`(?i)[.!?;,\r\n]|\b(?:and|or|so|but|then|however|instead)\b`)
	reportedExecutionTargetPattern = regexp.MustCompile(`(?i)^[a-z][a-z-]{1,30}[[:space:]]+(?:(?:to|on|with|out|according|as|what)[[:space:]]+){0,3}(?:(?:the|these|those|that|this|above|below|quoted|reported|its)[[:space:]]+)*(?:instructions?|commands?|steps?|requests?|directions?|(?:quoted|reported)[[:space:]]+(?:text|content)|it|them|this|that)\b`)
	outputMetricPrefixPattern      = regexp.MustCompile(`(?i)(?:[0-9][0-9,.]*|\b(?:input|generation|completion|response|budget|allowance|quota|count|counts|usage|includes|included|measures|measured|consumed|remaining|available))[[:space:]]+$`)
	outputMetricSuffixPattern      = regexp.MustCompile(`(?i)^output[[:space:]]+tokens?\b(?:[[:space:]]+(?:are|were|is|was|cost|costs|remain|count|measure)\b|[[:space:]]*:[[:space:]]*[0-9])`)
	outputTokenNounPattern         = regexp.MustCompile(`(?i)^output[[:space:]]+tokens?$`)
	camelEvidenceIdentifierPattern = regexp.MustCompile(`^(?:[a-z]+|[A-Z][a-z]+)(?:[A-Z][a-z0-9]+)+$`)
)

func submissionInstructionIsData(text string, location []int, rule string) bool {
	start, end := location[0], location[1]
	clauseStart := strings.LastIndexAny(text[:start], ".!?;\r\n") + 1
	prefix := strings.TrimLeft(text[clauseStart:start], " \t>#:*-\\\"'`([{<‘’“”")
	if len(prefix) <= 256 && !strings.ContainsAny(text[start:end], ".!?;\r\n") && !prohibitionBreakPattern.MatchString(text[start:end]) && (prohibitionPrefixPattern.MatchString(prefix) ||
		prohibitionClausePattern.MatchString(prefix) && prohibitionJoinPattern.MatchString(prefix) && !prohibitionBreakPattern.MatchString(prefix) &&
			(secretExtractionPattern.MatchString(prefix) || toolExfiltrationPattern.MatchString(prefix) || overridePattern.MatchString(prefix))) {
		return true
	}
	if reportedInstructionIsData(prefix, text[start:end], text[end:], text[:clauseStart]) {
		return true
	}
	if rule == "prompt_secret_extraction" && outputTokenNounPattern.MatchString(text[start:end]) {
		return outputMetricPrefixPattern.MatchString(prefix) && !toolDirectiveSuffixPattern.MatchString(prefix) ||
			outputMetricSuffixPattern.MatchString(text[start:])
	}
	return false
}

func reportedInstructionIsData(prefix, matched, suffix, preceding string) bool {
	frame := reportedInstructionPattern.FindStringIndex(prefix)
	if frame == nil {
		return false
	}
	tail := strings.TrimSpace(prefix[frame[1]:])
	for _, pair := range [][2]string{{`"`, `"`}, {"'", "'"}, {"`", "`"}, {"‘", "’"}, {"“", "”"}} {
		if strings.HasPrefix(tail, pair[0]) {
			closeAt := strings.Index(suffix, pair[1])
			return !strings.Contains(strings.TrimPrefix(tail, pair[0]), pair[1]) && !strings.Contains(matched, pair[1]) && closeAt >= 0 &&
				!reportedExecutionIsDirective(preceding+prefix[:frame[1]]+suffix[closeAt+len(pair[1]):])
		}
	}
	return !strings.ContainsAny(tail, ",;:") && !prohibitionBreakPattern.MatchString(tail) && attackExamplePattern.MatchString(suffix) &&
		!reportedExecutionIsDirective(preceding+prefix[:frame[1]]+suffix)
}

func reportedExecutionIsDirective(text string) bool {
	for _, clause := range reportedExecutionClausePattern.Split(text, -1) {
		clause = strings.Trim(strings.ToLower(clause), " \t>#:*-\\\"'`([{<‘’“”")
		words := strings.Fields(clause)
		for len(words) > 0 && (words[0] == "please" || words[0] == "kindly" || words[0] == "now" || words[0] == "then" || words[0] == "instead") {
			words = words[1:]
		}
		if len(words) >= 4 && (words[0] == "i" || words[0] == "we") &&
			(words[1] == "want" || words[1] == "need" || words[1] == "require") && words[2] == "you" {
			words = words[3:]
			if words[0] == "to" {
				words = words[1:]
			}
		}
		if len(words) >= 3 && (words[0] == "can" || words[0] == "could" || words[0] == "would" || words[0] == "will" || words[0] == "may") && words[1] == "you" {
			words = words[2:]
		}
		if len(words) >= 2 && (words[0] == "you" || words[0] == "assistant") {
			words = words[1:]
			if words[0] == "must" || words[0] == "should" {
				words = words[1:]
			}
			if len(words) >= 2 && words[0] == "need" && words[1] == "to" {
				words = words[2:]
			}
		}
		if len(words) >= 4 && words[0] == "never" && (words[1] == "forget" || words[1] == "hesitate" || words[1] == "refuse" || words[1] == "fail") && words[2] == "to" {
			words = words[3:]
		}
		if len(words) < 2 {
			continue
		}
		switch words[0] {
		case "the", "a", "an", "this", "that", "these", "those", "i", "we", "they", "it", "never", "not":
			continue
		}
		if reportedExecutionTargetPattern.MatchString(strings.Join(words, " ")) {
			return true
		}
	}
	return false
}

func readableTechnicalIdentifierCandidate(value string) bool {
	if len(value) > 256 || strings.ContainsAny(value, "=+") || strings.Contains(value, "//") {
		return false
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '-' || r == '_' })
	if len(parts) < 2 {
		return false
	}
	hasCamel := false
	for _, part := range parts {
		if len(part) == 0 || len(part) > 64 {
			return false
		}
		camel := camelEvidenceIdentifierPattern.MatchString(part)
		if !camel && !looksLikeNaturalEvidenceWord(part) {
			return false
		}
		hasCamel = hasCamel || camel
	}
	if !hasCamel {
		return false
	}
	// Identifier syntax cannot exempt a meaningful or dangerous Base64 decode.
	for _, candidate := range append(parts, value) {
		if decoded, ok := decodeBase64(candidate); ok && (hasBinaryMagic(decoded) || printablePercentage(decoded) >= 60 ||
			len(dangerousSubmissionSignals(identitySecurityView(string(decoded)), true)) > 0) {
			return false
		}
	}
	return true
}

func toolExfiltrationIsDirective(text string, start int) bool {
	if start < 0 || start > len(text) {
		return false
	}
	prefix := strings.TrimRightFunc(text[:start], func(value rune) bool {
		return unicode.IsSpace(value) || strings.ContainsRune("\\\"'`([{<", value)
	})
	if toolDirectiveStartPattern.MatchString(prefix) || toolDirectiveSuffixPattern.MatchString(prefix) {
		return true
	}
	return false
}

func hiddenControlRune(value rune) bool {
	return value == '\u200b' || value == '\u200c' || value == '\u200d' || value == '\u200e' || value == '\u200f' ||
		value == '\u2060' || value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069' ||
		unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t'
}

func ruleID(decoded bool, base string) string {
	if decoded {
		return "decoded_" + base
	}
	return base
}

func signalForLocation(view securityView, location []int, kind, ruleID, severity string, encoded bool) (SubmissionSecuritySignal, bool) {
	startRune := utf8.RuneCountInString(view.text[:location[0]])
	endRune := utf8.RuneCountInString(view.text[:location[1]])
	if startRune < 0 || endRune <= startRune || startRune >= len(view.spans) {
		return SubmissionSecuritySignal{}, false
	}
	if endRune > len(view.spans) {
		endRune = len(view.spans)
	}
	return signalForSourceSpan(view, sourceSpan{start: view.spans[startRune].start, end: view.spans[endRune-1].end}, kind, ruleID, severity, encoded)
}

func signalForSourceSpan(view securityView, span sourceSpan, kind, ruleID, severity string, encoded bool) (SubmissionSecuritySignal, bool) {
	if len(view.spans) == 0 {
		return SubmissionSecuritySignal{}, false
	}
	if span.start < 0 {
		span.start = 0
	}
	if span.end > view.spans[len(view.spans)-1].end {
		span.end = view.spans[len(view.spans)-1].end
	}
	if span.end <= span.start {
		return SubmissionSecuritySignal{}, false
	}
	return SubmissionSecuritySignal{Kind: kind, RuleID: ruleID, Severity: severity, Start: span.start, End: span.end, Encoded: encoded}, true
}

func normalizeSubmissionSecuritySignals(signals []SubmissionSecuritySignal) ([]SubmissionSecuritySignal, bool) {
	if len(signals) == 0 {
		return []SubmissionSecuritySignal{}, false
	}
	sort.Slice(signals, func(left, right int) bool {
		if signals[left].Start != signals[right].Start {
			return signals[left].Start < signals[right].Start
		}
		if signals[left].End != signals[right].End {
			return signals[left].End < signals[right].End
		}
		if signals[left].Kind != signals[right].Kind {
			return signals[left].Kind < signals[right].Kind
		}
		return signals[left].RuleID < signals[right].RuleID
	})
	out := make([]SubmissionSecuritySignal, 0, min(len(signals), submissionSecurityMaxSignals))
	seen := make(map[string]struct{}, len(signals))
	truncated := false
	for _, signal := range signals {
		if signal.End <= signal.Start || strings.TrimSpace(signal.Kind) == "" || strings.TrimSpace(signal.RuleID) == "" {
			continue
		}
		key := strconv.Itoa(signal.Start) + ":" + strconv.Itoa(signal.End) + ":" + signal.Kind + ":" + signal.RuleID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if len(out) >= submissionSecurityMaxSignals {
			truncated = true
			continue
		}
		out = append(out, signal)
	}
	return out, truncated
}
