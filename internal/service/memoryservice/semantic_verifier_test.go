package memoryservice

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestValidateSemanticVerifierResponseRequiresWholeCoverage(t *testing.T) {
	relationships := []repository.SemanticRelationshipInput{{
		SubjectName: "Dense-Mem",
		SubjectKind: domain.SemanticEntityProject,
		Predicate:   "uses",
		ObjectName:  "Postgres",
		ObjectValue: "Postgres",
		ObjectKind:  domain.SemanticEntityConcept,
		Confidence:  0.7,
	}}
	req := buildSemanticVerifierRequest("verify-1", relationships)
	resp := semanticVerifierResponseForRequest(req)
	resp.RelationshipResults = nil

	err := validateSemanticVerifierResponse(req, resp)

	require.ErrorContains(t, err, "relationship_results coverage mismatch")
}

func TestValidateSemanticVerifierResponseRejectsUnknownFieldsByAllowlist(t *testing.T) {
	relationships := []repository.SemanticRelationshipInput{{
		SubjectName: "Dense-Mem",
		SubjectKind: domain.SemanticEntityProject,
		Predicate:   "uses",
		ObjectName:  "Postgres",
		ObjectValue: "Postgres",
		ObjectKind:  domain.SemanticEntityConcept,
		Confidence:  0.7,
	}}
	req := buildSemanticVerifierRequest("verify-1", relationships)
	resp := semanticVerifierResponseForRequest(req)
	badPredicate := "likes"
	resp.RelationshipResults[0].PredicateKey = &badPredicate

	err := validateSemanticVerifierResponse(req, resp)

	require.ErrorContains(t, err, "predicate_key is outside allowlist")
}

func TestVerifySemanticRelationshipsAppliesOnlyEntailedResolvedRelationships(t *testing.T) {
	relationships := []repository.SemanticRelationshipInput{{
		SubjectName: "Dense-Mem",
		SubjectKind: domain.SemanticEntityProject,
		Predicate:   "uses",
		ObjectValue: "Postgres",
		ObjectKind:  domain.SemanticEntityConcept,
		Tier:        domain.SemanticTierCandidate,
		Status:      domain.SemanticStatusActive,
		Confidence:  0.7,
	}}

	verified, err := verifySemanticRelationships(context.Background(), &stubSemanticVerifier{}, "verify-1", relationships)

	require.NoError(t, err)
	require.Len(t, verified, 1)
	require.Equal(t, domain.SemanticTierValidatedClaim, verified[0].Tier)
	require.Equal(t, domain.SemanticStatusActive, verified[0].Status)
	require.Equal(t, "uses", verified[0].Predicate)
	require.Equal(t, "stub-semantic-verifier", verified[0].VerifierModel)
	require.Equal(t, "entailed", verified[0].EvidenceVerdict)
	require.Equal(t, "novel", verified[0].KnowledgeAlignment)
	require.Equal(t, "evidence entails relationship", verified[0].VerificationRationale)
	require.Contains(t, verified[0].VerificationRawJSON, `"relationship_results"`)
}

func TestVerifySemanticRelationshipsEmptyInput(t *testing.T) {
	verified, err := verifySemanticRelationships(context.Background(), nil, "verify-1", nil)

	require.NoError(t, err)
	require.Nil(t, verified)
}

func TestVerifySemanticRelationshipsRequiresProviderForRelationships(t *testing.T) {
	_, err := verifySemanticRelationships(context.Background(), nil, "verify-1", semanticRelationshipsFixture())

	require.ErrorContains(t, err, "provider is required")
}

func TestVerifySemanticRelationshipsPropagatesProviderError(t *testing.T) {
	want := errors.New("provider failed")

	_, err := verifySemanticRelationships(context.Background(), &stubSemanticVerifier{err: want}, "verify-1", semanticRelationshipsFixture())

	require.ErrorIs(t, err, want)
}

func TestVerifySemanticRelationshipsWrapsInvalidVerifierResponseAsMalformed(t *testing.T) {
	relationships := semanticRelationshipsFixture()
	req := buildSemanticVerifierRequest("verify-1", relationships)
	resp := semanticVerifierResponseForRequest(req)
	resp.EntityResults = nil

	_, err := verifySemanticRelationships(context.Background(), &stubSemanticVerifier{resp: &resp}, "verify-1", relationships)

	require.ErrorIs(t, err, verifier.ErrVerifierMalformedResponse)
	require.ErrorContains(t, err, "entity_results coverage mismatch")
}

