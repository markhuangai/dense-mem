package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const (
	semanticReviewSourceDefaultCandidateLimit = 5
	semanticProposalDefaultMaxAttempts        = 5
)

type SemanticPlacementReviewCatalog interface {
	ListSemanticReviewEntityCandidates(ctx context.Context, input repository.SemanticReviewEntityCandidateInput) ([]repository.SemanticReviewEntityCandidate, error)
	ResolveSemanticReviewPredicateCandidates(ctx context.Context, input repository.SemanticReviewPredicateResolutionInput) ([]repository.SemanticReviewPredicateResolution, error)
	ListSemanticReviewPredicateOptions(ctx context.Context, input repository.SemanticReviewPredicateOptionsInput) ([]string, error)
}

type SemanticProposalProvider interface {
	ProposeSemantic(ctx context.Context, req verifier.ProviderProposalRequest) (verifier.ProviderProposal, error)
	ModelName() string
}

type SemanticPlacementReviewSourceDependencies struct {
	Ledger           repository.LedgerRepository
	Catalog          SemanticPlacementReviewCatalog
	ProposalProvider SemanticProposalProvider
	CandidateLimit   int
}

type semanticPlacementReviewSource struct {
	ledger           repository.LedgerRepository
	catalog          SemanticPlacementReviewCatalog
	proposalProvider SemanticProposalProvider
	candidateLimit   int
}

type placementReviewEntityHint struct {
	Ref             string
	Name            string
	EntityKind      string
	KnownEntityID   string
	IdentityContext map[string]any
	Evidence        []placementReviewEvidenceSpanHint
}

type placementReviewRelationshipSpec struct {
	Ref                 string
	SubjectRef          string
	SubjectName         string
	Predicate           string
	PredicateCandidates []string
	RelationshipKind    string
	Polarity            string
	ObjectRef           string
	ObjectName          string
	ObjectValue         *verifier.SemanticValueObservation
	EvidenceID          string
	Quote               string
	Start               int
	End                 int
	ValidFrom           *time.Time
	ValidTo             *time.Time
	CorrectionTarget    *verifier.RelationshipCorrectionTarget
	ConflictContext     *verifier.RelationshipConflictContext
}

type semanticFailureDescriptor struct {
	Stage string
	Class string
}

func NewSemanticPlacementReviewSource(deps SemanticPlacementReviewSourceDependencies) SemanticPlacementReviewSource {
	limit := deps.CandidateLimit
	if limit <= 0 {
		limit = semanticReviewSourceDefaultCandidateLimit
	}
	return &semanticPlacementReviewSource{
		ledger:           deps.Ledger,
		catalog:          deps.Catalog,
		proposalProvider: deps.ProposalProvider,
		candidateLimit:   limit,
	}
}

