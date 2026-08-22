package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func (s *submissionAssessmentPlacementWorkerService) buildRequest(
	ctx context.Context,
	run repository.PlacementRun,
	plan submissionAssessmentPlan,
	proposal map[string]any,
) (verifier.SemanticAssessmentRequest, error) {
	candidateLimit := s.limits.MaxCandidatesPerSurface
	if candidateLimit <= 0 {
		candidateLimit = verifier.DefaultSemanticAssessmentLimits().MaxCandidatesPerSurface
	}
	entityInputs := make([]repository.SubmissionAssessmentEntityCatalogTarget, 0, len(plan.EntityTargets))
	for _, entity := range plan.EntityTargets {
		entityInputs = append(entityInputs, repository.SubmissionAssessmentEntityCatalogTarget{
			Ref:           entity.Target.Ref,
			Surface:       entity.Target.Name,
			EntityKind:    entity.Target.Kind,
			KnownEntityID: entity.KnownEntityID,
		})
	}
	entityCatalog, err := s.catalog.ListSubmissionAssessmentEntityCatalog(ctx, repository.SubmissionAssessmentEntityCatalogInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		Entities:       entityInputs,
		CandidateLimit: candidateLimit,
	})
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, fmt.Errorf("load submission entity catalog: %w", err)
	}
	if !entityCatalog.Complete {
		return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
			"entity_catalog",
			"submission entity catalog exceeds its configured complete bound",
			verifier.FailureMeasurement{Unit: "candidates", Observed: candidateLimit + 1, ObservedAtLeast: true, Limit: candidateLimit},
		)
	}
	evidence := make([]verifier.SemanticReviewEvidence, 0, len(plan.Items))
	for _, item := range plan.Items {
		preparedEvidence := verifier.PrepareSemanticAssessmentEvidence(semanticAssessmentEvidence(item.Fragment, item.EvidenceID))
		evidence = append(evidence, preparedEvidence)
	}
	contractEntities, entityGroups, err := submissionAssessmentGroundedEntities(plan, entityCatalog, evidence)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("catalog_context_validation", "submission entity catalog is invalid")
	}
	contract := verifier.SemanticAssessmentSubmissionContract{
		Entities:      contractEntities,
		Relationships: make([]verifier.SemanticAssessmentRequiredRelationshipRef, 0, len(plan.RelationshipTargets)),
	}
	for _, relationship := range plan.RelationshipTargets {
		contract.Relationships = append(contract.Relationships, relationship.Target)
	}
	predicateOptions, err := s.submissionAssessmentPredicateOptions(ctx, run, plan)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, err
	}
	req := verifier.SemanticAssessmentRequest{
		RequestID:                "submission-assessment:" + run.PlacementRunID,
		TeamID:                   run.TeamID,
		OwnerProfileID:           run.OwnerProfileID,
		Evidence:                 evidence,
		ClientProposal:           assessmentClientProposalWithoutTrustedContext(proposal),
		EntityCandidateGroups:    entityGroups,
		PredicateOptions:         predicateOptions,
		RequiredRelationshipRefs: append([]verifier.SemanticAssessmentRequiredRelationshipRef(nil), contract.Relationships...),
		SubmissionContract:       &contract,
	}
	prepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(req, s.limits)
	if len(validationErrors) == 0 {
		return prepared, nil
	}
	for _, validationError := range validationErrors {
		switch validationError.Field {
		case "candidate_context_tokens":
			limit := s.limits.MaxCandidateContextTokens
			if limit <= 0 {
				limit = verifier.DefaultSemanticAssessmentLimits().MaxCandidateContextTokens
			}
			return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
				"catalog_context",
				"submission assessment catalog exceeds its configured token budget",
				verifier.FailureMeasurement{Unit: "tokens", Observed: prepared.CandidateContextTokens, Limit: limit},
			)
		case "input_tokens":
			limit := s.limits.MaxInputTokens
			if limit <= 0 {
				limit = verifier.DefaultSemanticAssessmentLimits().MaxInputTokens
			}
			return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
				"assessment_input",
				"submission assessment input exceeds its configured token budget",
				verifier.FailureMeasurement{Unit: "tokens", Observed: prepared.InputTokens, Limit: limit},
			)
		}
	}
	return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("trusted_context_validation", "submission assessment contract is invalid")
}

