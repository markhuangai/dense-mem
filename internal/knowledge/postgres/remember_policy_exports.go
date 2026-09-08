package postgres

// These pure helpers are exposed only for the bounded repository compatibility
// facade. They do not expose a database handle or create another write path.

func NormalizeSynchronousRememberCommitInput(input SynchronousRememberCommitInput) SynchronousRememberCommitInput {
	return normalizeSynchronousRememberCommitInput(input)
}

func ValidateSynchronousRememberCommitInput(input SynchronousRememberCommitInput) error {
	return validateSynchronousRememberCommitInput(input)
}

func ValidateSynchronousRememberCommitInputWithSecurity(input SynchronousRememberCommitInput, allowUnsafe bool) error {
	return validateSynchronousRememberCommitInputWithSecurity(input, allowUnsafe)
}

func ValidateEvidenceSecurityResults(evidence []EvidenceInput, results []EvidenceSecurityResult, allowUnsafe bool) error {
	return validateEvidenceSecurityResults(evidence, results, allowUnsafe)
}
