package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSemanticAssessmentEntityResolutionGuards(t *testing.T) {
	fragment := repository.EvidenceFragment{FragmentID: uuid.NewString(), Content: "Mark works on Dense-Mem."}
	entityID := uuid.NewString()
	group := &verifier.SemanticAssessmentEntityCandidateGroup{
		EvidenceID: "ev", Start: 0, End: 4,
		Candidates: []verifier.SemanticAssessmentEntityCandidate{{
			EntityID: entityID, CanonicalName: "Mark", Kind: string(domain.EntityKindPerson),
		}},
	}
	response := verifier.SemanticAssessmentResponse{EntityResults: []verifier.SemanticAssessmentEntityResult{
		{Ref: "reuse", EvidenceID: "ev", Start: 0, End: 4, Surface: "Mark", Kind: "person", Action: "reuse", CandidateEntityID: testStringPointer(entityID), Confidence: 1, Rationale: "exact"},
		{Ref: "held-create", EvidenceID: "ev", Start: 0, End: 4, Surface: "Mark", Kind: "person", Action: "create", Confidence: 1, Rationale: "held"},
		{Ref: "new-create", EvidenceID: "ev", Start: 5, End: 10, Surface: "works", Kind: "concept", Action: "create", Confidence: 1, Rationale: "new"},
		{Ref: "ambiguous", EvidenceID: "ev", Start: 0, End: 4, Surface: "Mark", Kind: "person", Action: "ambiguous", Confidence: 1, Rationale: "ambiguous"},
	}}

	resolutions, states, err := semanticAssessmentEntityResolutions(response, fragment, uuid.NewString(), map[string]*verifier.SemanticAssessmentEntityCandidateGroup{
		assessmentCandidateGroupKey("ev", 0, 4): group,
	}, nil)
	require.NoError(t, err)
	require.Len(t, resolutions, 4)
	assert.Equal(t, string(domain.EntityResolutionReuse), resolutions[0].Action)
	assert.Equal(t, entityID, resolutions[0].EntityID)
	assert.Equal(t, string(domain.EntityResolutionAmbiguous), resolutions[1].Action)
	assert.Equal(t, "identity", resolutions[1].SemanticReviewKind)
	assert.Equal(t, string(domain.EntityResolutionCreate), resolutions[2].Action)
	assert.Equal(t, "semantic_assessment", resolutions[2].IdentityContext["source"])
	assert.Equal(t, string(domain.EntityResolutionAmbiguous), resolutions[3].Action)
	assert.True(t, states["reuse"].resolved)
	assert.False(t, states["held-create"].resolved)

	response.EntityResults[0].Action = "unsupported"
	_, _, err = semanticAssessmentEntityResolutions(response, fragment, uuid.NewString(), nil, nil)
	require.ErrorContains(t, err, "unsupported stored entity assessment action")

	assert.False(t, assessmentReusableCandidate(nil, entityID, "person"))
	truncated := *group
	truncated.CandidateContextTruncated = true
	assert.False(t, assessmentReusableCandidate(&truncated, entityID, "person"))
	assert.False(t, assessmentReusableCandidate(group, uuid.NewString(), "person"))
	assert.True(t, assessmentReusableCandidate(group, entityID, "person"))
	assert.False(t, assessmentCompatibleCandidateExists(nil, "person"))
	assert.True(t, assessmentCompatibleCandidateExists(group, "person"))
	assert.Empty(t, assessmentEntityReviewOptions(nil))
	assert.Equal(t, entityID, assessmentEntityReviewOptions(group)[0]["entity_id"])
}

