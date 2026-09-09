package serverapp

import (
	graphapp "github.com/markhuangai/dense-mem/internal/graph"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

type graphStoreSource interface {
	GraphStore() graphcontract.Store
}

func buildGraphApplication(source graphStoreSource) graphapp.Service {
	if source == nil {
		return graphapp.New(nil)
	}
	return graphapp.New(source.GraphStore())
}
