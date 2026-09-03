package memoryservice

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func submissionAssessmentCommitInput(
	scope repository.RememberCommitScope,
	plan submissionAssessmentPlan,
	response assessor.SemanticAssessmentResponse,
	assessment *repository.SubmissionAssessment,
	reused bool,
) (repository.CommitSubmissionAssessmentInput, error) {
	if assessment == nil || strings.TrimSpace(assessment.AssessmentID) == "" {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("persisted submission assessment is required before semantic commit")
	}
	usedKnownEvidenceIDs := make(map[string]struct{}, len(plan.knownEvidenceByID))
	unsupportedEntities := repairSubmissionAssessmentResponse(&plan, &response)
	items := make([]repository.SubmissionAssessmentItemInput, 0, len(plan.Items))
	for _, item := range plan.Items {
		items = append(items, repository.SubmissionAssessmentItemInput{
			FragmentID: item.Fragment.FragmentID,
		})
	}
	entityResolutions := make([]repository.SubmissionAssessmentEntityResolutionInput, 0, len(response.EntityResults))
	entityKinds := make(map[string]string, len(response.EntityResults))
	entityResolutionsByGrounding := make(map[string]struct {
		action        string
		candidateID   string
		knownEntityID string
		mentionRef    string
	}, len(response.EntityResults))
	entityRefAliases := make(map[string]string, len(response.EntityResults))
	for _, result := range response.EntityResults {
		target, ok := plan.entityTargetsByRef[result.Ref]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor entity result is outside the contract")
		}
		if _, unsupported := unsupportedEntities[result.Ref]; unsupported {
			continue
		}
		if result.AnchorRef != nil && strings.TrimSpace(*result.AnchorRef) != "" {
			anchorRef := strings.TrimSpace(*result.AnchorRef)
			for _, anchor := range target.Target.Anchors {
				if anchor.AnchorRef != anchorRef {
					continue
				}
				if _, known := plan.knownEvidenceByID[anchor.EvidenceID]; known {
					usedKnownEvidenceIDs[anchor.EvidenceID] = struct{}{}
				}
				break
			}
		}
		if target.KnownEntityID != "" && result.Action == string(domain.EntityResolutionCreate) {
			return repository.CommitSubmissionAssessmentInput{}, fmt.Errorf("%w: assessor changed exact entity constraint for %s", errSubmissionAssessmentStaleInput, result.Ref)
		}
		item, ok := plan.itemsByEvidenceID[result.EvidenceID]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor entity grounding is outside the run")
		}
		resolution := repository.SemanticEntityResolutionInput{
			MentionRef:    result.Ref,
			Action:        result.Action,
			EntityKind:    result.Kind,
			CanonicalName: target.Target.Name,
			FragmentID:    item.Fragment.FragmentID,
			VerifierResult: map[string]any{
				"action":   result.Action,
				"decision": "server_accepted_grounding",
			},
			Metadata: map[string]any{
				"semantic_contract": domain.ContractVersion,
			},
			AssessmentID: assessment.AssessmentID,
		}
		if target.KnownEntityID != "" {
			resolution.ExactEntityID = target.KnownEntityID
		}
		start, end := result.Start, result.End
		resolution.SpanStart = &start
		resolution.SpanEnd = &end
		switch result.Action {
		case string(domain.EntityResolutionReuse):
			if result.CandidateEntityID == nil {
				if target.KnownEntityID == "" {
					result.Action = string(domain.EntityResolutionCreate)
					resolution.Action = result.Action
					resolution.IdentityContext = map[string]any{
						"surface": result.Surface,
						"source":  "submission_assessment",
					}
				} else {
					candidate := target.KnownEntityID
					result.CandidateEntityID = &candidate
				}
			}
			if result.Action == string(domain.EntityResolutionReuse) {
				if target.KnownEntityID != "" && *result.CandidateEntityID != target.KnownEntityID {
					return repository.CommitSubmissionAssessmentInput{}, fmt.Errorf("%w: assessor changed exact entity constraint for %s", errSubmissionAssessmentStaleInput, result.Ref)
				}
				resolution.EntityID = *result.CandidateEntityID
			}
		case string(domain.EntityResolutionCreate):
			resolution.IdentityContext = map[string]any{
				"surface": result.Surface,
				"source":  "submission_assessment",
			}
		default:
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned unsupported entity action")
		}
		entityKinds[result.Ref] = result.Kind
		groundingKey := fmt.Sprintf("%s:%d:%d:%s", result.EvidenceID, result.Start, result.End, result.Kind)
		candidateID := resolution.EntityID
		if previous, exists := entityResolutionsByGrounding[groundingKey]; exists {
			if previous.action != resolution.Action || previous.candidateID != candidateID ||
				(previous.knownEntityID != "" && target.KnownEntityID != "" && previous.knownEntityID != target.KnownEntityID) {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned conflicting entity groundings")
			}
			entityRefAliases[result.Ref] = previous.mentionRef
			continue
		}
		entityResolutionsByGrounding[groundingKey] = struct {
			action        string
			candidateID   string
			knownEntityID string
			mentionRef    string
		}{action: resolution.Action, candidateID: candidateID, knownEntityID: target.KnownEntityID, mentionRef: resolution.MentionRef}
		entityRefAliases[result.Ref] = resolution.MentionRef
		entityResolutions = append(entityResolutions, repository.SubmissionAssessmentEntityResolutionInput{
			Resolution: resolution,
		})
	}
	if len(entityKinds)+len(unsupportedEntities) != len(plan.EntityTargets) {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor omitted an entity result")
	}

	observations := make([]repository.SubmissionAssessmentRelationshipObservationInput, 0, len(response.RelationshipResults))
	registrations := make([]repository.SubmissionPredicateRegistrationInput, 0)
	relationshipResultsByRef := make(map[string]repository.SubmissionRelationshipResultInput, len(response.RelationshipResults))
	seenRelationshipRefs := make(map[string]struct{}, len(response.RelationshipResults))
	for _, result := range response.RelationshipResults {
		target, ok := plan.relationshipsByRef[result.Ref]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor relationship result is outside the contract")
		}
		if _, duplicate := seenRelationshipRefs[result.Ref]; duplicate {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned duplicate relationship result")
		}
		seenRelationshipRefs[result.Ref] = struct{}{}
		switch result.Disposition {
		case "not_supported":
			reason := "not_supported_by_evidence"
			if result.Reason != nil && strings.TrimSpace(*result.Reason) != "" {
				reason = strings.TrimSpace(*result.Reason)
			}
			relationshipResultsByRef[result.Ref] = repository.SubmissionRelationshipResultInput{
				RelationshipRef: result.Ref,
				Disposition:     "not_stored",
				Reason:          reason,
			}
			continue
		case "stored":
		default:
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned unsupported relationship disposition")
		}
		if target.KnownEvidenceUnavailable {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor stored relationship with unavailable known evidence")
		}
		if len(result.Splits) == 0 {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor stored relationship has no split")
		}
		if len(result.Splits) > 1 && (target.CorrectionTarget != nil || target.ConflictContext != nil) {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor split an exact lifecycle operation")
		}
		for _, split := range result.Splits {
			if unsupportedEntityResult(split, unsupportedEntities) {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor stored split references an ungrounded Entity")
			}
			if split.PredicateStatus != "resolved" && split.PredicateStatus != "registration_required" {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor returned unsupported predicate status")
			}
			validFrom, validTo, err := semanticAssessmentValidity(split)
			if err != nil {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor validity is invalid")
			}
			allowedEvidence := make(map[string]struct{}, len(target.Target.EvidenceIDs)+len(target.Target.KnownEvidenceIDs))
			submittedEvidence := make(map[string]struct{}, len(target.Target.EvidenceIDs))
			for _, evidenceID := range target.Target.EvidenceIDs {
				allowedEvidence[evidenceID] = struct{}{}
				submittedEvidence[evidenceID] = struct{}{}
			}
			for _, evidenceID := range target.Target.KnownEvidenceIDs {
				allowedEvidence[evidenceID] = struct{}{}
			}
			hasSubmittedSupport := false
			for _, span := range split.Evidence {
				if _, ok := allowedEvidence[span.EvidenceID]; !ok {
					if _, submitted := plan.itemsByEvidenceID[span.EvidenceID]; !submitted {
						if _, known := plan.knownEvidenceByID[span.EvidenceID]; !known {
							return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor evidence span is outside the run")
						}
					}
					return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor support is outside the relationship allowlist")
				}
				if _, ok := submittedEvidence[span.EvidenceID]; ok {
					hasSubmittedSupport = true
				}
			}
			if !hasSubmittedSupport {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor relationship must retain submitted support")
			}
			for _, span := range split.Evidence {
				if _, known := plan.knownEvidenceByID[span.EvidenceID]; known {
					usedKnownEvidenceIDs[span.EvidenceID] = struct{}{}
				}
			}
			supports, err := submissionAssessmentSupports(plan, assessment.AssessmentID, split.Evidence)
			if err != nil {
				return repository.CommitSubmissionAssessmentInput{}, err
			}
			primarySupport, additionalSupports := semanticAssessmentPrimarySupport(supports)
			if primarySupport == nil {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor relationship has no support")
			}
			_, ok := submissionAssessmentItemForFragment(plan, primarySupport.FragmentID)
			if !ok {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor support is outside the run")
			}
			observationRef := submissionAssessmentObservationRef(result.Ref, split.SplitIndex, len(result.Splits))
			objectRef, objectValue, err := semanticAssessmentObject(observationRef, split)
			if err != nil {
				return repository.CommitSubmissionAssessmentInput{}, err
			}
			if entityKinds[split.SubjectRef] == "" || (objectRef != "" && entityKinds[objectRef] == "") {
				return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor stored split references an ungrounded Entity")
			}
			subjectRef := split.SubjectRef
			if canonicalRef := entityRefAliases[subjectRef]; canonicalRef != "" {
				subjectRef = canonicalRef
			}
			if canonicalRef := entityRefAliases[objectRef]; canonicalRef != "" {
				objectRef = canonicalRef
			}
			observation := repository.SemanticRelationshipDecisionInput{
				Ref: observationRef, SubjectRef: subjectRef, OriginalPredicate: split.OriginalPredicate,
				ObjectRef: objectRef, ObjectValue: objectValue, Polarity: split.Polarity,
				ScopeKey: "", ValidFrom: validFrom, ValidTo: validTo, AssessorAccepted: true,
				Model: assessment.Model, ResponseHash: assessment.ResponseHash,
				Support: primarySupport, Supports: additionalSupports,
				ObservationMetadata: map[string]any{
					"semantic_contract": domain.ContractVersion, "assessment_id": assessment.AssessmentID,
					"support_policy": "server_accepted_grounded_response", "relationship_ref": result.Ref,
					"split_index": split.SplitIndex,
				},
				RelationshipMetadata: map[string]any{
					"assessment_response_hash":   assessment.ResponseHash,
					"submitted_relationship_ref": result.Ref, "split_index": split.SplitIndex,
				},
				AssessmentID: assessment.AssessmentID,
			}
			if target.KnownPredicateKey != "" {
				observation.ExactPredicateKey = target.KnownPredicateKey
			}
			if target.CorrectionTarget != nil {
				copy := *target.CorrectionTarget
				observation.CorrectionTarget = &copy
			}
			if target.ConflictContext != nil {
				copy := *target.ConflictContext
				observation.ConflictContext = &copy
			}
			switch split.PredicateStatus {
			case "resolved":
				if split.PredicateKey == nil || split.PredicateVersion == nil {
					return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor resolved predicate is incomplete")
				}
				if target.KnownPredicateKey != "" && *split.PredicateKey != target.KnownPredicateKey {
					return repository.CommitSubmissionAssessmentInput{}, fmt.Errorf("%w: assessor changed exact predicate constraint for %s", errSubmissionAssessmentStaleInput, result.Ref)
				}
				observation.PredicateKey = *split.PredicateKey
				observation.PredicateVersion = *split.PredicateVersion
			case "registration_required":
				if target.KnownPredicateKey != "" {
					return repository.CommitSubmissionAssessmentInput{}, fmt.Errorf("%w: assessor could not preserve exact predicate constraint for %s", errSubmissionAssessmentStaleInput, result.Ref)
				}
				if split.PredicateRegistration == nil {
					return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor predicate registration is incomplete")
				}
				registrations = append(registrations, repository.SubmissionPredicateRegistrationInput{
					RelationshipRef: observationRef, PredicateKey: split.PredicateRegistration.PredicateKey,
					SubjectKind: entityKinds[split.SubjectRef], ObjectKind: relationshipObjectKind(split, entityKinds, target.ObjectKind),
					RelationshipKind:   split.PredicateRegistration.RelationshipKind,
					CurrentCardinality: split.PredicateRegistration.CurrentCardinality,
				})
			}
			observations = append(observations, repository.SubmissionAssessmentRelationshipObservationInput{
				RelationshipRef: result.Ref,
				SplitIndex:      split.SplitIndex,
				Observation:     observation,
			})
		}
		relationshipResultsByRef[result.Ref] = repository.SubmissionRelationshipResultInput{
			RelationshipRef: result.Ref,
			Disposition:     "stored",
		}
	}
	knownEvidenceIDs := make([]string, 0, len(usedKnownEvidenceIDs))
	for evidenceID := range usedKnownEvidenceIDs {
		knownEvidenceIDs = append(knownEvidenceIDs, evidenceID)
	}
	sort.Strings(knownEvidenceIDs)
	knownEvidenceSnapshot := make([]repository.SubmissionAssessmentKnownEvidence, 0, len(knownEvidenceIDs))
	for _, evidenceID := range knownEvidenceIDs {
		knownEvidenceSnapshot = append(knownEvidenceSnapshot, plan.knownEvidenceByID[evidenceID])
	}
	if len(seenRelationshipRefs) != len(plan.RelationshipTargets) {
		return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor omitted a relationship result")
	}
	relationshipResults := make([]repository.SubmissionRelationshipResultInput, 0, len(plan.RelationshipTargets))
	for _, target := range plan.RelationshipTargets {
		ref := target.Target.ProposalID
		result, ok := relationshipResultsByRef[ref]
		if !ok {
			return repository.CommitSubmissionAssessmentInput{}, errors.New("submission assessor omitted a relationship result")
		}
		relationshipResults = append(relationshipResults, result)
	}
	return repository.CommitSubmissionAssessmentInput{
		RememberCommitScope:      scope,
		AssessmentID:             assessment.AssessmentID,
		Items:                    items,
		KnownEvidenceSnapshot:    knownEvidenceSnapshot,
		EntityResolutions:        entityResolutions,
		RelationshipObservations: observations,
		PredicateRegistrations:   registrations,
		RelationshipResults:      relationshipResults,
		Payload: map[string]any{
			"assessor_contract":           domain.ContractVersion,
			"assessment_id":               assessment.AssessmentID,
			"model":                       assessment.Model,
			"tokenizer":                   assessment.Tokenizer,
			"input_tokens":                assessment.InputTokens,
			"output_tokens":               assessment.OutputTokens,
			"candidate_context_tokens":    assessment.CandidateContextTokens,
			"candidate_context_truncated": assessment.CandidateContextTruncated,
			"response_hash":               assessment.ResponseHash,
			"request_id":                  assessment.RequestID,
			"assessment_reused":           reused,
		},
	}, nil
}

