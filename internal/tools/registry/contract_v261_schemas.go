package registry

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/remember"
)

func terminalRememberOutputSchema(version string) map[string]any {
	return terminalRememberOutputSchemaForActions(version, terminalErrorNextActions(false))
}
func dreamTerminalRememberOutputSchema(version string) map[string]any {
	return terminalRememberOutputSchemaForActions(version, terminalErrorNextActions(true))
}

func terminalRememberOutputSchemaForActions(version string, nextActions []string) map[string]any {
	return closedObject(
		[]string{"contract_version", "submission_id", "submission_kind", "processing_state", "search_state", "correlation_id", "evidence", "relationship_results", "errors"},
		map[string]any{
			"contract_version":     schemaEnum(contractVersionSchemaValues(version)),
			"submission_id":        schemaString("Submission ID.", 128),
			"submission_kind":      schemaEnum([]string{"remember"}),
			"processing_state":     schemaEnum([]string{"completed", "failed"}),
			"search_state":         schemaEnum([]string{"current", "not_required"}),
			"correlation_id":       schemaString("Request correlation ID.", 128),
			"evidence":             array(terminalEvidenceSchema(), 0, 20),
			"relationship_results": submissionRelationshipResultsSchema(),
			"errors":               array(terminalErrorSchemaForActions(nextActions), 0, 50),
			"warnings":             stringArraySchema("Bounded server diagnostics about optional assessor context.", 20, 512),
		},
	)
}

func terminalEvidenceSchema() map[string]any {
	return closedObject(
		[]string{"disposition", "evidence_index", "superseded_evidence_ids", "search_state"},
		map[string]any{
			"disposition":             schemaEnum([]string{"stored", "not_stored"}),
			"evidence_id":             schemaString("Durable evidence ID when stored.", 128),
			"content_hash":            schemaString("Evidence content hash.", 128),
			"evidence_index":          map[string]any{"type": "integer", "minimum": 0},
			"superseded_evidence_ids": stringArraySchema("Evidence ID superseded by this evidence.", 50, 128),
			"search_state":            schemaEnum([]string{"current", "not_required"}),
			"reason":                  schemaString("Bounded reason when not stored.", 256),
		},
	)
}

func terminalCorrectionOutputSchema(version string) map[string]any {
	return closedObject(
		[]string{"contract_version", "submission_id", "submission_kind", "processing_state", "search_state", "correlation_id", "errors"},
		map[string]any{
			"contract_version":      schemaEnum(contractVersionSchemaValues(version)),
			"submission_id":         schemaString("Correction submission ID.", 128),
			"submission_kind":       schemaEnum([]string{"relationship_correction"}),
			"processing_state":      schemaEnum([]string{"awaiting_confirmation", "completed", "rejected", "failed"}),
			"search_state":          schemaEnum([]string{"current", "pending", "not_required", "failed"}),
			"correlation_id":        schemaString("Request correlation ID.", 128),
			"awaiting_confirmation": relationshipCorrectionConfirmationSchema(),
			"correction_result":     relationshipCorrectionResultSchema(),
			"errors":                array(terminalErrorSchema(), 0, 50),
		},
	)
}

func contractVersionSchemaValues(version string) []string {
	if version == domain.ContractVersion {
		return domain.AcceptedContractVersions()
	}
	return []string{version}
}

func terminalErrorSchema() map[string]any {
	return terminalErrorSchemaForActions(terminalErrorNextActions(false))
}

func terminalErrorSchemaForActions(nextActions []string) map[string]any {
	return closedObject(
		[]string{"code", "message", "retryable", "next_action", "remediation"},
		map[string]any{
			"code":        schemaEnum(contractErrorCodes()),
			"message":     schemaString("Bounded safe submission error.", 512),
			"retryable":   map[string]any{"type": "boolean"},
			"next_action": schemaEnum(nextActions),
			"remediation": schemaString("Bounded action the caller can take next.", 512),
			"reason_code": schemaString("Code-specific bounded reason.", 128),
			"details":     actionableErrorDetailsSchema(),
		},
	)
}

func terminalErrorNextActions(includeDreamFeedback bool) []string {
	result := []string{
		string(remember.TerminalNextActionRetrySameRequest),
		string(remember.TerminalNextActionResubmitRemember),
		string(remember.TerminalNextActionRetryCorrection),
		string(remember.TerminalNextActionContactOperator),
		string(remember.TerminalNextActionNone),
	}
	if includeDreamFeedback {
		result = append(result, string(remember.TerminalNextActionRetryDreamFeedback))
	}
	return result
}

func contractErrorCodes() []string {
	seen := map[string]struct{}{}
	result := make([]string, 0)
	add := func(values []string) {
		for _, value := range values {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	add(remember.TerminalErrorCodes())
	add(remember.SubmissionErrorCodes())
	return result
}
