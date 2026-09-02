package memoryservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
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

func TestSubmissionAssessmentProvidesAnchorsForEarlierCanonicalAndAliasMentions(t *testing.T) {
	evidence := []assessor.SemanticReviewEvidence{
		assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
			EvidenceID: "evidence:0", EvidenceIndex: 0, Content: "Dense-Mem ships the service.",
		}),
		assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
			EvidenceID: "evidence:1", EvidenceIndex: 1, Content: "It stores data.",
		}),
	}
	candidate := repository.SemanticReviewEntityCandidate{
		EntityID: "entity-dense-mem", EntityKind: "project", CanonicalName: "Dense-Mem",
		ActiveNames: []string{"Dense-Mem", "Dense Memory"}, Status: "active",
	}
	plan := submissionAssessmentPlan{EntityTargets: []submissionAssessmentEntityTarget{{
		Target: assessor.SemanticAssessmentRequiredEntityRef{
			Ref: "entity:subject", Name: "Dense-Mem", Kind: "project", EvidenceIDs: []string{"evidence:0", "evidence:1"},
		},
	}}}
	entities, groups, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups: []repository.SubmissionAssessmentEntityCatalogGroup{{
			Ref: "entity:subject", Candidates: []repository.SemanticReviewEntityCandidate{candidate}, Complete: true,
		}},
	}, evidence)
	require.NoError(t, err)
	require.Len(t, entities, 1)
	require.Len(t, entities[0].Groundings, 2)
	require.Len(t, entities[0].Anchors, 1)
	require.Len(t, groups, 2)
	var pronoun assessor.SemanticAssessmentEntityGrounding
	for _, grounding := range entities[0].Groundings {
		if grounding.Surface == "It" {
			pronoun = grounding
		}
	}
	require.Equal(t, "a0_0", pronoun.AnchorRef)

	aliasEvidence := []assessor.SemanticReviewEvidence{assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
		EvidenceID: "evidence:0", EvidenceIndex: 0, Content: "DENSE Memory stores data.",
	})}
	aliasPlan := submissionAssessmentGroundingTestPlan("Dense-Mem", "project")
	aliasEntities, _, err := submissionAssessmentGroundedEntities(aliasPlan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups: []repository.SubmissionAssessmentEntityCatalogGroup{{
			Ref: "entity:subject", Candidates: []repository.SemanticReviewEntityCandidate{candidate}, Complete: true,
		}},
	}, aliasEvidence)
	require.NoError(t, err)
	require.Len(t, aliasEntities[0].Anchors, 1)
	require.Equal(t, "DENSE Memory", aliasEntities[0].Anchors[0].Surface)
}

func TestSubmissionAssessmentGroundsPronounAfterRepeatedEntityMentions(t *testing.T) {
	evidence := []assessor.SemanticReviewEvidence{
		assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
			EvidenceID: "evidence:0", EvidenceIndex: 0, Content: "Alice met Alice.",
		}),
		assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
			EvidenceID: "evidence:1", EvidenceIndex: 1, Content: "She deployed PostgreSQL.",
		}),
	}
	candidate := repository.SemanticReviewEntityCandidate{
		EntityID: "entity-alice", EntityKind: "person", CanonicalName: "Alice", ActiveNames: []string{"Alice"}, Status: "active",
	}
	plan := submissionAssessmentPlan{EntityTargets: []submissionAssessmentEntityTarget{{
		Target: assessor.SemanticAssessmentRequiredEntityRef{
			Ref: "entity:subject", Name: "Alice", Kind: "person", EvidenceIDs: []string{"evidence:0", "evidence:1"},
		},
	}}}
	entities, groups, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups: []repository.SubmissionAssessmentEntityCatalogGroup{{
			Ref: "entity:subject", Candidates: []repository.SemanticReviewEntityCandidate{candidate}, Complete: true,
		}},
	}, evidence)
	require.NoError(t, err)
	require.Len(t, entities, 1)
	require.Len(t, entities[0].Anchors, 2)
	require.Len(t, groups, 3)
	var pronoun *assessor.SemanticAssessmentEntityGrounding
	for index := range entities[0].Groundings {
		if entities[0].Groundings[index].Surface == "She" {
			pronoun = &entities[0].Groundings[index]
			break
		}
	}
	require.NotNil(t, pronoun)
	require.Equal(t, entities[0].Anchors[1].AnchorRef, pronoun.AnchorRef)
}