func (s *semanticPlacementReviewSource) BuildSemanticReviewJob(
	ctx context.Context,
	run repository.PlacementRun,
) (SemanticReviewJob, error) {
	if s.ledger == nil {
		return SemanticReviewJob{}, errors.New("semantic review source: ledger repository is required")
	}
	if s.catalog == nil {
		return SemanticReviewJob{}, errors.New("semantic review source: catalog repository is required")
	}
	placement, err := s.ledger.GetPlacementRun(ctx, repository.GetPlacementRunInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		IngestID:       run.IngestID,
	})
	if err != nil {
		return SemanticReviewJob{}, err
	}
	item, fragment, ok := nextSemanticReviewPlacementItem(placement)
	if !ok {
		return SemanticReviewJob{}, errors.New("semantic review source: no claimable placement item")
	}
	evidenceID := fmt.Sprintf("evidence:%d", fragment.EvidenceIndex)
	evidence := semanticReviewEvidence(fragment, evidenceID)
	proposal := placement.Proposal
	if validationErrors := reviewSourceConflictContextShapeErrors(proposal); len(validationErrors) > 0 {
		return semanticReviewPreflightFailureJob(run, item, evidence, validationErrors), nil
	}
	trustedCorrectionTargets := reviewSourceCorrectionTargets(proposal)
	trustedConflictContexts := reviewSourceConflictContexts(proposal)
	if validationErrors := s.validateReviewSourceConflictContexts(ctx, run, trustedConflictContexts); len(validationErrors) > 0 {
		return semanticReviewPreflightFailureJob(run, item, evidence, validationErrors), nil
	}
	entityHints := placementReviewEntityHints(proposal)
	var validationErrors []verifier.SemanticValidationError
	providerProposal, validationErrors, retryable, failure, err := s.placementReviewProviderProposal(ctx, run, evidence, proposal)
	if err != nil {
		return semanticReviewRetryablePreflightJob(run, item, evidence, []verifier.SemanticValidationError{{
			Field:   "provider_proposal",
			Message: "semantic extraction provider failed",
		}}, failure), nil
	}
	if len(validationErrors) > 0 {
		if retryable {
			return semanticReviewRetryablePreflightJob(run, item, evidence, validationErrors, failure), nil
		}
		return semanticReviewPreflightFailureJob(run, item, evidence, validationErrors), nil
	}
	if providerProposal != nil {
		proposal = reviewSourceProposalFromProvider(*providerProposal)
		proposal = reviewSourceProposalWithTrustedCorrectionTargets(proposal, trustedCorrectionTargets)
		var reattachErrors []verifier.SemanticValidationError
		proposal, reattachErrors = reviewSourceProposalWithTrustedConflictContexts(proposal, trustedConflictContexts)
		if len(reattachErrors) > 0 {
			return semanticReviewPreflightFailureJob(run, item, evidence, reattachErrors), nil
		}
		entityHints = placementReviewEntityHints(proposal)
	}
	relationships, validationErrors := placementReviewRelationshipSpecs(proposal, fragment, evidenceID)
	if len(validationErrors) > 0 {
		return semanticReviewPreflightFailureJob(run, item, evidence, validationErrors), nil
	}
	mentions, err := s.placementReviewEntityMentions(ctx, run, fragment, entityHints, relationships, evidenceID)
	if err != nil {
		return SemanticReviewJob{}, err
	}
	observations, err := s.placementReviewRelationshipObservations(ctx, run, relationships, mentions)
	if err != nil {
		return SemanticReviewJob{}, err
	}
	return SemanticReviewJob{
		TeamID:          run.TeamID,
		OwnerProfileID:  run.OwnerProfileID,
		IngestID:        run.IngestID,
		PlacementRunID:  run.PlacementRunID,
		PlacementItemID: item.PlacementItemID,
		MaxAttempts:     run.MaxAttempts,
		Request: verifier.SemanticReviewRequest{
			RequestID:                "semantic-review:" + item.PlacementItemID,
			TeamID:                   run.TeamID,
			OwnerProfileID:           run.OwnerProfileID,
			Evidence:                 []verifier.SemanticReviewEvidence{evidence},
			EntityMentions:           mentions,
			RelationshipObservations: observations,
		},
	}, nil
}

func (s *semanticPlacementReviewSource) placementReviewProviderProposal(
	ctx context.Context,
	run repository.PlacementRun,
	evidence verifier.SemanticReviewEvidence,
	clientProposal map[string]any,
) (*verifier.ProviderProposal, []verifier.SemanticValidationError, bool, semanticFailureDescriptor, error) {
	if s.proposalProvider == nil {
		return nil, []verifier.SemanticValidationError{{
			Field:   "proposal",
			Message: "extraction provider is required when proposal has no relationship hints",
		}}, false, semanticFailureDescriptor{}, nil
	}
	predicateOptions, err := s.catalog.ListSemanticReviewPredicateOptions(ctx, repository.SemanticReviewPredicateOptionsInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		QueryText:      evidence.Content,
	})
	if err != nil {
		return nil, nil, false, semanticFailureDescriptor{
			Stage: semanticFailureStagePredicateCatalog,
			Class: semanticFailureClassLookupFailed,
		}, err
	}
	req, validationErrors := verifier.PrepareProviderProposalRequest(verifier.ProviderProposalRequest{
		RequestID:         "semantic-extraction:" + evidence.FragmentID,
		Evidence:          []verifier.SemanticReviewEvidence{evidence},
		EntityHints:       placementReviewObjectArray(clientProposal, "entity_hints", "entities"),
		RelationshipHints: placementReviewObjectArray(clientProposal, "relationship_hints", "relationships"),
		PredicateOptions:  predicateOptions,
	})
	if len(validationErrors) > 0 {
		return nil, validationErrors, false, semanticFailureDescriptor{}, nil
	}
	var lastValidationErrors []verifier.SemanticValidationError
	feedback := []string{}
	maxAttempts := semanticProposalDefaultMaxAttempts
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptReq := req
		attemptReq.Attempt = attempt
		attemptReq.ValidationFeedback = feedback
		proposal, err := s.proposalProvider.ProposeSemantic(ctx, attemptReq)
		if err != nil {
			if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
				lastValidationErrors = semanticMalformedValidationErrors("provider_proposal", err)
				feedback = semanticValidationMessages(lastValidationErrors)
				continue
			}
			return nil, nil, false, semanticFailureDescriptor{
				Stage: semanticFailureStageExtraction,
				Class: semanticProviderFailureClass(err),
			}, err
		}
		if validationErrors := verifier.ValidateProviderProposal(req, proposal); len(validationErrors) > 0 {
			lastValidationErrors = validationErrors
			feedback = semanticValidationMessages(validationErrors)
			continue
		}
		return &proposal, nil, false, semanticFailureDescriptor{}, nil
	}
	return nil, lastValidationErrors, true, semanticFailureDescriptor{
		Stage: semanticFailureStageExtraction,
		Class: semanticFailureClassMalformedResponse,
	}, nil
}

