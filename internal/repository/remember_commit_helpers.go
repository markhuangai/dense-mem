package repository

import knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"

// Remember validation remains available to legacy repository tests and
// fixtures, but the knowledge PostgreSQL owner supplies the policy.
func normalizeSynchronousRememberCommitInput(input SynchronousRememberCommitInput) SynchronousRememberCommitInput {
	normalized := knowledgepostgres.NormalizeSynchronousRememberCommitInput(toKnowledgeSynchronousRememberCommitInput(input))
	return fromKnowledgeSynchronousRememberCommitInput(normalized)
}

func validateSynchronousRememberCommitInput(input SynchronousRememberCommitInput) error {
	return validateSynchronousRememberCommitInputWithSecurity(input, false)
}

func validateSynchronousRememberCommitInputWithSecurity(input SynchronousRememberCommitInput, allowUnsafe bool) error {
	return knowledgepostgres.ValidateSynchronousRememberCommitInputWithSecurity(
		toKnowledgeSynchronousRememberCommitInput(input),
		allowUnsafe,
	)
}

func validateEvidenceSecurityResults(evidence []EvidenceInput, results []EvidenceSecurityResult, allowUnsafe bool) error {
	converted := make([]knowledgepostgres.EvidenceSecurityResult, len(results))
	for index, result := range results {
		converted[index] = knowledgepostgres.EvidenceSecurityResult{
			FragmentID:    result.FragmentID,
			EvidenceID:    result.EvidenceID,
			EvidenceIndex: result.EvidenceIndex,
			Decision:      result.Decision,
			Safe:          result.Safe,
			Signals:       make([]knowledgepostgres.SecuritySignalInput, len(result.Signals)),
		}
		for signalIndex, signal := range result.Signals {
			converted[index].Signals[signalIndex] = knowledgepostgres.SecuritySignalInput(signal)
		}
		if result.Signals == nil {
			converted[index].Signals = nil
		}
	}
	return knowledgepostgres.ValidateEvidenceSecurityResults(evidence, converted, allowUnsafe)
}
