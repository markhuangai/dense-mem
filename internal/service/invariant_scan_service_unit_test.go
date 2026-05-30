package service

import (
	"context"
	"errors"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type unitInvariantClient struct {
	err error
}

func (c *unitInvariantClient) ExecuteRead(context.Context, neo4j.ManagedTransactionWork) (any, error) {
	if c.err != nil {
		return nil, c.err
	}
	return nil, nil
}

func TestInvariantScanServiceCleanAndErrorAudit(t *testing.T) {
	ctx := context.Background()
	audit := new(MockAuditService)
	audit.On("SystemQuery", ctx, "invariant_scan", mock.MatchedBy(func(metadata map[string]interface{}) bool {
		return metadata["violations"] == 0 && metadata["status"] == "clean" && metadata["success"] == true
	}), mock.Anything, "admin", "127.0.0.1", "corr-clean").Return(nil)
	svc := NewInvariantScanService(&unitInvariantClient{}, audit)

	result, err := svc.ScanWithAudit(ctx, nil, "admin", "127.0.0.1", "corr-clean")
	require.NoError(t, err)
	require.Equal(t, "clean", result.Status)
	require.Equal(t, 0, result.Violations)

	scanErr := errors.New("neo4j failed")
	audit.On("SystemQuery", ctx, "invariant_scan", mock.MatchedBy(func(metadata map[string]interface{}) bool {
		return metadata["violations"] == 0 && metadata["status"] == "error" && metadata["success"] == false && metadata["error"] != ""
	}), mock.Anything, "admin", "127.0.0.1", "corr-error").Return(errors.New("audit failed"))
	svc = NewInvariantScanService(&unitInvariantClient{err: scanErr}, audit)

	result, err = svc.ScanWithAudit(ctx, nil, "admin", "127.0.0.1", "corr-error")
	require.ErrorIs(t, err, scanErr)
	require.Equal(t, "error", result.Status)
	audit.AssertExpectations(t)
}
