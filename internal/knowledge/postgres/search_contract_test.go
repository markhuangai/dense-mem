package postgres

import (
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

var (
	_ searchcontract.SearchRepository = (*searchFixtureStore)(nil)
	_ recallcontract.Repository       = (*searchFixtureStore)(nil)
	_ recallcontract.SearchRepository = (*searchFixtureStore)(nil)
)
