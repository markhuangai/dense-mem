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

func TestLedgerCreateIngestValidationRejectsHashMismatch(t *testing.T) {
	input := validCreateIngestInput()
	input.Evidence[0].ContentHash = "sha256:wrong"

	err := validateCreateIngestInput(normalizeCreateIngestInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "content_hash does not match content hash")
}

func TestLedgerCreateIngestValidationRequiresRequestHashWithIdempotencyKey(t *testing.T) {
	input := validCreateIngestInput()
	input.IdempotencyKey = "idem"
	input.RequestHash = ""

	err := validateCreateIngestInput(normalizeCreateIngestInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "request_hash is required")
}

func TestLedgerCreateIngestValidationRejectsTooManyEvidenceItems(t *testing.T) {
	input := validCreateIngestInput()
	input.Evidence = make([]EvidenceInput, maxEvidenceItems+1)
	for i := range input.Evidence {
		input.Evidence[i].Content = "exact evidence " + strings.Repeat("x", i+1)
	}

	err := validateCreateIngestInput(normalizeCreateIngestInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestLedgerCreateIngestValidationUsesCanonicalAuthority(t *testing.T) {
	for _, authority := range []domain.Authority{
		domain.AuthorityAuthoritative,
		domain.AuthorityPrimary,
		domain.AuthoritySecondary,
		domain.AuthorityInferred,
		domain.AuthorityUnknown,
	} {
		input := validCreateIngestInput()
		input.Evidence[0].Authority = string(authority)
		err := validateCreateIngestInput(normalizeCreateIngestInput(input))
		require.NoError(t, err, "authority %s", authority)
	}

	input := validCreateIngestInput()
	input.Evidence[0].Authority = "derived"
	err := validateCreateIngestInput(normalizeCreateIngestInput(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authority is unsupported")
}

func TestLedgerAdvanceSourceRevisionValidationUsesCanonicalAuthority(t *testing.T) {
	input := validAdvanceSourceRevisionInput()
	input.Authority = string(domain.AuthorityInferred)
	require.NoError(t, validateAdvanceSourceRevisionInput(normalizeAdvanceSourceRevisionInput(input)))

	input.Authority = "derived"
	err := validateAdvanceSourceRevisionInput(normalizeAdvanceSourceRevisionInput(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authority is unsupported")
}

func TestLedgerCreateIngestValidationAllowsPerEvidenceSourceRevisionSupersedes(t *testing.T) {
	input := validCreateIngestInput()
	input.Evidence = []EvidenceInput{
		{
			Content:                       "first revised source fragment",
			SourceKey:                     "doc://policy",
			SourceRevisionToken:           "rev-2",
			ExpectedPreviousRevisionToken: "rev-1",
			SourceRevisionContentHash:     "sha256:revision",
			SupersedesFragmentIDs:         []string{uuid.NewString()},
		},
		{
			Content:                       "second revised source fragment",
			SourceKey:                     "doc://policy",
			SourceRevisionToken:           "rev-2",
			ExpectedPreviousRevisionToken: "rev-1",
			SourceRevisionContentHash:     "sha256:revision",
			SupersedesFragmentIDs:         []string{uuid.NewString()},
		},
	}

	err := validateCreateIngestInput(normalizeCreateIngestInput(input))

	require.NoError(t, err)
}

func TestLedgerCreateIngestFailsClosedWithoutDependencies(t *testing.T) {
	_, err := (*LedgerRepositoryImpl)(nil).CreateIngest(context.Background(), validCreateIngestInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is required")

	repo := &LedgerRepositoryImpl{db: &gorm.DB{}}
	_, err = repo.CreateIngest(context.Background(), validCreateIngestInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rls helper is required")
}

func TestLedgerClaimNextPlacementRunRejectsSubSecondLease(t *testing.T) {
	repo := &LedgerRepositoryImpl{}

	run, err := repo.ClaimNextPlacementRun(context.Background(), uuid.NewString(), "worker-a", 500*time.Millisecond)

	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "lease must be at least one second")
}

func TestLedgerSourceCreateUniqueViolationBecomesRevisionConflict(t *testing.T) {
	err := &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "evidence_sources_owner_key_unique",
	}

	require.True(t, isPostgresUniqueConstraint(err, "evidence_sources_owner_key_unique"))
	require.True(t, errors.Is(translateSourceCreateError(err), ErrSourceRevisionConflict))
}

func validCreateIngestInput() CreateIngestInput {
	return CreateIngestInput{
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
		Evidence: []EvidenceInput{{
			Content: strings.Repeat("exact evidence ", 2),
		}},
	}
}

func validAdvanceSourceRevisionInput() AdvanceSourceRevisionInput {
	return AdvanceSourceRevisionInput{
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
		SourceKey:      "doc://policy",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:policy",
	}
}
