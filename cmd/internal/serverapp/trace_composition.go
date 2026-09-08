package serverapp

import "github.com/markhuangai/dense-mem/internal/trace"

func buildContextApplication(store trace.SemanticTraceStore) trace.Service {
	return trace.NewSemantic(store)
}