func TestSemanticAssessmentEntityResolutionHonorsApprovedCurrentSelection(t *testing.T) {
	fragment := repository.EvidenceFragment{FragmentID: uuid.NewString(), Content: "Mark works on Dense-Mem."}
	selectedID := uuid.NewString()
	otherID := uuid.NewString()
	response := verifier.SemanticAssessmentResponse{EntityResults: []verifier.SemanticAssessmentEntityResult{{
		Ref: "mark", EvidenceID: "ev", Start: 0, End: 4, Surface: "Mark", Kind: "person", Action: "ambiguous", Confidence: 1, Rationale: "multiple candidates",
	}}}
	response = applySemanticAssessmentReviewOverrides(response, repository.PlacementAssessmentReviewOverrides{
		EntitySelections: map[string]string{"mark": selectedID},
	})
	group := &verifier.SemanticAssessmentEntityCandidateGroup{
		EvidenceID: "ev", Start: 0, End: 4, CandidateContextTruncated: true,
		Candidates: []verifier.SemanticAssessmentEntityCandidate{
			{EntityID: selectedID, CanonicalName: "Mark", Kind: "person"},
			{EntityID: otherID, CanonicalName: "Mark A.", Kind: "person"},
		},
	}

	resolutions, states, err := semanticAssessmentEntityResolutions(response, fragment, uuid.NewString(), map[string]*verifier.SemanticAssessmentEntityCandidateGroup{
		assessmentCandidateGroupKey("ev", 0, 4): group,
	}, map[string]string{"mark": selectedID})
	require.NoError(t, err)
	require.Len(t, resolutions, 1)
	assert.Equal(t, string(domain.EntityResolutionReuse), resolutions[0].Action)
	assert.Equal(t, selectedID, resolutions[0].EntityID)
	assert.True(t, states["mark"].resolved)
}

