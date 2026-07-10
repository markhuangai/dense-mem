package assertionservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

type lifecycleStore struct {
	captureStore
	assertion      *domain.Assertion
	updated        []domain.Assertion
	writeResult    WriteResult
	missing        []string
	err            error
	profileID      string
	assertionID    string
	tier           domain.AssertionTier
	status         domain.AssertionStatus
	at             time.Time
	updates        []StateUpdate
	refs           []domain.LegacyMemoryRef
	assertionIDs   []string
	finalizeCalled bool
}

func (s *lifecycleStore) GetAssertion(_ context.Context, profileID, assertionID string) (*domain.Assertion, error) {
	s.profileID, s.assertionID = profileID, assertionID
	return s.assertion, s.err
}

func (s *lifecycleStore) UpdateAssertionState(_ context.Context, profileID, assertionID string, tier domain.AssertionTier, status domain.AssertionStatus, at time.Time) (*domain.Assertion, WriteResult, error) {
	s.profileID, s.assertionID, s.tier, s.status, s.at = profileID, assertionID, tier, status, at
	return s.assertion, s.writeResult, s.err
}

func (s *lifecycleStore) UpdateAssertionStates(_ context.Context, profileID string, updates []StateUpdate, at time.Time) ([]domain.Assertion, WriteResult, error) {
	s.profileID, s.updates, s.at = profileID, append([]StateUpdate(nil), updates...), at
	return s.updated, s.writeResult, s.err
}

func (s *lifecycleStore) LinkLegacyDecomposition(_ context.Context, profileID string, refs []domain.LegacyMemoryRef, assertionIDs []string, at time.Time) error {
	s.profileID, s.refs, s.assertionIDs, s.at = profileID, append([]domain.LegacyMemoryRef(nil), refs...), append([]string(nil), assertionIDs...), at
	return s.err
}

func (s *lifecycleStore) FinalizeLegacyMigration(_ context.Context, profileID string, updates []StateUpdate, refs []domain.LegacyMemoryRef, at time.Time) ([]domain.Assertion, WriteResult, error) {
	s.profileID, s.updates, s.refs, s.at, s.finalizeCalled = profileID, append([]StateUpdate(nil), updates...), append([]domain.LegacyMemoryRef(nil), refs...), at, true
	return s.updated, s.writeResult, s.err
}

func (s *lifecycleStore) MissingLegacyRefs(_ context.Context, profileID string, refs []domain.LegacyMemoryRef) ([]string, error) {
	s.profileID, s.refs = profileID, append([]domain.LegacyMemoryRef(nil), refs...)
	return append([]string(nil), s.missing...), s.err
}

func TestServiceAssertionLifecycleDelegatesValidatedInputs(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	assertion := validBundle(t).Assertions[0]
	store := &lifecycleStore{
		assertion:   &assertion,
		updated:     []domain.Assertion{assertion},
		writeResult: WriteResult{Superseded: []SupersededAssertion{{AssertionID: "old-1"}}},
	}
	svc := New(store)

	got, err := svc.GetAssertion(context.Background(), " team-1 ", " assertion-1 ")
	require.NoError(t, err)
	require.Same(t, &assertion, got)
	require.Equal(t, "team-1", store.profileID)
	require.Equal(t, "assertion-1", store.assertionID)

	got, result, err := svc.UpdateState(context.Background(), " team-1 ", " assertion-1 ", domain.AssertionTierFact, domain.AssertionStatusActive, now)
	require.NoError(t, err)
	require.Same(t, &assertion, got)
	require.Equal(t, store.writeResult, result)
	require.Equal(t, now, store.at)
	require.Equal(t, domain.AssertionTierFact, store.tier)

	updates := []StateUpdate{{AssertionID: " assertion-1 ", Tier: domain.AssertionTierFact, Status: domain.AssertionStatusActive}}
	updated, result, err := svc.UpdateStates(context.Background(), " team-1 ", updates, now)
	require.NoError(t, err)
	require.Equal(t, store.updated, updated)
	require.Equal(t, store.writeResult, result)
	require.Equal(t, "assertion-1", store.updates[0].AssertionID)

	refs := []domain.LegacyMemoryRef{{Type: "fragment", ID: "legacy-1"}}
	require.NoError(t, svc.LinkLegacyDecomposition(context.Background(), " team-1 ", refs, []string{"assertion-1"}, now))
	require.Equal(t, refs, store.refs)
	require.Equal(t, []string{"assertion-1"}, store.assertionIDs)

	updated, result, err = svc.FinalizeLegacyMigration(context.Background(), " team-1 ", updates, refs, now)
	require.NoError(t, err)
	require.True(t, store.finalizeCalled)
	require.Equal(t, store.updated, updated)
	require.Equal(t, store.writeResult, result)

	require.NoError(t, svc.CheckLegacyRefs(context.Background(), " team-1 ", refs))
	require.Equal(t, "team-1", store.profileID)
}

