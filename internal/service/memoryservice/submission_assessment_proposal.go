package memoryservice

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func submissionStageRememberRequest(staged *repository.Submission) (RememberRequest, submissionAssessmentProposal, error) {
	if staged == nil {
		return RememberRequest{}, submissionAssessmentProposal{}, errors.New("staged submission is required")
	}
	if len(staged.Evidence) == 0 {
		return RememberRequest{}, submissionAssessmentProposal{}, errors.New("staged submission has no evidence")
	}
	req := RememberRequest{ContractVersion: domain.ContractVersion, Evidence: make([]RememberEvidenceInput, 0, len(staged.Evidence))}
	for index, evidence := range staged.Evidence {
		if evidence.EvidenceIndex != index {
			return RememberRequest{}, submissionAssessmentProposal{}, errors.New("staged submission evidence indices are not contiguous")
		}
		req.Evidence = append(req.Evidence, RememberEvidenceInput{
			Content:                evidence.Content,
			SourceType:             evidence.SourceType,
			Source:                 evidence.Source,
			SourceGroup:            evidence.SourceGroup,
			Authority:              evidence.Authority,
			SourceKey:              evidence.SourceKey,
			SourceRevision:         evidence.SourceRevision,
			PreviousSourceRevision: evidence.PreviousSourceRevision,
			SupersedesEvidenceIDs:  append([]string(nil), evidence.SupersedesEvidenceIDs...),
			IdempotencyKey:         evidence.IdempotencyKey,
			Labels:                 append([]string(nil), evidence.Labels...),
			Metadata:               cloneSubmissionMetadata(evidence.Metadata),
		})
	}
	entities, err := submissionStageObjectArray(staged.Proposal, "entities")
	if err != nil {
		return RememberRequest{}, submissionAssessmentProposal{}, err
	}
	relationships, err := submissionStageObjectArray(staged.Proposal, "relationships")
	if err != nil {
		return RememberRequest{}, submissionAssessmentProposal{}, err
	}
	req.EntityHints = entities
	req.RelationshipHints = relationships
	if err := ValidateSubmissionProposal(req); err != nil {
		return RememberRequest{}, submissionAssessmentProposal{}, err
	}
	proposal, err := parseSubmissionAssessmentProposal(req)
	if err != nil {
		return RememberRequest{}, submissionAssessmentProposal{}, err
	}
	return req, proposal, nil
}

func cloneSubmissionMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		result[key] = value
	}
	return result
}

func submissionStageObjectArray(proposal map[string]any, key string) ([]map[string]any, error) {
	if proposal == nil {
		return nil, fmt.Errorf("staged proposal.%s is required", key)
	}
	raw, exists := proposal[key]
	if !exists {
		return nil, fmt.Errorf("staged proposal.%s is required", key)
	}
	return submissionObjectArray(raw, "staged proposal."+key)
}

func submissionObjectArray(raw any, label string) ([]map[string]any, error) {
	switch typed := raw.(type) {
	case []map[string]any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if item == nil {
				return nil, fmt.Errorf("%s contains an invalid object", label)
			}
			result = append(result, item)
		}
		return result, nil
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			fields, ok := submissionProposalObject(item)
			if !ok {
				return nil, fmt.Errorf("%s contains an invalid object", label)
			}
			result = append(result, fields)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%s must be an array", label)
	}
}

func parseSubmissionAssessmentProposal(req RememberRequest) (submissionAssessmentProposal, error) {
	proposal := submissionAssessmentProposal{
		Entities:      make([]submissionAssessmentEntityProposal, 0, len(req.EntityHints)),
		Relationships: make([]submissionAssessmentRelationshipProposal, 0, len(req.RelationshipHints)),
	}
	for index, entity := range req.EntityHints {
		spans, err := submissionProposalSpans(entity["evidence"], len(req.Evidence), fmt.Sprintf("proposal.entities[%d].evidence", index))
		if err != nil || len(spans) != 1 {
			return submissionAssessmentProposal{}, errors.New("submission entity proposal is invalid")
		}
		proposal.Entities = append(proposal.Entities, submissionAssessmentEntityProposal{
			Ref:           submissionProposalString(entity, "ref"),
			Name:          submissionProposalString(entity, "name"),
			KnownEntityID: submissionProposalString(entity, "known_entity_id"),
			Span:          spans[0],
		})
	}
	for index, relationship := range req.RelationshipHints {
		predicate, ok := submissionProposalObject(relationship["predicate"])
		if !ok {
			return submissionAssessmentProposal{}, errors.New("submission relationship predicate is invalid")
		}
		predicateSpan, err := submissionProposalSpanFromFields(predicate, "predicate", index)
		if err != nil {
			return submissionAssessmentProposal{}, errors.New("submission relationship predicate is invalid")
		}
		evidence, err := submissionProposalSpans(relationship["evidence"], len(req.Evidence), fmt.Sprintf("proposal.relationships[%d].evidence", index))
		if err != nil {
			return submissionAssessmentProposal{}, errors.New("submission relationship evidence is invalid")
		}
		objectValue, _ := submissionProposalObject(relationship["object_value"])
		proposal.Relationships = append(proposal.Relationships, submissionAssessmentRelationshipProposal{
			ProposalID:    submissionProposalString(relationship, "proposal_id"),
			SubjectRef:    submissionProposalString(relationship, "subject_ref"),
			Predicate:     submissionProposalString(predicate, "surface"),
			PredicateSpan: predicateSpan,
			ObjectRef:     submissionProposalString(relationship, "object_ref"),
			ObjectValue:   objectValue,
			Evidence:      evidence,
			Polarity:      firstSubmissionProposalString(relationship, "polarity", "+"),
			Modality:      firstSubmissionProposalString(relationship, "modality", "statement"),
		})
	}
	return proposal, nil
}

