package processor

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/stretchr/testify/require"
)

type rememberEvidenceHandleLedger struct {
	rememberFailureLedgerStub
	resolved map[string]map[string]string
	err      error
	scope    []string
}

func (s *rememberEvidenceHandleLedger) ResolveMemoryHandles(_ context.Context, team, owner, space string, _ map[string][]string) (map[string]map[string]string, error) {
	s.scope = []string{team, owner, space}
	return s.resolved, s.err
}

func TestEvidenceOnlyUnitRememberResolvesHistoricalSupersessionWithoutRewritingRequest(t *testing.T) {
	historical, destination := uuid.NewString(), uuid.NewString()
	ledger := &rememberEvidenceHandleLedger{resolved: map[string]map[string]string{"evidence": {historical: destination}}}
	processor := rememberSynchronousProcessor{ledger: ledger}
	input := remember.RememberProcessRequest{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), SpaceID: uuid.NewString(), RequestHash: "original-request-hash", OriginalRequest: []byte("exact original request"), Evidence: []remember.EvidenceInput{{Content: "Exact replacement Evidence.", SupersedesEvidenceIDs: []string{historical}}}}
	resolved, err := processor.resolveRememberEvidenceHandles(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, []string{input.TeamID, input.OwnerProfileID, input.SpaceID}, ledger.scope)
	require.Equal(t, []string{destination}, resolved.Evidence[0].SupersedesEvidenceIDs)
	require.Equal(t, input.RequestHash, resolved.RequestHash)
	require.Equal(t, input.OriginalRequest, resolved.OriginalRequest)
	require.Equal(t, input.Evidence[0].Content, resolved.Evidence[0].Content)
	resolved.Evidence[0].SupersedesEvidenceIDs[0] = uuid.NewString()
	require.Equal(t, []string{historical}, input.Evidence[0].SupersedesEvidenceIDs)
}

func TestEvidenceOnlyUnitRememberRejectsUnavailableHistoricalHandleBeforeProviderWork(t *testing.T) {
	for _, resolutionError := range []error{domain.ErrMemoryWriteDenied, nil} {
		ledger := &rememberEvidenceHandleLedger{err: resolutionError}
		processor := rememberSynchronousProcessor{ledger: ledger}
		input := remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "Exact replacement Evidence.", SupersedesEvidenceIDs: []string{uuid.NewString()}}}}
		_, err := processor.prepareRemember(t.Context(), input, remember.RememberAssessmentSnapshot{}, remember.RememberAssessmentScope{}, time.Now())
		require.Error(t, err)
		var preparation *rememberPreparationError
		require.ErrorAs(t, err, &preparation)
		require.Equal(t, "assessment", preparation.Phase)
		if resolutionError != nil {
			require.ErrorIs(t, err, resolutionError)
		} else {
			require.ErrorContains(t, preparation.Cause, "historical Evidence handle resolution was incomplete")
		}
	}
}
