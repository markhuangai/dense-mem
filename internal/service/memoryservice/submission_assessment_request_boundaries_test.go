package memoryservice

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestSubmissionAssessmentGroundsAliasesWithBoundedNames(t *testing.T) {
	evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
		EvidenceID: "evidence:0", Content: "DENSE \t Memory protects PostgreSQL.",
	})
	plan := submissionAssessmentGroundingTestPlan("Dense-Mem", "project")
	candidate := repository.SemanticReviewEntityCandidate{
		EntityID: "entity-dense-mem", EntityKind: "project", CanonicalName: "Dense-Mem",
		ActiveNames: []string{"Dense-Mem", "Dense Memory"}, Status: "active",
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
	require.Equal(t, "DENSE \t Memory", entities[0].Groundings[0].Surface)
	require.Len(t, groups, 1)
	require.Equal(t, candidate.EntityID, groups[0].Candidates[0].EntityID)
}

func TestSubmissionAssessmentRejectsPronounsAndPartialWordGroundings(t *testing.T) {
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
			evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{EvidenceID: "evidence:0", Content: test.content})
			plan := submissionAssessmentGroundingTestPlan(test.entity, "concept")
			entities, groups, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
				Complete: true,
				Groups:   []repository.SubmissionAssessmentEntityCatalogGroup{{Ref: "entity:subject", Complete: true}},
			}, []assessor.SemanticReviewEvidence{evidence})
			require.NoError(t, err)
			require.Len(t, entities, 1)
			require.Empty(t, entities[0].Groundings)
			require.Empty(t, groups)
		})
	}
}

func TestSubmissionAssessmentDefaultsMissingEntityKindsFromCatalog(t *testing.T) {
	evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{EvidenceID: "evidence:0", Content: "Dense-Mem protects PostgreSQL."})
	plan := submissionAssessmentGroundingTestPlan("Dense-Mem", "")
	candidate := repository.SemanticReviewEntityCandidate{EntityID: "entity-dense-mem", EntityKind: "project", CanonicalName: "Dense-Mem", ActiveNames: []string{"Dense-Mem"}, Status: "active"}
	entities, _, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups:   []repository.SubmissionAssessmentEntityCatalogGroup{{Ref: "entity:subject", Candidates: []repository.SemanticReviewEntityCandidate{candidate}, Complete: true}},
	}, []assessor.SemanticReviewEvidence{evidence})
	require.NoError(t, err)
	require.Equal(t, "project", entities[0].Kind)

	plan = submissionAssessmentGroundingTestPlan("Dense-Mem", "")
	entities, _, err = submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups:   []repository.SubmissionAssessmentEntityCatalogGroup{{Ref: "entity:subject", Complete: true}},
	}, []assessor.SemanticReviewEvidence{evidence})
	require.NoError(t, err)
	require.Equal(t, "other", entities[0].Kind)
}

func TestSubmissionAssessmentGroundingRejectsMissingKnownEntity(t *testing.T) {
	evidence := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{EvidenceID: "evidence:0", Content: "The retired entity is no longer active."})
	plan := submissionAssessmentGroundingTestPlan("", "")
	plan.EntityTargets[0].KnownEntityID = "known-entity-id"
	plan.entityTargetsByRef = map[string]submissionAssessmentEntityTarget{"entity:subject": plan.EntityTargets[0]}
	_, _, err := submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups:   []repository.SubmissionAssessmentEntityCatalogGroup{{Ref: "entity:subject", Complete: true}},
	}, []assessor.SemanticReviewEvidence{evidence})
	require.ErrorIs(t, err, errSubmissionAssessmentStaleInput)

	plan = submissionAssessmentGroundingTestPlan("Dense-Mem", "project")
	plan.EntityTargets[0].KnownEntityID = "known-entity-id"
	plan.entityTargetsByRef = map[string]submissionAssessmentEntityTarget{"entity:subject": plan.EntityTargets[0]}
	_, _, err = submissionAssessmentGroundedEntities(plan, repository.SubmissionAssessmentEntityCatalogResult{
		Complete: true,
		Groups: []repository.SubmissionAssessmentEntityCatalogGroup{{
			Ref: "entity:subject", Complete: true,
			Candidates: []repository.SemanticReviewEntityCandidate{{EntityID: "same-name-in-space", EntityKind: "project", CanonicalName: "Dense-Mem"}},
		}},
	}, []assessor.SemanticReviewEvidence{evidence})
	require.ErrorIs(t, err, errSubmissionAssessmentStaleInput)
}

