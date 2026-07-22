package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestInvariantScanWithAuditRecordsRemovedResult(t *testing.T) {
	ctx := context.Background()
	keyID := "key-1"
	audit := &MockAuditService{}
	audit.On("SystemQuery", mock.Anything, "invariant_scan", mock.MatchedBy(func(metadata map[string]interface{}) bool {
		return metadata["status"] == "removed" &&
			metadata["success"] == false &&
			metadata["error"] == ErrInvariantScanRemoved.Error()
	}), &keyID, "admin", "127.0.0.1", "corr-1").Return(nil).Once()
	svc := NewInvariantScanService(nil, audit)

	result, err := svc.ScanWithAudit(ctx, &keyID, "admin", "127.0.0.1", "corr-1")

	require.True(t, errors.Is(err, ErrInvariantScanRemoved))
	require.Equal(t, "removed", result.Status)
	require.Equal(t, 0, result.Violations)
	audit.AssertExpectations(t)
}