func TestSemanticAssessmentRelationshipHelpersRouteAllReviewKinds(t *testing.T) {
	entityID := uuid.NewString()
	group := &verifier.SemanticAssessmentEntityCandidateGroup{Candidates: []verifier.SemanticAssessmentEntityCandidate{{EntityID: entityID, CanonicalName: "Mark", Kind: "person"}}}
	entities := map[string]assessmentEntityCommitState{
		"subject": {resolved: true, group: group, kind: "person"},
		"object":  {resolved: true, group: group, kind: "product"},
	}
	key := "works_on"
	version := 1
	objectRef := "object"
	result := verifier.SemanticAssessmentRelationshipResult{
		Ref: "relationship", SubjectRef: "subject", PredicateStatus: "resolved", PredicateKey: &key, PredicateVersion: &version,
		ObjectRef: &objectRef, Modality: "statement", ScopeStatus: "absent", TemporalVerdict: "absent",
	}
	predicates := map[string]verifier.SemanticAssessmentPredicateOption{
		assessmentPredicateKey(key, version): {
			PredicateKey: key, Version: version, Aliases: []string{"works on"},
			AllowedSubjectKinds: []string{"person"}, AllowedObjectKinds: []string{"product"},
		},
	}
	assert.Empty(t, semanticAssessmentRelationshipReviewKind(result, entities, predicates))

	result.PredicateStatus = "needs_review"
	assert.Equal(t, "predicate", semanticAssessmentRelationshipReviewKind(result, entities, predicates))
	result.PredicateStatus = "resolved"
	result.PredicateKey = nil
	assert.Equal(t, "predicate", semanticAssessmentRelationshipReviewKind(result, entities, predicates))
	result.PredicateKey = &key
	entities["subject"] = assessmentEntityCommitState{resolved: false, group: group, kind: "person"}
	assert.Equal(t, "identity", semanticAssessmentRelationshipReviewKind(result, entities, predicates))
	entities["subject"] = assessmentEntityCommitState{resolved: true, group: group, kind: "person"}
	result.ScopeStatus = "needs_review"
	assert.Equal(t, "scope", semanticAssessmentRelationshipReviewKind(result, entities, predicates))
	result.ScopeStatus = "absent"
	result.TemporalVerdict = "ambiguous"
	assert.Equal(t, "time", semanticAssessmentRelationshipReviewKind(result, entities, predicates))

	fragment := repository.EvidenceFragment{
		FragmentID: uuid.NewString(), Content: "Mark works on Dense-Mem.", SourceID: uuid.NewString(), SourceRevisionID: uuid.NewString(),
	}
	_, err := semanticAssessmentSupport(fragment, uuid.NewString(), nil)
	require.ErrorContains(t, err, "no evidence span")
	_, err = semanticAssessmentSupport(fragment, uuid.NewString(), []verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "ev", Start: 20, End: 99}})
	require.Error(t, err)
	supports, err := semanticAssessmentSupport(fragment, "assessment", []verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "ev", Start: 0, End: 4}, {EvidenceID: "ev", Start: 5, End: 10}})
	require.NoError(t, err)
	require.Len(t, supports, 2)
	assert.Equal(t, "Mark", supports[0].Quote)
	assert.Equal(t, "works", supports[1].Quote)
	assert.Contains(t, supports[1].SourceGroupKey, ":5:10")
	primarySupport, additionalSupports := semanticAssessmentPrimarySupport(supports)
	require.NotNil(t, primarySupport)
	assert.Equal(t, supports[0], *primarySupport)
	assert.Equal(t, supports[1:], additionalSupports)

	valueResult := verifier.SemanticAssessmentRelationshipResult{Ref: "value", ObjectValue: &verifier.SemanticAssessmentValue{ValueType: "number", CanonicalValue: "42", Display: testStringPointer("forty-two"), Unit: testStringPointer("items")}}
	ref, value, err := semanticAssessmentObject(valueResult)
	require.NoError(t, err)
	assert.Empty(t, ref)
	assert.Equal(t, "forty-two", value.Display)
	_, _, err = semanticAssessmentObject(verifier.SemanticAssessmentRelationshipResult{})
	require.ErrorContains(t, err, "object is missing")
	refResult := verifier.SemanticAssessmentRelationshipResult{ObjectRef: testStringPointer("object")}
	ref, value, err = semanticAssessmentObject(refResult)
	require.NoError(t, err)
	assert.Equal(t, "object", ref)
	assert.Nil(t, value)

	validFrom := "2026-07-28T00:00:00Z"
	validTo := "2026-07-29T00:00:00Z"
	from, to, err := semanticAssessmentValidity(verifier.SemanticAssessmentRelationshipResult{ValidFrom: &validFrom, ValidTo: &validTo})
	require.NoError(t, err)
	assert.True(t, from.Before(*to))
	invalid := "not-a-time"
	_, _, err = semanticAssessmentValidity(verifier.SemanticAssessmentRelationshipResult{ValidFrom: &invalid})
	require.Error(t, err)
	assert.Empty(t, semanticAssessmentScopeKey(verifier.SemanticAssessmentRelationshipResult{ScopeStatus: "absent"}))
	scopeKey := "project:one"
	assert.Equal(t, scopeKey, semanticAssessmentScopeKey(verifier.SemanticAssessmentRelationshipResult{ScopeStatus: "resolved", ScopeKey: &scopeKey}))

	for kind, reason := range map[string]string{
		"identity": "identity_needs_review", "predicate": "predicate_needs_review", "scope": "scope_needs_review", "time": "time_needs_review", "other": "support_confidence",
	} {
		assert.Equal(t, reason, reviewTaskReasonForAssessmentKind(kind))
		assert.NotEmpty(t, assessmentReviewQuestion(kind))
		assert.NotEmpty(t, assessmentReviewGuidance(kind))
	}

	entities["subject"] = assessmentEntityCommitState{resolved: false, group: group, kind: "person"}
	entities["object"] = assessmentEntityCommitState{resolved: false, group: group, kind: "product"}
	result.ObjectRef = &objectRef
	orderedPredicates := []verifier.SemanticAssessmentPredicateOption{predicates[assessmentPredicateKey(key, version)]}
	identityOptions := assessmentReviewOptions("identity", result, entities, orderedPredicates)
	require.Len(t, identityOptions, 1)
	predicateOptions := assessmentReviewOptions("predicate", result, entities, orderedPredicates)
	require.Len(t, predicateOptions, 1)
	assert.Equal(t, key, predicateOptions[0]["predicate_key"])
	assert.Equal(t, "submit_new_evidence", assessmentReviewOptions("scope", result, entities, orderedPredicates)[0]["action"])
}