func firstSubmissionProposalString(fields map[string]any, key, fallback string) string {
	if value := submissionProposalString(fields, key); value != "" {
		return value
	}
	return fallback
}

func submissionEvidenceID(index int) string {
	return fmt.Sprintf("evidence:%d", index)
}

func submissionAssessmentRequiredProposal(proposal submissionAssessmentProposal) *verifier.SubmissionAssessmentRequiredProposal {
	required := &verifier.SubmissionAssessmentRequiredProposal{
		Entities:      make([]verifier.SubmissionAssessmentRequiredEntity, 0, len(proposal.Entities)),
		Relationships: make([]verifier.SubmissionAssessmentRequiredRelationship, 0, len(proposal.Relationships)),
	}
	for _, entity := range proposal.Entities {
		required.Entities = append(required.Entities, verifier.SubmissionAssessmentRequiredEntity{
			Ref:        entity.Ref,
			Surface:    entity.Name,
			EvidenceID: submissionEvidenceID(entity.Span.EvidenceIndex),
			Start:      entity.Span.Start,
			End:        entity.Span.End,
		})
	}
	for _, relationship := range proposal.Relationships {
		evidence := make([]verifier.SemanticAssessmentEvidenceSpan, 0, len(relationship.Evidence))
		for _, span := range relationship.Evidence {
			evidence = append(evidence, verifier.SemanticAssessmentEvidenceSpan{
				EvidenceID: submissionEvidenceID(span.EvidenceIndex),
				Start:      span.Start,
				End:        span.End,
			})
		}
		requiredRelationship := verifier.SubmissionAssessmentRequiredRelationship{
			ProposalID:          relationship.ProposalID,
			SubjectRef:          relationship.SubjectRef,
			OriginalPredicate:   relationship.Predicate,
			PredicateEvidenceID: submissionEvidenceID(relationship.PredicateSpan.EvidenceIndex),
			PredicateStart:      relationship.PredicateSpan.Start,
			PredicateEnd:        relationship.PredicateSpan.End,
			ObjectRef:           relationship.ObjectRef,
			ObjectValueType:     submissionProposalString(relationship.ObjectValue, "type"),
			Polarity:            relationship.Polarity,
			Modality:            relationship.Modality,
			Evidence:            evidence,
		}
		if value, ok := submissionProposalObjectValue(relationship.ObjectValue); ok {
			requiredRelationship.ObjectValueType = value.ValueType
			requiredRelationship.ObjectValueCanonical = value.CanonicalValue
			requiredRelationship.ObjectValueDisplay = value.Display
			requiredRelationship.ObjectValueUnit = value.Unit
		}
		required.Relationships = append(required.Relationships, requiredRelationship)
	}
	return required
}

func submissionProposalObjectValue(fields map[string]any) (*verifier.SemanticAssessmentValue, bool) {
	if fields == nil {
		return nil, false
	}
	valueType := submissionProposalString(fields, "type")
	raw, exists := fields["value"]
	if !exists {
		return nil, false
	}
	canonical, ok := submissionProposalCanonicalValue(raw)
	if !ok {
		return nil, false
	}
	value := &verifier.SemanticAssessmentValue{ValueType: valueType, CanonicalValue: canonical}
	for _, field := range []struct {
		name   string
		target **string
	}{
		{name: "display", target: &value.Display},
		{name: "unit", target: &value.Unit},
	} {
		rawText, exists := fields[field.name]
		if !exists {
			continue
		}
		text, ok := rawText.(string)
		if !ok {
			return nil, false
		}
		text = strings.TrimSpace(text)
		*field.target = &text
	}
	return value, true
}

func submissionProposalCanonicalValue(raw any) (string, bool) {
	switch value := raw.(type) {
	case string:
		return value, true
	case bool:
		return strconv.FormatBool(value), true
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", false
		}
		return strconv.FormatFloat(value, 'f', -1, 64), true
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return "", false
		}
		return strconv.FormatFloat(float64(value), 'f', -1, 32), true
	case int:
		return strconv.Itoa(value), true
	case int32:
		return strconv.FormatInt(int64(value), 10), true
	case int64:
		return strconv.FormatInt(value, 10), true
	case json.Number:
		text := value.String()
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return "", false
		}
		return text, true
	default:
		return "", false
	}
}

func submissionAssessmentEntityCandidate(candidate repository.SemanticReviewEntityCandidate) verifier.SemanticAssessmentEntityCandidate {
	prepared := assessmentEntityCandidate(candidate)
	prepared.IdentityContext = map[string]any{}
	return prepared
}
