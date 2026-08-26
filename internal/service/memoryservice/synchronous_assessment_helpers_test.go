package memoryservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestSubmissionAssessmentPlanSupportsTypedValuesAndLifecycleFences(t *testing.T) {
	input := synchronousAssessmentInput(t)
	relationship := input.Snapshot.Proposal["relationship_hints"].([]any)[0].(map[string]any)
	relationship["object"] = map[string]any{"value": map[string]any{
		"type": "number", "value": float64(42), "display": "42 items", "unit": "items",
	}}
	relationship["valid_from"] = "2026-08-01T00:00:00Z"
	relationship["valid_to"] = "2026-08-31T00:00:00Z"
	relationship["correction_target"] = map[string]any{"relationship_id": uuid.NewString(), "expected_version": 3}
	relationship["conflict_context"] = map[string]any{"conflict_id": uuid.NewString(), "expected_version": 5}

	plan, err := buildSubmissionAssessmentPlan(input.Snapshot)
	require.NoError(t, err)
	require.Len(t, plan.RelationshipTargets, 1)
	target := plan.RelationshipTargets[0]
	require.NotNil(t, target.Target.ObjectValue)
	assert.Equal(t, "number", target.Target.ObjectValue.ValueType)
	assert.Equal(t, "42", target.Target.ObjectValue.CanonicalValue)
	assert.Equal(t, "items", *target.Target.ObjectValue.Unit)
	assert.NotNil(t, target.CorrectionTarget)
	assert.Equal(t, 3, target.CorrectionTarget.ExpectedVersion)
	assert.NotNil(t, target.ConflictContext)
	assert.Equal(t, 5, target.ConflictContext.ExpectedVersion)
	assert.Equal(t, "2026-08-01T00:00:00Z", *target.Target.ValidFrom)
	assert.Equal(t, "2026-08-31T00:00:00Z", *target.Target.ValidTo)
}

func TestSubmissionAssessmentSharedHelpers(t *testing.T) {
	proposal := map[string]any{
		"relationship_hints": []any{map[string]any{
			"ref": "r:one", "correction_target": map[string]any{"relationship_id": "old"},
			"conflict_context": map[string]any{"conflict_id": "conflict"},
		}},
	}
	clean := assessmentClientProposalWithoutTrustedContext(proposal)
	assert.NotContains(t, clean["relationship_hints"].([]any)[0].(map[string]any), "correction_target")
	assert.NotContains(t, clean["relationship_hints"].([]any)[0].(map[string]any), "conflict_context")
	assert.Contains(t, proposal["relationship_hints"].([]any)[0].(map[string]any), "correction_target")

	assert.Equal(t, "value", proposalString(map[string]any{"value": " value "}, "value"))
	assert.Equal(t, 2, mustProposalInt(t, map[string]any{"value": int64(2)}, "value"))
	assert.Equal(t, 3, mustProposalInt(t, map[string]any{"value": float64(3)}, "value"))
	_, ok := proposalInt(map[string]any{"value": 1.5}, "value")
	assert.False(t, ok)

	validTime, err := proposalOptionalTime(map[string]any{"at": "2026-08-01T01:02:03Z"}, "at")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC), *validTime)
	for _, value := range []any{time.Date(2026, 8, 1, 0, 0, 0, 0, time.FixedZone("x", 3600)), &time.Time{}} {
		parsed, err := proposalOptionalTime(map[string]any{"at": value}, "at")
		require.NoError(t, err)
		require.NotNil(t, parsed)
		assert.Equal(t, time.UTC, parsed.Location())
	}
	_, err = proposalOptionalTime(map[string]any{"at": "not-a-time"}, "at")
	assert.Error(t, err)
	_, err = proposalOptionalTime(map[string]any{"at": true}, "at")
	assert.Error(t, err)

	assert.Equal(t, "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", semanticAssessmentHash([]byte("test")))
	assert.Equal(t, "o200k_base", assessmentTokenizer(assessor.SemanticAssessmentLimits{}))
	assert.Equal(t, "cl100k_base", assessmentTokenizer(assessor.SemanticAssessmentLimits{Tokenizer: " cl100k_base "}))
	assert.Equal(t, "response_contract", assessmentValidationStage(""))
	assert.Equal(t, "custom", assessmentValidationStage("custom"))

	groups := assessmentGroupsBySpan([]assessor.SemanticAssessmentEntityCandidateGroup{{EvidenceID: "evidence:0", Start: 1, End: 3}})
	assert.Len(t, groups, 1)
	evidence := semanticAssessmentEvidence(repository.EvidenceFragment{FragmentID: "fragment", EvidenceIndex: 0, Content: "text", Authority: "primary"}, "evidence:0")
	assert.Equal(t, "fragment", evidence.FragmentID)
	assert.Equal(t, "primary", evidence.Authority)

	assert.Equal(t, "malformed_response", firstMalformedFailure(nil))
	malformed := &assessor.MalformedResponseError{FailureClass: "malformed_exhausted", Attempts: 3}
	class, attempts := semanticAssessmentMalformedFailure(malformed)
	assert.Equal(t, "malformed_exhausted", class)
	assert.Equal(t, 3, attempts)

	original := errors.New("original")
	assert.NoError(t, terminalizeAfterError(original, func() error { return nil }))
	assert.Error(t, terminalizeAfterError(original, func() error { return errors.New("completion") }))
}