func TestSemanticAssessmentCandidateHelpersBoundAndPrefetch(t *testing.T) {
	original := repository.SemanticReviewEntityCandidate{
		EntityID: uuid.NewString(), CanonicalName: "Evidence", EntityKind: "concept", Status: "active", IdentityContext: map[string]any{"source": "catalog"},
	}
	candidate := assessmentEntityCandidate(original)
	original.IdentityContext["source"] = "changed"
	assert.Equal(t, "catalog", candidate.IdentityContext["source"])

	group := &verifier.SemanticAssessmentEntityCandidateGroup{}
	addAssessmentEntityCandidate(nil, candidate)
	addAssessmentEntityCandidate(group, verifier.SemanticAssessmentEntityCandidate{})
	for index := 0; index < verifier.SemanticAssessmentMaxEntityCandidatesPerSurface+1; index++ {
		candidate.EntityID = fmt.Sprintf("entity-%d", index)
		addAssessmentEntityCandidate(group, candidate)
	}
	addAssessmentEntityCandidate(group, group.Candidates[0])
	assert.Len(t, group.Candidates, verifier.SemanticAssessmentMaxEntityCandidatesPerSurface)
	assert.True(t, group.CandidateContextTruncated)

	fragment := repository.EvidenceFragment{EvidenceIndex: 0, Content: "Evidence is durable."}
	hint := placementReviewEntityHint{Name: "Evidence", Evidence: []placementReviewEvidenceSpanHint{{evidenceIndex: 0, start: 0, end: 8}}}
	assert.Equal(t, []assessmentTextSpan{{start: 0, end: 8, surface: "Evidence"}}, assessmentHintSpans(fragment, hint))
	hint.Evidence = []placementReviewEvidenceSpanHint{{evidenceIndex: 1, start: 0, end: 8}}
	assert.Empty(t, assessmentHintSpans(fragment, hint))
	assert.Nil(t, exactTokenSpans(fragment.Content, ""))
	assert.Nil(t, exactTokenSpans(fragment.Content, "very-long-not-present"))
	assert.False(t, assessmentTokenBoundary([]rune("mark_2"), 0, 4))
	assert.True(t, assessmentTokenBoundary([]rune("Mark."), 0, 4))

	_, _, _, catalog, _, worker := semanticAssessmentWorkerFixture(t)
	service := worker.(*semanticAssessmentPlacementWorkerService)
	ledger := service.ledger.(*semanticAssessmentWorkerLedgerStub)
	run := *ledger.run
	catalog.entityMatches = repository.SemanticAssessmentEntityMatchResult{Matches: []repository.SemanticAssessmentEntityMatch{{Candidate: original, MatchedName: "Evidence"}}, Truncated: true}
	knownID := uuid.NewString()
	catalog.knownCandidates = map[string][]repository.SemanticReviewEntityCandidate{
		knownID: []repository.SemanticReviewEntityCandidate{{EntityID: knownID, CanonicalName: "Evidence", EntityKind: "concept", Status: "active"}},
	}
	proposal := map[string]any{"entity_hints": []any{map[string]any{"ref": "known", "name": "Evidence", "known_entity_id": knownID}}}
	groups, truncated, err := service.prefetchEntityCandidates(context.Background(), run, ledger.placement.Evidence[0], proposal, "evidence:0")
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, groups, 1)
	assert.True(t, groups[0].CandidateContextTruncated)
	assert.Len(t, groups[0].Candidates, 2)

	catalog.entityMatches = repository.SemanticAssessmentEntityMatchResult{}
	scopedProposal := map[string]any{"entity_hints": []any{map[string]any{
		"ref":             "known",
		"name":            "Evidence",
		"known_entity_id": knownID,
		"evidence": []any{map[string]any{
			"evidence_index": 1,
			"start":          0,
			"end":            8,
		}},
	}}}
	groups, truncated, err = service.prefetchEntityCandidates(context.Background(), run, ledger.placement.Evidence[0], scopedProposal, "evidence:0")
	require.NoError(t, err)
	assert.False(t, truncated)
	assert.Empty(t, groups)

	catalog.entityMatchesErr = errors.New("catalog unavailable")
	_, _, err = service.prefetchEntityCandidates(context.Background(), run, ledger.placement.Evidence[0], nil, "evidence:0")
	require.ErrorContains(t, err, "load exact entity candidates")
	catalog.entityMatchesErr = nil
	catalog.knownCandidates[knownID] = []repository.SemanticReviewEntityCandidate{{EntityID: knownID, Status: "inactive"}}
	_, _, err = service.prefetchEntityCandidates(context.Background(), run, ledger.placement.Evidence[0], proposal, "evidence:0")
	require.ErrorContains(t, err, "not a current active candidate")
}

