package repository

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateRetractEvidenceClassifiesInvalidEvidenceID(t *testing.T) {
	err := validateRetractEvidenceInput(RetractEvidenceInput{
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
		EvidenceIDs:    []string{"not-a-uuid"},
		Reason:         "entered in error",
		IdempotencyKey: "retract-invalid-id",
		RequestHash:    "sha256:retract-invalid-id",
	})
	require.ErrorIs(t, err, ErrEvidenceLifecycleIDInvalid)
}