func TestBuildSemanticVerifierRequestDefaultsRequestID(t *testing.T) {
	req := buildSemanticVerifierRequest(" ", semanticRelationshipsFixture())

	require.Equal(t, "semantic-placement", req.RequestID)
	require.Equal(t, semanticVerifierSchemaVersion, req.SchemaVersion)
	require.Contains(t, req.Relationships, semanticVerifierRelationshipRef(0))
}

func TestBuildSemanticVerifierRequestIncludesBoundedContext(t *testing.T) {
	relationships := semanticRelationshipsFixture()
	ctx := repository.SemanticVerifierContext{
		EntityCandidates: map[string][]repository.SemanticEntityCandidate{
			repository.SemanticVerifierEntityCandidateKey("Dense-Mem", domain.SemanticEntityProject): {{
				EntityID:      "entity-dense-mem",
				CanonicalName: "Dense-Mem",
				Kind:          domain.SemanticEntityProject,
			}},
		},
		RelationshipCandidates: map[int][]repository.SemanticRelationshipCandidate{
			0: {{
				RelationshipID: "rel-existing",
				SubjectName:    "Dense-Mem",
				Predicate:      "uses",
				ObjectValue:    "Postgres",
				Tier:           domain.SemanticTierValidatedClaim,
				Status:         domain.SemanticStatusActive,
			}},
		},
	}

	req := buildSemanticVerifierRequestWithContext("verify-1", relationships, ctx)

	subject := req.Entities[semanticVerifierSubjectRef(0)]
	require.Contains(t, subject.Candidate, "entity-dense-mem")
	require.Equal(t, []semanticVerifierEntityCandidate{{
		EntityID:      "entity-dense-mem",
		CanonicalName: "Dense-Mem",
		Kind:          string(domain.SemanticEntityProject),
	}}, subject.Candidates)
	relationship := req.Relationships[semanticVerifierRelationshipRef(0)]
	require.Contains(t, relationship.AllowedPredicates, "uses")
	require.Equal(t, []string{"uses"}, relationship.PredicateCandidates)
	require.Contains(t, relationship.RelatedRelationship, "rel-existing")
	require.Equal(t, []string{"rel-existing"}, relationship.ExistingRelationshipIDs)
	require.Equal(t, []semanticVerifierRelationshipCandidate{{
		RelationshipID: "rel-existing",
		Subject:        "Dense-Mem",
		Predicate:      "uses",
		Object:         "Postgres",
		Tier:           string(domain.SemanticTierValidatedClaim),
		Status:         string(domain.SemanticStatusActive),
	}}, relationship.ExistingRelationships)
}

