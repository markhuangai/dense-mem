package memoryservice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func semanticAssessmentEntityResolutions(
	response verifier.SemanticAssessmentResponse,
	fragment repository.EvidenceFragment,
	assessmentID string,
	groups map[string]*verifier.SemanticAssessmentEntityCandidateGroup,
) ([]repository.PlacementEntityResolutionInput, map[string]assessmentEntityCommitState, error) {
	resolutions := make([]repository.PlacementEntityResolutionInput, 0, len(response.EntityResults))
	states := make(map[string]assessmentEntityCommitState, len(response.EntityResults))
	for _, result := range response.EntityResults {
		group := groups[assessmentCandidateGroupKey(result.EvidenceID, result.Start, result.End)]
		action := result.Action
		switch result.Action {
		case string(domain.EntityResolutionReuse):
			if result.CandidateEntityID == nil || !assessmentReusableCandidate(group, *result.CandidateEntityID, result.Kind) {
				action = string(domain.EntityResolutionAmbiguous)
			}
		case string(domain.EntityResolutionCreate):
			if group != nil && (group.CandidateContextTruncated || assessmentCompatibleCandidateExists(group, result.Kind)) {
				action = string(domain.EntityResolutionAmbiguous)
			}
		case string(domain.EntityResolutionAmbiguous):
		default:
			return nil, nil, fmt.Errorf("unsupported stored entity assessment action %q", result.Action)
		}
		start, end := result.Start, result.End
		resolution := repository.PlacementEntityResolutionInput{
			MentionRef:    result.Ref,
			Action:        action,
			EntityKind:    result.Kind,
			CanonicalName: result.Surface,
			FragmentID:    fragment.FragmentID,
			SpanStart:     &start,
			SpanEnd:       &end,
			VerifierResult: map[string]any{
				"confidence": result.Confidence,
				"rationale":  result.Rationale,
				"action":     result.Action,
			},
			Metadata: map[string]any{
				"semantic_contract": "dense-mem.v2.4",
			},
			AssessmentID: assessmentID,
		}
		switch action {
		case string(domain.EntityResolutionReuse):
			resolution.EntityID = *result.CandidateEntityID
		case string(domain.EntityResolutionCreate):
			resolution.IdentityContext = map[string]any{"surface": result.Surface, "source": "semantic_assessment"}
		case string(domain.EntityResolutionAmbiguous):
			resolution.SemanticReviewKind = "identity"
			resolution.ReviewQuestion = assessmentReviewQuestion("identity")
			resolution.ReviewOptions = assessmentEntityReviewOptions(group)
			resolution.ReviewGuidance = assessmentReviewGuidance("identity")
		}
		resolutions = append(resolutions, resolution)
		states[result.Ref] = assessmentEntityCommitState{resolved: action != string(domain.EntityResolutionAmbiguous), group: group}
	}
	return resolutions, states, nil
}

func assessmentReusableCandidate(group *verifier.SemanticAssessmentEntityCandidateGroup, entityID, kind string) bool {
	if group == nil || group.CandidateContextTruncated {
		return false
	}
	matching := 0
	for _, candidate := range group.Candidates {
		if candidate.Kind != kind {
			continue
		}
		matching++
		if candidate.EntityID != entityID {
			return false
		}
	}
	return matching == 1
}

func assessmentCompatibleCandidateExists(group *verifier.SemanticAssessmentEntityCandidateGroup, kind string) bool {
	if group == nil {
		return false
	}
	for _, candidate := range group.Candidates {
		if candidate.Kind == kind {
			return true
		}
	}
	return false
}

func assessmentEntityReviewOptions(group *verifier.SemanticAssessmentEntityCandidateGroup) []map[string]any {
	if group == nil {
		return []map[string]any{}
	}
	options := make([]map[string]any, 0, len(group.Candidates))
	for _, candidate := range group.Candidates {
		options = append(options, map[string]any{
			"entity_id":      candidate.EntityID,
			"canonical_name": candidate.CanonicalName,
			"kind":           candidate.Kind,
		})
	}
	return options
}

