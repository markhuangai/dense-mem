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

func TestLedgerCreateIngestValidationRequiresCompleteSpaceFence(t *testing.T) {
	t.Run("generation without space", func(t *testing.T) {
		input := validCreateIngestInput()
		input.SpaceGeneration = 1

		err := validateCreateIngestInput(normalizeCreateIngestInput(input))

		require.ErrorContains(t, err, "space_id is required")
	})

	t.Run("space without generation", func(t *testing.T) {
		input := validCreateIngestInput()
		input.SpaceID = uuid.NewString()

		err := validateCreateIngestInput(normalizeCreateIngestInput(input))

		require.ErrorContains(t, err, "space_generation is required")
	})

	t.Run("complete fence", func(t *testing.T) {
		input := validCreateIngestInput()
		input.SpaceID = uuid.NewString()
		input.SpaceGeneration = 4

		require.NoError(t, validateCreateIngestInput(normalizeCreateIngestInput(input)))
	})
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

func TestLedgerCreateIngestValidationAllowsDirectEvidenceSupersedes(t *testing.T) {
	input := validCreateIngestInput()
	input.RequestHash = "sha256:request"
	input.Evidence = []EvidenceInput{
		{
			Content:                   "first revised source fragment",
			SourceKey:                 "doc://policy",
			SourceRevisionToken:       "rev-2",
			SourceRevisionContentHash: "sha256:revision",
			SupersedesEvidenceIDs:     []string{uuid.NewString()},
			IdempotencyKey:            "evidence-a",
		},
		{
			Content:                   "second revised source fragment",
			SourceKey:                 "doc://policy",
			SourceRevisionToken:       "rev-2",
			SourceRevisionContentHash: "sha256:revision",
			SupersedesEvidenceIDs:     []string{uuid.NewString()},
			IdempotencyKey:            "evidence-b",
		},
	}

	err := validateCreateIngestInput(normalizeCreateIngestInput(input))

	require.NoError(t, err)
}

func TestLedgerCreateIngestValidationRequiresConsistentSourceRevisionProvenance(t *testing.T) {
	baseEvidence := []EvidenceInput{
		{
			Content: "first source fragment", SourceType: "document", Authority: "primary",
			SourceKey: "doc://batch", SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:revision",
			SourceRevisionEnvelope: map[string]any{"source": "docs", "metadata": map[string]any{"section": "one"}},
		},
		{
			Content: "second source fragment", SourceType: "document", Authority: "primary",
			SourceKey: "doc://batch", SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:revision",
			SourceRevisionEnvelope: map[string]any{"source": "docs", "metadata": map[string]any{"section": "one"}},
		},
	}

	consistent := validCreateIngestInput()
	consistent.Evidence = append([]EvidenceInput(nil), baseEvidence...)
	require.NoError(t, validateCreateIngestInput(normalizeCreateIngestInput(consistent)))

	for _, test := range []struct {
		name   string
		mutate func(*EvidenceInput)
	}{
		{name: "source kind", mutate: func(item *EvidenceInput) { item.SourceType = "manual" }},
		{name: "authority", mutate: func(item *EvidenceInput) { item.Authority = "secondary" }},
		{name: "revision envelope", mutate: func(item *EvidenceInput) {
			item.SourceRevisionEnvelope = map[string]any{"source": "docs", "metadata": map[string]any{"section": "two"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validCreateIngestInput()
			input.Evidence = append([]EvidenceInput(nil), baseEvidence...)
			test.mutate(&input.Evidence[1])

			err := validateCreateIngestInput(normalizeCreateIngestInput(input))

			require.ErrorContains(t, err, "including source provenance")
		})
	}
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
