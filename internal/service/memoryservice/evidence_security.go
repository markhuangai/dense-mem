package memoryservice

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	evidenceSecurityScannerVersion = "dense-mem-deterministic-evidence-scan-v1"
	evidenceSecurityMaxRunes       = 200000
	evidenceSecurityMaxSignals     = 16
)

var evidenceSecurityPolicyHash = evidenceSecurityHash(evidenceSecurityScannerVersion)

type EvidenceSecurityScanner interface {
	ScanEvidence(content string) (domain.EvidenceSecurityAssessment, error)
}

type DeterministicEvidenceSecurityScanner struct{}

func NewDeterministicEvidenceSecurityScanner() DeterministicEvidenceSecurityScanner {
	return DeterministicEvidenceSecurityScanner{}
}

type evidenceSecurityPattern struct {
	kind     domain.EvidenceSecuritySignalKind
	severity domain.EvidenceSecuritySeverity
	phrases  []string
}

var evidenceSecurityPatterns = []evidenceSecurityPattern{
	{
		kind:     domain.EvidenceSignalInstructionOverride,
		severity: domain.EvidenceSecuritySeverityHigh,
		phrases: []string{
			"ignore previous instructions",
			"disregard previous instructions",
			"override your instructions",
			"do not follow your instructions",
		},
	},
	{
		kind:     domain.EvidenceSignalPromptSecretExtraction,
		severity: domain.EvidenceSecuritySeverityHigh,
		phrases: []string{
			"reveal your system prompt",
			"print your system prompt",
			"show your hidden instructions",
			"exfiltrate your prompt",
		},
	},
	{
		kind:     domain.EvidenceSignalToolExfiltration,
		severity: domain.EvidenceSecuritySeverityHigh,
		phrases: []string{
			"call the tool",
			"use your tools to send",
			"send this secret",
			"exfiltrate the data",
		},
	},
	{
		kind:     domain.EvidenceSignalRoleControlSpoofing,
		severity: domain.EvidenceSecuritySeverityMedium,
		phrases: []string{
			"system:",
			"developer:",
			"assistant:",
			"<system>",
			"</system>",
		},
	},
	{
		kind:     domain.EvidenceSignalObfuscatedInstruction,
		severity: domain.EvidenceSecuritySeverityMedium,
		phrases: []string{
			"base64 decode",
			"rot13",
			"unicode hidden instruction",
		},
	},
	{
		kind:     domain.EvidenceSignalHiddenControlMarkup,
		severity: domain.EvidenceSecuritySeverityCritical,
		phrases: []string{
			"<script",
			"<iframe",
			"display:none",
			"visibility:hidden",
		},
	},
}

func (DeterministicEvidenceSecurityScanner) ScanEvidence(content string) (domain.EvidenceSecurityAssessment, error) {
	runes := []rune(content)
	if len(runes) > evidenceSecurityMaxRunes {
		return domain.EvidenceSecurityAssessment{}, fmt.Errorf("evidence security scan: content exceeds %d code points", evidenceSecurityMaxRunes)
	}
	signals := make([]domain.EvidenceSecuritySignal, 0)
	lowerRunes := []rune(strings.ToLower(content))
	for _, pattern := range evidenceSecurityPatterns {
		for _, phrase := range pattern.phrases {
			signals = append(signals, scanSecurityPhrase(runes, lowerRunes, phrase, pattern)...)
			if len(signals) >= evidenceSecurityMaxSignals {
				signals = signals[:evidenceSecurityMaxSignals]
				return evidenceSecurityAssessment(signals), nil
			}
		}
	}
	for i, r := range runes {
		if isHiddenControlRune(r) {
			signals = append(signals, domain.EvidenceSecuritySignal{
				Kind:      domain.EvidenceSignalHiddenControlMarkup,
				Severity:  domain.EvidenceSecuritySeverityMedium,
				SpanStart: i,
				SpanEnd:   i + 1,
				Quote:     string(r),
			})
			if len(signals) >= evidenceSecurityMaxSignals {
				signals = signals[:evidenceSecurityMaxSignals]
				break
			}
		}
	}
	return evidenceSecurityAssessment(signals), nil
}

func scanSecurityPhrase(original, lower []rune, phrase string, pattern evidenceSecurityPattern) []domain.EvidenceSecuritySignal {
	needle := []rune(strings.ToLower(phrase))
	if len(needle) == 0 || len(needle) > len(lower) {
		return nil
	}
	out := make([]domain.EvidenceSecuritySignal, 0, 1)
	for start := 0; start <= len(lower)-len(needle); start++ {
		if !runeSliceEqual(lower[start:start+len(needle)], needle) {
			continue
		}
		out = append(out, domain.EvidenceSecuritySignal{
			Kind:      pattern.kind,
			Severity:  pattern.severity,
			SpanStart: start,
			SpanEnd:   start + len(needle),
			Quote:     string(original[start : start+len(needle)]),
		})
		if len(out) >= evidenceSecurityMaxSignals {
			return out
		}
	}
	return out
}

func evidenceSecurityAssessment(signals []domain.EvidenceSecuritySignal) domain.EvidenceSecurityAssessment {
	return domain.EvidenceSecurityAssessment{
		Decision:       evidenceSecurityDecision(signals),
		EventKind:      domain.EvidenceSecurityEventDeterministicScan,
		ScanPolicyHash: evidenceSecurityPolicyHash,
		Signals:        signals,
	}
}

func evidenceSecurityDecision(signals []domain.EvidenceSecuritySignal) domain.EvidenceSecurityDecision {
	if len(signals) == 0 {
		return domain.EvidenceSecurityPass
	}
	highOrCritical := 0
	for _, signal := range signals {
		if signal.Severity == domain.EvidenceSecuritySeverityCritical {
			return domain.EvidenceSecurityQuarantine
		}
		if signal.Severity == domain.EvidenceSecuritySeverityHigh {
			highOrCritical++
		}
	}
	if highOrCritical >= 2 || len(signals) >= 3 {
		return domain.EvidenceSecurityQuarantine
	}
	return domain.EvidenceSecurityGuarded
}

func runeSliceEqual(left, right []rune) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func isHiddenControlRune(r rune) bool {
	if r == '\n' || r == '\r' || r == '\t' {
		return false
	}
	return unicode.IsControl(r) || r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\ufeff'
}

func evidenceSecurityHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func evidenceScanner(deps Dependencies) EvidenceSecurityScanner {
	if deps.EvidenceScanner != nil {
		return deps.EvidenceScanner
	}
	return NewDeterministicEvidenceSecurityScanner()
}
