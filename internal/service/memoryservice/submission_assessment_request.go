package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func (s *assessmentEngine) buildRequest(
	ctx context.Context,
	scope RememberAssessmentScope,
	plan submissionAssessmentPlan,
	proposal map[string]any,
) (assessor.SemanticAssessmentRequest, error) {
	candidateLimit := s.limits.MaxCandidatesPerSurface
	if candidateLimit <= 0 {
		candidateLimit = assessor.DefaultSemanticAssessmentLimits().MaxCandidatesPerSurface
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
		TeamID:         scope.TeamID,
		OwnerProfileID: scope.OwnerProfileID,
		SpaceID:        scope.SpaceID,
		Entities:       entityInputs,
		CandidateLimit: candidateLimit,
	})
	if err != nil {
		return assessor.SemanticAssessmentRequest{}, submissionAssessmentDatabaseError("load submission entity catalog", err)
	}
	if !entityCatalog.Complete {
		return assessor.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
			"entity_catalog",
			"submission entity catalog exceeds its configured complete bound",
			assessor.FailureMeasurement{Unit: "candidates", Observed: candidateLimit + 1, ObservedAtLeast: true, Limit: candidateLimit},
		)
	}
	knownIDs := make([]string, 0, len(plan.knownEvidenceByID))
	for evidenceID := range plan.knownEvidenceByID {
		knownIDs = append(knownIDs, evidenceID)
	}
	sort.Strings(knownIDs)
	evidence := make([]assessor.SemanticReviewEvidence, 0, len(plan.Items))
	for _, item := range plan.Items {
		prepared := semanticAssessmentEvidence(item.Fragment, item.EvidenceID)
		// Citation order is deterministic across the separate provider fields:
		// explicitly authorized known evidence precedes submitted evidence so a
		// known canonical/alias mention may anchor a later submitted pronoun.
		prepared.EvidenceIndex = len(knownIDs) + item.Fragment.EvidenceIndex
		preparedEvidence := assessor.PrepareSemanticAssessmentEvidence(prepared)
		evidence = append(evidence, preparedEvidence)
	}
	knownEvidence := make([]assessor.SemanticReviewEvidence, 0, len(plan.knownEvidenceByID))
	for knownIndex, evidenceID := range knownIDs {
		known := plan.knownEvidenceByID[evidenceID]
		knownEvidence = append(knownEvidence, assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{
			EvidenceID: known.EvidenceID, FragmentID: known.FragmentID, EvidenceIndex: knownIndex, Content: known.Content,
			Authority: known.Authority, SourceID: known.SourceID, SourceRevisionID: known.SourceRevisionID,
			CurrentSourceRevisionID: known.CurrentSourceRevisionID,
		}))
	}
	knownEvidenceByID := make(map[string]assessor.SemanticReviewEvidence, len(knownEvidence))
	for _, item := range knownEvidence {
		knownEvidenceByID[item.EvidenceID] = item
	}
	contractEntities, entityGroups, err := submissionAssessmentGroundedEntities(plan, entityCatalog, evidence, knownEvidenceByID)
	if err != nil {
		var preflightErr *semanticAssessmentPreflightError
		if errors.As(err, &preflightErr) {
			return assessor.SemanticAssessmentRequest{}, err
		}
		return assessor.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightErrorWithCause("catalog_context_validation", "submission entity catalog is invalid", err)
	}
	// Keep the derived, evidence-backed grounding catalog on the plan as well
	// as in the provider request. Commit-time repair uses the plan and must be
	// able to recover a missing grounding without inventing a span.
	for _, target := range contractEntities {
		entry, ok := plan.entityTargetsByRef[target.Ref]
		if !ok {
			continue
		}
		entry.Target = target
		plan.entityTargetsByRef[target.Ref] = entry
	}
	contract := assessor.SemanticAssessmentSubmissionContract{
		Entities:      contractEntities,
		Relationships: make([]assessor.SemanticAssessmentRequiredRelationshipRef, 0, len(plan.RelationshipTargets)),
	}
	for _, relationship := range plan.RelationshipTargets {
		target := relationship.Target
		target.KnownPredicateKey = relationship.KnownPredicateKey
		target.KnownEvidenceUnavailable = relationship.KnownEvidenceUnavailable
		if relationship.CorrectionTarget != nil || relationship.ConflictContext != nil {
			target.MaxSplits = 1
		}
		contract.Relationships = append(contract.Relationships, target)
	}
	predicateOptions, err := s.submissionAssessmentPredicateOptions(ctx, scope, plan)
	if err != nil {
		return assessor.SemanticAssessmentRequest{}, err
	}
	requestID := strings.TrimSpace(scope.IngestID)
	if requestID == "" {
		requestID = "request"
	}
	req := assessor.SemanticAssessmentRequest{
		RequestID:                "synchronous-remember:" + requestID,
		TeamID:                   scope.TeamID,
		OwnerProfileID:           scope.OwnerProfileID,
		Evidence:                 evidence,
		KnownEvidence:            knownEvidence,
		ClientProposal:           assessmentClientProposalWithoutTrustedContext(proposal),
		EntityCandidateGroups:    entityGroups,
		PredicateOptions:         predicateOptions,
		RequiredRelationshipRefs: append([]assessor.SemanticAssessmentRequiredRelationshipRef(nil), contract.Relationships...),
		SubmissionContract:       &contract,
	}
	prepared, validationErrors := assessor.PrepareSemanticAssessmentRequest(req, s.limits)
	if len(validationErrors) == 0 {
		return prepared, nil
	}
	for _, validationError := range validationErrors {
		switch validationError.Field {
		case "candidate_context_tokens":
			limit := s.limits.MaxCandidateContextTokens
			if limit <= 0 {
				limit = assessor.DefaultSemanticAssessmentLimits().MaxCandidateContextTokens
			}
			return assessor.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
				"catalog_context",
				"submission assessment catalog exceeds its configured token budget",
				assessor.FailureMeasurement{Unit: "tokens", Observed: prepared.CandidateContextTokens, Limit: limit},
			)
		case "input_tokens":
			limit := s.limits.MaxInputTokens
			if limit <= 0 {
				limit = assessor.DefaultSemanticAssessmentLimits().MaxInputTokens
			}
			return assessor.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
				"assessment_input",
				"submission assessment input exceeds its configured token budget",
				assessor.FailureMeasurement{Unit: "tokens", Observed: prepared.InputTokens, Limit: limit},
			)
		}
	}
	return assessor.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("trusted_context_validation", "submission assessment contract is invalid")
}