func TestSubmissionAssessmentLeavesPronounAmbiguousAcrossAnchorNames(t *testing.T) {
	evidence := []assessor.SemanticReviewEvidence{assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
		EvidenceID: "evidence:0", EvidenceIndex: 0, Content: "Dense-Mem met Dense Memory. It deployed PostgreSQL.",
	})}
	candidate := repository.SemanticReviewEntityCandidate{
		EntityID: "entity-dense-mem", EntityKind: "project", CanonicalName: "Dense-Mem", ActiveNames: []string{"Dense-Mem", "Dense Memory"}, Status: "active",
	}
	plan := submissionAssessmentPlan{EntityTargets: []submissionAssessmentEntityTarget{{
		Target: assessor.SemanticAssessmentRequiredEntityRef{
			Ref: "entity:subject", Name: "Dense-Mem", Kind: "project", EvidenceIDs: []string{"evidence:0"},
		},
	}}}
	entities, groups, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups: []repository.SubmissionAssessmentEntityCatalogGroup{{
			Ref: "entity:subject", Candidates: []repository.SemanticReviewEntityCandidate{candidate}, Complete: true,
		}},
	}, evidence)
	require.NoError(t, err)
	require.Len(t, entities, 1)
	require.Len(t, entities[0].Anchors, 2)
	require.Len(t, entities[0].Groundings, 2)
	require.Len(t, groups, 2)
	for _, grounding := range entities[0].Groundings {
		require.NotEqual(t, "It", grounding.Surface)
	}
}

func TestSubmissionAssessmentProvidesKnownEvidenceAnchorForSubmittedPronoun(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.input.Snapshot.Evidence[0].Content = "It uses Beta."
	knownID := uuid.NewString()
	known := repository.SubmissionAssessmentKnownEvidence{
		TeamID: fixture.input.Scope.TeamID, EvidenceID: knownID, FragmentID: knownID,
		IngestID: uuid.NewString(), OwnerProfileID: uuid.NewString(),
		Content: "Alpha is the system described by the submitted sentence.", ContentHash: "known-anchor-hash", Authority: "primary",
		SpaceID: uuid.NewString(), SpaceGeneration: 1,
	}
	fixture.input.Snapshot.Proposal["relationship_hints"].([]any)[0].(map[string]any)["known_evidence_ids"] = []any{knownID}
	fixture.catalog.knownEvidence = []repository.SubmissionAssessmentKnownEvidence{known}
	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
	require.NoError(t, engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan))
	request, err := engine.buildRequest(t.Context(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
	require.NoError(t, err)
	require.Len(t, request.KnownEvidence, 1)
	require.Less(t, request.KnownEvidence[0].EvidenceIndex, request.Evidence[0].EvidenceIndex)
	require.Len(t, request.SubmittedEntities, 3)
	var subject assessor.SemanticAssessmentSubmittedEntity
	for _, entity := range request.SubmittedEntities {
		if entity.Ref == "entity:0:subject" {
			subject = entity
			break
		}
	}
	var pronoun *assessor.SemanticAssessmentEntityGrounding
	for index := range subject.Groundings {
		if subject.Groundings[index].Surface == "It" {
			pronoun = &subject.Groundings[index]
			break
		}
	}
	require.NotNil(t, pronoun)
	require.NotEmpty(t, subject.Anchors)
	require.Equal(t, knownID, subject.Anchors[0].EvidenceID)
	require.Equal(t, subject.Anchors[0].AnchorRef, pronoun.AnchorRef)
}

func TestSubmissionAssessmentDoesNotGroundUnanchoredPronouns(t *testing.T) {
	evidence := []assessor.SemanticReviewEvidence{assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
		EvidenceID: "evidence:0", EvidenceIndex: 0, Content: "It stores data.",
	})}
	plan := submissionAssessmentGroundingTestPlan("Dense-Mem", "project")
	candidate := repository.SemanticReviewEntityCandidate{
		EntityID: "entity-dense-mem", EntityKind: "project", CanonicalName: "Dense-Mem",
		ActiveNames: []string{"Dense-Mem"}, Status: "active",
	}
	entities, groups, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups: []repository.SubmissionAssessmentEntityCatalogGroup{{
			Ref: "entity:subject", Candidates: []repository.SemanticReviewEntityCandidate{candidate}, Complete: true,
		}},
	}, evidence)
	require.NoError(t, err)
	require.Empty(t, entities[0].Groundings)
	require.Empty(t, entities[0].Anchors)
	require.Empty(t, groups)
}