func semanticAssessmentRelationshipReviewKind(
	result verifier.SemanticAssessmentRelationshipResult,
	entities map[string]assessmentEntityCommitState,
	predicates map[string]verifier.SemanticAssessmentPredicateOption,
) string {
	if result.PredicateStatus != "resolved" || result.PredicateKey == nil || result.PredicateVersion == nil {
		return "predicate"
	}
	if _, ok := predicates[assessmentPredicateKey(*result.PredicateKey, *result.PredicateVersion)]; !ok {
		return "predicate"
	}
	if state, ok := entities[result.SubjectRef]; !ok || !state.resolved {
		return "identity"
	}
	if result.ObjectRef != nil {
		if state, ok := entities[*result.ObjectRef]; !ok || !state.resolved {
			return "identity"
		}
	}
	if result.ScopeStatus == "needs_review" || result.Modality != "statement" {
		return "scope"
	}
	if result.TemporalVerdict == "ambiguous" || result.TemporalVerdict == "contradicted" {
		return "time"
	}
	return ""
}

func semanticAssessmentSupport(
	fragment repository.EvidenceFragment,
	assessmentID string,
	spans []verifier.SemanticAssessmentEvidenceSpan,
) (*repository.EvidenceSupportInput, error) {
	if len(spans) == 0 {
		return nil, errors.New("semantic assessment relationship has no evidence span")
	}
	span := spans[0]
	quote, err := verifier.SemanticEvidenceSpan(fragment.Content, span.Start, span.End)
	if err != nil {
		return nil, err
	}
	return &repository.EvidenceSupportInput{
		FragmentID:       fragment.FragmentID,
		SourceGroupKey:   "semantic_assessment:" + assessmentID + ":" + span.EvidenceID,
		SourceID:         fragment.SourceID,
		SourceRevisionID: fragment.SourceRevisionID,
		SpanStart:        span.Start,
		SpanEnd:          span.End,
		Quote:            quote,
		Authority:        string(domain.AuthorityPrimary),
		Metadata: map[string]any{
			"semantic_contract": "dense-mem.v2.4",
			"assessment_id":     assessmentID,
		},
	}, nil
}

func semanticAssessmentObject(
	result verifier.SemanticAssessmentRelationshipResult,
) (string, *repository.PlacementValueInput, error) {
	if result.ObjectRef != nil {
		return *result.ObjectRef, nil, nil
	}
	if result.ObjectValue == nil {
		return "", nil, errors.New("semantic assessment relationship object is missing")
	}
	display := result.ObjectValue.CanonicalValue
	if result.ObjectValue.Display != nil {
		display = *result.ObjectValue.Display
	}
	unit := ""
	if result.ObjectValue.Unit != nil {
		unit = *result.ObjectValue.Unit
	}
	return "", &repository.PlacementValueInput{
		Ref:            "value:" + result.Ref,
		ValueType:      result.ObjectValue.ValueType,
		CanonicalValue: result.ObjectValue.CanonicalValue,
		Display:        display,
		Unit:           unit,
	}, nil
}

func semanticAssessmentValidity(result verifier.SemanticAssessmentRelationshipResult) (*time.Time, *time.Time, error) {
	parse := func(value *string) (*time.Time, error) {
		if value == nil {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, *value)
		if err != nil {
			return nil, err
		}
		parsed = parsed.UTC()
		return &parsed, nil
	}
	from, err := parse(result.ValidFrom)
	if err != nil {
		return nil, nil, err
	}
	to, err := parse(result.ValidTo)
	if err != nil {
		return nil, nil, err
	}
	return from, to, nil
}

func semanticAssessmentScopeKey(result verifier.SemanticAssessmentRelationshipResult) string {
	if result.ScopeStatus == "resolved" && result.ScopeKey != nil {
		return *result.ScopeKey
	}
	return ""
}

func semanticAssessmentRelationshipReviewInput(
	result verifier.SemanticAssessmentRelationshipResult,
	objectRef string,
	objectValue *repository.PlacementValueInput,
	support *repository.EvidenceSupportInput,
	assessmentID, policyVersion, model, responseHash string,
	threshold *float64,
	gateResult, kind string,
	options []map[string]any,
) repository.PlacementRelationshipReviewInput {
	confidence := result.Confidence
	return repository.PlacementRelationshipReviewInput{
		Ref:                     result.Ref,
		SubjectRef:              result.SubjectRef,
		OriginalPredicate:       result.OriginalPredicate,
		ObjectRef:               objectRef,
		ObjectValue:             objectValue,
		Polarity:                result.Polarity,
		EvidenceVerdict:         result.EvidenceVerdict,
		Confidence:              &confidence,
		Rationale:               result.Rationale,
		Model:                   model,
		ResponseHash:            responseHash,
		Support:                 support,
		Reason:                  reviewTaskReasonForAssessmentKind(kind),
		AssessmentID:            assessmentID,
		AssessmentPolicyVersion: policyVersion,
		ThresholdUsed:           threshold,
		GateResult:              gateResult,
		SemanticReviewKind:      kind,
		ReviewQuestion:          assessmentReviewQuestion(kind),
		ReviewOptions:           options,
		ReviewGuidance:          assessmentReviewGuidance(kind),
		Payload: map[string]any{
			"semantic_contract": "dense-mem.v2.4",
			"modality":          result.Modality,
			"scope_status":      result.ScopeStatus,
			"temporal_verdict":  result.TemporalVerdict,
		},
	}
}