func semanticReviewPreflightFailureJob(
	run repository.PlacementRun,
	item repository.PlacementItem,
	evidence verifier.SemanticReviewEvidence,
	validationErrors []verifier.SemanticValidationError,
) SemanticReviewJob {
	return SemanticReviewJob{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		PlacementItemID:  item.PlacementItemID,
		MaxAttempts:      run.MaxAttempts,
		ValidationErrors: validationErrors,
		Request: verifier.SemanticReviewRequest{
			RequestID:      "semantic-review:" + item.PlacementItemID,
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			Evidence:       []verifier.SemanticReviewEvidence{evidence},
		},
	}
}

func semanticReviewRetryablePreflightJob(
	run repository.PlacementRun,
	item repository.PlacementItem,
	evidence verifier.SemanticReviewEvidence,
	validationErrors []verifier.SemanticValidationError,
	failure semanticFailureDescriptor,
) SemanticReviewJob {
	job := semanticReviewPreflightFailureJob(run, item, evidence, nil)
	job.RetryableValidationErrors = validationErrors
	job.FailureStage = failure.Stage
	job.FailureClass = failure.Class
	return job
}

func reviewSourceProposalFromProvider(proposal verifier.ProviderProposal) map[string]any {
	entities := make([]map[string]any, 0, len(proposal.EntityProposals))
	for _, entity := range proposal.EntityProposals {
		item := map[string]any{
			"ref":              entity.Ref,
			"name":             entity.Name,
			"entity_kind":      entity.EntityKind,
			"identity_context": entity.IdentityContext,
			"evidence":         reviewSourceEvidenceSpans(entity.Evidence),
		}
		if entity.KnownEntityID != nil {
			item["known_entity_id"] = *entity.KnownEntityID
		}
		entities = append(entities, item)
	}
	relationships := make([]map[string]any, 0, len(proposal.RelationshipProposals))
	for _, relationship := range proposal.RelationshipProposals {
		item := map[string]any{
			"proposal_id":          relationship.ProposalID,
			"subject_ref":          relationship.SubjectRef,
			"predicate":            relationship.OriginalPredicate,
			"predicate_candidates": relationship.PredicateCandidates,
			"relationship_kind":    relationship.RelationshipKind,
			"polarity":             relationship.Polarity,
			"modality":             relationship.Modality,
			"evidence":             reviewSourceEvidenceSpans(relationship.Evidence),
		}
		if relationship.ObjectRef != "" {
			item["object_ref"] = relationship.ObjectRef
		}
		if relationship.ObjectValue != nil {
			item["object_value"] = map[string]any{
				"ref":     relationship.ObjectValue.Ref,
				"type":    relationship.ObjectValue.Type,
				"value":   relationship.ObjectValue.Value,
				"display": relationship.ObjectValue.Display,
				"unit":    relationship.ObjectValue.Unit,
			}
		}
		if relationship.ValidFrom != nil {
			item["valid_from"] = *relationship.ValidFrom
		}
		if relationship.ValidTo != nil {
			item["valid_to"] = *relationship.ValidTo
		}
		relationships = append(relationships, item)
	}
	return map[string]any{
		"entity_hints":       entities,
		"relationship_hints": relationships,
	}
}

