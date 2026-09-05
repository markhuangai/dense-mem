package serverapp

import "github.com/markhuangai/dense-mem/internal/service/graphview"

func buildGraphApplication(store graphview.SemanticStore) graphview.Service {
	return graphview.NewSemantic(store)
}
