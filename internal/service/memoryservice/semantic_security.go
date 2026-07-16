package memoryservice

import (
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const semanticSecurityMaxSignals = 64

type semanticSecuritySignal struct {
	EvidenceID string `json:"evidence_id"`
	Kind       string `json:"kind"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

type semanticEvidenceSecurityAssessment struct {
	EvidenceIndex int
	Assessment    domain.EvidenceSecurityAssessment
}

func semanticEvidenceID(index int) string {
	return fmt.Sprintf("ev_%d", index)
}

func semanticSecurityEvidenceByID(evidence []domain.MemoryEvidence) map[string]domain.MemoryEvidence {
	out := make(map[string]domain.MemoryEvidence, len(evidence))
	for _, item := range evidence {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		item.Content = content
		out[semanticEvidenceID(item.Index)] = item
	}
	return out
}

func validateSemanticSecuritySignals(signals []semanticSecuritySignal, evidenceByID map[string]domain.MemoryEvidence, eventKind domain.EvidenceSecurityEventKind) ([]semanticEvidenceSecurityAssessment, error) {
	if signals == nil {
		return nil, errorsSecuritySignalsRequired(eventKind)
	}
	if len(signals) == 0 {
		return nil, nil
	}
	if len(signals) > semanticSecurityMaxSignals {
		return nil, fmt.Errorf("semantic security: security_signals exceeds limit")
	}
	grouped := map[string][]domain.EvidenceSecuritySignal{}
	order := make([]string, 0)
	for i, signal := range signals {
		evidenceID := strings.TrimSpace(signal.EvidenceID)
		evidence, ok := evidenceByID[evidenceID]
		if !ok {
			return nil, fmt.Errorf("semantic security: security_signals[%d].evidence_id %q is unknown", i, evidenceID)
		}
		kind := domain.EvidenceSecuritySignalKind(strings.TrimSpace(signal.Kind))
		if !kind.IsValid() {
			return nil, fmt.Errorf("semantic security: security_signals[%d].kind is invalid", i)
		}
		contentRunes := []rune(evidence.Content)
		if signal.Start < 0 || signal.End <= signal.Start || signal.End > len(contentRunes) {
			return nil, fmt.Errorf("semantic security: security_signals[%d].span is invalid", i)
		}
		if _, exists := grouped[evidenceID]; !exists {
			order = append(order, evidenceID)
		}
		grouped[evidenceID] = append(grouped[evidenceID], domain.EvidenceSecuritySignal{
			Kind:      kind,
			Severity:  semanticModelSignalSeverity(kind),
			SpanStart: signal.Start,
			SpanEnd:   signal.End,
			Quote:     string(contentRunes[signal.Start:signal.End]),
		})
	}
	sort.SliceStable(order, func(i, j int) bool {
		return evidenceByID[order[i]].Index < evidenceByID[order[j]].Index
	})
	out := make([]semanticEvidenceSecurityAssessment, 0, len(order))
	for _, evidenceID := range order {
		out = append(out, semanticEvidenceSecurityAssessment{
			EvidenceIndex: evidenceByID[evidenceID].Index,
			Assessment: domain.EvidenceSecurityAssessment{
				Decision:  domain.EvidenceSecurityQuarantine,
				EventKind: eventKind,
				Signals:   grouped[evidenceID],
			},
		})
	}
	return out, nil
}

func errorsSecuritySignalsRequired(eventKind domain.EvidenceSecurityEventKind) error {
	switch eventKind {
	case domain.EvidenceSecurityEventReviewerSignal:
		return fmt.Errorf("semantic reviewer: security_signals is required")
	case domain.EvidenceSecurityEventVerifierSignal:
		return fmt.Errorf("semantic verifier: security_signals is required")
	default:
		return fmt.Errorf("semantic security: security_signals is required")
	}
}

func semanticModelSignalSeverity(kind domain.EvidenceSecuritySignalKind) domain.EvidenceSecuritySeverity {
	if kind == domain.EvidenceSignalHiddenControlMarkup {
		return domain.EvidenceSecuritySeverityCritical
	}
	return domain.EvidenceSecuritySeverityHigh
}

func verifierSecurityAssessments(req semanticVerifierRequest, resp semanticVerifierResponse) ([]semanticEvidenceSecurityAssessment, error) {
	return validateSemanticSecuritySignals(resp.SecuritySignals, req.evidenceByID, domain.EvidenceSecurityEventVerifierSignal)
}

func semanticVerifierEvidence(relationships []repository.SemanticRelationshipInput, evidence []domain.MemoryEvidence) ([]semanticVerifierEvidenceRequest, map[string]domain.MemoryEvidence) {
	byIndex := map[int]domain.MemoryEvidence{}
	for _, item := range evidence {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		item.Content = content
		byIndex[item.Index] = item
	}
	for _, relationship := range relationships {
		if _, ok := byIndex[relationship.EvidenceIndex]; ok {
			continue
		}
		content := strings.TrimSpace(relationship.Quote)
		if content == "" {
			content = semanticVerifierStatement(relationship)
		}
		byIndex[relationship.EvidenceIndex] = domain.MemoryEvidence{
			Index:   relationship.EvidenceIndex,
			Content: content,
		}
	}
	indexes := make([]int, 0, len(byIndex))
	for index := range byIndex {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	payload := make([]semanticVerifierEvidenceRequest, 0, len(indexes))
	evidenceByID := make(map[string]domain.MemoryEvidence, len(indexes))
	for _, index := range indexes {
		item := byIndex[index]
		evidenceID := semanticEvidenceID(index)
		payload = append(payload, semanticVerifierEvidenceRequest{
			EvidenceID: evidenceID,
			Content:    item.Content,
		})
		evidenceByID[evidenceID] = item
	}
	return payload, evidenceByID
}

func semanticVerifierRuneOffset(rel repository.SemanticRelationshipInput, start bool) int {
	if start {
		return rel.SpanStart
	}
	return rel.SpanEnd
}
