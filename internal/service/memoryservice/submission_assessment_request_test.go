package memoryservice

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestSubmissionAssessmentGroundsActiveAliasWithFlexibleWhitespace(t *testing.T) {
	evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
		EvidenceID: "evidence:0",
		Content:    "DENSE \t Memory protects PostgreSQL.",
	})
	plan := submissionAssessmentGroundingTestPlan("Dense-Mem", "project")
	candidate := repository.SemanticReviewEntityCandidate{
		TeamID: "team-a", EntityID: "entity-dense-mem", EntityKind: "project", CanonicalName: "Dense-Mem",
		ActiveNames: []string{"Dense-Mem", "Dense Memory"}, Status: "active", IdentityContext: map[string]any{},
	}

	entities, groups, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups: []repository.SubmissionAssessmentEntityCatalogGroup{{
			Ref: "entity:subject", Candidates: []repository.SemanticReviewEntityCandidate{candidate}, Complete: true,
		}},
	}, []assessor.SemanticReviewEvidence{evidence})

	require.NoError(t, err)
	require.Len(t, entities, 1)
	require.Len(t, entities[0].Groundings, 1)
	assert.Equal(t, "DENSE \t Memory", entities[0].Groundings[0].Surface)
	require.Len(t, groups, 1)
	require.Len(t, groups[0].Candidates, 1)
	assert.Equal(t, candidate.EntityID, groups[0].Candidates[0].EntityID)
}

func TestSubmissionAssessmentRejectsPronounAndPartialWordGrounding(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		entity  string
	}{
		{name: "pronoun", content: "It uses Redis.", entity: "It"},
		{name: "partial word", content: "Alphabet uses Redis.", entity: "Alpha"},
		{name: "identifier continuation", content: "C++20 uses Redis.", entity: "C++"},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
				EvidenceID: "evidence:0",
				Content:    test.content,
			})
			plan := submissionAssessmentGroundingTestPlan(test.entity, "concept")

			entities, groups, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
				Complete: true,
				Groups:   []repository.SubmissionAssessmentEntityCatalogGroup{{Ref: "entity:subject", Complete: true}},
			}, []assessor.SemanticReviewEvidence{evidence})

			require.NoError(t, err)
			require.Len(t, entities, 1)
			assert.Empty(t, entities[0].Groundings)
			assert.Empty(t, groups)
		})
	}
}

func TestSubmissionAssessmentChoosesKindWhenHintIsOmitted(t *testing.T) {
	evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
		EvidenceID: "evidence:0",
		Content:    "Dense-Mem protects PostgreSQL.",
	})
	plan := submissionAssessmentGroundingTestPlan("Dense-Mem", "")
	candidate := repository.SemanticReviewEntityCandidate{
		TeamID: "team-a", EntityID: "entity-dense-mem", EntityKind: "project", CanonicalName: "Dense-Mem",
		ActiveNames: []string{"Dense-Mem"}, Status: "active", IdentityContext: map[string]any{},
	}

	entities, groups, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups: []repository.SubmissionAssessmentEntityCatalogGroup{{
			Ref: "entity:subject", Candidates: []repository.SemanticReviewEntityCandidate{candidate}, Complete: true,
		}},
	}, []assessor.SemanticReviewEvidence{evidence})

	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "project", entities[0].Kind)
	require.Len(t, groups, 1)
}

func TestSubmissionAssessmentDefaultsNewEntityKindWhenHintIsOmitted(t *testing.T) {
	evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
		EvidenceID: "evidence:0",
		Content:    "Dense-Mem protects PostgreSQL.",
	})
	plan := submissionAssessmentGroundingTestPlan("Dense-Mem", "")

	entities, _, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups:   []repository.SubmissionAssessmentEntityCatalogGroup{{Ref: "entity:subject", Complete: true}},
	}, []assessor.SemanticReviewEvidence{evidence})

	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "other", entities[0].Kind)
}

func TestSubmissionAssessmentTreatsMissingKnownEntityAsStaleInput(t *testing.T) {
	evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
		EvidenceID: "evidence:0",
		Content:    "The retired entity is no longer active.",
	})
	plan := submissionAssessmentGroundingTestPlan("", "")
	plan.EntityTargets[0].KnownEntityID = "known-entity-id"
	plan.entityTargetsByRef = map[string]submissionAssessmentEntityTarget{
		"entity:subject": plan.EntityTargets[0],
	}

	_, _, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups:   []repository.SubmissionAssessmentEntityCatalogGroup{{Ref: "entity:subject", Complete: true}},
	}, []assessor.SemanticReviewEvidence{evidence})

	require.ErrorIs(t, err, errSubmissionAssessmentStaleInput)
}

func submissionAssessmentGroundingTestPlan(name, kind string) submissionAssessmentPlan {
	target := submissionAssessmentEntityTarget{Target: assessor.SemanticAssessmentRequiredEntityRef{
		Ref: "entity:subject", Name: name, Kind: kind, EvidenceIDs: []string{"evidence:0"},
	}}
	return submissionAssessmentPlan{EntityTargets: []submissionAssessmentEntityTarget{target}}
}
