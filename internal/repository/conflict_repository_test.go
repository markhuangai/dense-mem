package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestConflictNextReviewAtDoesNotReturnPastDue(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	assert.Equal(t, now.Add(24*time.Hour), conflictNextReviewAt(now, now.Add(-time.Hour)))
	assert.Equal(t, now.Add(24*time.Hour), conflictNextReviewAt(now, now))
}

func TestConflictNextReviewAtUsesFutureDueInsideDailyInterval(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	due := now.Add(time.Hour)

	assert.Equal(t, due, conflictNextReviewAt(now, due))
	assert.Equal(t, now.Add(24*time.Hour), conflictNextReviewAt(now, now.Add(48*time.Hour)))
}

func TestRelationshipConflictScopeKeySeparatesPrivateSpaces(t *testing.T) {
	record := &RelationshipRecord{
		TeamID: "team", SubjectEntityID: "subject", PredicateKey: "predicate",
		RelationshipKind: "state", CurrentCardinality: "one", Polarity: "+", ScopeKey: "",
	}

	shared := relationshipConflictScopeKey(record, "shared-space", string(domain.MemorySpaceTeamShared))
	privateA := relationshipConflictScopeKey(record, "private-a", string(domain.MemorySpaceProfilePrivate))
	privateB := relationshipConflictScopeKey(record, "private-b", string(domain.MemorySpaceProfilePrivate))

	assert.Equal(t, shared, relationshipConflictScopeKey(record, "another-shared-space", string(domain.MemorySpaceTeamShared)))
	assert.NotEqual(t, privateA, privateB)
	assert.NotEqual(t, shared, privateA)
}

func TestNormalizeConflictReviewRunInputRejectsInvalidTimezone(t *testing.T) {
	_, err := normalizeConflictReviewRunInput(ConflictReviewRunInput{
		TeamID:   "00000000-0000-0000-0000-000000000001",
		WorkerID: "worker-a",
		Timezone: "Mars/Base",
	})

	assert.ErrorContains(t, err, "timezone is invalid")
}

func TestValidateClaimConflictDerivedEvidenceTasksRejectsSubsecondLease(t *testing.T) {
	err := validateClaimConflictDerivedEvidenceTasksInput(ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      "00000000-0000-0000-0000-000000000001",
		ReviewRunID: "00000000-0000-0000-0000-000000000002",
		WorkerID:    "worker-a",
		Limit:       1,
		Lease:       500 * time.Millisecond,
	})

	assert.ErrorContains(t, err, "at least 1 second")
}

func TestConflictTimesEqualAtDatabasePrecision(t *testing.T) {
	stored := time.Date(2026, 7, 31, 12, 0, 0, 123456000, time.UTC)
	request := stored.Add(789 * time.Nanosecond)

	assert.True(t, conflictTimesEqualAtDatabasePrecision(stored, request))
	assert.False(t, conflictTimesEqualAtDatabasePrecision(stored, stored.Add(time.Microsecond)))
}
