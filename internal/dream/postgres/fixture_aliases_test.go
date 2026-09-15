package postgres

import (
	"crypto/sha256"
	"encoding/hex"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

type CreateIngestInput = knowledgepostgres.CreateIngestInput
type EvidenceInput = knowledgepostgres.EvidenceInput
type EvidenceFragment = knowledgepostgres.EvidenceFragment
type ApplyRelationshipDecisionInput = knowledgepostgres.ApplyRelationshipDecisionInput
type RelationshipDecisionResult = knowledgepostgres.RelationshipDecisionResult
type EvidenceSupportInput = knowledgepostgres.EvidenceSupportInput
type RetractEvidenceInput = knowledgepostgres.RetractEvidenceInput
type SecurityEventInput = knowledgepostgres.SecurityEventInput
type UpsertSearchDocumentInput = knowledgepostgres.UpsertSearchDocumentInput
type SearchDocumentResult = knowledgepostgres.SearchDocumentResult
type SearchDocumentEmbedding = knowledgepostgres.SearchDocumentEmbedding
type SecurityEventDraft = knowledgecontract.SecurityEventDraft
type CompleteSearchDocumentsWithEmbeddingsInput = knowledgecontract.CompleteSearchDocumentsWithEmbeddingsInput
type EnsureSemanticPredicateCandidateInput = knowledgecontract.EnsureSemanticPredicateCandidateInput

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func searchDocumentHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
