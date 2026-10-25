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
	prohibitionPrefixPattern        = regexp.MustCompile(`(?is)^(?:please[[:space:]]+)?(?:never|do[[:space:]]+not|don't|must[[:space:]]+not|should[[:space:]]+not)[[:space:]]+(?:ever[[:space:]]+)?$`)
	prohibitionClausePattern        = regexp.MustCompile(`(?is)^(?:please[[:space:]]+)?(?:never|do[[:space:]]+not|don't|must[[:space:]]+not|should[[:space:]]+not)[[:space:]]+(?:ever[[:space:]]+)?(?:reveal|show|send|dump|print|exfiltrate|return|output|transmit|upload|post|ignore|disregard|forget|override)\b`)
	prohibitionURLPattern           = regexp.MustCompile(`(?i)\bhttps?://[^[:space:]"'<>]+`)
	prohibitionClauseEndPattern     = regexp.MustCompile(`[;\r\n]|[.!?]["'\x60’”)\]}>]*(?:[[:space:]]|$)`)
	prohibitionEllipsisPattern      = regexp.MustCompile(`\.{2,}`)
	prohibitionJoinPattern          = regexp.MustCompile(`(?is)\b(?:and|or|nor)[[:space:]]+$`)
	prohibitionBreakPattern         = regexp.MustCompile(`(?i)\b(?:but|instead|then|however|except|unless|nevertheless)\b`)
	prohibitionContextPattern       = regexp.MustCompile(prohibitionURLPattern.String() + "|" + prohibitionEllipsisPattern.String() + "|" + prohibitionClauseEndPattern.String() + "|" + prohibitionBreakPattern.String())
	reportedInstructionPattern      = regexp.MustCompile(`(?is)^[[:alpha:]][[:alpha:]'[:space:]-]{1,100}[[:space:]]+(?:quoted|quotes|reported|reports|described|describes|cited|cites|gives|gave|uses|used)\b[[:space:]]*`)
	reportedReferencePattern        = regexp.MustCompile("(?U)" + reportedInstructionPattern.String())
	attackExamplePattern            = regexp.MustCompile(`(?is)^[^,.!?;\n]{0,80}\bas[[:space:]]+(?:an?[[:space:]]+)?(?:example|attack|malicious|prohibited)\b`)
	reportedDirectivePrefixPattern  = regexp.MustCompile(`(?is)^(?:(?:never|do[[:space:]]+not|don't)[[:space:]]+(?:ever[[:space:]]+)?(?:forget|hesitate|refuse|fail)[[:space:]]+to\b|(?:you\b[[:space:]]+|assistant\b[,:]?[[:space:]]*)(?:can|could|would|will|shall|may)\b|` + directivePrefixExpression + `|you\b|(?:now|then|instead|just|simply|also|always|immediately|quickly)\b)\s*`)
	reportedExecutionClausePattern  = regexp.MustCompile(`(?i)[.!?;,\r\n]|\b(?:and|or|so|but|then|however|instead)\b`)
	reportedExecutionTargetPattern  = regexp.MustCompile(`(?i)^[a-z][a-z-]{1,30}[[:space:]]+(?:(?:to|on|with|out|according|as|what)[[:space:]]+){0,3}(?:(?:the|these|those|that|this|above|below|quoted|reported|its)[[:space:]]+)*(?:instructions?|commands?|steps?|requests?|directions?|(?:quoted|reported)[[:space:]]+(?:text|content|string|message|line|sentence|phrase|payload|prompt|snippet|passage)|quot(?:e|es|ation|ations)|report[[:space:]]+says?|it|them|this|that)\b`)
	reportedStorageStatementPattern = regexp.MustCompile(`(?i)^(?:store|archive|record|document)[[:space:]]+(?:it|this|that|them|the[[:space:]]+(?:quote|quoted[[:space:]]+(?:text|content|string|message)|report))(?:[[:space:]]+(?:in|into)[[:space:]]+(?:(?:the|an?|my|our|your)[[:space:]]+)?(?:[a-z-]+[[:space:]]+)?(?:archive|runbook|report|notes|record|database|file))?$`)
	outputMetricPrefixPattern       = regexp.MustCompile(`(?i)(?:[0-9][0-9,.]*|\b(?:input|generation|completion|response|budget|allowance|quota|count|counts|usage|includes|included|measures|measured|consumed|remaining|available))[[:space:]]+$`)
	outputMetricSuffixPattern       = regexp.MustCompile(`(?i)^output[[:space:]]+tokens?\b(?:[[:space:]]+(?:are|were|is|was|cost|costs|remain|count|measure)\b|[[:space:]]*:[[:space:]]*[0-9])`)
	outputTokenNounPattern          = regexp.MustCompile(`(?i)^output[[:space:]]+tokens?$`)
	camelEvidenceIdentifierPattern  = regexp.MustCompile(`^(?:[a-z]+|[A-Z][a-z]+)(?:[A-Z][a-z0-9]+)+$`)
)