func TestSemanticAssessmentCandidatePrefetchCapsGroupsAndTrimClonesTrustedRefs(t *testing.T) {
	_, _, _, catalog, _, worker := semanticAssessmentWorkerFixture(t)
	service := worker.(*semanticAssessmentPlacementWorkerService)
	ledger := service.ledger.(*semanticAssessmentWorkerLedgerStub)
	run := *ledger.run

	fragment := ledger.placement.Evidence[0]
	names := make([]string, 0, verifier.SemanticAssessmentMaxEntityResults+1)
	matches := make([]repository.SemanticAssessmentEntityMatch, 0, verifier.SemanticAssessmentMaxEntityResults+1)
	for index := 0; index <= verifier.SemanticAssessmentMaxEntityResults; index++ {
		name := fmt.Sprintf("Known%d", index)
		names = append(names, name)
		matches = append(matches, repository.SemanticAssessmentEntityMatch{
			Candidate: repository.SemanticReviewEntityCandidate{
				EntityID: fmt.Sprintf("entity-%d", index), CanonicalName: name, EntityKind: "concept", Status: "active",
			},
			MatchedName: name,
		})
	}
	fragment.Content = strings.Join(names, " ")
	catalog.entityMatches = repository.SemanticAssessmentEntityMatchResult{Matches: matches}

	groups, truncated, err := service.prefetchEntityCandidates(context.Background(), run, fragment, nil, "evidence:0")
	require.NoError(t, err)
	assert.True(t, truncated)
	require.Len(t, groups, verifier.SemanticAssessmentMaxEntityResults)
	for _, group := range groups {
		assert.True(t, group.CandidateContextTruncated)
	}

	_, _, _, request, _, _ := semanticAssessmentConfidenceFixture(t)
	request.RequiredRelationshipRefs = []verifier.SemanticAssessmentRequiredRelationshipRef{{
		ProposalID: "proposal-1",
		Evidence:   []verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 0, End: 8}},
	}}
	cloned := cloneSemanticAssessmentRequestForTrim(request)
	assert.True(t, trimSemanticAssessmentCandidates(&cloned, 0))
	assert.False(t, trimSemanticAssessmentCandidates(&verifier.SemanticAssessmentRequest{}, 1))
	cloned.RequiredRelationshipRefs[0].Evidence[0].EvidenceID = "changed"
	assert.Equal(t, "evidence:0", request.RequiredRelationshipRefs[0].Evidence[0].EvidenceID)
}

