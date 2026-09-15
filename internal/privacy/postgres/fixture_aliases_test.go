package postgres

import (
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
)

type CreateIngestInput = knowledgepostgres.CreateIngestInput
type EvidenceInput = knowledgepostgres.EvidenceInput
type ApplyRelationshipDecisionInput = knowledgepostgres.ApplyRelationshipDecisionInput
type EvidenceSupportInput = knowledgepostgres.EvidenceSupportInput
type CorrectRelationshipInput = knowledgepostgres.CorrectRelationshipInput
type RelationshipCorrectionPatch = knowledgepostgres.RelationshipCorrectionPatch
type RelationshipCorrectionEntityPatch = knowledgepostgres.RelationshipCorrectionEntityPatch
type RelationshipCorrectionSupport = knowledgepostgres.RelationshipCorrectionSupport

type RememberAttemptRecordInput = knowledgecontract.RememberAttemptRecordInput
type TraceRelationshipInput = tracepostgres.TraceRelationshipInput