func mustProposalInt(t *testing.T, fields map[string]any, key string) int {
	t.Helper()
	value, ok := proposalInt(fields, key)
	require.True(t, ok)
	return value
}

func firstMalformedFailure(err error) string {
	class, _ := semanticAssessmentMalformedFailure(err)
	return class
}

func TestSubmissionAssessmentPredicateOptionsAndPreflightBranches(t *testing.T) {
	input := synchronousAssessmentInput(t)
	plan, err := buildSubmissionAssessmentPlan(input.Snapshot)
	require.NoError(t, err)
	candidate := repository.SemanticReviewPredicateCandidate{
		PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"project"},
		AllowedObjectKinds: []string{"product"}, RelationshipKind: "state", CurrentCardinality: "many", LifecycleState: "active",
	}
	catalog := &submissionAssessmentWorkerCatalogStub{predicateComplete: true, predicateOptions: []repository.SemanticReviewPredicateCandidate{candidate}}
	engine := newAssessmentEngine(SynchronousAssessmentDependencies{Catalog: catalog}, input.Scope.TeamID, input.Scope.OwnerProfileID)
	options, err := engine.submissionAssessmentPredicateOptions(context.Background(), input.Scope, plan)
	require.NoError(t, err)
	require.Len(t, options, 1)
	assert.Equal(t, "uses", options[0].PredicateKey)

	overflowCatalog := &submissionAssessmentWorkerCatalogStub{}
	_, err = newAssessmentEngine(SynchronousAssessmentDependencies{Catalog: overflowCatalog}, input.Scope.TeamID, input.Scope.OwnerProfileID).submissionAssessmentPredicateOptions(context.Background(), input.Scope, plan)
	var preflight *semanticAssessmentPreflightError
	require.ErrorAs(t, err, &preflight)
	assert.Equal(t, "predicate_options_overflow", preflight.stage)

	entityIncomplete := &submissionAssessmentWorkerCatalogStub{entityComplete: false}
	_, err = newAssessmentEngine(SynchronousAssessmentDependencies{Catalog: entityIncomplete}, input.Scope.TeamID, input.Scope.OwnerProfileID).buildRequest(context.Background(), input.Scope, plan, input.Snapshot.Proposal)
	require.ErrorAs(t, err, &preflight)
	assert.Equal(t, "entity_catalog", preflight.stage)

	entityFailure := &submissionAssessmentWorkerCatalogStub{entityErr: errors.New("catalog unavailable")}
	_, err = newAssessmentEngine(SynchronousAssessmentDependencies{Catalog: entityFailure}, input.Scope.TeamID, input.Scope.OwnerProfileID).buildRequest(context.Background(), input.Scope, plan, input.Snapshot.Proposal)
	assert.ErrorContains(t, err, "load submission entity catalog")
}

