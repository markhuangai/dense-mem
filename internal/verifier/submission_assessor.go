package verifier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const SubmissionAssessmentSchemaName = "dense_mem_submission_assessment_response"

// SubmissionAssessmentResponse adds a complete, per-evidence security verdict
// to the existing closed semantic-assessment result. The semantic fields keep
// their V2.4 meaning so promotion can reuse the same support validation.
type SubmissionAssessmentResponse struct {
	RequestID           string                                   `json:"request_id"`
	SecurityAssessments []SubmissionSecurityAssessment           `json:"security_assessments"`
	EntityResults       []SemanticAssessmentEntityResult         `json:"entity_results"`
	RelationshipResults []SubmissionAssessmentRelationshipResult `json:"relationship_results"`
	OutputTokens        int                                      `json:"-"`
	InputTokens         int                                      `json:"-"`
	ProviderTurns       int                                      `json:"-"`
}

type SubmissionSecurityAssessment struct {
	EvidenceID    string                   `json:"evidence_id"`
	Verdict       string                   `json:"verdict"`
	Signals       []SemanticSecuritySignal `json:"signals"`
	Justification string                   `json:"justification"`
}

type SubmissionAssessmentRelationshipResult struct {
	SemanticAssessmentRelationshipResult
	PredicateCandidate *SubmissionPredicateCandidate `json:"predicate_candidate"`
}

type SubmissionPredicateCandidate struct {
	PredicateKey     string `json:"predicate_key"`
	RelationshipKind string `json:"relationship_kind"`
}

// SubmissionAssessmentRequiredProposal is a server-derived, immutable binding
// for the client proposal. It never enters the provider payload; the provider
// receives only ClientProposal as untrusted data.
type SubmissionAssessmentRequiredProposal struct {
	Entities      []SubmissionAssessmentRequiredEntity
	Relationships []SubmissionAssessmentRequiredRelationship
}

type SubmissionAssessmentRequiredEntity struct {
	Ref        string
	Surface    string
	EvidenceID string
	Start      int
	End        int
}

type SubmissionAssessmentRequiredRelationship struct {
	ProposalID          string
	SubjectRef          string
	OriginalPredicate   string
	PredicateEvidenceID string
	PredicateStart      int
	PredicateEnd        int
	ObjectRef           string
	ObjectValueType     string
	Polarity            string
	Modality            string
	Evidence            []SemanticAssessmentEvidenceSpan
}

func SubmissionAssessmentResponseSchema() map[string]any {
	return closedObject(
		[]string{"request_id", "security_assessments", "entity_results", "relationship_results"},
		map[string]any{
			"request_id":           stringSchema(1, 128),
			"security_assessments": submissionSecurityAssessmentSchema(),
			"entity_results":       semanticAssessmentEntityResultSchema(),
			"relationship_results": submissionAssessmentRelationshipResultSchema(),
		},
	)
}

func submissionAssessmentRelationshipResultSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": SemanticAssessmentMaxRelationshipResults,
		"items": closedObject(
			[]string{
				"ref", "subject_ref", "original_predicate", "predicate_status", "predicate_key", "predicate_version",
				"predicate_candidate", "object_ref", "object_value", "polarity", "modality", "evidence", "valid_from", "valid_to",
				"scope_status", "scope_key", "evidence_verdict", "temporal_verdict", "confidence", "rationale",
			},
			map[string]any{
				"ref":                stringSchema(1, 128),
				"subject_ref":        stringSchema(1, 128),
				"original_predicate": stringSchema(1, 256),
				"predicate_status":   enumSchema([]string{"resolved", "needs_review"}),
				"predicate_key":      nullableStringSchema(128),
				"predicate_version":  nullableIntegerSchema(1),
				"predicate_candidate": map[string]any{
					"anyOf": []any{map[string]any{"type": "null"}, closedObject(
						[]string{"predicate_key", "relationship_kind"},
						map[string]any{
							"predicate_key":     stringSchema(1, 64),
							"relationship_kind": enumSchema(domain.RelationshipKinds()),
						},
					)},
				},
				"object_ref": nullableStringSchema(128),
				"object_value": map[string]any{
					"anyOf": []any{map[string]any{"type": "null"}, semanticAssessmentValueSchema()},
				},
				"polarity":         enumSchema([]string{"+", "-"}),
				"modality":         enumSchema([]string{"statement", "question", "proposal", "speculation", "quoted"}),
				"evidence":         semanticAssessmentEvidenceSpanSchema(),
				"valid_from":       nullableDateTimeSchema(),
				"valid_to":         nullableDateTimeSchema(),
				"scope_status":     enumSchema([]string{"resolved", "absent", "needs_review"}),
				"scope_key":        nullableStringSchema(256),
				"evidence_verdict": enumSchema(domain.VerificationVerdicts()),
				"temporal_verdict": enumSchema([]string{"entailed", "absent", "ambiguous", "contradicted"}),
				"confidence":       numberSchema(0, 1),
				"rationale":        stringSchema(1, 1000),
			},
		),
	}
}

func submissionSecurityAssessmentSchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": SemanticAssessmentMaxEvidenceSpans,
		"items": closedObject(
			[]string{"evidence_id", "verdict", "signals", "justification"},
			map[string]any{
				"evidence_id":   stringSchema(1, 128),
				"verdict":       enumSchema([]string{"no_concern", "concern"}),
				"signals":       semanticSecuritySignalSchema(),
				"justification": stringSchema(1, 1000),
			},
		),
	}
}

func DecodeSubmissionAssessmentResponseJSON(raw []byte, limits SemanticAssessmentLimits) (SubmissionAssessmentResponse, error) {
	limits = normalizeSemanticAssessmentLimits(limits)
	outputTokens, err := CountTokens(string(raw), limits.Tokenizer)
	if err != nil {
		return SubmissionAssessmentResponse{}, err
	}
	if outputTokens > limits.MaxOutputTokens {
		return SubmissionAssessmentResponse{}, fmt.Errorf("submission assessment response exceeds %d token limit", limits.MaxOutputTokens)
	}
	if validationErrors := validateSubmissionAssessmentResponseRaw(raw); len(validationErrors) > 0 {
		return SubmissionAssessmentResponse{}, errors.New(semanticAssessmentJoinedErrors(validationErrors))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response SubmissionAssessmentResponse
	if err := decoder.Decode(&response); err != nil {
		return SubmissionAssessmentResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SubmissionAssessmentResponse{}, errors.New("submission assessment response contains trailing JSON")
	}
	response.OutputTokens = outputTokens
	return response, nil
}

func PrepareSubmissionAssessmentResponse(
	req SemanticAssessmentRequest,
	response SubmissionAssessmentResponse,
	limits SemanticAssessmentLimits,
) (SubmissionAssessmentResponse, []SemanticValidationError) {
	limits = normalizeSemanticAssessmentLimits(limits)
	response.RequestID = strings.TrimSpace(response.RequestID)
	for index := range response.SecurityAssessments {
		assessment := &response.SecurityAssessments[index]
		assessment.EvidenceID = strings.TrimSpace(assessment.EvidenceID)
		assessment.Verdict = strings.TrimSpace(assessment.Verdict)
		assessment.Justification = strings.TrimSpace(assessment.Justification)
		for signalIndex := range assessment.Signals {
			assessment.Signals[signalIndex].EvidenceID = strings.TrimSpace(assessment.Signals[signalIndex].EvidenceID)
			assessment.Signals[signalIndex].Kind = strings.TrimSpace(assessment.Signals[signalIndex].Kind)
		}
	}

	semanticRelationships := make([]SemanticAssessmentRelationshipResult, 0, len(response.RelationshipResults))
	for _, relationship := range response.RelationshipResults {
		semanticRelationships = append(semanticRelationships, relationship.SemanticAssessmentRelationshipResult)
	}
	semantic, semanticErrors := PrepareSemanticAssessmentResponse(req, SemanticAssessmentResponse{
		RequestID:           response.RequestID,
		SecuritySignals:     []SemanticSecuritySignal{},
		EntityResults:       response.EntityResults,
		RelationshipResults: semanticRelationships,
	}, limits)
	response.RequestID = semantic.RequestID
	response.EntityResults = semantic.EntityResults
	for index := range response.RelationshipResults {
		response.RelationshipResults[index].SemanticAssessmentRelationshipResult = semantic.RelationshipResults[index]
	}

	errs := append([]SemanticValidationError{}, semanticErrors...)
	errs = append(errs, validateSubmissionSecurityAssessments(req, response.SecurityAssessments)...)
	errs = append(errs, validateSubmissionPredicateCandidates(response.RelationshipResults)...)
	errs = append(errs, validateSubmissionAssessmentRequiredProposal(
		req.RequiredSubmissionProposal,
		response.EntityResults,
		response.RelationshipResults,
	)...)
	if len(errs) > 0 {
		return response, errs
	}
	canonical, err := json.Marshal(response)
	if err != nil {
		return response, []SemanticValidationError{semanticErr("response", "cannot be normalized")}
	}
	outputTokens, err := CountTokens(string(canonical), limits.Tokenizer)
	if err != nil {
		return response, []SemanticValidationError{semanticErr("tokenizer", err.Error())}
	}
	if outputTokens > limits.MaxOutputTokens {
		return response, []SemanticValidationError{semanticErr("output_tokens", fmt.Sprintf("must be less than or equal to %d", limits.MaxOutputTokens))}
	}
	response.OutputTokens = outputTokens
	return response, nil
}

func validateSubmissionPredicateCandidates(results []SubmissionAssessmentRelationshipResult) []SemanticValidationError {
	var errs []SemanticValidationError
	for index := range results {
		result := &results[index]
		field := fmt.Sprintf("relationship_results[%d].predicate_candidate", index)
		switch result.PredicateStatus {
		case "resolved":
			if result.PredicateCandidate != nil {
				errs = append(errs, semanticErr(field, "must be null for resolved predicates"))
			}
		case "needs_review":
			if result.PredicateCandidate == nil {
				errs = append(errs, semanticErr(field, "is required for a novel predicate"))
				continue
			}
			result.PredicateCandidate.PredicateKey = strings.TrimSpace(result.PredicateCandidate.PredicateKey)
			result.PredicateCandidate.RelationshipKind = strings.TrimSpace(result.PredicateCandidate.RelationshipKind)
			if !submissionPredicateKeyAllowed(result.PredicateCandidate.PredicateKey) {
				errs = append(errs, semanticErr(field+".predicate_key", "must be a bounded lowercase snake_case key"))
			}
			if !semanticOneOf(result.PredicateCandidate.RelationshipKind, domain.RelationshipKinds()...) {
				errs = append(errs, semanticErr(field+".relationship_kind", "is unsupported"))
			}
		}
	}
	return errs
}

func submissionPredicateKeyAllowed(value string) bool {
	if value == "" || len([]rune(value)) > 64 {
		return false
	}
	for index, runeValue := range value {
		if (runeValue >= 'a' && runeValue <= 'z') || (runeValue >= '0' && runeValue <= '9' && index > 0) || (runeValue == '_' && index > 0 && index < len(value)-1) {
			continue
		}
		return false
	}
	return true
}

func normalizeSubmissionAssessmentRequiredProposal(
	required *SubmissionAssessmentRequiredProposal,
	evidenceByID map[string]SemanticReviewEvidence,
) []SemanticValidationError {
	if required == nil {
		return nil
	}
	var errs []SemanticValidationError
	if len(required.Entities) == 0 {
		errs = append(errs, semanticErr("required_submission_proposal.entities", "is required"))
	}
	if len(required.Relationships) == 0 {
		errs = append(errs, semanticErr("required_submission_proposal.relationships", "is required"))
	}
	if len(required.Entities) > SemanticAssessmentMaxEntityResults {
		errs = append(errs, semanticErr("required_submission_proposal.entities", fmt.Sprintf("must contain at most %d entries", SemanticAssessmentMaxEntityResults)))
	}
	if len(required.Relationships) > SemanticAssessmentMaxRelationshipResults {
		errs = append(errs, semanticErr("required_submission_proposal.relationships", fmt.Sprintf("must contain at most %d entries", SemanticAssessmentMaxRelationshipResults)))
	}

	entities := make(map[string]struct{}, len(required.Entities))
	for index := range required.Entities {
		entity := &required.Entities[index]
		field := fmt.Sprintf("required_submission_proposal.entities[%d]", index)
		entity.Ref = strings.TrimSpace(entity.Ref)
		entity.Surface = strings.TrimSpace(entity.Surface)
		entity.EvidenceID = strings.TrimSpace(entity.EvidenceID)
		if entity.Ref == "" || len([]rune(entity.Ref)) > 128 {
			errs = append(errs, semanticErr(field+".ref", "is required and must be at most 128 characters"))
		} else if _, exists := entities[entity.Ref]; exists {
			errs = append(errs, semanticErr(field+".ref", "is duplicated"))
		} else {
			entities[entity.Ref] = struct{}{}
		}
		evidence, exists := evidenceByID[entity.EvidenceID]
		if !exists {
			errs = append(errs, semanticErr(field+".evidence_id", "is unknown"))
			continue
		}
		exact, err := semanticExactSpanQuote(evidence.Content, entity.Start, entity.End, entity.Surface)
		if err != nil {
			errs = append(errs, semanticErr(field+".span", "is invalid"))
			continue
		}
		entity.Surface = exact
	}

	relationships := make(map[string]struct{}, len(required.Relationships))
	for index := range required.Relationships {
		relationship := &required.Relationships[index]
		field := fmt.Sprintf("required_submission_proposal.relationships[%d]", index)
		relationship.ProposalID = strings.TrimSpace(relationship.ProposalID)
		relationship.SubjectRef = strings.TrimSpace(relationship.SubjectRef)
		relationship.OriginalPredicate = strings.TrimSpace(relationship.OriginalPredicate)
		relationship.PredicateEvidenceID = strings.TrimSpace(relationship.PredicateEvidenceID)
		relationship.ObjectRef = strings.TrimSpace(relationship.ObjectRef)
		relationship.ObjectValueType = strings.TrimSpace(relationship.ObjectValueType)
		relationship.Polarity = strings.TrimSpace(relationship.Polarity)
		relationship.Modality = strings.TrimSpace(relationship.Modality)
		if relationship.ProposalID == "" || len([]rune(relationship.ProposalID)) > 128 {
			errs = append(errs, semanticErr(field+".proposal_id", "is required and must be at most 128 characters"))
		} else if _, exists := relationships[relationship.ProposalID]; exists {
			errs = append(errs, semanticErr(field+".proposal_id", "is duplicated"))
		} else {
			relationships[relationship.ProposalID] = struct{}{}
		}
		if _, exists := entities[relationship.SubjectRef]; !exists {
			errs = append(errs, semanticErr(field+".subject_ref", "is unknown"))
		}
		if (relationship.ObjectRef == "") == (relationship.ObjectValueType == "") {
			errs = append(errs, semanticErr(field+".object", "requires exactly one object ref or value type"))
		} else if relationship.ObjectRef != "" {
			if _, exists := entities[relationship.ObjectRef]; !exists {
				errs = append(errs, semanticErr(field+".object_ref", "is unknown"))
			}
		} else if !semanticOneOf(relationship.ObjectValueType, domain.ValueTypes()...) {
			errs = append(errs, semanticErr(field+".object_value_type", "is unsupported"))
		}
		predicateEvidence, exists := evidenceByID[relationship.PredicateEvidenceID]
		if !exists {
			errs = append(errs, semanticErr(field+".predicate.evidence_id", "is unknown"))
		} else if _, err := semanticExactSpanQuote(predicateEvidence.Content, relationship.PredicateStart, relationship.PredicateEnd, relationship.OriginalPredicate); err != nil {
			errs = append(errs, semanticErr(field+".predicate.span", "is invalid"))
		}
		if !semanticOneOf(relationship.Polarity, "+", "-") {
			errs = append(errs, semanticErr(field+".polarity", "is unsupported"))
		}
		if !semanticOneOf(relationship.Modality, "statement", "question", "proposal", "speculation", "quoted") {
			errs = append(errs, semanticErr(field+".modality", "is unsupported"))
		}
		if len(relationship.Evidence) == 0 || len(relationship.Evidence) > SemanticAssessmentMaxEvidenceSpans {
			errs = append(errs, semanticErr(field+".evidence", fmt.Sprintf("must contain between 1 and %d spans", SemanticAssessmentMaxEvidenceSpans)))
			continue
		}
		seenSpans := make(map[string]struct{}, len(relationship.Evidence))
		for evidenceIndex := range relationship.Evidence {
			span := &relationship.Evidence[evidenceIndex]
			span.EvidenceID = strings.TrimSpace(span.EvidenceID)
			evidence, exists := evidenceByID[span.EvidenceID]
			if !exists {
				errs = append(errs, semanticErr(fmt.Sprintf("%s.evidence[%d].evidence_id", field, evidenceIndex), "is unknown"))
				continue
			}
			if _, err := semanticExactSpanQuote(evidence.Content, span.Start, span.End, ""); err != nil {
				errs = append(errs, semanticErr(fmt.Sprintf("%s.evidence[%d]", field, evidenceIndex), "span is invalid"))
			}
			key := assessmentSpanKey(span.EvidenceID, span.Start, span.End)
			if _, exists := seenSpans[key]; exists {
				errs = append(errs, semanticErr(fmt.Sprintf("%s.evidence[%d]", field, evidenceIndex), "is duplicated"))
			}
			seenSpans[key] = struct{}{}
		}
	}
	return errs
}

func validateSubmissionAssessmentRequiredProposal(
	required *SubmissionAssessmentRequiredProposal,
	entities []SemanticAssessmentEntityResult,
	relationships []SubmissionAssessmentRelationshipResult,
) []SemanticValidationError {
	if required == nil {
		return nil
	}
	var errs []SemanticValidationError
	expectedEntities := make(map[string]SubmissionAssessmentRequiredEntity, len(required.Entities))
	for _, entity := range required.Entities {
		expectedEntities[entity.Ref] = entity
	}
	if len(entities) != len(expectedEntities) {
		errs = append(errs, semanticErr("entity_results", "must contain exactly one result per submitted entity"))
	}
	seenEntities := make(map[string]struct{}, len(entities))
	for index, result := range entities {
		expected, exists := expectedEntities[result.Ref]
		if !exists {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].ref", index), "must retain a submitted entity ref"))
			continue
		}
		seenEntities[result.Ref] = struct{}{}
		if result.Surface != expected.Surface || result.EvidenceID != expected.EvidenceID || result.Start != expected.Start || result.End != expected.End {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d]", index), "must retain the submitted exact entity span"))
		}
	}
	for ref := range expectedEntities {
		if _, exists := seenEntities[ref]; !exists {
			errs = append(errs, semanticErr("entity_results", "is missing a submitted entity result"))
		}
	}

	expectedRelationships := make(map[string]SubmissionAssessmentRequiredRelationship, len(required.Relationships))
	for _, relationship := range required.Relationships {
		expectedRelationships[relationship.ProposalID] = relationship
	}
	if len(relationships) != len(expectedRelationships) {
		errs = append(errs, semanticErr("relationship_results", "must contain exactly one result per submitted relationship"))
	}
	seenRelationships := make(map[string]struct{}, len(relationships))
	for index, submitted := range relationships {
		result := submitted.SemanticAssessmentRelationshipResult
		expected, exists := expectedRelationships[result.Ref]
		if !exists {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].ref", index), "must retain a submitted proposal_id"))
			continue
		}
		seenRelationships[result.Ref] = struct{}{}
		if result.SubjectRef != expected.SubjectRef ||
			result.OriginalPredicate != expected.OriginalPredicate ||
			!submissionAssessmentRequiredObjectMatches(expected, result) ||
			!submissionAssessmentRequiredEvidenceMatches(expected.Evidence, result.Evidence) ||
			result.Polarity != expected.Polarity ||
			result.Modality != expected.Modality {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d]", index), "must retain the submitted structure and exact evidence spans"))
		}
	}
	for ref := range expectedRelationships {
		if _, exists := seenRelationships[ref]; !exists {
			errs = append(errs, semanticErr("relationship_results", "is missing a submitted relationship result"))
		}
	}
	return errs
}

