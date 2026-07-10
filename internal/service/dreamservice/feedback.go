package dreamservice

import (
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func dreamResolutionEvidence(d *domain.Dream, decision, feedback string) (memoryservice.EvidenceInput, error) {
	content := dreamResolutionContent(d, decision, feedback)
	if strings.TrimSpace(feedback) == "" {
		return memoryservice.EvidenceInput{}, fmt.Errorf("resolve dream feedback: feedback is required for %s", decision)
	}
	labels := []string{"dream_feedback", "dream_resolution"}
	if decision == "confirm_false" {
		labels = append(labels, "dream_rejected")
	} else {
		labels = append(labels, "dream_confirmed")
	}
	return memoryservice.EvidenceInput{
		Content:        content,
		SourceType:     string(domain.SourceTypeManual),
		Source:         "dream_feedback:" + d.DreamID,
		Authority:      string(domain.AuthorityAuthoritative),
		SourceGroup:    "user-dream-feedback:" + d.DreamID,
		IdempotencyKey: "dream-feedback:" + d.DreamID + ":" + decision,
		Labels:         labels,
		Metadata: map[string]any{
			"dream_id":         d.DreamID,
			"dream_status":     string(d.Status),
			"dream_likelihood": d.Likelihood,
			"dream_confidence": d.Confidence,
			"dream_decision":   decision,
		},
	}, nil
}

func dreamResolutionContent(d *domain.Dream, decision, feedback string) string {
	_ = d
	_ = decision
	return strings.TrimSpace(feedback)
}