func TestSubmissionAssessmentDeterministicQuarantineConvertsEvidenceAndProposalSignals(t *testing.T) {
	input := synchronousAssessmentInput(t)
	plan, err := buildSubmissionAssessmentPlan(input.Snapshot)
	require.NoError(t, err)
	scan := SubmissionSecurityBatchScan{
		Items:         []SubmissionSecurityScan{{}},
		EvidenceCount: 1,
		Signals: []SubmissionSecurityBatchSignal{{
			EvidenceIndex: 0, Source: submissionSecuritySourceEvidence,
			SubmissionSecuritySignal: SubmissionSecuritySignal{Kind: "prompt_injection", RuleID: "rule", Severity: "high", Start: 0, End: 5},
		}, {
			EvidenceIndex: -1, Source: submissionSecuritySourceProposal,
			SubmissionSecuritySignal: SubmissionSecuritySignal{Kind: "exfiltration", RuleID: "rule", Severity: "critical", Start: 0, End: 1},
		}},
	}
	quarantines, err := submissionAssessmentDeterministicQuarantines(plan, scan)
	require.NoError(t, err)
	require.Len(t, quarantines, 1)
	assert.Equal(t, input.Snapshot.Evidence[0].FragmentID, quarantines[0].FragmentID)
	require.Len(t, quarantines[0].Signals, 2)
	assert.Equal(t, "Dense", quarantines[0].Signals[0].Quote)
	assert.Equal(t, "submission", quarantines[0].Signals[1].Metadata["scope"])

	_, err = submissionAssessmentDeterministicQuarantines(submissionAssessmentPlan{}, scan)
	assert.Error(t, err)
	scan.Signals[0].EvidenceIndex = 99
	_, err = submissionAssessmentDeterministicQuarantines(plan, scan)
	assert.Error(t, err)
	scan.Signals = nil
	quarantines, err = submissionAssessmentDeterministicQuarantines(plan, scan)
	require.NoError(t, err)
	require.Len(t, quarantines, 1)
}

func TestSubmissionAssessmentStatusAndStaleInputHelpers(t *testing.T) {
	assert.Contains(t, SubmissionErrorCodes(), string(SubmissionErrorEmbeddingUnavailable))
	assert.Contains(t, SubmissionNextActions(), string(SubmissionNextActionRetrySameRequest))
	assert.Equal(t, string(SubmissionErrorNoSupportedMemory), submissionStatusErrorForCode("", "rejected").Code)
	assert.Equal(t, string(SubmissionErrorInternalFailure), submissionStatusErrorForCode("unknown", "failed").Code)
	assert.Equal(t, string(SubmissionErrorPolicyRejected), correctionStatusErrorForCode("", "rejected").Code)
	assert.Equal(t, SubmissionErrorInputBudgetExceeded, submissionFailureCode("assessment_input", ""))
	assert.Equal(t, SubmissionErrorProviderUnavailable, submissionFailureCode("assessment", "provider_unavailable"))
	assert.Equal(t, SubmissionErrorDatabaseFailure, submissionFailureCode("database", ""))
	assert.Equal(t, SubmissionErrorConfigurationInvalid, submissionFailureCode("configuration", ""))
	assert.Equal(t, SubmissionErrorInternalFailure, submissionFailureCode("other", "other"))
	assert.True(t, IsRememberStaleInputError(errSubmissionAssessmentStaleInput))
	assert.False(t, IsRememberStaleInputError(errors.New("unrelated")))
	assert.Equal(t, "date", relationshipObjectKind(assessor.SemanticAssessmentRelationshipSplit{ObjectValue: &assessor.SemanticAssessmentValue{ValueType: "date"}}, map[string]string{}, "relationship_ref"))
	assert.Equal(t, "project", relationshipObjectKind(assessor.SemanticAssessmentRelationshipSplit{ObjectRef: stringPointer("entity:object")}, map[string]string{"entity:object": "project"}, "other"))
	assert.Equal(t, "date", relationshipObjectKind(assessor.SemanticAssessmentRelationshipSplit{ObjectValue: &assessor.SemanticAssessmentValue{ValueType: "date"}}, map[string]string{}, "other"))
	assert.Equal(t, "other", relationshipObjectKind(assessor.SemanticAssessmentRelationshipSplit{}, map[string]string{}, "other"))
	assert.Equal(t, "r#split:1", submissionAssessmentObservationRef("r", 1, 2))
	assert.Equal(t, "r", submissionAssessmentObservationRef("r", 0, 1))

	var completionCalled bool
	assert.NoError(t, terminalizeAfterError(nil, func() error { completionCalled = true; return nil }))
	assert.True(t, completionCalled)
}

