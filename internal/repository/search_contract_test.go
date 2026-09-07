package repository

import (
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

var (
	_ searchcontract.SearchRepository = (*SearchRepositoryImpl)(nil)
	_ recallcontract.Repository       = (*SearchRepositoryImpl)(nil)
	_ recallcontract.SearchRepository = (*SearchRepositoryImpl)(nil)
)
