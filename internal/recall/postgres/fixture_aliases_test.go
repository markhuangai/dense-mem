package postgres

import (
	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

type CreateIngestInput = knowledgepostgres.CreateIngestInput
type EvidenceInput = knowledgepostgres.EvidenceInput
type EvidenceFragment = knowledgepostgres.EvidenceFragment
type UpsertSearchDocumentInput = knowledgepostgres.UpsertSearchDocumentInput
type SearchDocumentResult = knowledgepostgres.SearchDocumentResult
type FullTextSearchInput = knowledgepostgres.FullTextSearchInput
type ExactVectorSearchInput = knowledgepostgres.ExactVectorSearchInput
type SearchReadiness = searchcontract.SearchReadiness
type EvidenceConflictResolutionInput = knowledgecontract.EvidenceConflictResolutionInput
type EvidenceConflictGetInput = conflictcontract.EvidenceConflictGetInput
type RetractEvidenceInput = knowledgepostgres.RetractEvidenceInput
type AdvanceSourceRevisionInput = knowledgepostgres.AdvanceSourceRevisionInput