func (s *submissionAssessmentPlacementWorkerService) submissionAssessmentPredicateOptions(
	ctx context.Context,
	run repository.PlacementRun,
	plan submissionAssessmentPlan,
) ([]verifier.SemanticAssessmentPredicateOption, error) {
	limit := s.limits.MaxPredicateOptions
	if limit <= 0 {
		limit = verifier.DefaultSemanticAssessmentLimits().MaxPredicateOptions
	}
	if limit > verifier.SemanticAssessmentMaxPredicateOptions {
		limit = verifier.SemanticAssessmentMaxPredicateOptions
	}

	proposedKeys := make([]string, 0, len(plan.RelationshipTargets))
	seenKeys := make(map[string]struct{}, len(plan.RelationshipTargets))
	queryParts := make([]string, 0, len(plan.Items)+len(plan.RelationshipTargets))
	for _, item := range plan.Items {
		queryParts = append(queryParts, item.Fragment.Content)
	}
	for _, relationship := range plan.RelationshipTargets {
		key := strings.TrimSpace(relationship.ProposedPredicate)
		if key == "" {
			continue
		}
		queryParts = append(queryParts, key)
		if _, exists := seenKeys[key]; exists {
			continue
		}
		seenKeys[key] = struct{}{}
		proposedKeys = append(proposedKeys, key)
	}

	resolved, err := s.catalog.ResolveSemanticReviewPredicateCandidates(ctx, repository.SemanticReviewPredicateResolutionInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		Predicates:     proposedKeys,
		Limit:          limit + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve submission predicate candidates: %w", err)
	}

	candidates := make([]repository.SemanticReviewPredicateCandidate, 0, limit)
	seenCandidates := make(map[string]struct{}, len(resolved))
	appendCandidate := func(candidate repository.SemanticReviewPredicateCandidate) {
		if strings.TrimSpace(candidate.PredicateKey) == "" || candidate.Version < 1 || candidate.LifecycleState != "active" {
			return
		}
		key := fmt.Sprintf("%s:%d", candidate.PredicateKey, candidate.Version)
		if _, exists := seenCandidates[key]; exists {
			return
		}
		seenCandidates[key] = struct{}{}
		candidates = append(candidates, candidate)
	}
	for _, resolution := range resolved {
		appendCandidate(resolution.Candidate)
	}
	if len(candidates) > limit {
		return nil, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
			"predicate_options_overflow",
			"submission predicate candidates exceed the configured request bound",
			verifier.FailureMeasurement{Unit: "candidates", Observed: len(candidates), Limit: limit},
		)
	}

	ranked, err := s.catalog.ListSemanticAssessmentPredicateOptions(ctx, repository.SemanticAssessmentPredicateOptionsInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		QueryText:      strings.Join(queryParts, "\n"),
		ProposedKeys:   proposedKeys,
		Limit:          limit,
	})
	if err != nil {
		return nil, fmt.Errorf("load submission predicate options: %w", err)
	}
	for _, candidate := range ranked {
		if len(candidates) == limit {
			break
		}
		appendCandidate(candidate)
	}

	options := make([]verifier.SemanticAssessmentPredicateOption, 0, len(candidates))
	for _, candidate := range candidates {
		options = append(options, verifier.SemanticAssessmentPredicateOption{
			PredicateKey:        candidate.PredicateKey,
			Version:             candidate.Version,
			Aliases:             append([]string(nil), candidate.Aliases...),
			AllowedSubjectKinds: append([]string(nil), candidate.AllowedSubjectKinds...),
			AllowedObjectKinds:  append([]string(nil), candidate.AllowedObjectKinds...),
			RelationshipKind:    candidate.RelationshipKind,
			CurrentCardinality:  candidate.CurrentCardinality,
		})
	}
	return options, nil
}