func reviewSourceEvidenceSpans(spans []verifier.ProviderEvidenceSpan) []map[string]any {
	out := make([]map[string]any, 0, len(spans))
	for _, span := range spans {
		out = append(out, map[string]any{
			"evidence_index": span.EvidenceIndex,
			"start":          span.Start,
			"end":            span.End,
		})
	}
	return out
}

func nextSemanticReviewPlacementItem(placement *repository.CreateIngestResult) (repository.PlacementItem, repository.EvidenceFragment, bool) {
	if placement == nil {
		return repository.PlacementItem{}, repository.EvidenceFragment{}, false
	}
	evidenceByIndex := map[int]repository.EvidenceFragment{}
	for _, fragment := range placement.Evidence {
		evidenceByIndex[fragment.EvidenceIndex] = fragment
	}
	for _, item := range placement.Items {
		status := strings.TrimSpace(item.Status)
		if status != string(domain.PlacementRunQueued) && status != string(domain.PlacementRunProcessing) {
			continue
		}
		fragment, ok := evidenceByIndex[item.EvidenceIndex]
		if !ok || strings.TrimSpace(fragment.Content) == "" {
			return repository.PlacementItem{}, repository.EvidenceFragment{}, false
		}
		return item, fragment, true
	}
	return repository.PlacementItem{}, repository.EvidenceFragment{}, false
}

func semanticReviewEvidence(fragment repository.EvidenceFragment, evidenceID string) verifier.SemanticReviewEvidence {
	return verifier.SemanticReviewEvidence{
		EvidenceID:              evidenceID,
		FragmentID:              fragment.FragmentID,
		EvidenceIndex:           fragment.EvidenceIndex,
		Content:                 fragment.Content,
		Authority:               fragment.Authority,
		SourceID:                fragment.SourceID,
		SourceRevisionID:        fragment.SourceRevisionID,
		CurrentSourceRevisionID: fragment.SourceRevisionID,
	}
}

func placementReviewEntityHints(proposal map[string]any) map[string]placementReviewEntityHint {
	out := map[string]placementReviewEntityHint{}
	for _, raw := range placementReviewObjectArray(proposal, "entity_hints", "entities") {
		ref := reviewString(raw, "ref")
		name := reviewString(raw, "name")
		if ref == "" {
			ref = name
		}
		if ref == "" {
			continue
		}
		identityContext, _ := reviewMap(raw["identity_context"])
		out[ref] = placementReviewEntityHint{
			Ref:             ref,
			Name:            name,
			EntityKind:      reviewFirstNonEmpty(reviewString(raw, "entity_kind"), string(domain.EntityKindOther)),
			KnownEntityID:   reviewString(raw, "known_entity_id"),
			IdentityContext: identityContext,
			Evidence:        placementReviewEvidenceSpanHints(raw),
		}
	}
	for index, relationship := range placementReviewObjectArray(proposal, "relationship_hints", "relationships") {
		relationshipRef := reviewFirstNonEmpty(
			reviewString(relationship, "ref"),
			reviewString(relationship, "proposal_id"),
			fmt.Sprintf("relationship:%d", index),
		)
		addInlineEntity := func(role string, raw any) {
			entity, ok := reviewMap(raw)
			if !ok {
				return
			}
			name := reviewString(entity, "name")
			if name == "" {
				return
			}
			ref := relationshipRef + ":" + role
			out[ref] = placementReviewEntityHint{
				Ref:           ref,
				Name:          name,
				EntityKind:    reviewFirstNonEmpty(reviewString(entity, "entity_kind"), string(domain.EntityKindOther)),
				KnownEntityID: reviewString(entity, "known_entity_id"),
				Evidence:      placementReviewEvidenceSpanHints(entity),
			}
		}
		addInlineEntity("subject", relationship["subject"])
		if object, ok := reviewMap(relationship["object"]); ok {
			addInlineEntity("object", object["entity"])
		}
	}
	return out
}

