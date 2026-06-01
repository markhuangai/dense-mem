package skillpackservice

import (
	"context"
	"fmt"
	"strings"
)

const minAutoDecisionConfidence = 0.80

func conflictPrompt(idx int, item SkillPackItem, inspected InspectItem, reason string) ConflictPrompt {
	return ConflictPrompt{
		Index:             idx,
		Reason:            reason,
		FactIDs:           factSummaryIDs(inspected.ConflictingFacts),
		SupersededFactIDs: factSummaryIDs(inspected.SupersededMatches),
		AllowedActions:    allowedConflictActions(item, len(inspected.ConflictingFacts) > 0),
	}
}

func factSummaryIDs(facts []FactSummary) []string {
	if len(facts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		ids = append(ids, fact.FactID)
	}
	return ids
}

func allowedConflictActions(item SkillPackItem, hasActiveConflict bool) []string {
	actions := []string{DecisionImportAnyway, DecisionSkip}
	if hasActiveConflict {
		actions = append(actions, DecisionSupersedeLocal)
	}
	if item.SourceKind == SourceKindFact {
		actions = append(actions, DecisionDemoteToClaim)
	}
	return actions
}

func requiredDecisions(inspection *InspectResult, selected map[int]bool, decisions map[int]string) []ConflictPrompt {
	var missing []ConflictPrompt
	for _, prompt := range inspection.DecisionsRequired {
		if !selected[prompt.Index] {
			continue
		}
		action := decisions[prompt.Index]
		if action == "" {
			missing = append(missing, prompt)
			continue
		}
		if !actionAllowed(action, prompt.AllowedActions) {
			prompt.Reason = "invalid conflict decision"
			missing = append(missing, prompt)
		}
	}
	return missing
}

func (s *service) recommendMissingDecisions(ctx context.Context, profileID, artifactHash, sourceURL, mode string, inspection *InspectResult, selected map[int]bool, decisions map[int]string, apply bool) {
	for i := range inspection.DecisionsRequired {
		prompt := &inspection.DecisionsRequired[i]
		if !selected[prompt.Index] || decisions[prompt.Index] != "" {
			continue
		}
		recommendation, ok := s.recommendDecision(ctx, profileID, artifactHash, sourceURL, mode, inspection, *prompt, apply)
		prompt.Recommendation = recommendation
		if apply && ok {
			decisions[prompt.Index] = recommendation.Action
		}
	}
}

func (s *service) recommendDecision(ctx context.Context, profileID, artifactHash, sourceURL, mode string, inspection *InspectResult, prompt ConflictPrompt, apply bool) (*DecisionRecommendation, bool) {
	if s.deps.ConflictDecider == nil {
		return &DecisionRecommendation{Error: "conflict decider is required"}, false
	}
	if prompt.Index < 0 || prompt.Index >= len(inspection.Items) {
		return &DecisionRecommendation{Error: fmt.Sprintf("decision prompt index %d is out of range", prompt.Index)}, false
	}
	prompt.Recommendation = nil
	result, err := s.deps.ConflictDecider.Decide(ctx, ConflictDecisionRequest{
		ProfileID:    profileID,
		ArtifactHash: artifactHash,
		Mode:         mode,
		SourceURL:    sourceURL,
		Item:         inspection.Items[prompt.Index].Item,
		Inspection:   inspection.Items[prompt.Index],
		Prompt:       prompt,
	})
	if err != nil {
		return &DecisionRecommendation{Error: err.Error()}, false
	}
	recommendation := &DecisionRecommendation{
		Action:     result.Action,
		Confidence: result.Confidence,
		Rationale:  result.Rationale,
		Model:      result.Model,
	}
	if !actionAllowed(result.Action, prompt.AllowedActions) {
		recommendation.Error = fmt.Sprintf("recommended action %q is not allowed", result.Action)
		return recommendation, false
	}
	if result.Confidence < 0 || result.Confidence > 1 {
		recommendation.Error = fmt.Sprintf("confidence %.3f is outside [0,1]", result.Confidence)
		return recommendation, false
	}
	if strings.TrimSpace(result.Rationale) == "" {
		recommendation.Error = "rationale is required"
		return recommendation, false
	}
	if apply && result.Confidence < minAutoDecisionConfidence {
		recommendation.Error = fmt.Sprintf("confidence %.2f below auto-decision threshold %.2f", result.Confidence, minAutoDecisionConfidence)
		return recommendation, false
	}
	return recommendation, true
}

func actionAllowed(action string, allowed []string) bool {
	for _, candidate := range allowed {
		if action == candidate {
			return true
		}
	}
	return false
}

func duplicateInspectionStatus(status string) bool {
	return status == "duplicate_fact" || status == "already_claimed"
}
