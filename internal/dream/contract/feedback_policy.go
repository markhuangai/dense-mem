package contract

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
)

// LifecycleStatus is the sole Dream feedback decision-to-status mapping.
func LifecycleStatus(decision string) string {
	switch decision {
	case "reject":
		return string(domain.DreamStatusRejected)
	case "stale":
		return string(domain.DreamStatusStale)
	case "reinforce":
		return string(domain.DreamStatusReinforced)
	default:
		return ""
	}
}

func NormalizeUpdateHypothesisStatusInput(input UpdateHypothesisStatusInput) UpdateHypothesisStatusInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ActorProfileID = strings.TrimSpace(input.ActorProfileID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	input.Status = strings.TrimSpace(input.Status)
	input.Decision = strings.TrimSpace(input.Decision)
	input.InvalidatedReason = strings.TrimSpace(input.InvalidatedReason)
	return input
}

func ValidateUpdateHypothesisStatusInput(input UpdateHypothesisStatusInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"actor_profile_id": input.ActorProfileID,
		"hypothesis_id":    input.HypothesisID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if !domain.DreamStatus(input.Status).IsValid() || input.Status == "submitted" {
		return fmt.Errorf("unsupported hypothesis status %q", input.Status)
	}
	expected := LifecycleStatus(input.Decision)
	if expected == "" {
		return fmt.Errorf("unsupported feedback decision %q", input.Decision)
	}
	if input.Status != expected {
		return fmt.Errorf("decision %q requires status %q", input.Decision, expected)
	}
	return nil
}