func reviewTaskReasonForAssessmentKind(kind string) string {
	switch kind {
	case "identity":
		return "identity_needs_review"
	case "predicate":
		return "predicate_needs_review"
	case "scope":
		return "scope_needs_review"
	case "time":
		return "time_needs_review"
	default:
		return "support_confidence"
	}
}

func assessmentReviewOptions(
	kind string,
	result verifier.SemanticAssessmentRelationshipResult,
	entities map[string]assessmentEntityCommitState,
	predicates map[string]verifier.SemanticAssessmentPredicateOption,
) []map[string]any {
	switch kind {
	case "identity":
		options := []map[string]any{}
		appendOptions := func(state assessmentEntityCommitState) {
			for _, option := range assessmentEntityReviewOptions(state.group) {
				duplicate := false
				for _, existing := range options {
					if existing["entity_id"] == option["entity_id"] {
						duplicate = true
						break
					}
				}
				if !duplicate {
					options = append(options, option)
				}
			}
		}
		if state, ok := entities[result.SubjectRef]; ok && !state.resolved {
			appendOptions(state)
		}
		if result.ObjectRef != nil {
			if state, ok := entities[*result.ObjectRef]; ok && !state.resolved {
				appendOptions(state)
			}
		}
		return options
	case "predicate":
		keys := make([]string, 0, len(predicates))
		for key := range predicates {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		options := make([]map[string]any, 0, len(keys))
		for _, key := range keys {
			predicate := predicates[key]
			options = append(options, map[string]any{
				"predicate_key": predicate.PredicateKey,
				"version":       predicate.Version,
				"aliases":       append([]string(nil), predicate.Aliases...),
			})
		}
		return options
	default:
		return []map[string]any{{"action": "submit_new_evidence"}}
	}
}

func assessmentReviewQuestion(kind string) string {
	switch kind {
	case "identity":
		return "Choose the current entity that matches this evidence mention."
	case "predicate":
		return "Choose the registered predicate that matches this relationship."
	case "scope":
		return "Provide new evidence that resolves the relationship scope."
	case "time":
		return "Provide new evidence that resolves the relationship time."
	default:
		return "Provide new evidence before this relationship can receive support."
	}
}

func assessmentReviewGuidance(kind string) string {
	switch kind {
	case "identity", "predicate":
		return "Select only a server-supplied current option."
	default:
		return "A selection alone cannot grant relationship support; submit exact new evidence."
	}
}

func applySemanticAssessmentReviewOverrides(
	response verifier.SemanticAssessmentResponse,
	overrides repository.PlacementAssessmentReviewOverrides,
) verifier.SemanticAssessmentResponse {
	for index := range response.EntityResults {
		result := &response.EntityResults[index]
		candidateID, ok := overrides.EntitySelections[result.Ref]
		if !ok || strings.TrimSpace(candidateID) == "" {
			continue
		}
		result.Action = string(domain.EntityResolutionReuse)
		result.CandidateEntityID = stringPointer(candidateID)
	}
	for index := range response.RelationshipResults {
		result := &response.RelationshipResults[index]
		override, ok := overrides.PredicateSelections[result.Ref]
		if !ok || strings.TrimSpace(override.PredicateKey) == "" || override.PredicateVersion < 1 {
			continue
		}
		result.PredicateStatus = "resolved"
		result.PredicateKey = stringPointer(override.PredicateKey)
		result.PredicateVersion = intPointer(override.PredicateVersion)
	}
	return response
}

func stringPointer(value string) *string {
	value = strings.TrimSpace(value)
	return &value
}

func intPointer(value int) *int {
	return &value
}