func TestServiceAssertionLifecycleRejectsUnsupportedAndInvalidCalls(t *testing.T) {
	unsupported := New(&captureStore{})
	_, err := unsupported.GetAssertion(context.Background(), "team-1", "assertion-1")
	require.ErrorContains(t, err, "does not support reads")
	_, _, err = unsupported.UpdateState(context.Background(), "team-1", "assertion-1", domain.AssertionTierFact, domain.AssertionStatusActive, time.Time{})
	require.ErrorContains(t, err, "does not support state updates")
	_, _, err = unsupported.UpdateStates(context.Background(), "team-1", []StateUpdate{{AssertionID: "a", Tier: domain.AssertionTierFact, Status: domain.AssertionStatusActive}}, time.Time{})
	require.ErrorContains(t, err, "does not support batch state updates")
	require.ErrorContains(t, unsupported.LinkLegacyDecomposition(context.Background(), "team-1", []domain.LegacyMemoryRef{{Type: "fragment", ID: "f"}}, []string{"a"}, time.Time{}), "does not support legacy links")
	_, _, err = unsupported.FinalizeLegacyMigration(context.Background(), "team-1", []StateUpdate{{AssertionID: "a", Tier: domain.AssertionTierFact, Status: domain.AssertionStatusActive}}, []domain.LegacyMemoryRef{{Type: "fragment", ID: "f"}}, time.Time{})
	require.ErrorContains(t, err, "does not support legacy migration")
	require.ErrorContains(t, unsupported.CheckLegacyRefs(context.Background(), "team-1", []domain.LegacyMemoryRef{{Type: "fragment", ID: "f"}}), "does not support legacy reference checks")

	svc := New(&lifecycleStore{})
	_, err = svc.GetAssertion(context.Background(), "", "a")
	require.Error(t, err)
	_, _, err = svc.UpdateState(context.Background(), "team-1", "", domain.AssertionTierFact, domain.AssertionStatusActive, time.Time{})
	require.Error(t, err)
	_, _, err = svc.UpdateState(context.Background(), "team-1", "a", "bad", domain.AssertionStatusActive, time.Time{})
	require.Error(t, err)
	_, _, err = svc.UpdateStates(context.Background(), "", nil, time.Time{})
	require.Error(t, err)
	_, _, err = svc.UpdateStates(context.Background(), "team-1", []StateUpdate{{AssertionID: "a", Tier: "bad", Status: domain.AssertionStatusActive}}, time.Time{})
	require.Error(t, err)
	_, _, err = svc.UpdateStates(context.Background(), "team-1", []StateUpdate{
		{AssertionID: "a", Tier: domain.AssertionTierFact, Status: domain.AssertionStatusActive},
		{AssertionID: "a", Tier: domain.AssertionTierFact, Status: domain.AssertionStatusActive},
	}, time.Time{})
	require.ErrorContains(t, err, "duplicate assertion_id")
	require.Error(t, svc.LinkLegacyDecomposition(context.Background(), "", nil, nil, time.Time{}))
	_, _, err = svc.FinalizeLegacyMigration(context.Background(), "", nil, nil, time.Time{})
	require.Error(t, err)
	_, _, err = svc.FinalizeLegacyMigration(context.Background(), "team-1", []StateUpdate{{AssertionID: "a", Tier: domain.AssertionTierFact, Status: domain.AssertionStatusActive}}, []domain.LegacyMemoryRef{{Type: "bad", ID: "f"}}, time.Time{})
	require.Error(t, err)
	require.Error(t, svc.CheckLegacyRefs(context.Background(), "", nil))
	require.Error(t, svc.CheckLegacyRefs(context.Background(), "team-1", []domain.LegacyMemoryRef{{Type: "fragment", ID: ""}}))
	require.ErrorContains(t, svc.CheckLegacyRefs(context.Background(), "team-1", []domain.LegacyMemoryRef{{Type: "fragment", ID: "f"}, {Type: "fragment", ID: "f"}}), "duplicate legacy ref")
}

func TestServiceAssertionLifecyclePropagatesStoreErrorsAndMissingRefs(t *testing.T) {
	store := &lifecycleStore{err: errors.New("store failed")}
	svc := New(store)
	_, err := svc.GetAssertion(context.Background(), "team-1", "a")
	require.ErrorContains(t, err, "store failed")
	_, _, err = svc.UpdateState(context.Background(), "team-1", "a", domain.AssertionTierFact, domain.AssertionStatusActive, time.Time{})
	require.ErrorContains(t, err, "store failed")

	store.err = nil
	store.missing = []string{"fact:z", "claim:a"}
	err = svc.CheckLegacyRefs(context.Background(), "team-1", []domain.LegacyMemoryRef{{Type: "fact", ID: "z"}})
	require.ErrorContains(t, err, "claim:a, fact:z")
}

func TestAssertionIDIncludesTemporalAndPolarityIdentity(t *testing.T) {
	from := time.Date(2026, 7, 10, 12, 0, 0, 123, time.FixedZone("offset", 3600))
	to := from.Add(time.Hour)
	first := AssertionID(" team-1 ", " entity-1 ", "Works On", "entity:project-1", domain.PolarityPlus, &from, &to)
	second := AssertionID("team-1", "entity-1", "works_on", "entity:project-1", domain.PolarityPlus, &from, &to)
	require.Equal(t, first, second)
	require.NotEqual(t, first, AssertionID("team-1", "entity-1", "works_on", "entity:project-1", domain.PolarityMinus, &from, &to))
	require.NotEqual(t, first, AssertionID("team-1", "entity-1", "works_on", "entity:project-1", domain.PolarityPlus, nil, nil))
	require.Empty(t, formatTimeKey(nil))
	zero := time.Time{}
	require.Empty(t, formatTimeKey(&zero))
}