func TestSubmissionAssessmentPlanRetainsExactReviewContext(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	relationships := fixture.input.Snapshot.Proposal["relationship_hints"].([]any)
	first := relationships[0].(map[string]any)
	second := relationships[1].(map[string]any)
	first["correction_target"] = map[string]any{"relationship_id": uuid.NewString(), "expected_version": 3}
	second["conflict_context"] = map[string]any{"conflict_id": uuid.NewString(), "expected_version": 4}
	first["valid_from"] = "2026-01-01T01:00:00+01:00"
	first["valid_to"] = "2026-01-03T00:00:00Z"

	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	require.Equal(t, 3, plan.relationshipsByRef["r:uses"].CorrectionTarget.ExpectedVersion)
	require.Equal(t, 4, plan.relationshipsByRef["r:latency"].ConflictContext.ExpectedVersion)
	require.Equal(t, "2026-01-01T00:00:00Z", *plan.relationshipsByRef["r:uses"].Target.ValidFrom)
	require.Equal(t, "2026-01-03T00:00:00Z", *plan.relationshipsByRef["r:uses"].Target.ValidTo)
}

func TestSubmissionAssessmentPlanRetainsExactIDsAndPredicateKeys(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	relationships := fixture.input.Snapshot.Proposal["relationship_hints"].([]any)
	original := relationships[0].(map[string]any)
	knownID := uuid.NewString()
	subject := map[string]any{}
	for key, value := range original["subject"].(map[string]any) {
		subject[key] = value
	}
	subject["known_entity_id"] = knownID
	duplicate := map[string]any{}
	for key, value := range original {
		duplicate[key] = value
	}
	duplicate["ref"] = "r:uses-known"
	duplicate["subject"] = subject
	fixture.input.Snapshot.Proposal["relationship_hints"] = append(relationships, duplicate)
	original["predicate"].(map[string]any)["proposed_key"] = "proposal-only"
	original["predicate"].(map[string]any)["known_predicate_key"] = "exact-predicate"

	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	foundKnown := false
	for _, target := range plan.EntityTargets {
		if target.KnownEntityID == knownID {
			foundKnown = true
		}
	}
	require.True(t, foundKnown)
	require.Equal(t, "exact-predicate", plan.relationshipsByRef["r:uses"].KnownPredicateKey)
	require.Equal(t, "exact-predicate", plan.relationshipsByRef["r:uses"].Target.PredicateHint)
}

func TestSubmissionAssessmentPlanRejectsInvalidReviewAndValueInput(t *testing.T) {
	for _, field := range []string{"correction_target", "conflict_context"} {
		t.Run(field, func(t *testing.T) {
			fixture := synchronousAssessmentFixture(t)
			fixture.input.Snapshot.Proposal["relationship_hints"].([]any)[0].(map[string]any)[field] = map[string]any{"expected_version": 1}
			_, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
			require.Error(t, err)
		})
	}

	fixture := synchronousAssessmentFixture(t)
	relationship := fixture.input.Snapshot.Proposal["relationship_hints"].([]any)[0].(map[string]any)
	relationship["evidence_indices"] = []any{99}
	_, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.Error(t, err)

	base := map[string]any{"type": "number", "value": 42}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unsupported type", mutate: func(value map[string]any) { value["type"] = "unsupported" }},
		{name: "missing canonical value", mutate: func(value map[string]any) { value["value"] = []string{"unsupported"} }},
		{name: "display too long", mutate: func(value map[string]any) { value["display"] = strings.Repeat("x", 4097) }},
		{name: "unit too long", mutate: func(value map[string]any) { value["unit"] = strings.Repeat("x", 129) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := map[string]any{}
			for key, item := range base {
				value[key] = item
			}
			test.mutate(value)
			_, err := submissionAssessmentValueFromProposal(value)
			require.Error(t, err)
		})
	}
}