func TestSubmissionAssessmentLoadsKnownEvidenceAsImmutableSeparateContext(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	knownID := uuid.NewString()
	knownOwnerID := uuid.NewString()
	known := repository.SubmissionAssessmentKnownEvidence{
		TeamID: fixture.input.Scope.TeamID, EvidenceID: knownID, FragmentID: knownID,
		IngestID: uuid.NewString(), OwnerProfileID: knownOwnerID,
		Content: "Known context confirms Alpha uses Beta.", ContentHash: "known-hash", Authority: "primary",
		SpaceID: uuid.NewString(), SpaceGeneration: 1,
	}
	relationship := fixture.input.Snapshot.Proposal["relationship_hints"].([]any)[0].(map[string]any)
	relationship["known_evidence_ids"] = []any{knownID}
	fixture.catalog.knownEvidence = []repository.SubmissionAssessmentKnownEvidence{known}
	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
	require.NoError(t, engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan))
	require.Len(t, fixture.catalog.knownEvidenceInputs, 1)
	require.Equal(t, []string{knownID}, fixture.catalog.knownEvidenceInputs[0].EvidenceIDs)
	require.Equal(t, known, plan.knownEvidenceByID[knownID])

	request, err := engine.buildRequest(t.Context(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
	require.NoError(t, err)
	require.Len(t, request.KnownEvidence, 1)
	require.Equal(t, knownID, request.KnownEvidence[0].EvidenceID)
	require.Len(t, request.Evidence, len(fixture.input.Snapshot.Evidence))
	var uses assessor.SemanticAssessmentSubmittedRelationship
	for _, relationship := range request.SubmittedRelationships {
		if relationship.Ref == "r:uses" {
			uses = relationship
			break
		}
	}
	require.Equal(t, []string{knownID}, uses.KnownEvidenceIDs)

	supports, err := submissionAssessmentSupports(plan, "assessment-known", []assessor.SemanticAssessmentEvidenceSpan{
		{EvidenceID: knownID, Start: 0, End: len([]rune(known.Content))},
		{EvidenceID: "evidence:0", Start: 0, End: 5},
	})
	require.NoError(t, err)
	require.Len(t, supports, 2)
	require.Empty(t, supports[0].EvidenceOwnerProfileID)
	require.Equal(t, knownOwnerID, supports[1].EvidenceOwnerProfileID)
}

func TestSubmissionAssessmentKnownEvidenceCatalogValidation(t *testing.T) {
	knownID := uuid.NewString()
	newPlan := func(t *testing.T, fixture synchronousAssessmentFixtureValue) submissionAssessmentPlan {
		t.Helper()
		fixture.input.Snapshot.Proposal["relationship_hints"].([]any)[0].(map[string]any)["known_evidence_ids"] = []any{knownID}
		plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
		require.NoError(t, err)
		return plan
	}
	validKnown := func(fixture synchronousAssessmentFixtureValue) repository.SubmissionAssessmentKnownEvidence {
		return repository.SubmissionAssessmentKnownEvidence{
			TeamID: fixture.input.Scope.TeamID, EvidenceID: knownID, FragmentID: knownID,
			IngestID: uuid.NewString(), OwnerProfileID: uuid.NewString(), Content: "known context",
			ContentHash: "known-hash", Authority: "primary", SpaceID: uuid.NewString(), SpaceGeneration: 1,
		}
	}

	t.Run("catalog interface is required", func(t *testing.T) {
		fixture := synchronousAssessmentFixture(t)
		plan := newPlan(t, fixture)
		engine := newAssessmentEngine(SynchronousAssessmentDependencies{Catalog: catalogWithoutKnownEvidence{}}, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
		require.ErrorContains(t, engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan), "known evidence catalog is required")
	})

	t.Run("provider error is classified", func(t *testing.T) {
		fixture := synchronousAssessmentFixture(t)
		fixture.catalog.knownEvidenceErr = errors.New("catalog unavailable")
		plan := newPlan(t, fixture)
		engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
		require.ErrorContains(t, engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan), "load submission known evidence")
	})

	t.Run("unrequested item is rejected", func(t *testing.T) {
		fixture := synchronousAssessmentFixture(t)
		item := validKnown(fixture)
		item.EvidenceID = uuid.NewString()
		item.FragmentID = item.EvidenceID
		fixture.catalog.knownEvidence = []repository.SubmissionAssessmentKnownEvidence{item}
		plan := newPlan(t, fixture)
		engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
		require.ErrorContains(t, engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan), "unrequested item")
	})

	t.Run("invalid item is rejected", func(t *testing.T) {
		fixture := synchronousAssessmentFixture(t)
		item := validKnown(fixture)
		item.Content = ""
		fixture.catalog.knownEvidence = []repository.SubmissionAssessmentKnownEvidence{item}
		plan := newPlan(t, fixture)
		engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
		require.ErrorContains(t, engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan), "catalog is invalid")
	})

	t.Run("duplicate item is rejected", func(t *testing.T) {
		fixture := synchronousAssessmentFixture(t)
		item := validKnown(fixture)
		fixture.catalog.knownEvidence = []repository.SubmissionAssessmentKnownEvidence{item, item}
		plan := newPlan(t, fixture)
		engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
		require.ErrorContains(t, engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan), "duplicate item")
	})

	t.Run("missing item marks relationship unavailable", func(t *testing.T) {
		fixture := synchronousAssessmentFixture(t)
		plan := newPlan(t, fixture)
		engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
		require.NoError(t, engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan))
		var target *submissionAssessmentRelationshipTarget
		for index := range plan.RelationshipTargets {
			if plan.RelationshipTargets[index].Target.ProposalID == "r:uses" {
				target = &plan.RelationshipTargets[index]
				break
			}
		}
		require.NotNil(t, target)
		require.True(t, target.KnownEvidenceUnavailable)
	})
}

