package registry

func validEvidenceOnlySubmission() map[string]any {
	return evidenceOnlyForContent("Dense-Mem uses PostgreSQL.")
}
func withEvidenceOnlyRequest(input map[string]any) map[string]any {
	output := cloneMap(input)
	output["target_space"] = "team_shared"
	if _, ok := output["idempotency_key"]; !ok {
		output["idempotency_key"] = "remember-test-batch"
	}
	return output
}