func submissionInstructionIsData(text string, location []int, rule string) bool {
	start, end := location[0], location[1]
	clauseStart := strings.LastIndexAny(text[:start], ".!?;\r\n") + 1
	prefix := strings.TrimLeft(text[clauseStart:start], " \t>#:*-\\\"'`([{<‘’“”")
	if len(prefix) <= 256 && !strings.ContainsAny(text[start:end], ".!?;\r\n") && !prohibitionBreakPattern.MatchString(text[start:end]) && (prohibitionPrefixPattern.MatchString(prefix) ||
		prohibitionClausePattern.MatchString(prefix) && prohibitionJoinPattern.MatchString(prefix) && !prohibitionBreakPattern.MatchString(prefix) &&
			(secretExtractionPattern.MatchString(prefix) || toolExfiltrationPattern.MatchString(prefix) || overridePattern.MatchString(prefix))) {
		if !prohibitionHasTrailingException(text[end:]) {
			return true
		}
	}
	if reportedInstructionIsData(prefix, text[start:end], text[end:], text[:clauseStart]) {
		return true
	}
	if rule == "prompt_secret_extraction" && outputTokenNounPattern.MatchString(text[start:end]) {
		return !toolDirectiveSuffixPattern.MatchString(prefix) &&
			(outputMetricPrefixPattern.MatchString(prefix) || outputMetricSuffixPattern.MatchString(text[start:]))
	}
	return false
}

func prohibitionHasTrailingException(trailing string) bool {
	for {
		location := prohibitionContextPattern.FindStringIndex(trailing)
		if location == nil {
			return false
		}
		token := trailing[location[0]:location[1]]
		// URL paths can contain both sentence punctuation and exception words.
		if strings.Contains(token, "://") {
			ending := strings.TrimRight(token, "\"'`’”)]}>,")
			if boundary := strings.LastIndexAny(ending, ",;"); boundary >= 0 {
				exception := strings.TrimRight(ending[boundary+1:], ".!?")
				if match := prohibitionBreakPattern.FindStringIndex(exception); match != nil && match[0] == 0 && match[1] == len(exception) {
					return true
				}
			}
			if !strings.HasSuffix(ending, "..") && strings.ContainsRune(".!?;", rune(ending[len(ending)-1])) {
				return false
			}
		} else if !prohibitionEllipsisPattern.MatchString(token) {
			return prohibitionBreakPattern.MatchString(token)
		}
		trailing = trailing[location[1]:]
	}
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
	reported := prefix[frame[1]:] + matched
	return !strings.Contains(reported, ":") && !reportedExecutionClausePattern.MatchString(reported) && !prohibitionBreakPattern.MatchString(reported) && attackExamplePattern.MatchString(suffix) &&
		!reportedExecutionIsDirective(preceding+prefix[:frame[1]]+suffix)
}

func reportedExecutionIsDirective(text string) bool {
	for _, clause := range reportedExecutionClausePattern.Split(text, -1) {
		clause = strings.Trim(strings.ToLower(clause), " \t>#:*-\\\"'`([{<‘’“”")
		clause = strings.Join(strings.Fields(clause), " ")
		for {
			prefix := reportedDirectivePrefixPattern.FindStringIndex(clause)
			if prefix == nil {
				break
			}
			clause = strings.TrimSpace(clause[prefix[1]:])
		}
		words := strings.Fields(clause)
		if len(words) < 2 {
			continue
		}
		switch words[0] {
		case "the", "a", "an", "this", "that", "these", "those", "i", "we", "they", "it", "never", "not":
			continue
		}
		normalized := strings.Join(words, " ")
		if len(words) > 2 && (words[1] == "what" || words[1] == "whatever") {
			reference := strings.Join(words[2:], " ")
			if frame := reportedReferencePattern.FindStringIndex(reference); frame != nil {
				normalized = strings.TrimSpace(words[0] + " it " + reference[frame[1]:])
			}
		}
		if reportedExecutionTargetPattern.MatchString(normalized) && !reportedStorageStatementPattern.MatchString(normalized) {
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
	clauseStart := strings.LastIndexAny(text[:start], ".!?;\r\n") + 1
	report := strings.TrimLeft(text[clauseStart:start], " \t>#:*-\\\"'`([{<‘’“”")
	return reportedInstructionPattern.MatchString(report) && reportedExecutionIsDirective(text)
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