func TestSubmissionAssessmentRejectsAggregateKnownEvidenceContentBeforeCatalogConstruction(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	knownID := uuid.NewString()
	fixture.input.Snapshot.Proposal["relationship_hints"].([]any)[0].(map[string]any)["known_evidence_ids"] = []any{knownID}
	fixture.catalog.knownEvidence = []repository.SubmissionAssessmentKnownEvidence{{
		TeamID: fixture.input.Scope.TeamID, EvidenceID: knownID, FragmentID: knownID,
		IngestID: uuid.NewString(), OwnerProfileID: uuid.NewString(),
		Content:     strings.Repeat("x", assessor.SemanticAssessmentMaxKnownEvidenceRunes+1),
		ContentHash: "known-hash", Authority: "primary", SpaceID: uuid.NewString(), SpaceGeneration: 1,
	}}

	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
	err = engine.loadKnownEvidence(t.Context(), fixture.input.Scope, &plan)
	require.Equal(t, "known_evidence_context", mustPreflightStage(err))
	_, err = AssessSynchronousRemember(t.Context(), fixture.deps, fixture.input)
	require.ErrorIs(t, err, rememberapp.ErrRememberInputBudgetExceeded)
	require.Empty(t, fixture.catalog.entityInputs, "aggregate known evidence bound must run before entity catalog construction")
	require.Zero(t, fixture.provider.calls, "provider must not receive an over-budget request")
}