func placementReviewRelationshipSpecs(
	proposal map[string]any,
	fragment repository.EvidenceFragment,
	evidenceID string,
) ([]placementReviewRelationshipSpec, []verifier.SemanticValidationError) {
	rawRelationships := placementReviewObjectArray(proposal, "relationship_hints", "relationships")
	out := make([]placementReviewRelationshipSpec, 0, len(rawRelationships))
	var validationErrors []verifier.SemanticValidationError
	for i, raw := range rawRelationships {
		span, ok := placementReviewRelationshipSpan(raw, fragment)
		if !ok {
			continue
		}
		spec := placementReviewRelationshipSpec{
			Ref:                 reviewFirstNonEmpty(reviewString(raw, "proposal_id"), reviewString(raw, "ref"), reviewString(raw, "legacy_id"), fmt.Sprintf("relationship:%d", i)),
			SubjectRef:          reviewFirstNonEmpty(reviewString(raw, "subject_ref"), reviewString(raw, "subject")),
			SubjectName:         reviewFirstNonEmpty(reviewString(raw, "subject_name"), reviewString(raw, "subject")),
			Predicate:           reviewString(raw, "predicate"),
			PredicateCandidates: reviewStringArray(raw, "predicate_candidates"),
			RelationshipKind:    reviewString(raw, "relationship_kind"),
			Polarity:            reviewString(raw, "polarity"),
			ObjectRef:           reviewString(raw, "object_ref"),
			ObjectName:          reviewString(raw, "object_name"),
			EvidenceID:          evidenceID,
			Quote:               span.quote,
			Start:               span.start,
			End:                 span.end,
		}
		if spec.Polarity == "" {
			spec.Polarity = "+"
		}
		if spec.Polarity != "+" && spec.Polarity != "-" {
			validationErrors = append(validationErrors, verifier.SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].polarity", i),
				Message: "is unsupported",
			})
			continue
		}
		validFrom, err := reviewOptionalTime(raw, "valid_from")
		if err != nil {
			validationErrors = append(validationErrors, verifier.SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].valid_from", i),
				Message: err.Error(),
			})
			continue
		}
		validTo, err := reviewOptionalTime(raw, "valid_to")
		if err != nil {
			validationErrors = append(validationErrors, verifier.SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].valid_to", i),
				Message: err.Error(),
			})
			continue
		}
		if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
			validationErrors = append(validationErrors, verifier.SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].valid_to", i),
				Message: "must not be before valid_from",
			})
			continue
		}
		spec.ValidFrom = validFrom
		spec.ValidTo = validTo
		if spec.ObjectRef == "" && raw["object_value"] == nil {
			spec.ObjectRef = reviewString(raw, "object")
			spec.ObjectName = reviewFirstNonEmpty(spec.ObjectName, reviewString(raw, "object"))
		}
		if objectValue, ok := placementReviewObjectValue(raw, spec.Ref); ok {
			spec.ObjectValue = &objectValue
			spec.ObjectRef = ""
		}
		if target, ok := placementReviewCorrectionTarget(raw); ok {
			spec.CorrectionTarget = &target
		}
		if _, exists := raw["conflict_context"]; exists {
			context, ok := placementReviewConflictContext(raw)
			if !ok {
				validationErrors = append(validationErrors, verifier.SemanticValidationError{
					Field:   fmt.Sprintf("relationship_hints[%d].conflict_context", i),
					Message: "must include conflict_id and expected_version",
				})
				continue
			}
			spec.ConflictContext = &context
		}
		if spec.SubjectRef == "" || spec.Predicate == "" || (spec.ObjectRef == "" && spec.ObjectValue == nil) {
			continue
		}
		if len(spec.PredicateCandidates) == 0 {
			validationErrors = append(validationErrors, verifier.SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].predicate_candidates", i),
				Message: "is required",
			})
			continue
		}
		if spec.RelationshipKind == "" {
			validationErrors = append(validationErrors, verifier.SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].relationship_kind", i),
				Message: "is required",
			})
			continue
		}
		out = append(out, spec)
	}
	return out, validationErrors
}

type placementReviewSpan struct {
	start int
	end   int
	quote string
}

type placementReviewEvidenceSpanHint struct {
	evidenceIndex int
	start         int
	end           int
}

