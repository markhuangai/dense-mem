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
		Source:         "dream_feedback:" + d.DreamID,
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
	trimmedFeedback := strings.TrimSpace(feedback)
	parts := []string{}
	if decision == "confirm_false" {
		parts = append(parts, "Incorrect dream hypothesis: "+trimmedFeedback)
	} else if trimmedFeedback != "" {
		parts = append(parts, trimmedFeedback)
	}
	parts = append(parts, "Dense-Mem dream hypothesis: "+d.Hypothesis)
	if d.WhatIf != "" {
		parts = append(parts, "What-if: "+d.WhatIf)
	}
	if d.PossibleOutcome != "" {
		parts = append(parts, "Possible outcome: "+d.PossibleOutcome)
	}
	if decision == "confirm_true" {
		parts = append(parts, "Dream decision: confirmed accurate")
	} else if decision == "confirm_false" {
		parts = append(parts, "Dream decision: confirmed false")
	}
	return strings.Join(parts, "\n")
}