func submissionAssessmentGroundedEntities(
	plan submissionAssessmentPlan,
	catalog repository.SubmissionAssessmentEntityCatalogResult,
	evidence []verifier.SemanticReviewEvidence,
) ([]verifier.SemanticAssessmentRequiredEntityRef, []verifier.SemanticAssessmentEntityCandidateGroup, error) {
	groupsByRef := make(map[string]repository.SubmissionAssessmentEntityCatalogGroup, len(catalog.Groups))
	for _, group := range catalog.Groups {
		if _, exists := groupsByRef[group.Ref]; exists || !group.Complete {
			return nil, nil, errors.New("entity catalog group is duplicated or incomplete")
		}
		groupsByRef[group.Ref] = group
	}
	evidenceByID := make(map[string]verifier.SemanticReviewEvidence, len(evidence))
	for _, item := range evidence {
		evidenceByID[item.EvidenceID] = item
	}
	contractEntities := make([]verifier.SemanticAssessmentRequiredEntityRef, 0, len(plan.EntityTargets))
	groups := make([]verifier.SemanticAssessmentEntityCandidateGroup, 0, len(plan.EntityTargets))
	for entityIndex, entity := range plan.EntityTargets {
		catalogGroup, ok := groupsByRef[entity.Target.Ref]
		if !ok {
			return nil, nil, errors.New("entity catalog target is missing")
		}
		if len(catalogGroup.Candidates) > verifier.SemanticAssessmentMaxEntityCandidatesPerSurface {
			return nil, nil, errors.New("entity catalog candidate bound is exceeded")
		}
		allowedNames := map[string]submissionAssessmentGroundingName{}
		addName := func(name string, candidates []repository.SemanticReviewEntityCandidate) {
			if !submissionAssessmentGroundingNameAllowed(name) {
				return
			}
			normalized := submissionAssessmentNormalizedEntityName(name)
			if normalized == "" {
				return
			}
			entry := allowedNames[normalized]
			if entry.Name == "" {
				entry.Name = strings.TrimSpace(name)
			}
			if entry.Candidates == nil {
				entry.Candidates = map[string]repository.SemanticReviewEntityCandidate{}
			}
			for _, candidate := range candidates {
				entry.Candidates[candidate.EntityID] = candidate
			}
			allowedNames[normalized] = entry
		}
		addName(entity.Target.Name, catalogGroup.Candidates)
		for _, candidate := range catalogGroup.Candidates {
			for _, activeName := range candidate.ActiveNames {
				addName(activeName, []repository.SemanticReviewEntityCandidate{candidate})
			}
		}
		target := entity.Target
		target.Groundings = []verifier.SemanticAssessmentEntityGrounding{}
		groundingIndex := 0
		for _, evidenceID := range target.EvidenceIDs {
			evidenceItem, ok := evidenceByID[evidenceID]
			if !ok {
				return nil, nil, errors.New("entity target evidence is missing")
			}
			occurrences := map[string]submissionAssessmentGroundingOccurrence{}
			for _, allowed := range allowedNames {
				for _, span := range submissionAssessmentEntityNameOccurrences(evidenceItem.Content, allowed.Name) {
					key := fmt.Sprintf("%d:%d", span[0], span[1])
					occurrence := occurrences[key]
					occurrence.Start = span[0]
					occurrence.End = span[1]
					occurrence.Surface = string([]rune(evidenceItem.Content)[span[0]:span[1]])
					if occurrence.Candidates == nil {
						occurrence.Candidates = map[string]repository.SemanticReviewEntityCandidate{}
					}
					for candidateID, candidate := range allowed.Candidates {
						occurrence.Candidates[candidateID] = candidate
					}
					occurrences[key] = occurrence
				}
			}
			keys := make([]string, 0, len(occurrences))
			for key := range occurrences {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				occurrence := occurrences[key]
				startRef, startOK := verifier.SemanticAssessmentBoundaryRef(evidenceItem, occurrence.Start)
				endRef, endOK := verifier.SemanticAssessmentBoundaryRef(evidenceItem, occurrence.End)
				if !startOK || !endOK {
					return nil, nil, errors.New("entity grounding boundary is missing")
				}
				groundingRef := fmt.Sprintf("g%d_%d", entityIndex, groundingIndex)
				groundingIndex++
				target.Groundings = append(target.Groundings, verifier.SemanticAssessmentEntityGrounding{
					GroundingRef: groundingRef,
					EvidenceID:   evidenceID,
					Surface:      occurrence.Surface,
					StartRef:     startRef,
					EndRef:       endRef,
					Start:        occurrence.Start,
					End:          occurrence.End,
				})
				if len(target.Groundings) > verifier.SemanticAssessmentMaxEntityGroundings {
					return nil, nil, errors.New("submission assessment entity grounding bound is exceeded")
				}
				candidateIDs := make([]string, 0, len(occurrence.Candidates))
				for candidateID := range occurrence.Candidates {
					candidateIDs = append(candidateIDs, candidateID)
				}
				sort.Strings(candidateIDs)
				group := verifier.SemanticAssessmentEntityCandidateGroup{
					Surface:      occurrence.Surface,
					EvidenceID:   evidenceID,
					GroundingRef: groundingRef,
					Start:        occurrence.Start,
					End:          occurrence.End,
					Candidates:   make([]verifier.SemanticAssessmentEntityCandidate, 0, len(candidateIDs)),
				}
				for _, candidateID := range candidateIDs {
					candidate := occurrence.Candidates[candidateID]
					identityContext := map[string]any{}
					for field, value := range candidate.IdentityContext {
						identityContext[field] = value
					}
					group.Candidates = append(group.Candidates, verifier.SemanticAssessmentEntityCandidate{
						EntityID:        candidate.EntityID,
						CanonicalName:   candidate.CanonicalName,
						Kind:            candidate.EntityKind,
						IdentityContext: identityContext,
					})
				}
				groups = append(groups, group)
			}
		}
		contractEntities = append(contractEntities, target)
	}
	if len(groupsByRef) != len(contractEntities) {
		return nil, nil, errors.New("entity catalog includes an unknown target")
	}
	return contractEntities, groups, nil
}