func (s *assessmentEngine) loadKnownEvidence(
	ctx context.Context,
	scope RememberAssessmentScope,
	plan *submissionAssessmentPlan,
) error {
	if s == nil || s.catalog == nil || plan == nil {
		return errors.New("submission assessment known evidence catalog is required")
	}
	requested := make(map[string]struct{})
	for index := range plan.RelationshipTargets {
		for _, evidenceID := range plan.RelationshipTargets[index].KnownEvidenceIDs {
			requested[evidenceID] = struct{}{}
		}
	}
	if len(requested) == 0 {
		return nil
	}
	knownCatalog, ok := s.catalog.(SubmissionAssessmentKnownEvidenceCatalog)
	if !ok {
		return errors.New("submission assessment known evidence catalog is required")
	}
	ids := make([]string, 0, len(requested))
	for evidenceID := range requested {
		ids = append(ids, evidenceID)
	}
	sort.Strings(ids)
	loaded, err := knownCatalog.ListSubmissionAssessmentKnownEvidence(ctx, repository.SubmissionAssessmentKnownEvidenceInput{
		TeamID: scope.TeamID, OwnerProfileID: scope.OwnerProfileID, SpaceID: scope.SpaceID, EvidenceIDs: ids,
	})
	if err != nil {
		return submissionAssessmentDatabaseError("load submission known evidence", err)
	}
	if plan.knownEvidenceByID == nil {
		plan.knownEvidenceByID = map[string]repository.SubmissionAssessmentKnownEvidence{}
	}
	loadedIDs := make(map[string]struct{}, len(loaded.Evidence))
	knownEvidenceRunes := 0
	for _, item := range loaded.Evidence {
		item.EvidenceID = strings.TrimSpace(item.EvidenceID)
		item.FragmentID = strings.TrimSpace(item.FragmentID)
		if _, requested := requested[item.EvidenceID]; !requested {
			return deterministicSemanticAssessmentPreflightError("known_evidence_catalog", "submission known evidence catalog returned an unrequested item")
		}
		if item.EvidenceID == "" || item.FragmentID == "" || item.EvidenceID != item.FragmentID || strings.TrimSpace(item.Content) == "" {
			return deterministicSemanticAssessmentPreflightError("known_evidence_catalog", "submission known evidence catalog is invalid")
		}
		contentRunes := len([]rune(item.Content))
		if contentRunes > assessor.SemanticAssessmentMaxKnownEvidenceRunes-knownEvidenceRunes {
			observed := knownEvidenceRunes + contentRunes
			return deterministicSemanticAssessmentPreflightErrorWithMeasurement(
				"known_evidence_context",
				"submission known evidence exceeds its configured aggregate content bound",
				assessor.FailureMeasurement{Unit: "runes", Observed: observed, Limit: assessor.SemanticAssessmentMaxKnownEvidenceRunes},
			)
		}
		knownEvidenceRunes += contentRunes
		if _, duplicate := loadedIDs[item.EvidenceID]; duplicate {
			return deterministicSemanticAssessmentPreflightError("known_evidence_catalog", "submission known evidence catalog returned a duplicate item")
		}
		loadedIDs[item.EvidenceID] = struct{}{}
		plan.knownEvidenceByID[item.EvidenceID] = item
	}
	for index := range plan.RelationshipTargets {
		target := &plan.RelationshipTargets[index]
		filtered := make([]string, 0, len(target.KnownEvidenceIDs))
		missing := false
		for _, evidenceID := range target.KnownEvidenceIDs {
			if _, ok := plan.knownEvidenceByID[evidenceID]; !ok {
				missing = true
				continue
			}
			filtered = append(filtered, evidenceID)
		}
		target.KnownEvidenceIDs = filtered
		target.Target.KnownEvidenceIDs = append([]string(nil), filtered...)
		target.KnownEvidenceUnavailable = missing
		target.Target.KnownEvidenceUnavailable = missing
		plan.relationshipsByRef[target.Target.ProposalID] = *target
	}
	for index := range plan.EntityTargets {
		target := &plan.EntityTargets[index]
		filtered := make([]string, 0, len(target.Target.KnownEvidenceIDs))
		for _, evidenceID := range target.Target.KnownEvidenceIDs {
			if _, ok := plan.knownEvidenceByID[evidenceID]; ok {
				filtered = append(filtered, evidenceID)
			}
		}
		target.Target.KnownEvidenceIDs = filtered
		plan.entityTargetsByRef[target.Target.Ref] = *target
	}
	return nil
}

