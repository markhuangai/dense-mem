package postgres

import (
	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
)

type CreateIngestInput = knowledgepostgres.CreateIngestInput
type EvidenceInput = knowledgepostgres.EvidenceInput
type EvidenceFragment = knowledgepostgres.EvidenceFragment
type ApplyRelationshipDecisionInput = knowledgepostgres.ApplyRelationshipDecisionInput
type UpsertSearchDocumentInput = knowledgepostgres.UpsertSearchDocumentInput
type SearchDocumentResult = knowledgepostgres.SearchDocumentResult
type FullTextSearchInput = knowledgepostgres.FullTextSearchInput
type ExactVectorSearchInput = knowledgepostgres.ExactVectorSearchInput
type SearchReadiness = searchcontract.SearchReadiness
type EvidenceConflictResolutionInput = knowledgecontract.EvidenceConflictResolutionInput
type EvidenceConflictGetInput = conflictcontract.EvidenceConflictGetInput
type EvidenceSupportInput = knowledgepostgres.EvidenceSupportInput
type RetractEvidenceInput = knowledgepostgres.RetractEvidenceInput
type TraceRelationshipInput = tracepostgres.TraceRelationshipInput
type ApplyRelationshipSupportDecisionInput = knowledgepostgres.ApplyRelationshipSupportDecisionInput
type AdvanceSourceRevisionInput = knowledgepostgres.AdvanceSourceRevisionInput