func TestSubmissionSecurityAdaptersAndAudit(t *testing.T) {
	if _, err := ScanSubmissionEvidence("safe evidence"); err != nil {
		t.Fatalf("safe evidence scan failed: %v", err)
	}
	batch, err := ScanSubmissionBatch([]string{"safe evidence"})
	require.NoError(t, err)
	assert.Equal(t, 1, batch.EvidenceCount)
	_, err = scanSubmissionWithProviderProposal([]string{"safe evidence"}, map[string]any{"hint": "safe"})
	require.NoError(t, err)
	pass := submissionSecurityPassEvent()
	assert.Equal(t, "deterministic_scan", pass.EventKind)
	quarantine := submissionSecurityBatchQuarantineEvent(SubmissionSecurityBatchScan{
		Signals: []SubmissionSecurityBatchSignal{{Source: submissionSecuritySourceEvidence, SubmissionSecuritySignal: SubmissionSecuritySignal{Kind: "prompt_injection", Severity: "high", Start: 0, End: 1}}},
	})
	assert.Equal(t, "quarantine", quarantine.Decision)

	auditor := &memorySecurityAuditStub{}
	actor := requestctx.Actor{TeamID: uuid.New(), OwnerID: uuid.New(), Role: "member"}
	scan := SubmissionSecurityBatchScan{EvidenceCount: 1, Signals: []SubmissionSecurityBatchSignal{{EvidenceIndex: 0, Source: submissionSecuritySourceEvidence, SubmissionSecuritySignal: SubmissionSecuritySignal{Kind: "prompt_injection", RuleID: "rule", Severity: "high", Start: 0, End: 1}}}}
	require.ErrorIs(t, recordSubmissionSecurityRejection(context.Background(), nil, actor, "remember", scan, ErrEvidenceSecurityRejected), ErrSecurityAuditPersistence)
	require.NoError(t, recordSubmissionSecurityRejection(context.Background(), auditor, actor, "remember", scan, ErrEvidenceSecurityRejected))
	require.Len(t, auditor.inputs, 1)
	assert.Equal(t, SubmissionSecurityErrorRejected, auditor.inputs[0].ReasonCode)
	require.NoError(t, recordSubmissionSecurityRejection(context.Background(), auditor, actor, "remember", scan, errors.New("wrapped")))
}

type memorySecurityAuditStub struct {
	inputs []SecurityRejectionAuditInput
}

func (s *memorySecurityAuditStub) RecordSecurityRejection(_ context.Context, input SecurityRejectionAuditInput) error {
	s.inputs = append(s.inputs, input)
	return nil
}

func TestSubmissionAssessmentErrorWrappers(t *testing.T) {
	cause := errors.New("cause")
	consumed := &submissionAssessmentConsumedTurnsError{cause: cause, providerTurns: 2}
	assert.EqualError(t, consumed, "cause")
	assert.ErrorIs(t, consumed, cause)
	assert.Equal(t, 2, submissionAssessmentConsumedProviderTurns(consumed))
	assert.Equal(t, 0, submissionAssessmentConsumedProviderTurns(errors.New("other")))
	assert.EqualError(t, (&submissionAssessmentConsumedTurnsError{}), "submission assessment session failed after consuming provider turns")
	assert.Equal(t, 0, submissionAssessmentConsumedProviderTurns(nil))

	assert.NotNil(t, newSynchronousAssessmentEngine(SynchronousAssessmentDependencies{}, "team", "owner"))
	assert.EqualError(t, (&NoSupportedMemoryError{}), "submission assessment contains no supported memory")
	assert.Error(t, (&NoSupportedMemoryError{}))

	candidate := &assessor.SemanticAssessmentEntityCandidateGroup{Candidates: []assessor.SemanticAssessmentEntityCandidate{{Kind: "project"}}}
	assert.True(t, assessmentCompatibleCandidateExists(candidate, "project"))
	assert.False(t, assessmentCompatibleCandidateExists(candidate, "product"))
	assert.False(t, assessmentCompatibleCandidateExists(nil, "project"))

	assert.Equal(t, "primary", mustSupportAuthority(t, "primary"))
	assert.Equal(t, "primary", mustSupportAuthority(t, ""))
	_, err := semanticSupportAuthority("unsupported")
	assert.Error(t, err)
}

