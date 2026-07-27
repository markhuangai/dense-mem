package dreamservice

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestReadPathErrorBranches(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ctx := dreamTestContext(teamID, ownerID)
	cfg := cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}}

	_, _, err := New(Dependencies{
		Store:     &dreamRepositoryStub{listErr: errors.New("list failed")},
		AppConfig: cfg,
	}).List(ctx, "ignored-profile", ListOptions{})
	require.ErrorContains(t, err, "list failed")

	_, err = New(Dependencies{
		Store:     &dreamRepositoryStub{getErr: repository.ErrDreamHypothesisNotFound},
		AppConfig: cfg,
	}).Get(ctx, "ignored-profile", hypothesisID)
	require.ErrorIs(t, err, ErrDreamNotFound)

	_, err = New(Dependencies{
		Store:     &dreamRepositoryStub{getErr: errors.New("get failed")},
		AppConfig: cfg,
	}).Get(ctx, "ignored-profile", hypothesisID)
	require.ErrorContains(t, err, "get failed")

	_, err = New(Dependencies{
		Store:     &dreamRepositoryStub{recallErr: errors.New("recall failed")},
		AppConfig: cfg,
	}).Recall(ctx, "ignored-profile", "PostgreSQL", 5)
	require.ErrorContains(t, err, "recall failed")

	_, err = New(Dependencies{
		Store:     &dreamRepositoryStub{latestErr: errors.New("latest failed")},
		AppConfig: cfg,
	}).ListRuns(ctx, "ignored-profile", 5)
	require.ErrorContains(t, err, "latest failed")

	_, err = New(Dependencies{
		Store:     &dreamRepositoryStub{latestErr: errors.New("status latest failed")},
		AppConfig: cfg,
	}).Status(ctx, "ignored-profile")
	require.ErrorContains(t, err, "status latest failed")
}

func TestGeneratedProposalTargetValidationEdges(t *testing.T) {
	sourceID := uuid.NewString()
	subjectID := uuid.NewString()
	objectValueID := uuid.NewString()
	inputs := map[string]repository.DreamInput{
		sourceID: {
			RelationshipID:   sourceID,
			Version:          1,
			Tier:             "fact",
			Status:           "active",
			SubjectEntityID:  subjectID,
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectValueID:    objectValueID,
		},
	}

	proposals, rejected := dreamProposalsFromGenerated([]GeneratedDream{
		{
			Hypothesis:      "Dense-Mem may use pgvector.",
			ObjectValueID:   objectValueID,
			SourceRefs:      []domain.DreamSourceRef{{Type: "fact", ID: sourceID}},
			Likelihood:      2,
			Confidence:      0.5,
			PossibleOutcome: "Store embeddings in PostgreSQL.",
		},
		{
			Hypothesis:      "Invalid because it sets both object endpoint kinds.",
			ObjectEntityID:  subjectID,
			ObjectValueID:   objectValueID,
			SourceRefs:      []domain.DreamSourceRef{{Type: "fact", ID: sourceID}},
			PredicateKey:    "uses",
			SubjectEntityID: subjectID,
		},
		{
			Hypothesis: "Invalid source type.",
			SourceRefs: []domain.DreamSourceRef{{Type: "observation", ID: sourceID}},
		},
	}, inputs, 5, "")

	require.Len(t, proposals, 1)
	assert.Equal(t, objectValueID, proposals[0].ObjectValueID)
	assert.Empty(t, proposals[0].ObjectEntityID)
	require.NotNil(t, proposals[0].Likelihood)
	assert.Equal(t, 1.0, *proposals[0].Likelihood)
	assert.Equal(t, "dream-v2.provider", proposals[0].GeneratorVersion)
	assert.Equal(t, 2, rejected)
}
