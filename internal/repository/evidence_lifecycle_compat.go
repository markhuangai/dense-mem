package repository

import knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"

func validateRetractEvidenceInput(input RetractEvidenceInput) error {
	return knowledgepostgres.ValidateRetractEvidenceInput(input)
}