func TestSubmissionAssessmentRejectsExcessiveKnownEvidenceAnchorsAsInputBudget(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	knownID := uuid.NewString()
	fixture.input.Snapshot.Proposal["relationship_hints"].([]any)[0].(map[string]any)["known_evidence_ids"] = []any{knownID}
	fixture.catalog.knownEvidence = []repository.SubmissionAssessmentKnownEvidence{{
		TeamID: fixture.input.Scope.TeamID, EvidenceID: knownID, FragmentID: knownID,
		IngestID: uuid.NewString(), OwnerProfileID: uuid.NewString(),
		Content:     strings.Repeat("Alpha ", assessor.SemanticAssessmentMaxEntityGroundings+1),
		ContentHash: "known-hash", Authority: "primary", SpaceID: uuid.NewString(), SpaceGeneration: 1,
	}}

	_, err := AssessSynchronousRemember(t.Context(), fixture.deps, fixture.input)
	require.ErrorIs(t, err, rememberapp.ErrRememberInputBudgetExceeded)
	require.Zero(t, fixture.provider.calls, "anchor-bound requests must not reach the provider")
}

func TestSubmissionAssessmentAnchorCandidateAndPronounHelpers(t *testing.T) {
	candidate := repository.SemanticReviewEntityCandidate{
		EntityID: "entity-a", EntityKind: "person", CanonicalName: "A Person",
		IdentityContext: map[string]any{"source": "catalog"},
	}
	other := repository.SemanticReviewEntityCandidate{EntityID: "entity-b", EntityKind: "person", CanonicalName: "B Person"}
	anchorCandidates := candidateGroupCandidatesForAnchor(assessor.SemanticAssessmentEntityAnchor{
		CandidateEntityIDs: []string{"entity-a"},
	}, []repository.SemanticReviewEntityCandidate{other, candidate})
	require.Len(t, anchorCandidates, 1)
	require.Equal(t, "entity-a", anchorCandidates[0].EntityID)
	require.Equal(t, "catalog", anchorCandidates[0].IdentityContext["source"])

	pronouns := submissionAssessmentPronounOccurrences("They use it. He helps us.")
	require.Len(t, pronouns, 4)
	for index := 1; index < len(pronouns); index++ {
		require.LessOrEqual(t, pronouns[index-1][0], pronouns[index][0])
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

type catalogWithoutKnownEvidence struct{}

func (catalogWithoutKnownEvidence) ListSubmissionAssessmentEntityCatalog(context.Context, repository.SubmissionAssessmentEntityCatalogInput) (repository.SubmissionAssessmentEntityCatalogResult, error) {
	return repository.SubmissionAssessmentEntityCatalogResult{}, nil
}

func (catalogWithoutKnownEvidence) ResolveSemanticReviewPredicateCandidates(context.Context, repository.SemanticReviewPredicateResolutionInput) ([]repository.SemanticReviewPredicateResolution, error) {
	return nil, nil
}

func (catalogWithoutKnownEvidence) ListSemanticAssessmentPredicateOptions(context.Context, repository.SemanticAssessmentPredicateOptionsInput) ([]repository.SemanticReviewPredicateCandidate, error) {
	return nil, nil
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