func TestValidateSemanticVerifierResponseRejectsEntityContractViolations(t *testing.T) {
	req, resp := semanticVerifierFixture()
	subjectRef := semanticVerifierSubjectRef(0)

	tests := []struct {
		name    string
		mutate  func(*semanticVerifierRequest, *semanticVerifierResponse)
		wantErr string
	}{
		{
			name: "schema version mismatch",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.SchemaVersion = "other"
			},
			wantErr: "schema_version mismatch",
		},
		{
			name: "request id mismatch",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RequestID = "other"
			},
			wantErr: "request_id mismatch",
		},
		{
			name: "unknown ref",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.EntityResults[0].Ref = "unknown"
			},
			wantErr: "unknown entity result ref",
		},
		{
			name: "duplicate ref",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.EntityResults = append(resp.EntityResults, resp.EntityResults[0])
			},
			wantErr: "duplicate entity result ref",
		},
		{
			name: "confidence out of range",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.EntityResults[0].Confidence = 1.1
			},
			wantErr: "confidence out of range",
		},
		{
			name: "missing rationale",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.EntityResults[0].Rationale = " "
			},
			wantErr: "rationale is required",
		},
		{
			name: "oversized rationale",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.EntityResults[0].Rationale = strings.Repeat("x", 1001)
			},
			wantErr: "rationale is required",
		},
		{
			name: "reuse without candidate",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.EntityResults[0].Action = "reuse"
			},
			wantErr: "reuse requires candidate allowlist",
		},
		{
			name: "reuse with candidate and empty allowlist",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				candidate := "entity-1"
				resp.EntityResults[0].Action = "reuse"
				resp.EntityResults[0].CandidateEntityID = &candidate
			},
			wantErr: "reuse requires candidate allowlist",
		},
		{
			name: "reuse outside allowlist",
			mutate: func(req *semanticVerifierRequest, resp *semanticVerifierResponse) {
				candidate := "entity-1"
				req.Entities[subjectRef].Candidate["entity-allowed"] = struct{}{}
				resp.EntityResults[0].Action = "reuse"
				resp.EntityResults[0].CandidateEntityID = &candidate
			},
			wantErr: "candidate_entity_id is outside allowlist",
		},
		{
			name: "create with candidate",
			mutate: func(req *semanticVerifierRequest, resp *semanticVerifierResponse) {
				candidate := "entity-1"
				req.Entities[subjectRef].Candidate[candidate] = struct{}{}
				resp.EntityResults[0].CandidateEntityID = &candidate
			},
			wantErr: "requires null candidate_entity_id",
		},
		{
			name: "invalid action",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.EntityResults[0].Action = "merge"
			},
			wantErr: "action is invalid",
		},
		{
			name: "coverage mismatch",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.EntityResults = nil
			},
			wantErr: "entity_results coverage mismatch",
		},
		{
			name: "valid reuse with allowlisted candidate",
			mutate: func(req *semanticVerifierRequest, resp *semanticVerifierResponse) {
				candidate := "entity-1"
				req.Entities[subjectRef].Candidate[candidate] = struct{}{}
				resp.EntityResults[0].Action = "reuse"
				resp.EntityResults[0].CandidateEntityID = &candidate
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqCopy := cloneSemanticVerifierRequest(req)
			respCopy := cloneSemanticVerifierResponse(resp)
			tt.mutate(&reqCopy, &respCopy)

			err := validateSemanticVerifierResponse(reqCopy, respCopy)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateSemanticVerifierResponseRejectsRelationshipContractViolations(t *testing.T) {
	req, resp := semanticVerifierFixture()
	relationshipRef := semanticVerifierRelationshipRef(0)

	tests := []struct {
		name    string
		mutate  func(*semanticVerifierRequest, *semanticVerifierResponse)
		wantErr string
	}{
		{
			name: "unknown ref",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].Ref = "unknown"
			},
			wantErr: "unknown relationship result ref",
		},
		{
			name: "duplicate ref",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults = append(resp.RelationshipResults, resp.RelationshipResults[0])
			},
			wantErr: "duplicate relationship result ref",
		},
		{
			name: "confidence out of range",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].Confidence = -0.01
			},
			wantErr: "confidence out of range",
		},
		{
			name: "missing rationale",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].Rationale = ""
			},
			wantErr: "rationale is required",
		},
		{
			name: "invalid evidence verdict",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].EvidenceVerdict = "maybe"
			},
			wantErr: "evidence_verdict is invalid",
		},
		{
			name: "invalid knowledge alignment",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].KnowledgeAlignment = "same"
			},
			wantErr: "knowledge_alignment is invalid",
		},
		{
			name: "resolved without predicate",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].PredicateKey = nil
			},
			wantErr: "resolved predicate requires predicate_key",
		},
		{
			name: "needs review with predicate",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].PredicateStatus = "needs_review"
			},
			wantErr: "needs_review requires null predicate_key",
		},
		{
			name: "invalid predicate status",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].PredicateStatus = "accepted"
			},
			wantErr: "predicate_status is invalid",
		},
		{
			name: "too many related relationships",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].RelatedRelationshipIDs = make([]string, 65)
				for i := range resp.RelationshipResults[0].RelatedRelationshipIDs {
					resp.RelationshipResults[0].RelatedRelationshipIDs[i] = "rel"
				}
			},
			wantErr: "related_relationship_ids exceeds limit",
		},
		{
			name: "empty related id",
			mutate: func(req *semanticVerifierRequest, resp *semanticVerifierResponse) {
				req.Relationships[relationshipRef].RelatedRelationship["rel-existing"] = struct{}{}
				resp.RelationshipResults[0].RelatedRelationshipIDs = []string{" "}
			},
			wantErr: "contains empty id",
		},
		{
			name: "duplicate related id",
			mutate: func(req *semanticVerifierRequest, resp *semanticVerifierResponse) {
				req.Relationships[relationshipRef].RelatedRelationship["rel-existing"] = struct{}{}
				resp.RelationshipResults[0].RelatedRelationshipIDs = []string{"rel-existing", "rel-existing"}
			},
			wantErr: "contains duplicate id",
		},
		{
			name: "related outside allowlist",
			mutate: func(req *semanticVerifierRequest, resp *semanticVerifierResponse) {
				req.Relationships[relationshipRef].RelatedRelationship["rel-existing"] = struct{}{}
				resp.RelationshipResults[0].RelatedRelationshipIDs = []string{"rel-outside"}
			},
			wantErr: "related_relationship_id is outside allowlist",
		},
		{
			name: "related id with empty allowlist",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].RelatedRelationshipIDs = []string{"rel-outside"}
			},
			wantErr: "related_relationship_id is outside allowlist",
		},
		{
			name: "coverage mismatch",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults = nil
			},
			wantErr: "relationship_results coverage mismatch",
		},
		{
			name: "valid needs review without predicate",
			mutate: func(_ *semanticVerifierRequest, resp *semanticVerifierResponse) {
				resp.RelationshipResults[0].PredicateStatus = "needs_review"
				resp.RelationshipResults[0].PredicateKey = nil
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqCopy := cloneSemanticVerifierRequest(req)
			respCopy := cloneSemanticVerifierResponse(resp)
			tt.mutate(&reqCopy, &respCopy)

			err := validateSemanticVerifierResponse(reqCopy, respCopy)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestApplySemanticVerifierResponsePreservesNonActiveOutcomes(t *testing.T) {
	req, resp := semanticVerifierFixture()

	resp.EntityResults[0].Action = "ambiguous"
	out := applySemanticVerifierResponse(req, resp, semanticRelationshipsFixture(), "stub-model")
	require.Len(t, out, 1)
	require.True(t, out[0].ObservationOnly)
	require.Equal(t, domain.SemanticStatusNeedsReview, out[0].Status)

	req, resp = semanticVerifierFixture()
	resp.RelationshipResults[0].EvidenceVerdict = "contradicted"
	out = applySemanticVerifierResponse(req, resp, semanticRelationshipsFixture(), "stub-model")
	require.Len(t, out, 1)
	require.False(t, out[0].ObservationOnly)
	require.Equal(t, domain.SemanticTierCandidate, out[0].Tier)
	require.Equal(t, domain.SemanticStatusRejected, out[0].Status)

	req, resp = semanticVerifierFixture()
	resp.RelationshipResults[0].EvidenceVerdict = "insufficient"
	out = applySemanticVerifierResponse(req, resp, semanticRelationshipsFixture(), "stub-model")
	require.Len(t, out, 1)
	require.Equal(t, domain.SemanticTierCandidate, out[0].Tier)
	require.Equal(t, domain.SemanticStatusPendingEvidence, out[0].Status)

	req, resp = semanticVerifierFixture()
	resp.RelationshipResults[0].PredicateStatus = "needs_review"
	resp.RelationshipResults[0].PredicateKey = nil
	out = applySemanticVerifierResponse(req, resp, semanticRelationshipsFixture(), "stub-model")
	require.Len(t, out, 1)
	require.True(t, out[0].ObservationOnly)
	require.Equal(t, domain.SemanticStatusNeedsReview, out[0].Status)
}

func TestSemanticVerifierStatementPreservesNegativePolarity(t *testing.T) {
	relationship := semanticRelationshipsFixture()[0]
	relationship.Polarity = domain.PolarityMinus

	require.Equal(t, "It is explicitly false that Dense-Mem uses Postgres", semanticVerifierStatement(relationship))
}

func semanticVerifierFixture() (semanticVerifierRequest, semanticVerifierResponse) {
	relationships := semanticRelationshipsFixture()
	req := buildSemanticVerifierRequest("verify-1", relationships)
	resp := semanticVerifierResponseForRequest(req)
	return req, resp
}

type stubSemanticVerifier struct {
	req  semanticVerifierRequest
	resp *semanticVerifierResponse
	err  error
}

func (s *stubSemanticVerifier) VerifySemantic(_ context.Context, req semanticVerifierRequest) (semanticVerifierResponse, error) {
	s.req = req
	if s.err != nil {
		return semanticVerifierResponse{}, s.err
	}
	if s.resp != nil {
		return cloneSemanticVerifierResponse(*s.resp), nil
	}
	return semanticVerifierResponseForRequest(req), nil
}

func (s *stubSemanticVerifier) ModelName() string {
	return "stub-semantic-verifier"
}

func semanticVerifierResponseForRequest(req semanticVerifierRequest) semanticVerifierResponse {
	resp := semanticVerifierResponse{
		SchemaVersion:       req.SchemaVersion,
		RequestID:           req.RequestID,
		EntityResults:       make([]semanticVerifierEntityResult, 0, len(req.Entities)),
		RelationshipResults: make([]semanticVerifierRelationshipResult, 0, len(req.Relationships)),
	}
	entityRefs := make([]string, 0, len(req.Entities))
	for ref := range req.Entities {
		entityRefs = append(entityRefs, ref)
	}
	sort.Strings(entityRefs)
	for _, ref := range entityRefs {
		resp.EntityResults = append(resp.EntityResults, semanticVerifierEntityResult{
			Ref:        ref,
			Action:     "create",
			Confidence: 0.75,
			Rationale:  "create entity",
		})
	}
	relationshipRefs := make([]string, 0, len(req.Relationships))
	for ref := range req.Relationships {
		relationshipRefs = append(relationshipRefs, ref)
	}
	sort.Strings(relationshipRefs)
	for _, ref := range relationshipRefs {
		relationship := req.Relationships[ref]
		predicates := make([]string, 0, len(relationship.AllowedPredicates))
		for predicate := range relationship.AllowedPredicates {
			predicates = append(predicates, predicate)
		}
		if len(predicates) == 0 {
			predicates = append(predicates, relationship.PredicateCandidates...)
		}
		sort.Strings(predicates)
		if len(predicates) == 0 {
			predicates = []string{"needs_review"}
		}
		predicate := predicates[0]
		resp.RelationshipResults = append(resp.RelationshipResults, semanticVerifierRelationshipResult{
			Ref:                    ref,
			PredicateStatus:        "resolved",
			PredicateKey:           &predicate,
			EvidenceVerdict:        "entailed",
			KnowledgeAlignment:     "novel",
			RelatedRelationshipIDs: []string{},
			Confidence:             0.7,
			Rationale:              "evidence entails relationship",
		})
	}
	return resp
}

func semanticRelationshipsFixture() []repository.SemanticRelationshipInput {
	return []repository.SemanticRelationshipInput{{
		SubjectName: "Dense-Mem",
		SubjectKind: domain.SemanticEntityProject,
		Predicate:   "uses",
		Polarity:    domain.PolarityPlus,
		ObjectValue: "Postgres",
		ObjectKind:  domain.SemanticEntityConcept,
		Confidence:  0.7,
	}}
}

func cloneSemanticVerifierRequest(req semanticVerifierRequest) semanticVerifierRequest {
	out := semanticVerifierRequest{
		SchemaVersion: req.SchemaVersion,
		RequestID:     req.RequestID,
		Entities:      map[string]semanticVerifierEntityRequest{},
		Relationships: map[string]semanticVerifierRelationshipRequest{},
	}
	for key, entity := range req.Entities {
		entity.Candidate = cloneStringSet(entity.Candidate)
		entity.Candidates = append([]semanticVerifierEntityCandidate(nil), entity.Candidates...)
		out.Entities[key] = entity
	}
	for key, relationship := range req.Relationships {
		relationship.AllowedPredicates = cloneStringSet(relationship.AllowedPredicates)
		relationship.RelatedRelationship = cloneStringSet(relationship.RelatedRelationship)
		relationship.PredicateCandidates = append([]string(nil), relationship.PredicateCandidates...)
		relationship.ExistingRelationshipIDs = append([]string(nil), relationship.ExistingRelationshipIDs...)
		relationship.ExistingRelationships = append([]semanticVerifierRelationshipCandidate(nil), relationship.ExistingRelationships...)
		out.Relationships[key] = relationship
	}
	return out
}

func cloneSemanticVerifierResponse(resp semanticVerifierResponse) semanticVerifierResponse {
	out := resp
	out.EntityResults = append([]semanticVerifierEntityResult(nil), resp.EntityResults...)
	out.RelationshipResults = append([]semanticVerifierRelationshipResult(nil), resp.RelationshipResults...)
	for i := range out.RelationshipResults {
		out.RelationshipResults[i].RelatedRelationshipIDs = append([]string(nil), out.RelationshipResults[i].RelatedRelationshipIDs...)
	}
	return out
}

func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}