func submissionAssessmentDuplicateResolutions(
	plan submissionAssessmentPlan,
	response assessor.SemanticAssessmentResponse,
) ([]repository.RememberDuplicateResolution, error) {
	resultsByEvidenceID := make(map[string]assessor.SemanticAssessmentEvidenceEquivalenceResult, len(response.EvidenceEquivalenceResults))
	for _, result := range response.EvidenceEquivalenceResults {
		resultsByEvidenceID[strings.TrimSpace(result.EvidenceID)] = result
	}
	groupsByEvidenceID := plan.duplicateCandidatesByEvidenceID
	resolutions := make([]repository.RememberDuplicateResolution, 0, len(plan.Items))
	for _, item := range plan.Items {
		evidenceID := item.EvidenceID
		resolution := repository.RememberDuplicateResolution{
			EvidenceIndex:   item.Fragment.EvidenceIndex,
			EvidenceID:      evidenceID,
			InputFragmentID: item.Fragment.FragmentID,
			Disposition:     "new",
		}
		if exact, ok := plan.exactDuplicateByEvidenceID[evidenceID]; ok {
			resolution.Disposition = "reuse"
			resolution.Exact = true
			resolution.CandidateFragmentID = exact.CandidateFragmentID
			resolution.CandidateOwnerID = exact.CandidateOwnerID
			resolutions = append(resolutions, resolution)
			continue
		}
		group, ok := groupsByEvidenceID[evidenceID]
		if !ok {
			if item.DuplicateAssessmentRequired {
				return nil, fmt.Errorf("submission assessor duplicate candidate group is missing for %s", evidenceID)
			}
			// Force-insert and lifecycle-bearing evidence is intentionally absent
			// from the equivalence allowlist and is always a new occurrence.
			resolutions = append(resolutions, resolution)
			continue
		}
		result, ok := resultsByEvidenceID[evidenceID]
		if !ok {
			return nil, fmt.Errorf("submission assessor duplicate result is missing for %s", evidenceID)
		}
		if result.Action != "reuse" {
			resolutions = append(resolutions, resolution)
			continue
		}
		candidateID := ""
		if result.CandidateEvidenceID != nil {
			candidateID = strings.TrimSpace(*result.CandidateEvidenceID)
		}
		for _, candidate := range group.Candidates {
			if candidate.FragmentID == candidateID {
				resolution.Disposition = "reuse"
				resolution.CandidateFragmentID = candidate.FragmentID
				resolution.CandidateOwnerID = candidate.OwnerProfileID
				break
			}
		}
		if resolution.Disposition != "reuse" {
			return nil, fmt.Errorf("submission assessor duplicate result selected an unknown candidate for %s", evidenceID)
		}
		resolutions = append(resolutions, resolution)
	}
	sort.Slice(resolutions, func(i, j int) bool { return resolutions[i].EvidenceIndex < resolutions[j].EvidenceIndex })
	return resolutions, nil
}
