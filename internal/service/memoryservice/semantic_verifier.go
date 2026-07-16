package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type semanticVerifierRequest struct {
	RequestID     string                                         `json:"request_id"`
	Evidence      []semanticVerifierEvidenceRequest              `json:"evidence"`
	Entities      map[string]semanticVerifierEntityRequest       `json:"entities"`
	Relationships map[string]semanticVerifierRelationshipRequest `json:"relationships"`
	evidenceByID  map[string]domain.MemoryEvidence               `json:"-"`
}

type semanticVerifierEvidenceRequest struct {
	EvidenceID string `json:"evidence_id"`
	Content    string `json:"content"`
}

type semanticVerifierEntityRequest struct {
	Ref        string                            `json:"ref"`
	Candidate  map[string]struct{}               `json:"-"`
	SourceName string                            `json:"source_name"`
	Candidates []semanticVerifierEntityCandidate `json:"candidates"`
}

type semanticVerifierEntityCandidate struct {
	EntityID      string `json:"entity_id"`
	CanonicalName string `json:"canonical_name"`
	Kind          string `json:"kind"`
}

type semanticVerifierRelationshipRequest struct {
	Ref                 string              `json:"ref"`
	AllowedPredicates   map[string]struct{} `json:"-"`
	PredicateCandidates []string            `json:"predicate_candidates"`
	Statement           string              `json:"statement"`
	EvidenceID          string              `json:"evidence_id"`
	EvidenceQuote       string              `json:"evidence_quote"`
	SpanStart           int                 `json:"start"`
	SpanEnd             int                 `json:"end"`
	InputIndex          int                 `json:"-"`
}

type semanticVerifierResponse struct {
	RequestID           string                               `json:"request_id"`
	SecuritySignals     []semanticSecuritySignal             `json:"security_signals"`
	EntityResults       []semanticVerifierEntityResult       `json:"entity_results"`
	RelationshipResults []semanticVerifierRelationshipResult `json:"relationship_results"`
}

type semanticVerifierEntityResult struct {
	Ref               string  `json:"ref"`
	Action            string  `json:"action"`
	CandidateEntityID *string `json:"candidate_entity_id"`
	Confidence        float64 `json:"confidence"`
	Rationale         string  `json:"rationale"`
}

type semanticVerifierRelationshipResult struct {
	Ref             string  `json:"ref"`
	PredicateStatus string  `json:"predicate_status"`
	PredicateKey    *string `json:"predicate_key"`
	EvidenceVerdict string  `json:"evidence_verdict"`
	Confidence      float64 `json:"confidence"`
	Rationale       string  `json:"rationale"`
}

var semanticVerifierSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "request_id": {"type": "string"},
    "security_signals": {
      "type": "array",
      "maxItems": 64,
      "items": {
        "type": "object",
        "properties": {
          "evidence_id": {"type": "string", "minLength": 1, "maxLength": 128},
          "kind": {"type": "string", "enum": ["role_control_spoofing", "instruction_override", "prompt_secret_extraction", "tool_exfiltration", "obfuscated_instruction", "hidden_control_markup"]},
          "start": {"type": "integer", "minimum": 0},
          "end": {"type": "integer", "minimum": 0}
        },
        "required": ["evidence_id", "kind", "start", "end"],
        "additionalProperties": false
      }
    },
    "entity_results": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "ref": {"type": "string"},
          "action": {"type": "string", "enum": ["reuse", "create", "ambiguous"]},
          "candidate_entity_id": {"type": ["string", "null"]},
          "confidence": {"type": "number", "minimum": 0, "maximum": 1},
          "rationale": {"type": "string", "minLength": 1, "maxLength": 1000}
        },
        "required": ["ref", "action", "candidate_entity_id", "confidence", "rationale"],
        "additionalProperties": false
      }
    },
    "relationship_results": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "ref": {"type": "string"},
	          "predicate_status": {"type": "string", "enum": ["resolved", "needs_review"]},
	          "predicate_key": {"type": ["string", "null"]},
	          "evidence_verdict": {"type": "string", "enum": ["entailed", "contradicted", "insufficient"]},
	          "confidence": {"type": "number", "minimum": 0, "maximum": 1},
	          "rationale": {"type": "string", "minLength": 1, "maxLength": 1000}
	        },
	        "required": ["ref", "predicate_status", "predicate_key", "evidence_verdict", "confidence", "rationale"],
	        "additionalProperties": false
	      }
    }
  },
  "required": ["request_id", "security_signals", "entity_results", "relationship_results"],
  "additionalProperties": false
}`)

type semanticVerificationResult struct {
	Relationships       []repository.SemanticRelationshipInput
	SecurityAssessments []semanticEvidenceSecurityAssessment
}

func verifySemanticRelationships(ctx context.Context, provider SemanticVerifier, requestID string, relationships []repository.SemanticRelationshipInput, contexts ...repository.SemanticVerifierContext) ([]repository.SemanticRelationshipInput, error) {
	result, err := verifySemanticRelationshipsWithEvidence(ctx, provider, requestID, relationships, nil, contexts...)
	if err != nil {
		return nil, err
	}
	return result.Relationships, nil
}

func verifySemanticRelationshipsWithEvidence(ctx context.Context, provider SemanticVerifier, requestID string, relationships []repository.SemanticRelationshipInput, evidence []domain.MemoryEvidence, contexts ...repository.SemanticVerifierContext) (semanticVerificationResult, error) {
	if len(relationships) == 0 {
		return semanticVerificationResult{}, nil
	}
	if provider == nil {
		return semanticVerificationResult{}, errors.New("semantic verifier: provider is required")
	}
	var verifierContext repository.SemanticVerifierContext
	if len(contexts) > 0 {
		verifierContext = contexts[0]
	}
	req := buildSemanticVerifierRequestWithEvidenceAndContext(requestID, relationships, evidence, verifierContext)
	resp, err := provider.VerifySemantic(ctx, req)
	if err != nil {
		return semanticVerificationResult{}, err
	}
	if err := validateSemanticVerifierResponse(req, resp); err != nil {
		raw, _ := json.Marshal(resp)
		return semanticVerificationResult{}, &verifier.MalformedResponseError{
			Provider: provider.ModelName(),
			Message:  err.Error(),
			RawJSON:  string(raw),
		}
	}
	assessments, err := verifierSecurityAssessments(req, resp)
	if err != nil {
		return semanticVerificationResult{}, err
	}
	if len(assessments) > 0 {
		return semanticVerificationResult{SecurityAssessments: assessments}, nil
	}
	return semanticVerificationResult{
		Relationships: applySemanticVerifierResponse(req, resp, relationships, provider.ModelName()),
	}, nil
}

func buildSemanticVerifierRequest(requestID string, relationships []repository.SemanticRelationshipInput) semanticVerifierRequest {
	return buildSemanticVerifierRequestWithContext(requestID, relationships, repository.SemanticVerifierContext{})
}

func buildSemanticVerifierRequestWithContext(requestID string, relationships []repository.SemanticRelationshipInput, verifierContext repository.SemanticVerifierContext) semanticVerifierRequest {
	return buildSemanticVerifierRequestWithEvidenceAndContext(requestID, relationships, nil, verifierContext)
}

func buildSemanticVerifierRequestWithEvidenceAndContext(requestID string, relationships []repository.SemanticRelationshipInput, evidence []domain.MemoryEvidence, verifierContext repository.SemanticVerifierContext) semanticVerifierRequest {
	req := semanticVerifierRequest{
		RequestID:     strings.TrimSpace(requestID),
		Entities:      map[string]semanticVerifierEntityRequest{},
		Relationships: map[string]semanticVerifierRelationshipRequest{},
	}
	if req.RequestID == "" {
		req.RequestID = "semantic-placement"
	}
	req.Evidence, req.evidenceByID = semanticVerifierEvidence(relationships, evidence)
	for i, rel := range relationships {
		subjectRef := semanticVerifierSubjectRef(i)
		subjectReq := semanticVerifierEntityRequest{
			Ref:        subjectRef,
			Candidate:  map[string]struct{}{},
			SourceName: rel.SubjectName,
		}
		applySemanticVerifierEntityCandidates(&subjectReq, verifierContext.EntityCandidates[repository.SemanticVerifierEntityCandidateKey(rel.SubjectName, rel.SubjectKind)])
		req.Entities[subjectRef] = subjectReq
		if strings.TrimSpace(rel.ObjectName) != "" {
			objectRef := semanticVerifierObjectRef(i)
			objectReq := semanticVerifierEntityRequest{
				Ref:        objectRef,
				Candidate:  map[string]struct{}{},
				SourceName: rel.ObjectName,
			}
			applySemanticVerifierEntityCandidates(&objectReq, verifierContext.EntityCandidates[repository.SemanticVerifierEntityCandidateKey(rel.ObjectName, rel.ObjectKind)])
			req.Entities[objectRef] = objectReq
		}
		relationshipRef := semanticVerifierRelationshipRef(i)
		predicateCandidates := semanticVerifierPredicateCandidates(rel.Predicate)
		req.Relationships[relationshipRef] = semanticVerifierRelationshipRequest{
			Ref:                 relationshipRef,
			AllowedPredicates:   stringSliceSet(predicateCandidates),
			PredicateCandidates: predicateCandidates,
			Statement:           semanticVerifierStatement(rel),
			EvidenceID:          semanticEvidenceID(rel.EvidenceIndex),
			EvidenceQuote:       rel.Quote,
			SpanStart:           semanticVerifierRuneOffset(rel, true),
			SpanEnd:             semanticVerifierRuneOffset(rel, false),
			InputIndex:          i,
		}
	}
	return req
}

func applySemanticVerifierEntityCandidates(req *semanticVerifierEntityRequest, candidates []repository.SemanticEntityCandidate) {
	if req == nil {
		return
	}
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.EntityID)
		if id == "" {
			continue
		}
		req.Candidate[id] = struct{}{}
		req.Candidates = append(req.Candidates, semanticVerifierEntityCandidate{
			EntityID:      id,
			CanonicalName: strings.TrimSpace(candidate.CanonicalName),
			Kind:          string(candidate.Kind),
		})
	}
}

func semanticVerifierPredicateCandidates(predicate string) []string {
	predicateSet := map[string]struct{}{}
	predicate = strings.TrimSpace(predicate)
	if predicate != "" {
		predicateSet[predicate] = struct{}{}
	}
	return sortedStringSet(predicateSet)
}

func stringSliceSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func sortedStringSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func validateSemanticVerifierResponse(req semanticVerifierRequest, resp semanticVerifierResponse) error {
	if resp.RequestID != req.RequestID {
		return fmt.Errorf("semantic verifier: request_id mismatch: expected %q", req.RequestID)
	}
	assessments, err := verifierSecurityAssessments(req, resp)
	if err != nil {
		return err
	}
	if resp.EntityResults == nil {
		return errors.New("semantic verifier: entity_results is required")
	}
	if resp.RelationshipResults == nil {
		return errors.New("semantic verifier: relationship_results is required")
	}
	if len(assessments) > 0 {
		return nil
	}
	if err := validateSemanticEntityResults(req, resp.EntityResults); err != nil {
		return err
	}
	if err := validateSemanticRelationshipResults(req, resp.RelationshipResults); err != nil {
		return err
	}
	return nil
}

func validateSemanticEntityResults(req semanticVerifierRequest, results []semanticVerifierEntityResult) error {
	seen := map[string]struct{}{}
	for _, result := range results {
		ref := strings.TrimSpace(result.Ref)
		expected, ok := req.Entities[ref]
		if !ok {
			return fmt.Errorf("semantic verifier: unknown entity result ref %q", ref)
		}
		if _, exists := seen[ref]; exists {
			return fmt.Errorf("semantic verifier: duplicate entity result ref %q", ref)
		}
		seen[ref] = struct{}{}
		if result.Confidence < 0 || result.Confidence > 1 {
			return fmt.Errorf("semantic verifier: entity %q confidence out of range", ref)
		}
		if strings.TrimSpace(result.Rationale) == "" || len(result.Rationale) > 1000 {
			return fmt.Errorf("semantic verifier: entity %q rationale is required", ref)
		}
		switch result.Action {
		case "reuse":
			if len(expected.Candidate) == 0 {
				return fmt.Errorf("semantic verifier: entity %q reuse requires candidate allowlist", ref)
			}
			if result.CandidateEntityID == nil || strings.TrimSpace(*result.CandidateEntityID) == "" {
				return fmt.Errorf("semantic verifier: entity %q reuse requires candidate_entity_id", ref)
			}
			if _, ok := expected.Candidate[*result.CandidateEntityID]; !ok {
				return fmt.Errorf("semantic verifier: entity %q candidate_entity_id is outside allowlist", ref)
			}
		case "create", "ambiguous":
			if result.CandidateEntityID != nil {
				return fmt.Errorf("semantic verifier: entity %q %s requires null candidate_entity_id", ref, result.Action)
			}
		default:
			return fmt.Errorf("semantic verifier: entity %q action is invalid", ref)
		}
	}
	if len(seen) != len(req.Entities) {
		return errors.New("semantic verifier: entity_results coverage mismatch")
	}
	return nil
}

func validateSemanticRelationshipResults(req semanticVerifierRequest, results []semanticVerifierRelationshipResult) error {
	seen := map[string]struct{}{}
	for _, result := range results {
		ref := strings.TrimSpace(result.Ref)
		expected, ok := req.Relationships[ref]
		if !ok {
			return fmt.Errorf("semantic verifier: unknown relationship result ref %q", ref)
		}
		if _, exists := seen[ref]; exists {
			return fmt.Errorf("semantic verifier: duplicate relationship result ref %q", ref)
		}
		seen[ref] = struct{}{}
		if result.Confidence < 0 || result.Confidence > 1 {
			return fmt.Errorf("semantic verifier: relationship %q confidence out of range", ref)
		}
		if strings.TrimSpace(result.Rationale) == "" || len(result.Rationale) > 1000 {
			return fmt.Errorf("semantic verifier: relationship %q rationale is required", ref)
		}
		if !semanticVerifierOneOf(result.EvidenceVerdict, "entailed", "contradicted", "insufficient") {
			return fmt.Errorf("semantic verifier: relationship %q evidence_verdict is invalid", ref)
		}
		switch result.PredicateStatus {
		case "resolved":
			if result.PredicateKey == nil || strings.TrimSpace(*result.PredicateKey) == "" {
				return fmt.Errorf("semantic verifier: relationship %q resolved predicate requires predicate_key", ref)
			}
			if _, ok := expected.AllowedPredicates[*result.PredicateKey]; !ok {
				return fmt.Errorf("semantic verifier: relationship %q predicate_key is outside allowlist", ref)
			}
		case "needs_review":
			if result.PredicateKey != nil {
				return fmt.Errorf("semantic verifier: relationship %q needs_review requires null predicate_key", ref)
			}
		default:
			return fmt.Errorf("semantic verifier: relationship %q predicate_status is invalid", ref)
		}
	}
	if len(seen) != len(req.Relationships) {
		return errors.New("semantic verifier: relationship_results coverage mismatch")
	}
	return nil
}

func applySemanticVerifierResponse(req semanticVerifierRequest, resp semanticVerifierResponse, relationships []repository.SemanticRelationshipInput, verifierModel string) []repository.SemanticRelationshipInput {
	entityAccepted := map[string]bool{}
	for _, result := range resp.EntityResults {
		entityAccepted[result.Ref] = result.Action == "reuse" || result.Action == "create"
	}
	resultByRef := make(map[string]semanticVerifierRelationshipResult, len(resp.RelationshipResults))
	for _, result := range resp.RelationshipResults {
		resultByRef[result.Ref] = result
	}
	rawResponse, _ := json.Marshal(resp)
	out := make([]repository.SemanticRelationshipInput, 0, len(relationships))
	for index, original := range relationships {
		ref := semanticVerifierRelationshipRef(index)
		result := resultByRef[ref]
		relationshipReq := req.Relationships[ref]
		input := original
		subjectOK := entityAccepted[semanticVerifierSubjectRef(relationshipReq.InputIndex)]
		objectRef := semanticVerifierObjectRef(relationshipReq.InputIndex)
		objectOK := true
		if _, exists := req.Entities[objectRef]; exists {
			objectOK = entityAccepted[objectRef]
		}
		input.Confidence = result.Confidence
		input.VerifierModel = strings.TrimSpace(verifierModel)
		input.EvidenceVerdict = result.EvidenceVerdict
		input.KnowledgeAlignment = "novel"
		input.VerificationRationale = result.Rationale
		input.VerificationRawJSON = string(rawResponse)
		if !subjectOK || !objectOK || result.PredicateStatus != "resolved" || result.PredicateKey == nil {
			input.ObservationOnly = true
			input.Tier = domain.SemanticTierCandidate
			input.Status = domain.SemanticStatusNeedsReview
			switch {
			case !subjectOK || !objectOK:
				input.PlacementOutcomeCategory = "identity_needs_review"
				input.PlacementReviewMessage = "Dense-Mem could not safely resolve one or more entities for this relationship."
			case result.PredicateStatus != "resolved" || result.PredicateKey == nil:
				input.PlacementOutcomeCategory = "predicate_needs_review"
				input.PlacementReviewMessage = "Dense-Mem could not safely resolve the relationship predicate."
			default:
				input.PlacementOutcomeCategory = "relationship_needs_review"
				input.PlacementReviewMessage = "Dense-Mem retained this observation for review."
			}
			out = append(out, input)
			continue
		}
		input.Predicate = strings.TrimSpace(*result.PredicateKey)
		switch result.EvidenceVerdict {
		case "entailed":
			input.Tier = domain.SemanticTierValidatedClaim
			input.Status = domain.SemanticStatusActive
		case "insufficient":
			input.Tier = domain.SemanticTierCandidate
			input.Status = domain.SemanticStatusPendingEvidence
		case "contradicted":
			input.Tier = domain.SemanticTierCandidate
			input.Status = domain.SemanticStatusRejected
		}
		out = append(out, input)
	}
	return out
}

func semanticVerifierOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func semanticVerifierSubjectRef(index int) string {
	return "rel_" + strconv.Itoa(index+1) + "_subject"
}

func semanticVerifierObjectRef(index int) string {
	return "rel_" + strconv.Itoa(index+1) + "_object"
}

func semanticVerifierRelationshipRef(index int) string {
	return "rel_" + strconv.Itoa(index+1)
}

func semanticVerifierStatement(rel repository.SemanticRelationshipInput) string {
	object := strings.TrimSpace(rel.ObjectName)
	if object == "" {
		object = strings.TrimSpace(rel.ObjectValue)
	}
	statement := strings.TrimSpace(rel.SubjectName + " " + strings.ReplaceAll(rel.Predicate, "_", " ") + " " + object)
	if rel.Polarity == domain.PolarityMinus {
		return "It is explicitly false that " + statement
	}
	return statement
}
