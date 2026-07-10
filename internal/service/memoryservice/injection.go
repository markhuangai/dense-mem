package memoryservice

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var injectionSignals = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"ignore_instructions", regexp.MustCompile(`(?i)\b(ignore|disregard|override|forget)\s+(?:all\s+|any\s+)?(?:(?:previous|prior)\s+)?(?:(?:system|developer)\s+)?(?:instructions?|messages?|prompts?)\b`)},
	{"prompt_exfiltration", regexp.MustCompile(`(?i)\b(reveal|print|show|repeat|expose|leak)\s+(the\s+)?(system|developer|hidden)\s+(prompt|message|instructions?)\b`)},
	{"credential_exfiltration", regexp.MustCompile(`(?i)\b(send|post|upload|exfiltrate|reveal|print)\b.{0,48}\b(api[-_ ]?keys?|tokens?|passwords?|credentials?|secrets?)\b`)},
	{"tool_coercion", regexp.MustCompile(`(?i)\b(call|invoke|execute|run|use)\s+(the\s+)?(tool|function|shell|terminal|mcp)\b`)},
	{"role_delimiter", regexp.MustCompile(`(?i)(<\/?\s*(system|developer|assistant)\s*>|\[\s*(system|developer)\s*\]|^\s*(system|developer)\s*:)`)},
	{"jailbreak", regexp.MustCompile(`(?i)\b(jailbreak|developer\s+mode|do\s+anything\s+now|bypass\s+(the\s+)?safety)\b`)},
}

func assessInjection(content string) domainSecurityAssessment {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, content)
	signals := make([]string, 0, 2)
	for _, candidate := range injectionSignals {
		if candidate.pattern.MatchString(cleaned) {
			signals = append(signals, candidate.name)
		}
	}
	sort.Strings(signals)
	return domainSecurityAssessment{quarantined: len(signals) > 0, signals: signals}
}

type domainSecurityAssessment struct {
	quarantined bool
	signals     []string
}

func assessEvidenceInjection(evidence []domain.MemoryEvidence) domain.MemorySecurityAssessment {
	signalSet := map[string]struct{}{}
	for _, item := range evidence {
		assessment := assessInjection(item.Content)
		for _, signal := range assessment.signals {
			signalSet[signal] = struct{}{}
		}
	}
	signals := make([]string, 0, len(signalSet))
	for signal := range signalSet {
		signals = append(signals, signal)
	}
	sort.Strings(signals)
	return domain.MemorySecurityAssessment{Quarantined: len(signals) > 0, Signals: signals}
}