func submissionAssessmentRequiredObjectMatches(
	expected SubmissionAssessmentRequiredRelationship,
	actual SemanticAssessmentRelationshipResult,
) bool {
	if expected.ObjectRef != "" {
		return actual.ObjectRef != nil && *actual.ObjectRef == expected.ObjectRef && actual.ObjectValue == nil
	}
	return actual.ObjectRef == nil && actual.ObjectValue != nil && actual.ObjectValue.ValueType == expected.ObjectValueType
}

func submissionAssessmentRequiredEvidenceMatches(
	expected []SemanticAssessmentEvidenceSpan,
	actual []SemanticAssessmentEvidenceSpan,
) bool {
	if len(expected) != len(actual) {
		return false
	}
	seen := make(map[string]struct{}, len(expected))
	for _, span := range expected {
		seen[assessmentSpanKey(span.EvidenceID, span.Start, span.End)] = struct{}{}
	}
	for _, span := range actual {
		key := assessmentSpanKey(span.EvidenceID, span.Start, span.End)
		if _, exists := seen[key]; !exists {
			return false
		}
		delete(seen, key)
	}
	return len(seen) == 0
}

func validateSubmissionSecurityAssessments(
	req SemanticAssessmentRequest,
	assessments []SubmissionSecurityAssessment,
) []SemanticValidationError {
	if assessments == nil {
		return []SemanticValidationError{semanticErr("security_assessments", "is required")}
	}
	if len(assessments) != len(req.Evidence) {
		return []SemanticValidationError{semanticErr("security_assessments", "must contain exactly one assessment per submitted evidence item")}
	}
	evidenceByID := semanticEvidenceByID(req.Evidence)
	seen := make(map[string]struct{}, len(assessments))
	var errs []SemanticValidationError
	for index, assessment := range assessments {
		field := fmt.Sprintf("security_assessments[%d]", index)
		if assessment.EvidenceID == "" {
			errs = append(errs, semanticErr(field+".evidence_id", "is required"))
		}
		if _, exists := seen[assessment.EvidenceID]; exists {
			errs = append(errs, semanticErr(field+".evidence_id", "is duplicated"))
		}
		seen[assessment.EvidenceID] = struct{}{}
		if _, exists := evidenceByID[assessment.EvidenceID]; !exists {
			errs = append(errs, semanticErr(field+".evidence_id", "is unknown"))
		}
		if !assessmentBoundedRequiredString(assessment.Justification, 1000) {
			errs = append(errs, semanticErr(field+".justification", "is required and must be bounded"))
		}
		switch assessment.Verdict {
		case "no_concern":
			if len(assessment.Signals) != 0 {
				errs = append(errs, semanticErr(field+".signals", "must be empty for no_concern"))
			}
		case "concern":
			if len(assessment.Signals) == 0 {
				errs = append(errs, semanticErr(field+".signals", "must identify at least one concern"))
			}
		default:
			errs = append(errs, semanticErr(field+".verdict", "is unsupported"))
		}
		for signalIndex, signal := range assessment.Signals {
			if signal.EvidenceID != assessment.EvidenceID {
				errs = append(errs, semanticErr(fmt.Sprintf("%s.signals[%d].evidence_id", field, signalIndex), "must match its assessment evidence_id"))
			}
		}
		errs = append(errs, prefixSubmissionSecurityErrors(field+".signals", validateSemanticAssessmentSecuritySignals(assessment.Signals, evidenceByID))...)
	}
	for _, evidence := range req.Evidence {
		if _, exists := seen[evidence.EvidenceID]; !exists {
			errs = append(errs, semanticErr("security_assessments", "is missing an evidence assessment"))
		}
	}
	return errs
}

func prefixSubmissionSecurityErrors(prefix string, errorsIn []SemanticValidationError) []SemanticValidationError {
	if len(errorsIn) == 0 {
		return nil
	}
	out := make([]SemanticValidationError, 0, len(errorsIn))
	for _, err := range errorsIn {
		out = append(out, semanticErr(prefix+"."+err.Field, err.Message))
	}
	return out
}

func (r SubmissionAssessmentResponse) HasSecurityConcern() bool {
	for _, assessment := range r.SecurityAssessments {
		if assessment.Verdict == "concern" {
			return true
		}
	}
	return false
}
