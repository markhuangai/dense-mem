package serverapp

import "github.com/markhuangai/dense-mem/internal/service/contextservice"

func buildContextApplication(store contextservice.SemanticTraceStore) contextservice.Service {
	return contextservice.NewSemantic(store)
}