func placementReviewEvidenceSpanHints(raw map[string]any) []placementReviewEvidenceSpanHint {
	spans := append(
		placementReviewObjectArray(raw, "evidence"),
		placementReviewObjectArray(raw, "supports")...,
	)
	if span, ok := reviewMap(raw["span"]); ok {
		spans = append(spans, span)
	}
	out := make([]placementReviewEvidenceSpanHint, 0, len(spans))
	for _, span := range spans {
		index, hasIndex := reviewInt(span, "evidence_index")
		start, hasStart := reviewInt(span, "start")
		end, hasEnd := reviewInt(span, "end")
		if !hasIndex || !hasStart || !hasEnd {
			continue
		}
		out = append(out, placementReviewEvidenceSpanHint{
			evidenceIndex: index,
			start:         start,
			end:           end,
		})
	}
	return out
}

func placementReviewRelationshipSpan(raw map[string]any, fragment repository.EvidenceFragment) (placementReviewSpan, bool) {
	spans := placementReviewObjectArray(raw, "supports", "evidence")
	for _, span := range spans {
		index, hasIndex := reviewInt(span, "evidence_index")
		if hasIndex && index != fragment.EvidenceIndex {
			continue
		}
		start, hasStart := reviewInt(span, "start")
		end, hasEnd := reviewInt(span, "end")
		if hasStart && hasEnd {
			return placementReviewSpan{
				start: start,
				end:   end,
				quote: reviewSpanQuote(fragment.Content, start, end),
			}, true
		}
		quote := reviewString(span, "quote")
		if quote == "" {
			continue
		}
		if start, end, ok := reviewFindSpan(fragment.Content, quote); ok {
			return placementReviewSpan{start: start, end: end, quote: quote}, true
		}
	}
	if len(spans) > 0 {
		return placementReviewSpan{}, false
	}
	return placementReviewSpan{start: 0, end: utf8.RuneCountInString(fragment.Content)}, true
}