func TestSubmissionAssessmentCommitHelperConversions(t *testing.T) {
	valueDisplay, valueUnit := "42 items", "items"
	objectRef := "entity:object"
	objectValue := assessor.SemanticAssessmentValue{ValueType: "number", CanonicalValue: "42", Display: &valueDisplay, Unit: &valueUnit}
	object, _, err := semanticAssessmentObject("r:uses", assessor.SemanticAssessmentRelationshipSplit{ObjectRef: &objectRef})
	require.NoError(t, err)
	assert.Equal(t, objectRef, object)
	_, converted, err := semanticAssessmentObject("r:uses", assessor.SemanticAssessmentRelationshipSplit{ObjectValue: &objectValue})
	require.NoError(t, err)
	require.NotNil(t, converted)
	assert.Equal(t, "42", converted.CanonicalValue)
	assert.Equal(t, "42 items", converted.Display)
	_, _, err = semanticAssessmentObject("r:uses", assessor.SemanticAssessmentRelationshipSplit{})
	assert.Error(t, err)

	from, to := "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"
	start, end, err := semanticAssessmentValidity(assessor.SemanticAssessmentRelationshipSplit{ValidFrom: &from, ValidTo: &to})
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), *start)
	assert.Equal(t, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), *end)
	_, _, err = semanticAssessmentValidity(assessor.SemanticAssessmentRelationshipSplit{ValidFrom: stringPointer("bad")})
	assert.Error(t, err)
	primary, additional := semanticAssessmentPrimarySupport([]repository.EvidenceSupportInput{{FragmentID: "one"}, {FragmentID: "two"}})
	require.NotNil(t, primary)
	assert.Equal(t, "one", primary.FragmentID)
	assert.Len(t, additional, 1)
	primary, additional = semanticAssessmentPrimarySupport(nil)
	assert.Nil(t, primary)
	assert.Empty(t, additional)

	plan := submissionAssessmentPlan{Items: []submissionAssessmentItem{{Fragment: repository.EvidenceFragment{FragmentID: "fragment", Content: "supported", Authority: "primary"}, EvidenceID: "evidence:0"}}, itemsByEvidenceID: map[string]submissionAssessmentItem{}}
	plan.itemsByEvidenceID["evidence:0"] = plan.Items[0]
	supports, err := submissionAssessmentSupports(plan, "assessment", []assessor.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 0, End: 9}})
	require.NoError(t, err)
	require.Len(t, supports, 1)
	assert.Equal(t, "supported", supports[0].Quote)
	_, err = submissionAssessmentSupports(plan, "assessment", nil)
	assert.Error(t, err)
	plan.Items[0].Fragment.Authority = "bad"
	plan.itemsByEvidenceID["evidence:0"] = plan.Items[0]
	_, err = submissionAssessmentSupports(plan, "assessment", []assessor.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 0, End: 9}})
	assert.Error(t, err)
	assert.True(t, unsupportedEntityResult(assessor.SemanticAssessmentRelationshipSplit{SubjectRef: "entity:bad"}, map[string]struct{}{"entity:bad": {}}))
	assert.False(t, unsupportedEntityResult(assessor.SemanticAssessmentRelationshipSplit{SubjectRef: "entity:ok"}, map[string]struct{}{}))
}

