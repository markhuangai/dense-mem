package serverapp

// This compatibility facade keeps the historical serverapp bootstrap API
// stable while authority classification lives in internal/operations.

import (
	"context"

	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

var errAuthorityBlocked = operations.ErrAuthorityBlocked

const cutoverMarkerVersion = operations.CutoverMarkerVersion

type authorityMode = operations.AuthorityMode

const authorityActive = operations.AuthorityActive

type authorityBootstrap = operations.AuthorityBootstrap
type authorityBootstrapStore = operationscontract.AuthorityReader

func ClassifyAuthority(ctx context.Context, store authorityBootstrapStore) (authorityBootstrap, error) {
	return operations.ClassifyAuthority(ctx, store)
}

func checkActiveAuthority(authority authorityBootstrap) error {
	return operations.CheckActiveAuthority(authority)
}
