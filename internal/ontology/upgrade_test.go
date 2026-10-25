package ontology

import (
	"context"
	"testing"

	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceUpgradeRequiresSystemComposition(t *testing.T) {
	var missing *MaintenanceUpgradeService
	require.ErrorIs(t, missing.Prepare(context.Background(), nil), contract.ErrConfiguration)
	service := NewMaintenanceUpgradeService(nil, nil)
	require.ErrorIs(t, service.Prepare(context.Background(), nil), contract.ErrConfiguration)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{Role: "manager"})
	require.ErrorIs(t, service.Prepare(ctx, nil), contract.ErrUnauthorized)
	_, err := service.Complete(ctx)
	require.ErrorIs(t, err, contract.ErrUnauthorized)
}