func TestBuildSynchronousRememberCommitInputRejectsMalformedAssessmentResponses(t *testing.T) {
	prepared, input := synchronousStoredPreparedAssessment(t)
	for _, mutate := range []func(*SynchronousAssessmentResult){
		func(result *SynchronousAssessmentResult) { result.Response.EntityResults[0].Ref = "entity:unknown" },
		func(result *SynchronousAssessmentResult) { result.Response.RelationshipResults[0].Splits = nil },
		func(result *SynchronousAssessmentResult) {
			result.Response.RelationshipResults[0].Ref = "relationship:unknown"
		},
		func(result *SynchronousAssessmentResult) {
			result.Response.RelationshipResults[0].Splits[0].Evidence = nil
		},
		func(result *SynchronousAssessmentResult) {
			result.Response.RelationshipResults[0].Splits[0].PredicateKey = nil
			result.Response.RelationshipResults[0].Splits[0].PredicateVersion = nil
		},
		func(result *SynchronousAssessmentResult) {
			result.Response.RelationshipResults[0].Splits[0].ObjectRef = nil
			result.Response.RelationshipResults[0].Splits[0].ObjectValue = nil
		},
	} {
		copyResult := *prepared
		copyResult.Response = prepared.Response
		copyResult.Response.EntityResults = append([]assessor.SemanticAssessmentEntityResult(nil), prepared.Response.EntityResults...)
		copyResult.Response.RelationshipResults = append([]assessor.SemanticAssessmentRelationshipResult(nil), prepared.Response.RelationshipResults...)
		copyResult.Response.RelationshipResults[0].Splits = append([]assessor.SemanticAssessmentRelationshipSplit(nil), prepared.Response.RelationshipResults[0].Splits...)
		mutate(&copyResult)
		_, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{TeamID: input.Scope.TeamID, OwnerProfileID: input.Scope.OwnerProfileID, IngestID: input.Scope.IngestID, Proposal: input.Snapshot.Proposal, Assessment: &copyResult})
		assert.Error(t, err)
	}
	_, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{})
	assert.Error(t, err)
}

func TestRememberInputHelpersPreserveSourceRevisionAndAuditMetadata(t *testing.T) {
	evidence := []RememberEvidenceInput{
		{Content: "first", Source: "wiki-ref", SourceKey: "wiki", SourceRevision: "v1", Authority: "primary", SourceGroup: "docs", SupersedesEvidenceIDs: []string{"old"}, Metadata: map[string]any{"tag": "a"}},
		{Content: "second", SourceKey: "wiki", SourceRevision: "v1", Authority: "secondary"},
		{Content: "no revision"},
	}
	hashes := sourceRevisionContentHashes(evidence)
	require.Len(t, hashes, 1)
	assert.Equal(t, hashes[sourceRevisionBatchKey(evidence[0])], hashes[sourceRevisionBatchKey(evidence[1])])
	assert.Empty(t, sourceRevisionBatchKey(evidence[2]))
	assert.Equal(t, "conversation", evidenceSourceType(""))
	assert.Equal(t, "document", evidenceSourceType(" document "))
	authority, metadata := ledgerAuthorityAndMetadata("", map[string]any{"tag": "a"})
	assert.Equal(t, string(domain.AuthorityPrimary), authority)
	assert.Equal(t, "a", metadata["tag"])
	authority, metadata = ledgerAuthorityAndMetadata("secondary", nil)
	assert.Equal(t, "secondary", authority)
	assert.Equal(t, "secondary", metadata["contract_authority"])
	intent := evidenceProcessingIntentMetadata(map[string]any{}, evidence[0])
	assert.Equal(t, []string{"old"}, intent["supersedes_evidence_ids"])
	assert.Equal(t, "docs", intent["contract_source_group"])
	envelope := sourceRevisionEnvelope(evidence[0])
	assert.Equal(t, "wiki-ref", envelope["source"])

	assert.Len(t, rememberArrayValues([]map[string]any{{"a": 1}}), 1)
	assert.Len(t, rememberArrayValues([]string{"a"}), 1)
	assert.Nil(t, rememberArrayValues(42))
	for _, value := range []any{int(1), int8(2), int16(3), int32(4), int64(5), uint(6), uint8(7), uint16(8), uint32(9), uint64(10), float64(11), float32(12), "13"} {
		_, ok := rememberEvidenceIndex(value)
		assert.True(t, ok, value)
	}
	for _, value := range []any{1.5, float32(2.5), "bad", struct{}{}} {
		_, ok := rememberEvidenceIndex(value)
		assert.False(t, ok, value)
	}
}

func mustSupportAuthority(t *testing.T, value string) string {
	t.Helper()
	result, err := semanticSupportAuthority(value)
	require.NoError(t, err)
	return result
}
