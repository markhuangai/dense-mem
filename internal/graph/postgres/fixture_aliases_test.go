package postgres

import knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
import graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
import tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"

type CreateIngestInput = knowledgepostgres.CreateIngestInput
type EvidenceInput = knowledgepostgres.EvidenceInput
type EntityRecord = knowledgepostgres.EntityRecord
type ApplyRelationshipDecisionInput = knowledgepostgres.ApplyRelationshipDecisionInput
type EvidenceSupportInput = knowledgepostgres.EvidenceSupportInput
type UpsertSearchDocumentInput = knowledgepostgres.UpsertSearchDocumentInput
type UpsertValueInput = knowledgepostgres.UpsertValueInput
type AppendCrossReferenceInput = knowledgepostgres.AppendCrossReferenceInput
type SemanticGraphQuery = graphcontract.Query
type SemanticGraphNodeDetailInput = graphcontract.NodeDetailInput
type TraceRelationshipInput = tracepostgres.TraceRelationshipInput

var ErrTraceRelationshipNotFound = tracepostgres.ErrTraceRelationshipNotFound
