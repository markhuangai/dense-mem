package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/observability"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	trace "github.com/markhuangai/dense-mem/internal/trace"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
	"gorm.io/gorm"
)

func buildContextApplication(store trace.SemanticTraceStore, logger observability.LogProvider) trace.Service {
	return trace.NewSemantic(store, logger)
}
func buildTraceStore(db *gorm.DB, rls storagepostgres.RLSHelper) trace.SemanticTraceStore {
	return tracepostgres.New(db, rls)
}