func TestSubmissionAssessmentEvidenceIndicesRejectUnsafeValues(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	for _, raw := range []map[string]any{
		{"evidence_indices": []any{0, 0}},
		{"evidence_indices": []any{99}},
		{"evidence_indices": []any{"invalid"}},
		{"evidence_indices": []any{}},
	} {
		_, err := submissionAssessmentEvidenceIDsFromProposal(raw, plan.itemsByEvidenceID)
		require.Error(t, err)
	}
}

func TestSubmissionAssessmentRequestPreflightBranches(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(synchronousAssessmentFixtureValue, submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan)
		want   string
	}{
		{
			name: "catalog load failure",
			mutate: func(fixture synchronousAssessmentFixtureValue, plan submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan) {
				fixture.catalog.entityErr = errTestAssessmentCatalog
				return fixture, plan
			},
			want: "load submission entity catalog",
		},
		{
			name: "incomplete entity catalog",
			mutate: func(fixture synchronousAssessmentFixtureValue, plan submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan) {
				fixture.catalog.entityComplete = false
				return fixture, plan
			},
			want: "entity catalog exceeds",
		},
		{
			name: "missing derived plan target",
			mutate: func(fixture synchronousAssessmentFixtureValue, plan submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan) {
				delete(plan.entityTargetsByRef, plan.EntityTargets[0].Target.Ref)
				return fixture, plan
			},
			want: "",
		},
		{
			name: "predicate resolution failure",
			mutate: func(fixture synchronousAssessmentFixtureValue, plan submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan) {
				fixture.catalog.predicateResolutionErr = errTestAssessmentCatalog
				return fixture, plan
			},
			want: "resolve submission predicate candidates",
		},
		{
			name: "predicate candidate overflow",
			mutate: func(fixture synchronousAssessmentFixtureValue, plan submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan) {
				fixture.catalog.predicateComplete = false
				return fixture, plan
			},
			want: "predicate candidates exceed",
		},
		{
			name: "predicate options failure",
			mutate: func(fixture synchronousAssessmentFixtureValue, plan submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan) {
				fixture.catalog.predicateOptionsErr = errTestAssessmentCatalog
				return fixture, plan
			},
			want: "load submission predicate options",
		},
		{
			name: "candidate context budget",
			mutate: func(fixture synchronousAssessmentFixtureValue, plan submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan) {
				fixture.deps.Limits.MaxCandidateContextTokens = 1
				return fixture, plan
			},
			want: "context exceeds",
		},
		{
			name: "input budget",
			mutate: func(fixture synchronousAssessmentFixtureValue, plan submissionAssessmentPlan) (synchronousAssessmentFixtureValue, submissionAssessmentPlan) {
				fixture.deps.Limits.MaxInputTokens = 1
				return fixture, plan
			},
			want: "input exceeds",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := synchronousAssessmentFixture(t)
			plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
			require.NoError(t, err)
			fixture, plan = test.mutate(fixture, plan)
			engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
			_, err = engine.buildRequest(t.Context(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
			if test.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.want)
		})
	}

	fixture := synchronousAssessmentFixture(t)
	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	fixture.input.Scope.IngestID = ""
	fixture.deps.Limits.MaxCandidatesPerSurface = 0
	fixture.deps.Limits.MaxPredicateOptions = assessor.SemanticAssessmentMaxPredicateOptions + 1
	fixture.catalog.predicateOptions = append(fixture.catalog.predicateOptions,
		repository.SemanticReviewPredicateCandidate{PredicateKey: "", Version: 0},
		fixture.catalog.predicateOptions[0],
	)
	engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
	request, err := engine.buildRequest(t.Context(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
	require.NoError(t, err)
	require.Equal(t, "synchronous-remember:request", request.RequestID)
}

func TestSubmissionAssessmentRequiredContextBudgetDiagnostics(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	fixture.deps.Limits.MaxCandidateContextTokens = 1
	engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
	_, err = engine.buildRequest(t.Context(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
	require.Error(t, err)
	reasonCode, details := SynchronousAssessmentFailureDetails(err)
	require.Equal(t, "entity_catalog", reasonCode)
	require.Equal(t, "assessor.required_entity_catalog", details["component"])
	require.Equal(t, true, details["server_owned"])
	require.Greater(t, details["observed"], 1)
	require.Equal(t, 1, details["limit"])
}

func TestSubmissionAssessmentCombinedRequiredContextUsesOperatorGuidance(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	relationships := fixture.input.Snapshot.Proposal["relationship_hints"].([]any)
	for _, raw := range relationships {
		relationship := raw.(map[string]any)
		predicate := relationship["predicate"].(map[string]any)
		predicate["known_predicate_key"] = predicate["proposed_key"]
	}
	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	fixture.deps.Limits.MaxInputTokens = 1_000_000
	fixture.deps.Limits.MaxCandidateContextTokens = 1_000_000
	engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
	request, err := engine.buildRequest(t.Context(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
	require.NoError(t, err)
	withoutEntities := request
	withoutEntities.EntityCandidateGroups = nil
	withoutPredicates := request
	withoutPredicates.PredicateOptions = nil
	withoutEntityInput, _, err := assessor.CountSemanticAssessmentRequestTokens(withoutEntities, fixture.deps.Limits)
	require.NoError(t, err)
	withoutPredicateInput, _, err := assessor.CountSemanticAssessmentRequestTokens(withoutPredicates, fixture.deps.Limits)
	require.NoError(t, err)
	limit := withoutEntityInput
	if withoutPredicateInput > limit {
		limit = withoutPredicateInput
	}
	require.Greater(t, request.InputTokens, limit)
	repairHeadroom := (assessor.SemanticAssessmentMaxProviderTurns - 1) * (fixture.deps.Limits.MaxOutputTokens + 4096)
	fixture.deps.Limits.MaxInputTokens = limit + repairHeadroom
	engine = newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
	_, err = engine.buildRequest(t.Context(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
	require.Error(t, err)
	reasonCode, details := SynchronousAssessmentFailureDetails(err)
	require.Equal(t, "entity_catalog", reasonCode)
	require.Equal(t, "assessor.required_entity_catalog", details["component"])
	require.Equal(t, true, details["server_owned"])
	require.Equal(t, request.InputTokens, details["observed"])
	require.Equal(t, limit+repairHeadroom, details["limit"])

	serverOwned := rememberapp.TerminalStatusErrorWithDetails(rememberapp.TerminalErrorInputBudgetExceeded, reasonCode, details)
	require.Equal(t, string(rememberapp.TerminalNextActionContactOperator), serverOwned.NextAction)
	require.Equal(t, "Ask an operator to review the configured assessor budget and server-owned context before retrying.", serverOwned.Remediation)
	require.NoError(t, rememberapp.ValidateTerminalStatusError(serverOwned))

	clientControlled := rememberapp.TerminalStatusErrorWithDetails(rememberapp.TerminalErrorInputBudgetExceeded, "assessment_input", map[string]any{"component": "assessor.required_input", "client_controlled": true})
	require.Equal(t, string(rememberapp.TerminalNextActionResubmitRemember), clientControlled.NextAction)
	require.Contains(t, clientControlled.Remediation, "new idempotency_key")
}

func TestSynchronousAssessmentFailureDetailsClassifiesPreflightStages(t *testing.T) {
	cases := []struct {
		stage     string
		component string
		client    bool
	}{
		{stage: "entity_catalog", component: "assessor.required_entity_catalog"},
		{stage: "known_evidence_context", component: "assessor.required_known_evidence"},
		{stage: "catalog_context", component: "assessor.optional_context"},
		{stage: "catalog_context_validation", component: "assessor.optional_context"},
		{stage: "predicate_options_overflow", component: "assessor.optional_context"},
		{stage: "predicate_context", component: "assessor.required_predicate_context"},
		{stage: "required_context", component: "assessor.required_context"},
		{stage: "assessment_input", component: "assessor.required_input", client: true},
		{stage: "input_tokens", component: "assessor.required_input", client: true},
		{stage: "provider_framing", component: "assessor.provider_framing"},
		{stage: "conversation_input_tokens", component: "assessor.conversation"},
		{stage: "conversation_candidate_context_tokens", component: "assessor.conversation_candidate_context"},
		{stage: "unknown", component: "assessor"},
	}
	for _, test := range cases {
		t.Run(test.stage, func(t *testing.T) {
			err := deterministicSemanticAssessmentPreflightErrorWithMeasurement(test.stage, "bounded failure", assessor.FailureMeasurement{
				Unit: "tokens", Observed: 12, ObservedAtLeast: true, Limit: 10,
			})
			reasonCode, details := SynchronousAssessmentFailureDetails(err)
			require.Equal(t, test.stage, reasonCode)
			require.Equal(t, test.component, details["component"])
			require.Equal(t, test.client, details["client_controlled"] == true)
			require.Equal(t, !test.client, details["server_owned"] == true)
			require.Equal(t, true, details["observed_at_least"])
		})
	}

	empty := &semanticAssessmentPreflightError{err: errors.New("preflight")}
	reasonCode, details := SynchronousAssessmentFailureDetails(empty)
	require.Equal(t, "assessor_preflight_failed", reasonCode)
	require.Equal(t, "assessor", details["component"])
	require.Equal(t, true, details["server_owned"])

	conversation := &assessor.MalformedResponseError{FailureClass: "input_budget", ValidationStage: "conversation_input_tokens"}
	reasonCode, details = SynchronousAssessmentFailureDetails(conversation)
	require.Equal(t, "assessor_conversation_input_exceeded", reasonCode)
	require.Equal(t, "assessor.conversation", details["component"])
	require.NotContains(t, details, "observed")
	_, details = SynchronousAssessmentFailureDetails(&assessor.MalformedResponseError{FailureClass: "provider"})
	require.Nil(t, details)
}

func TestSubmissionAssessmentCatalogFailuresCarryDatabaseClassification(t *testing.T) {
	tests := []struct {
		name string
		fail func(*submissionAssessmentWorkerCatalogStub)
	}{
		{name: "entity catalog", fail: func(catalog *submissionAssessmentWorkerCatalogStub) { catalog.entityErr = errTestAssessmentCatalog }},
		{name: "predicate resolution", fail: func(catalog *submissionAssessmentWorkerCatalogStub) {
			catalog.predicateResolutionErr = errTestAssessmentCatalog
		}},
		{name: "predicate options", fail: func(catalog *submissionAssessmentWorkerCatalogStub) {
			catalog.predicateOptionsErr = errTestAssessmentCatalog
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := synchronousAssessmentFixture(t)
			plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
			require.NoError(t, err)
			test.fail(fixture.catalog)
			engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
			_, err = engine.buildRequest(t.Context(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
			require.ErrorIs(t, err, rememberapp.ErrRememberDatabaseFailure)
		})
	}
}

var errTestAssessmentCatalog = errors.New("catalog test failure")
