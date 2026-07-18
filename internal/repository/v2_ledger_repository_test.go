package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2LedgerCreateIngestValidationRejectsHashMismatch(t *testing.T) {
	input := validV2CreateIngestInput()
	input.Evidence[0].ContentHash = "sha256:wrong"

	err := validateV2CreateIngestInput(normalizeV2CreateIngestInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "content_hash does not match content hash")
}

func TestV2LedgerCreateIngestValidationRequiresRequestHashWithIdempotencyKey(t *testing.T) {
	input := validV2CreateIngestInput()
	input.IdempotencyKey = "idem"
	input.RequestHash = ""

	err := validateV2CreateIngestInput(normalizeV2CreateIngestInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "request_hash is required")
}

func TestV2LedgerCreateIngestFailsClosedWithoutDependencies(t *testing.T) {
	_, err := (*V2LedgerRepositoryImpl)(nil).CreateIngest(context.Background(), validV2CreateIngestInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is required")

	repo := &V2LedgerRepositoryImpl{db: &gorm.DB{}}
	_, err = repo.CreateIngest(context.Background(), validV2CreateIngestInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rls helper is required")
}

func validV2CreateIngestInput() V2CreateIngestInput {
	return V2CreateIngestInput{
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
		Evidence: []V2EvidenceInput{{
			Content: strings.Repeat("exact evidence ", 2),
		}},
	}
}