func (s *assessmentEngine) submissionAssessmentPredicateOptions(
	ctx context.Context,
	scope RememberAssessmentScope,
	plan submissionAssessmentPlan,
) ([]assessor.SemanticAssessmentPredicateOption, error) {
	limit := s.limits.MaxPredicateOptions
	if limit <= 0 {
		limit = assessor.DefaultSemanticAssessmentLimits().MaxPredicateOptions
	}
	if limit > assessor.SemanticAssessmentMaxPredicateOptions {
		limit = assessor.SemanticAssessmentMaxPredicateOptions
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
		TeamID:         scope.TeamID,
		OwnerProfileID: scope.OwnerProfileID,
		Predicates:     proposedKeys,
		Limit:          limit + 1,
	})
	if err != nil {
		return nil, submissionAssessmentDatabaseError("resolve submission predicate candidates", err)
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
			assessor.FailureMeasurement{Unit: "candidates", Observed: len(candidates), Limit: limit},
		)
	}

	ranked, err := s.catalog.ListSemanticAssessmentPredicateOptions(ctx, repository.SemanticAssessmentPredicateOptionsInput{
		TeamID:         scope.TeamID,
		OwnerProfileID: scope.OwnerProfileID,
		QueryText:      strings.Join(queryParts, "\n"),
		ProposedKeys:   proposedKeys,
		Limit:          limit,
	})
	if err != nil {
		return nil, submissionAssessmentDatabaseError("load submission predicate options", err)
	}
	for _, candidate := range ranked {
		if len(candidates) == limit {
			break
		}
		appendCandidate(candidate)
	}

	options := make([]assessor.SemanticAssessmentPredicateOption, 0, len(candidates))
	for _, candidate := range candidates {
		options = append(options, assessor.SemanticAssessmentPredicateOption{
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

func submissionAssessmentDatabaseError(operation string, err error) error {
	return fmt.Errorf("%s: %w", operation, errors.Join(rememberapp.ErrRememberDatabaseFailure, err))
}

func submissionAssessmentGroundedEntities(
	plan submissionAssessmentPlan,
	catalog repository.SubmissionAssessmentEntityCatalogResult,
	evidence []assessor.SemanticReviewEvidence,
	knownEvidence ...map[string]assessor.SemanticReviewEvidence,
) ([]assessor.SemanticAssessmentRequiredEntityRef, []assessor.SemanticAssessmentEntityCandidateGroup, error) {
	groupsByRef := make(map[string]repository.SubmissionAssessmentEntityCatalogGroup, len(catalog.Groups))
	for _, group := range catalog.Groups {
		if _, exists := groupsByRef[group.Ref]; exists || !group.Complete {
			return nil, nil, errors.New("entity catalog group is duplicated or incomplete")
		}
		groupsByRef[group.Ref] = group
	}
	evidenceByID := make(map[string]assessor.SemanticReviewEvidence, len(evidence))
	for _, item := range evidence {
		evidenceByID[item.EvidenceID] = item
	}
	if len(knownEvidence) > 0 {
		for evidenceID, item := range knownEvidence[0] {
			evidenceByID[evidenceID] = item
		}
	}
	contractEntities := make([]assessor.SemanticAssessmentRequiredEntityRef, 0, len(plan.EntityTargets))
	groups := make([]assessor.SemanticAssessmentEntityCandidateGroup, 0, len(plan.EntityTargets))
	for entityIndex, entity := range plan.EntityTargets {
		catalogGroup, ok := groupsByRef[entity.Target.Ref]
		if !ok {
			return nil, nil, errors.New("entity catalog target is missing")
		}
		if entity.KnownEntityID != "" {
			knownCandidate := false
			for _, candidate := range catalogGroup.Candidates {
				if candidate.EntityID == entity.KnownEntityID {
					knownCandidate = true
					break
				}
			}
			if !knownCandidate {
				return nil, nil, fmt.Errorf("%w: exact entity catalog target is no longer active", errSubmissionAssessmentStaleInput)
			}
		}
		if len(catalogGroup.Candidates) > assessor.SemanticAssessmentMaxEntityCandidatesPerSurface {
			return nil, nil, errors.New("entity catalog candidate bound is exceeded")
		}
		allowedNames := map[string]submissionAssessmentGroundingName{}
		anchorNames := map[string]submissionAssessmentGroundingName{}
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
		addAnchorName := func(name string, candidates []repository.SemanticReviewEntityCandidate) {
			if !submissionAssessmentGroundingNameAllowed(name) || len(candidates) == 0 {
				return
			}
			normalized := submissionAssessmentNormalizedEntityName(name)
			if normalized == "" {
				return
			}
			entry := anchorNames[normalized]
			if entry.Name == "" {
				entry.Name = strings.TrimSpace(name)
			}
			if entry.Candidates == nil {
				entry.Candidates = map[string]repository.SemanticReviewEntityCandidate{}
			}
			for _, candidate := range candidates {
				entry.Candidates[candidate.EntityID] = candidate
			}
			anchorNames[normalized] = entry
		}
		target := entity.Target
		target.KnownEntityID = entity.KnownEntityID
		if target.Name == "" && len(catalogGroup.Candidates) == 1 {
			target.Name = catalogGroup.Candidates[0].CanonicalName
		}
		if target.Kind == "" {
			candidateKinds := make(map[string]struct{}, len(catalogGroup.Candidates))
			for _, candidate := range catalogGroup.Candidates {
				if kind := strings.TrimSpace(candidate.EntityKind); kind != "" {
					candidateKinds[kind] = struct{}{}
				}
			}
			if len(candidateKinds) == 1 {
				for kind := range candidateKinds {
					target.Kind = kind
				}
			} else {
				// A missing kind is a non-authoritative hint. Keep the closed
				// assessor contract valid while allowing the selected candidate's
				// catalog kind to determine reuse below.
				target.Kind = string(domain.EntityKindOther)
			}
		}
		if target.Name == "" || target.Kind == "" {
			return nil, nil, errors.New("known entity catalog target has no canonical identity")
		}
		addName(target.Name, catalogGroup.Candidates)
		for _, candidate := range catalogGroup.Candidates {
			addAnchorName(candidate.CanonicalName, []repository.SemanticReviewEntityCandidate{candidate})
			for _, activeName := range candidate.ActiveNames {
				addName(activeName, []repository.SemanticReviewEntityCandidate{candidate})
				addAnchorName(activeName, []repository.SemanticReviewEntityCandidate{candidate})
			}
		}
		target.Groundings = []assessor.SemanticAssessmentEntityGrounding{}
		target.Anchors = []assessor.SemanticAssessmentEntityAnchor{}
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
				startRef, startOK := assessor.SemanticAssessmentBoundaryRef(evidenceItem, occurrence.Start)
				endRef, endOK := assessor.SemanticAssessmentBoundaryRef(evidenceItem, occurrence.End)
				if !startOK || !endOK {
					return nil, nil, errors.New("entity grounding boundary is missing")
				}
				groundingRef := fmt.Sprintf("g%d_%d", entityIndex, groundingIndex)
				groundingIndex++
				target.Groundings = append(target.Groundings, assessor.SemanticAssessmentEntityGrounding{
					GroundingRef: groundingRef,
					EvidenceID:   evidenceID,
					Surface:      occurrence.Surface,
					StartRef:     startRef,
					EndRef:       endRef,
					Start:        occurrence.Start,
					End:          occurrence.End,
				})
				if len(target.Groundings) > assessor.SemanticAssessmentMaxEntityGroundings {
					return nil, nil, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
						"catalog_context",
						"submission assessment entity grounding bound is exceeded",
						assessor.FailureMeasurement{Unit: "groundings", Observed: len(target.Groundings), Limit: assessor.SemanticAssessmentMaxEntityGroundings},
					)
				}
				candidateIDs := make([]string, 0, len(occurrence.Candidates))
				for candidateID := range occurrence.Candidates {
					candidateIDs = append(candidateIDs, candidateID)
				}
				sort.Strings(candidateIDs)
				group := assessor.SemanticAssessmentEntityCandidateGroup{
					Surface:      occurrence.Surface,
					EvidenceID:   evidenceID,
					GroundingRef: groundingRef,
					Start:        occurrence.Start,
					End:          occurrence.End,
					Candidates:   make([]assessor.SemanticAssessmentEntityCandidate, 0, len(candidateIDs)),
				}
				for _, candidateID := range candidateIDs {
					candidate := occurrence.Candidates[candidateID]
					identityContext := map[string]any{}
					for field, value := range candidate.IdentityContext {
						identityContext[field] = value
					}
					group.Candidates = append(group.Candidates, assessor.SemanticAssessmentEntityCandidate{
						EntityID:        candidate.EntityID,
						CanonicalName:   candidate.CanonicalName,
						Kind:            candidate.EntityKind,
						IdentityContext: identityContext,
					})
				}
				groups = append(groups, group)
			}
		}
		anchorIndex := 0
		for _, evidenceID := range append(append([]string(nil), target.EvidenceIDs...), target.KnownEvidenceIDs...) {
			evidenceItem, ok := evidenceByID[evidenceID]
			if !ok {
				continue
			}
			occurrences := map[string]submissionAssessmentGroundingOccurrence{}
			for _, allowed := range anchorNames {
				for _, span := range submissionAssessmentEntityNameOccurrences(evidenceItem.Content, allowed.Name) {
					key := fmt.Sprintf("%d:%d", span[0], span[1])
					occurrence := occurrences[key]
					occurrence.Start, occurrence.End = span[0], span[1]
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
				if len(occurrence.Candidates) == 0 {
					continue
				}
				startRef, startOK := assessor.SemanticAssessmentBoundaryRef(evidenceItem, occurrence.Start)
				endRef, endOK := assessor.SemanticAssessmentBoundaryRef(evidenceItem, occurrence.End)
				if !startOK || !endOK {
					return nil, nil, errors.New("entity anchor boundary is missing")
				}
				candidateIDs := make([]string, 0, len(occurrence.Candidates))
				for candidateID := range occurrence.Candidates {
					candidateIDs = append(candidateIDs, candidateID)
				}
				sort.Strings(candidateIDs)
				anchorRef := fmt.Sprintf("a%d_%d", entityIndex, anchorIndex)
				anchorIndex++
				target.Anchors = append(target.Anchors, assessor.SemanticAssessmentEntityAnchor{
					AnchorRef: anchorRef, EvidenceID: evidenceID, Surface: occurrence.Surface,
					StartRef: startRef, EndRef: endRef, CandidateEntityIDs: candidateIDs,
					Start: occurrence.Start, End: occurrence.End,
				})
				if len(target.Anchors) > assessor.SemanticAssessmentMaxEntityGroundings {
					return nil, nil, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
						"catalog_context",
						"submission assessment entity anchor bound is exceeded",
						assessor.FailureMeasurement{Unit: "anchors", Observed: len(target.Anchors), Limit: assessor.SemanticAssessmentMaxEntityGroundings},
					)
				}
			}
		}
		for _, evidenceID := range target.EvidenceIDs {
			evidenceItem, ok := evidenceByID[evidenceID]
			if !ok {
				continue
			}
			for _, pronoun := range submissionAssessmentPronounOccurrences(evidenceItem.Content) {
				anchor, ok := submissionAssessmentPronounAnchor(target.Anchors, evidenceByID, evidenceItem, pronoun[0])
				if !ok {
					continue
				}
				startRef, startOK := assessor.SemanticAssessmentBoundaryRef(evidenceItem, pronoun[0])
				endRef, endOK := assessor.SemanticAssessmentBoundaryRef(evidenceItem, pronoun[1])
				if !startOK || !endOK {
					return nil, nil, errors.New("entity coreference boundary is missing")
				}
				groundingRef := fmt.Sprintf("g%d_coref_%d", entityIndex, groundingIndex)
				groundingIndex++
				surface := string([]rune(evidenceItem.Content)[pronoun[0]:pronoun[1]])
				target.Groundings = append(target.Groundings, assessor.SemanticAssessmentEntityGrounding{
					GroundingRef: groundingRef, EvidenceID: evidenceID, Surface: surface,
					StartRef: startRef, EndRef: endRef, AnchorRef: anchor.AnchorRef,
					Start: pronoun[0], End: pronoun[1],
				})
				groups = append(groups, assessor.SemanticAssessmentEntityCandidateGroup{
					Surface: surface, EvidenceID: evidenceID, GroundingRef: groundingRef,
					Start: pronoun[0], End: pronoun[1], Candidates: append([]assessor.SemanticAssessmentEntityCandidate(nil), candidateGroupCandidatesForAnchor(anchor, catalogGroup.Candidates)...),
				})
				if len(target.Groundings) > assessor.SemanticAssessmentMaxEntityGroundings {
					return nil, nil, deterministicSemanticAssessmentPreflightErrorWithMeasurement(
						"catalog_context",
						"submission assessment entity grounding bound is exceeded",
						assessor.FailureMeasurement{Unit: "groundings", Observed: len(target.Groundings), Limit: assessor.SemanticAssessmentMaxEntityGroundings},
					)
				}
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

func candidateGroupCandidatesForAnchor(
	anchor assessor.SemanticAssessmentEntityAnchor,
	candidates []repository.SemanticReviewEntityCandidate,
) []assessor.SemanticAssessmentEntityCandidate {
	allowed := make(map[string]struct{}, len(anchor.CandidateEntityIDs))
	for _, candidateID := range anchor.CandidateEntityIDs {
		allowed[candidateID] = struct{}{}
	}
	result := make([]assessor.SemanticAssessmentEntityCandidate, 0, len(allowed))
	for _, candidate := range candidates {
		if _, ok := allowed[candidate.EntityID]; !ok {
			continue
		}
		identityContext := map[string]any{}
		for field, value := range candidate.IdentityContext {
			identityContext[field] = value
		}
		result = append(result, assessor.SemanticAssessmentEntityCandidate{
			EntityID: candidate.EntityID, CanonicalName: candidate.CanonicalName,
			Kind: candidate.EntityKind, IdentityContext: identityContext,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EntityID < result[j].EntityID })
	return result
}

func submissionAssessmentPronounAnchor(
	anchors []assessor.SemanticAssessmentEntityAnchor,
	evidenceByID map[string]assessor.SemanticReviewEvidence,
	pronounEvidence assessor.SemanticReviewEvidence,
	pronounStart int,
) (assessor.SemanticAssessmentEntityAnchor, bool) {
	candidateIDs := make(map[string]struct{})
	anchorSurfaces := make(map[string]struct{})
	var selected assessor.SemanticAssessmentEntityAnchor
	var selectedEvidence assessor.SemanticReviewEvidence
	found := false
	for _, anchor := range anchors {
		anchorEvidence, ok := evidenceByID[anchor.EvidenceID]
		if !ok || len(anchor.CandidateEntityIDs) == 0 {
			continue
		}
		if anchorEvidence.EvidenceIndex > pronounEvidence.EvidenceIndex || (anchorEvidence.EvidenceIndex == pronounEvidence.EvidenceIndex && anchor.End > pronounStart) {
			continue
		}
		for _, candidateID := range anchor.CandidateEntityIDs {
			candidateIDs[candidateID] = struct{}{}
		}
		anchorSurfaces[submissionAssessmentNormalizedEntityName(anchor.Surface)] = struct{}{}
		if !found || anchorEvidence.EvidenceIndex > selectedEvidence.EvidenceIndex ||
			(anchorEvidence.EvidenceIndex == selectedEvidence.EvidenceIndex && anchor.End > selected.End) ||
			(anchorEvidence.EvidenceIndex == selectedEvidence.EvidenceIndex && anchor.End == selected.End && anchor.Start > selected.Start) ||
			(anchorEvidence.EvidenceIndex == selectedEvidence.EvidenceIndex && anchor.End == selected.End && anchor.Start == selected.Start && anchor.AnchorRef > selected.AnchorRef) {
			selected = anchor
			selectedEvidence = anchorEvidence
			found = true
		}
	}
	// Repeated mentions of one normalized name share an antecedent; distinct
	// canonical or alias names remain ambiguous even when they resolve alike.
	if !found || len(candidateIDs) != 1 || len(anchorSurfaces) != 1 {
		return assessor.SemanticAssessmentEntityAnchor{}, false
	}
	return selected, true
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

func submissionAssessmentPronounOccurrences(content string) [][2]int {
	pronouns := []string{
		"i", "me", "my", "mine", "myself", "you", "your", "yours", "yourself", "yourselves",
		"he", "him", "his", "himself", "she", "her", "hers", "herself", "it", "its", "itself",
		"we", "us", "our", "ours", "ourselves", "they", "them", "their", "theirs", "themselves",
		"this", "that", "these", "those",
	}
	seen := map[string]struct{}{}
	result := make([][2]int, 0)
	for _, pronoun := range pronouns {
		for _, span := range submissionAssessmentEntityNameOccurrences(content, pronoun) {
			key := fmt.Sprintf("%d:%d", span[0], span[1])
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, span)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i][0] != result[j][0] {
			return result[i][0] < result[j][0]
		}
		return result[i][1] < result[j][1]
	})
	return result
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