func TestSemanticAssessmentWorkerFailureAndStoredAssessmentBoundaries(t *testing.T) {
	ledger, _, commit, _, provider, worker := semanticAssessmentWorkerFixture(t)
	service := worker.(*semanticAssessmentPlacementWorkerService)
	for _, testCase := range []struct {
		name   string
		mutate func(*semanticAssessmentPlacementWorkerService)
		want   string
	}{
		{"ledger", func(s *semanticAssessmentPlacementWorkerService) { s.ledger = nil }, "ledger"},
		{"assessments", func(s *semanticAssessmentPlacementWorkerService) { s.assessments = nil }, "assessment"},
		{"commit", func(s *semanticAssessmentPlacementWorkerService) { s.commit = nil }, "commit"},
		{"catalog", func(s *semanticAssessmentPlacementWorkerService) { s.catalog = nil }, "catalog"},
		{"provider", func(s *semanticAssessmentPlacementWorkerService) { s.provider = nil }, "provider"},
		{"team", func(s *semanticAssessmentPlacementWorkerService) { s.teamID = "" }, "team_id"},
		{"worker", func(s *semanticAssessmentPlacementWorkerService) { s.workerID = "" }, "worker_id"},
		{"threshold", func(s *semanticAssessmentPlacementWorkerService) { s.globalConfidenceThreshold = -1 }, "threshold"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := *service
			testCase.mutate(&candidate)
			require.ErrorContains(t, candidate.validateDependencies(), testCase.want)
		})
	}

	provider.response = func(req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
		return verifier.SemanticAssessmentResponse{
			RequestID:       req.RequestID,
			SecuritySignals: []verifier.SemanticSecuritySignal{{EvidenceID: "evidence:0", Kind: "instruction_override", Start: 0, End: 8}},
			EntityResults:   []verifier.SemanticAssessmentEntityResult{}, RelationshipResults: []verifier.SemanticAssessmentRelationshipResult{},
		}, nil
	}
	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, ledger.appendSecurity, 1)
	assert.Equal(t, "Evidence", ledger.appendSecurity[0].SecurityEventDraft.Signals[0].Quote)
	require.Len(t, commit.completions, 1)
	assert.Equal(t, "security_signal", commit.completions[0].Payload["failure_stage"])

	_, _, _, _, response, _ := semanticAssessmentConfidenceFixture(t)
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	canonicalRaw, err := verifier.CanonicalJSON(raw)
	require.NoError(t, err)
	assessment := &repository.PlacementAssessment{NormalizedResponse: raw, ResponseHash: semanticAssessmentHash(canonicalRaw)}
	_, err = decodeStoredAssessment(nil, verifier.DefaultSemanticAssessmentLimits())
	require.ErrorContains(t, err, "is nil")
	assessment.ResponseHash = "sha256:wrong"
	_, err = decodeStoredAssessment(assessment, verifier.DefaultSemanticAssessmentLimits())
	require.ErrorContains(t, err, "hash mismatch")
	assessment.ResponseHash = semanticAssessmentHash(canonicalRaw)
	decoded, err := decodeStoredAssessment(assessment, verifier.DefaultSemanticAssessmentLimits())
	require.NoError(t, err)
	assert.Equal(t, response.RequestID, decoded.RequestID)

	noItem := repository.PlacementItem{}
	require.NoError(t, service.retryOrFail(context.Background(), *ledger.run, noItem, "item", false, false))
	assert.Equal(t, 1, ledger.finishCalls)
	terminalRun := *ledger.run
	terminalRun.MaxAttempts = terminalRun.Attempts
	require.NoError(t, service.retryOrFail(context.Background(), terminalRun, ledger.placement.Items[0], "attempt", false, false))
	assert.Len(t, commit.completions, 2)
	retryRun := *ledger.run
	retryRun.MaxAttempts = retryRun.Attempts + 1
	require.NoError(t, service.retryOrFail(context.Background(), retryRun, ledger.placement.Items[0], "provider", true, true))
	require.NotEmpty(t, commit.requeues)

	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	recordSemanticAssessmentGateBands(metrics, repository.CommitPlacementSemanticInput{
		RelationshipObservations: []repository.PlacementRelationshipDecisionInput{{GateResult: "meets_write_threshold"}},
		RelationshipReviews:      []repository.PlacementRelationshipReviewInput{{GateResult: "not_applicable"}},
	})
	assert.Equal(t, 1, metrics.AssessorConfidenceGateCount("meets_write_threshold"))
	assert.Equal(t, 1, metrics.AssessorConfidenceGateCount("not_applicable"))
	assert.Equal(t, verifier.DefaultSemanticAssessmentLimits().Tokenizer, assessmentTokenizer(verifier.SemanticAssessmentLimits{}))
	assert.Equal(t, "value", cloneAssessmentProposal(map[string]any{"value": "value"})["value"])
	assert.Empty(t, cloneAssessmentProposal(map[string]any{"bad": func() {}}))
	assert.Equal(t, repository.AssessmentPolicyVersion+":config-0", assessmentPolicyVersion(repository.AutoWriteConfidencePolicy{}))
	assert.Equal(t, repository.AssessmentPolicyVersion+":config-7", assessmentPolicyVersion(repository.AutoWriteConfidencePolicy{ConfigVersion: 7}))
	assert.Equal(t, "custom:config-1", assessmentPolicyVersion(repository.AutoWriteConfidencePolicy{Version: "custom", ConfigVersion: 1}))
	assert.NotEmpty(t, semanticAssessmentHash([]byte("assessment")))
	canonicalLeft, err := verifier.CanonicalJSON([]byte(`{"b":2,"a":1}`))
	require.NoError(t, err)
	canonicalRight, err := verifier.CanonicalJSON([]byte(`{"a":1,"b":2}`))
	require.NoError(t, err)
	assert.Equal(t, semanticAssessmentHash(canonicalLeft), semanticAssessmentHash(canonicalRight))
}

