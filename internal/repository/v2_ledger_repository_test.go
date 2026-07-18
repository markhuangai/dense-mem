package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
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

func TestV2LedgerCreateIngestValidationRejectsTooManyEvidenceItems(t *testing.T) {
	input := validV2CreateIngestInput()
	input.Evidence = make([]V2EvidenceInput, v2MaxEvidenceItems+1)
	for i := range input.Evidence {
		input.Evidence[i].Content = "exact evidence " + strings.Repeat("x", i+1)
	}

	err := validateV2CreateIngestInput(normalizeV2CreateIngestInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestV2LedgerCreateIngestValidationUsesCanonicalAuthority(t *testing.T) {
	for _, authority := range []domain.Authority{
		domain.AuthorityAuthoritative,
		domain.AuthorityPrimary,
		domain.AuthoritySecondary,
		domain.AuthorityInferred,
		domain.AuthorityUnknown,
	} {
		input := validV2CreateIngestInput()
		input.Evidence[0].Authority = string(authority)
		err := validateV2CreateIngestInput(normalizeV2CreateIngestInput(input))
		require.NoError(t, err, "authority %s", authority)
	}

	input := validV2CreateIngestInput()
	input.Evidence[0].Authority = "derived"
	err := validateV2CreateIngestInput(normalizeV2CreateIngestInput(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authority is unsupported")
}

func TestV2LedgerAdvanceSourceRevisionValidationUsesCanonicalAuthority(t *testing.T) {
	input := validV2AdvanceSourceRevisionInput()
	input.Authority = string(domain.AuthorityInferred)
	require.NoError(t, validateV2AdvanceSourceRevisionInput(normalizeV2AdvanceSourceRevisionInput(input)))

	input.Authority = "derived"
	err := validateV2AdvanceSourceRevisionInput(normalizeV2AdvanceSourceRevisionInput(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authority is unsupported")
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

func TestV2LedgerClaimNextPlacementRunRejectsSubSecondLease(t *testing.T) {
	repo := &V2LedgerRepositoryImpl{}

	run, err := repo.ClaimNextPlacementRun(context.Background(), uuid.NewString(), "worker-a", 500*time.Millisecond)

	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "lease must be at least one second")
}

func TestV2LedgerSourceCreateUniqueViolationBecomesRevisionConflict(t *testing.T) {
	err := &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "evidence_sources_owner_key_unique",
	}

	require.True(t, isPostgresUniqueConstraint(err, "evidence_sources_owner_key_unique"))
	require.True(t, errors.Is(translateV2SourceCreateError(err), ErrV2SourceRevisionConflict))
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

func validV2AdvanceSourceRevisionInput() V2AdvanceSourceRevisionInput {
	return V2AdvanceSourceRevisionInput{
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
		SourceKey:      "doc://policy",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:policy",
	}
}