type submissionAssessmentGroundingName struct {
	Name       string
	Candidates map[string]repository.SemanticReviewEntityCandidate
}

type submissionAssessmentGroundingOccurrence struct {
	Start      int
	End        int
	Surface    string
	Candidates map[string]repository.SemanticReviewEntityCandidate
}

func submissionAssessmentNormalizedEntityName(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func submissionAssessmentGroundingNameAllowed(value string) bool {
	switch submissionAssessmentNormalizedEntityName(value) {
	case "i", "me", "my", "mine", "myself",
		"you", "your", "yours", "yourself", "yourselves",
		"he", "him", "his", "himself", "she", "her", "hers", "herself",
		"it", "its", "itself", "we", "us", "our", "ours", "ourselves",
		"they", "them", "their", "theirs", "themselves",
		"this", "that", "these", "those":
		return false
	default:
		return true
	}
}

func submissionAssessmentEntityNameOccurrences(content, name string) [][2]int {
	contentRunes := []rune(content)
	nameRunes := []rune(strings.TrimSpace(name))
	if len(contentRunes) == 0 || len(nameRunes) == 0 {
		return nil
	}
	result := make([][2]int, 0)
	for start := 0; start < len(contentRunes); start++ {
		end, ok := submissionAssessmentEntityNameMatchAt(contentRunes, nameRunes, start)
		if !ok || !submissionAssessmentEntityBoundaries(contentRunes, start, end) {
			continue
		}
		result = append(result, [2]int{start, end})
	}
	return result
}

func submissionAssessmentEntityNameMatchAt(content, name []rune, start int) (int, bool) {
	contentIndex, nameIndex := start, 0
	for nameIndex < len(name) {
		if unicode.IsSpace(name[nameIndex]) {
			for nameIndex < len(name) && unicode.IsSpace(name[nameIndex]) {
				nameIndex++
			}
			if contentIndex >= len(content) || !unicode.IsSpace(content[contentIndex]) {
				return 0, false
			}
			for contentIndex < len(content) && unicode.IsSpace(content[contentIndex]) {
				contentIndex++
			}
			continue
		}
		if contentIndex >= len(content) || !strings.EqualFold(string(content[contentIndex]), string(name[nameIndex])) {
			return 0, false
		}
		contentIndex++
		nameIndex++
	}
	return contentIndex, true
}

func submissionAssessmentEntityBoundaries(content []rune, start, end int) bool {
	if start > 0 && submissionAssessmentEntityWordRune(content[start-1]) {
		return false
	}
	if end < len(content) && submissionAssessmentEntityWordRune(content[end]) {
		return false
	}
	return true
}

func submissionAssessmentEntityWordRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsNumber(value) || value == '_'
}