func TestSemanticAssessmentCandidateContextTrimming(t *testing.T) {
	_, _, _, request, _, _ := semanticAssessmentConfidenceFixture(t)
	limits := verifier.DefaultSemanticAssessmentLimits()
	prepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, validationErrors)

	limits.MaxCandidateContextTokens = prepared.CandidateContextTokens - 1
	trimmed, err := trimSemanticAssessmentCandidateContext(request, limits)
	require.NoError(t, err)
	assert.True(t, trimmed.CandidateContextTruncated)
	assert.Empty(t, trimmed.PredicateOptions)
	assert.LessOrEqual(t, trimmed.CandidateContextTokens, limits.MaxCandidateContextTokens)

	assert.True(t, semanticAssessmentLimitFailure([]verifier.SemanticValidationError{{Field: "candidate_context_tokens"}, {Field: "input_tokens"}}))
	assert.False(t, semanticAssessmentLimitFailure(nil))
	assert.False(t, semanticAssessmentLimitFailure([]verifier.SemanticValidationError{{Field: "request_id"}}))
	assert.False(t, trimOneSemanticAssessmentCandidate(nil))

	request.PredicateOptions = nil
	assert.True(t, trimOneSemanticAssessmentCandidate(&request))
	assert.True(t, request.EntityCandidateGroups[len(request.EntityCandidateGroups)-1].CandidateContextTruncated)
	for index := range request.EntityCandidateGroups {
		request.EntityCandidateGroups[index].Candidates = nil
	}
	assert.False(t, trimOneSemanticAssessmentCandidate(&request))

	request.TeamID = ""
	_, err = trimSemanticAssessmentCandidateContext(request, verifier.DefaultSemanticAssessmentLimits())
	require.ErrorContains(t, err, "candidate context is invalid")
	stage, terminal := semanticAssessmentPreflightFailure(err)
	assert.True(t, terminal)
	assert.Equal(t, "candidate_context_validation", stage)

	request.TeamID = uuid.NewString()
	request.PredicateOptions = nil
	request.EntityCandidateGroups = nil
	limits = verifier.DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	_, err = trimSemanticAssessmentCandidateContext(request, limits)
	require.ErrorContains(t, err, "exceeds configured token limits")
	stage, terminal = semanticAssessmentPreflightFailure(err)
	assert.True(t, terminal)
	assert.Equal(t, "candidate_context_limit", stage)
}

func TestSemanticAssessmentCandidateContextTrimmingKeepsTheLargestPriorityPrefix(t *testing.T) {
	_, _, _, request, _, _ := semanticAssessmentConfidenceFixture(t)
	for i := 0; i < 31; i++ {
		request.PredicateOptions = append(request.PredicateOptions, verifier.SemanticAssessmentPredicateOption{
			PredicateKey:        fmt.Sprintf("least_relevant_%02d", i),
			Version:             1,
			Aliases:             []string{fmt.Sprintf("least relevant %02d", i)},
			AllowedSubjectKinds: []string{"person"},
			AllowedObjectKinds:  []string{"product"},
			RelationshipKind:    "state",
			CurrentCardinality:  "many",
		})
	}
	limits := verifier.DefaultSemanticAssessmentLimits()
	prepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(cloneSemanticAssessmentRequestForTrim(request), limits)
	require.Empty(t, validationErrors)
	oneLess := cloneSemanticAssessmentRequestForTrim(request)
	require.True(t, trimOneSemanticAssessmentCandidate(&oneLess))
	oneLessPrepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(oneLess, limits)
	require.Empty(t, validationErrors)
	require.Less(t, oneLessPrepared.CandidateContextTokens, prepared.CandidateContextTokens)

	limits.MaxCandidateContextTokens = oneLessPrepared.CandidateContextTokens
	trimmed, err := trimSemanticAssessmentCandidateContext(request, limits)
	require.NoError(t, err)
	assert.Equal(t, oneLess.PredicateOptions, trimmed.PredicateOptions)
	assert.Equal(t, prepared.PredicateOptions[:len(prepared.PredicateOptions)-1], trimmed.PredicateOptions)
}
