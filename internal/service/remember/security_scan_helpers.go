package remember

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

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