func (s *semanticPlacementReviewSource) placementReviewEntityMentions(
	ctx context.Context,
	run repository.PlacementRun,
	fragment repository.EvidenceFragment,
	entityHints map[string]placementReviewEntityHint,
	relationships []placementReviewRelationshipSpec,
	evidenceID string,
) ([]verifier.SemanticEntityMention, error) {
	ordered := make([]placementReviewEntityHint, 0, len(relationships)*2)
	seen := map[string]struct{}{}
	add := func(ref, fallbackName string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		if _, exists := seen[ref]; exists {
			return
		}
		seen[ref] = struct{}{}
		hint, ok := entityHints[ref]
		if !ok {
			hint = placementReviewEntityHint{
				Ref:        ref,
				Name:       fallbackName,
				EntityKind: string(domain.EntityKindOther),
			}
		}
		if hint.Name == "" {
			hint.Name = fallbackName
		}
		ordered = append(ordered, hint)
	}
	for _, relationship := range relationships {
		add(relationship.SubjectRef, relationship.SubjectName)
		add(relationship.ObjectRef, relationship.ObjectName)
	}
	mentions := make([]verifier.SemanticEntityMention, 0, len(ordered))
	for _, hint := range ordered {
		mention := verifier.SemanticEntityMention{
			Ref:             hint.Ref,
			Surface:         hint.Name,
			Kind:            reviewFirstNonEmpty(hint.EntityKind, string(domain.EntityKindOther)),
			EvidenceID:      evidenceID,
			Start:           0,
			End:             1,
			Candidates:      nil,
			IdentityContext: hint.IdentityContext,
		}
		if start, end, ok := placementReviewEntityHintSpan(fragment, hint); ok {
			mention.Start = start
			mention.End = end
			mention.Surface = reviewSpanQuote(fragment.Content, start, end)
		} else if start, end, ok := reviewFindSpan(fragment.Content, hint.Name); ok {
			mention.Start = start
			mention.End = end
			mention.Surface = reviewSpanQuote(fragment.Content, start, end)
		} else if end := min(utf8.RuneCountInString(hint.Name), utf8.RuneCountInString(fragment.Content)); end > 0 {
			mention.End = end
			mention.Surface = reviewSpanQuote(fragment.Content, mention.Start, mention.End)
		}
		candidates, err := s.catalog.ListSemanticReviewEntityCandidates(ctx, repository.SemanticReviewEntityCandidateInput{
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			Name:           hint.Name,
			EntityKind:     mention.Kind,
			KnownEntityID:  hint.KnownEntityID,
			Limit:          s.candidateLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			mention.Candidates = append(mention.Candidates, verifier.SemanticEntityCandidate{
				TeamID:          candidate.TeamID,
				EntityID:        candidate.EntityID,
				CanonicalName:   candidate.CanonicalName,
				Kind:            candidate.EntityKind,
				IdentityContext: candidate.IdentityContext,
				Status:          candidate.Status,
			})
		}
		mentions = append(mentions, mention)
	}
	return mentions, nil
}

func placementReviewEntityHintSpan(fragment repository.EvidenceFragment, hint placementReviewEntityHint) (int, int, bool) {
	for _, span := range hint.Evidence {
		if span.evidenceIndex != fragment.EvidenceIndex {
			continue
		}
		if quote := reviewSpanQuote(fragment.Content, span.start, span.end); strings.TrimSpace(quote) != "" {
			return span.start, span.end, true
		}
	}
	return 0, 0, false
}

func (s *semanticPlacementReviewSource) placementReviewRelationshipObservations(
	ctx context.Context,
	run repository.PlacementRun,
	relationships []placementReviewRelationshipSpec,
	mentions []verifier.SemanticEntityMention,
) ([]verifier.SemanticRelationshipObservation, error) {
	out := make([]verifier.SemanticRelationshipObservation, 0, len(relationships))
	kindByRef := map[string]string{}
	for _, mention := range mentions {
		if mention.Ref != "" && mention.Kind != "" {
			kindByRef[mention.Ref] = mention.Kind
		}
	}
	for _, relationship := range relationships {
		predicateLabels := reviewSourcePredicateLabels(relationship)
		resolutions, err := s.catalog.ResolveSemanticReviewPredicateCandidates(ctx, repository.SemanticReviewPredicateResolutionInput{
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			Predicates:     predicateLabels,
			Limit:          s.candidateLimit,
		})
		if err != nil {
			return nil, err
		}
		obs := verifier.SemanticRelationshipObservation{
			Ref:                 relationship.Ref,
			SubjectRef:          relationship.SubjectRef,
			OriginalPredicate:   relationship.Predicate,
			Polarity:            relationship.Polarity,
			ObjectRef:           relationship.ObjectRef,
			ObjectValue:         relationship.ObjectValue,
			EvidenceID:          relationship.EvidenceID,
			Quote:               relationship.Quote,
			Start:               relationship.Start,
			End:                 relationship.End,
			ValidFrom:           relationship.ValidFrom,
			ValidTo:             relationship.ValidTo,
			CorrectionTarget:    relationship.CorrectionTarget,
			ConflictContext:     relationship.ConflictContext,
			PredicateCandidates: nil,
		}
		seenCandidates := map[string]struct{}{}
		appendCandidate := func(candidate repository.SemanticReviewPredicateCandidate) {
			key := strings.TrimSpace(candidate.PredicateKey)
			if key == "" || candidate.Version <= 0 {
				return
			}
			dedupeKey := fmt.Sprintf("%s:%d", key, candidate.Version)
			if _, exists := seenCandidates[dedupeKey]; exists {
				return
			}
			seenCandidates[dedupeKey] = struct{}{}
			obs.PredicateCandidates = append(obs.PredicateCandidates, verifier.SemanticPredicateCandidate{
				PredicateKey:        candidate.PredicateKey,
				Version:             candidate.Version,
				AllowedSubjectKinds: candidate.AllowedSubjectKinds,
				AllowedObjectKinds:  candidate.AllowedObjectKinds,
				RelationshipKind:    candidate.RelationshipKind,
				CurrentCardinality:  candidate.CurrentCardinality,
				LifecycleState:      candidate.LifecycleState,
			})
		}
		resolutionsByLabel := reviewSourcePredicateResolutionsByLabel(resolutions)
		for _, predicate := range predicateLabels {
			candidate, matched, ambiguous := reviewSourceCanonicalPredicateCandidate(resolutionsByLabel[predicate])
			if ambiguous {
				continue
			}
			if matched {
				appendCandidate(candidate)
			} else {
				appendCandidate(reviewSourceRequestLocalPredicateCandidate(predicate, relationship, kindByRef))
			}
			if len(obs.PredicateCandidates) >= s.candidateLimit {
				break
			}
		}
		out = append(out, obs)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

func reviewSourceRelationshipObjectKind(relationship placementReviewRelationshipSpec, kindByRef map[string]string) string {
	if relationship.ObjectValue != nil {
		return strings.TrimSpace(relationship.ObjectValue.Type)
	}
	return strings.TrimSpace(kindByRef[relationship.ObjectRef])
}

func placementReviewObjectValue(raw map[string]any, relationshipRef string) (verifier.SemanticValueObservation, bool) {
	objectValue, ok := reviewMap(raw["object_value"])
	if !ok {
		return verifier.SemanticValueObservation{}, false
	}
	valueType := reviewString(objectValue, "type")
	value := reviewAnyString(objectValue["value"])
	if valueType == "" || value == "" {
		return verifier.SemanticValueObservation{}, false
	}
	return verifier.SemanticValueObservation{
		Ref:     reviewFirstNonEmpty(reviewString(objectValue, "ref"), "value:"+relationshipRef),
		Type:    valueType,
		Value:   value,
		Display: reviewAnyString(objectValue["display"]),
		Unit:    reviewAnyString(objectValue["unit"]),
	}, true
}

func placementReviewCorrectionTarget(raw map[string]any) (verifier.RelationshipCorrectionTarget, bool) {
	target, ok := reviewMap(raw["correction_target"])
	if !ok {
		return verifier.RelationshipCorrectionTarget{}, false
	}
	relationshipID := reviewString(target, "relationship_id")
	expectedVersion, ok := reviewInt(target, "expected_version")
	if relationshipID == "" || !ok {
		return verifier.RelationshipCorrectionTarget{}, false
	}
	return verifier.RelationshipCorrectionTarget{
		RelationshipID:  relationshipID,
		ExpectedVersion: expectedVersion,
	}, true
}

func placementReviewObjectArray(raw map[string]any, keys ...string) []map[string]any {
	for _, key := range keys {
		values, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := values.(type) {
		case []map[string]any:
			return typed
		case []any:
			out := make([]map[string]any, 0, len(typed))
			for _, item := range typed {
				if fields, ok := reviewMap(item); ok {
					out = append(out, fields)
				}
			}
			return out
		}
	}
	return nil
}

func reviewMap(raw any) (map[string]any, bool) {
	fields, ok := raw.(map[string]any)
	return fields, ok
}

func reviewString(fields map[string]any, key string) string {
	if fields == nil {
		return ""
	}
	return reviewAnyString(fields[key])
}

func reviewStringArray(fields map[string]any, key string) []string {
	if fields == nil {
		return nil
	}
	raw := fields[key]
	out := []string{}
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	switch typed := raw.(type) {
	case []string:
		for _, value := range typed {
			add(value)
		}
	case []any:
		for _, value := range typed {
			add(reviewAnyString(value))
		}
	case string:
		add(typed)
	}
	return out
}

func reviewAnyString(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%g", value))
	case int:
		return strings.TrimSpace(fmt.Sprint(value))
	case int64:
		return strings.TrimSpace(fmt.Sprint(value))
	case bool:
		return strings.TrimSpace(fmt.Sprint(value))
	default:
		return ""
	}
}

func reviewFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func reviewInt(fields map[string]any, key string) (int, bool) {
	if fields == nil {
		return 0, false
	}
	switch value := fields[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func reviewOptionalTime(fields map[string]any, key string) (*time.Time, error) {
	if fields == nil {
		return nil, nil
	}
	raw, exists := fields[key]
	if !exists || raw == nil {
		return nil, nil
	}
	switch value := raw.(type) {
	case time.Time:
		parsed := value.UTC()
		return &parsed, nil
	case *time.Time:
		if value == nil {
			return nil, nil
		}
		parsed := value.UTC()
		return &parsed, nil
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return nil, fmt.Errorf("must be RFC3339 timestamp")
		}
		parsed = parsed.UTC()
		return &parsed, nil
	default:
		return nil, fmt.Errorf("must be RFC3339 timestamp")
	}
}

func reviewFindSpan(content string, quote string) (int, int, bool) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return 0, 0, false
	}
	index := strings.Index(content, quote)
	if index < 0 {
		return 0, 0, false
	}
	start := utf8.RuneCountInString(content[:index])
	end := start + utf8.RuneCountInString(quote)
	return start, end, true
}

func reviewSpanQuote(content string, start int, end int) string {
	runes := []rune(content)
	if start < 0 || end <= start || end > len(runes) {
		return ""
	}
	return string(runes[start:end])
}
