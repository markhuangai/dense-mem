package postgres

import (
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"gorm.io/gorm"
)

type TraceEvidenceInput = tracecontract.Input
type EvidenceTraceResult = tracecontract.EvidenceTraceResult
type TraceEvidenceFragment = tracecontract.TraceEvidenceFragment
type TraceEvidenceLifecycleEvent = tracecontract.TraceEvidenceLifecycleEvent
type TraceSearchDocument = tracecontract.TraceSearchDocument
type Store struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}
