package service

import (
	"context"
	"errors"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestInvariantScanWithAuditRecordsCleanAndErrorResults(t *testing.T) {
	ctx := context.Background()
	keyID := "key-1"

	t.Run("clean", func(t *testing.T) {
		client := &invariantScanClientStub{}
		audit := &MockAuditService{}
		audit.On("SystemQuery", mock.Anything, "invariant_scan", mock.MatchedBy(func(metadata map[string]interface{}) bool {
			return metadata["violations"] == 0 && metadata["status"] == "clean" && metadata["success"] == true
		}), &keyID, "admin", "127.0.0.1", "corr-1").Return(nil).Once()
		svc := NewInvariantScanService(client, audit)

		result, err := svc.ScanWithAudit(ctx, &keyID, "admin", "127.0.0.1", "corr-1")

		require.NoError(t, err)
		require.Equal(t, 1, client.calls)
		require.Equal(t, "clean", result.Status)
		require.Equal(t, 0, result.Violations)
		audit.AssertExpectations(t)
	})

	t.Run("scan error", func(t *testing.T) {
		client := &invariantScanClientStub{err: errors.New("neo4j unavailable")}
		audit := &MockAuditService{}
		audit.On("SystemQuery", mock.Anything, "invariant_scan", mock.MatchedBy(func(metadata map[string]interface{}) bool {
			return metadata["status"] == "error" && metadata["success"] == false && metadata["error"] == "invariant scan failed: neo4j unavailable"
		}), &keyID, "admin", "127.0.0.1", "corr-2").Return(nil).Once()
		svc := NewInvariantScanService(client, audit)

		result, err := svc.ScanWithAudit(ctx, &keyID, "admin", "127.0.0.1", "corr-2")

		require.ErrorContains(t, err, "neo4j unavailable")
		require.Equal(t, "error", result.Status)
		audit.AssertExpectations(t)
	})
}

type invariantScanClientStub struct {
	calls int
	err   error
}

func (s *invariantScanClientStub) ExecuteRead(context.Context, neo4j.ManagedTransactionWork) (any, error) {
	s.calls++
	return nil, s.err
}
