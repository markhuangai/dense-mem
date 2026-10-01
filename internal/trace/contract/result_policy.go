package contract

// Completeness preserves the inclusive budgets of an already normalized trace.
func Completeness(input Input, result *RelationshipTraceResult) (bool, string) {
	if len(result.SemanticEdges) >= input.MaxEdges {
		return true, "max_edges"
	}
	if len(result.Observations) >= input.MaxEvents || len(result.EvidenceSupports) >= input.MaxEvents ||
		len(result.SupportDecisionEvents) >= input.MaxEvents || len(result.VerificationEvents) >= input.MaxEvents ||
		len(result.Transitions) >= input.MaxEvents || len(result.EvidenceLifecycleEvents) >= input.MaxEvents {
		return true, "max_events"
	}
	return false, ""
}
