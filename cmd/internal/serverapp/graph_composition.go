package serverapp

import (
	graphapp "github.com/markhuangai/dense-mem/internal/graph"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

func buildGraphApplication(store graphcontract.Store) graphapp.Service {
	if store == nil {
		return graphapp.New(nil)
	}
	return graphapp.New(store)
}
