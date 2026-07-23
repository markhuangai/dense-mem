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
	v2SemanticReviewSourceDefaultCandidateLimit = 5
	v2SemanticProposalDefaultMaxAttempts        = 5
)

type V2SemanticPlacementReviewCatalog interface {
	ListV2SemanticReviewEntityCandidates(ctx context.Context, input repository.V2SemanticReviewEntityCandidateInput) ([]repository.V2SemanticReviewEntityCandidate, error)
	ListV2SemanticReviewPredicateCandidates(ctx context.Context, input repository.V2SemanticReviewPredicateCandidateInput) ([]repository.V2SemanticReviewPredicateCandidate, error)
	ListV2SemanticReviewPredicateOptions(ctx context.Context, input repository.V2SemanticReviewPredicateOptionsInput) ([]string, error)
}

type V2SemanticProposalProvider interface {
	ProposeV2Semantic(ctx context.Context, req verifier.V2ProviderProposalRequest) (verifier.V2ProviderProposal, error)
	ModelName() string
}

type V2SemanticPlacementReviewSourceDependencies struct {
	Ledger           repository.V2LedgerRepository
	Catalog          V2SemanticPlacementReviewCatalog
	ProposalProvider V2SemanticProposalProvider
	CandidateLimit   int
}

type v2SemanticPlacementReviewSource struct {
	ledger           repository.V2LedgerRepository
	catalog          V2SemanticPlacementReviewCatalog
	proposalProvider V2SemanticProposalProvider
	candidateLimit   int
}

type v2PlacementReviewEntityHint struct {
	Ref             string
	Name            string
	EntityKind      string
	KnownEntityID   string
	IdentityContext map[string]any
	Evidence        []v2PlacementReviewEvidenceSpanHint
}

type v2PlacementReviewRelationshipSpec struct {
	Ref                 string
	SubjectRef          string
	SubjectName         string
	Predicate           string
	PredicateCandidates []string
	RelationshipKind    string
	ObjectRef           string
	ObjectName          string
	ObjectValue         *verifier.V2SemanticValueObservation
	EvidenceID          string
	Quote               string
	Start               int
	End                 int
	ValidFrom           *time.Time
	ValidTo             *time.Time
	CorrectionTarget    *verifier.V2RelationshipCorrectionTarget
}

func NewV2SemanticPlacementReviewSource(deps V2SemanticPlacementReviewSourceDependencies) V2SemanticPlacementReviewSource {
	limit := deps.CandidateLimit
	if limit <= 0 {
		limit = v2SemanticReviewSourceDefaultCandidateLimit
	}
	return &v2SemanticPlacementReviewSource{
		ledger:           deps.Ledger,
		catalog:          deps.Catalog,
		proposalProvider: deps.ProposalProvider,
		candidateLimit:   limit,
	}
}

func (s *v2SemanticPlacementReviewSource) BuildV2SemanticReviewJob(
	ctx context.Context,
	run repository.V2PlacementRun,
) (V2SemanticReviewJob, error) {
	if s.ledger == nil {
		return V2SemanticReviewJob{}, errors.New("v2 semantic review source: ledger repository is required")
	}
	if s.catalog == nil {
		return V2SemanticReviewJob{}, errors.New("v2 semantic review source: catalog repository is required")
	}
	placement, err := s.ledger.GetPlacementRun(ctx, repository.V2GetPlacementRunInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		IngestID:       run.IngestID,
	})
	if err != nil {
		return V2SemanticReviewJob{}, err
	}
	item, fragment, ok := nextV2SemanticReviewPlacementItem(placement)
	if !ok {
		return V2SemanticReviewJob{}, errors.New("v2 semantic review source: no claimable placement item")
	}
	evidenceID := fmt.Sprintf("evidence:%d", fragment.EvidenceIndex)
	evidence := v2SemanticReviewEvidence(fragment, evidenceID)
	proposal := placement.Proposal
	trustedCorrectionTargets := v2ReviewSourceCorrectionTargets(proposal)
	entityHints := v2PlacementReviewEntityHints(proposal)
	var validationErrors []verifier.V2SemanticValidationError
	providerProposal, validationErrors, retryable, err := s.v2PlacementReviewProviderProposal(ctx, run, evidence, proposal)
	if err != nil {
		return v2SemanticReviewRetryablePreflightJob(run, item, evidence, []verifier.V2SemanticValidationError{{
			Field:   "provider_proposal",
			Message: "semantic extraction provider failed",
		}}), nil
	}
	if len(validationErrors) > 0 {
		if retryable {
			return v2SemanticReviewRetryablePreflightJob(run, item, evidence, validationErrors), nil
		}
		return v2SemanticReviewPreflightFailureJob(run, item, evidence, validationErrors), nil
	}
	if providerProposal != nil {
		proposal = v2ReviewSourceProposalFromProvider(*providerProposal)
		proposal = v2ReviewSourceProposalWithTrustedCorrectionTargets(proposal, trustedCorrectionTargets)
		entityHints = v2PlacementReviewEntityHints(proposal)
	}
	relationships, validationErrors := v2PlacementReviewRelationshipSpecs(proposal, fragment, evidenceID)
	if len(validationErrors) > 0 {
		return v2SemanticReviewPreflightFailureJob(run, item, evidence, validationErrors), nil
	}
	mentions, err := s.v2PlacementReviewEntityMentions(ctx, run, fragment, entityHints, relationships, evidenceID)
	if err != nil {
		return V2SemanticReviewJob{}, err
	}
	observations, err := s.v2PlacementReviewRelationshipObservations(ctx, run, relationships, mentions)
	if err != nil {
		return V2SemanticReviewJob{}, err
	}
	return V2SemanticReviewJob{
		TeamID:          run.TeamID,
		OwnerProfileID:  run.OwnerProfileID,
		IngestID:        run.IngestID,
		PlacementRunID:  run.PlacementRunID,
		PlacementItemID: item.PlacementItemID,
		MaxAttempts:     run.MaxAttempts,
		Request: verifier.V2SemanticReviewRequest{
			RequestID:                "semantic-review:" + item.PlacementItemID,
			TeamID:                   run.TeamID,
			OwnerProfileID:           run.OwnerProfileID,
			Evidence:                 []verifier.V2SemanticReviewEvidence{evidence},
			EntityMentions:           mentions,
			RelationshipObservations: observations,
		},
	}, nil
}

func (s *v2SemanticPlacementReviewSource) v2PlacementReviewProviderProposal(
	ctx context.Context,
	run repository.V2PlacementRun,
	evidence verifier.V2SemanticReviewEvidence,
	clientProposal map[string]any,
) (*verifier.V2ProviderProposal, []verifier.V2SemanticValidationError, bool, error) {
	if s.proposalProvider == nil {
		return nil, []verifier.V2SemanticValidationError{{
			Field:   "proposal",
			Message: "extraction provider is required when proposal has no relationship hints",
		}}, false, nil
	}
	predicateOptions, err := s.catalog.ListV2SemanticReviewPredicateOptions(ctx, repository.V2SemanticReviewPredicateOptionsInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
	})
	if err != nil {
		return nil, nil, false, err
	}
	req, validationErrors := verifier.PrepareV2ProviderProposalRequest(verifier.V2ProviderProposalRequest{
		RequestID:         "semantic-extraction:" + evidence.FragmentID,
		Evidence:          []verifier.V2SemanticReviewEvidence{evidence},
		EntityHints:       v2PlacementReviewObjectArray(clientProposal, "entity_hints", "entities"),
		RelationshipHints: v2PlacementReviewObjectArray(clientProposal, "relationship_hints", "relationships"),
		PredicateOptions:  predicateOptions,
	})
	if len(validationErrors) > 0 {
		return nil, validationErrors, false, nil
	}
	var lastValidationErrors []verifier.V2SemanticValidationError
	feedback := []string{}
	maxAttempts := v2SemanticProposalDefaultMaxAttempts
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptReq := req
		attemptReq.Attempt = attempt
		attemptReq.ValidationFeedback = feedback
		proposal, err := s.proposalProvider.ProposeV2Semantic(ctx, attemptReq)
		if err != nil {
			if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
				lastValidationErrors = v2SemanticMalformedValidationErrors("provider_proposal", err)
				feedback = v2SemanticValidationMessages(lastValidationErrors)
				continue
			}
			return nil, nil, false, err
		}
		if validationErrors := verifier.ValidateV2ProviderProposal(req, proposal); len(validationErrors) > 0 {
			lastValidationErrors = validationErrors
			feedback = v2SemanticValidationMessages(validationErrors)
			continue
		}
		return &proposal, nil, false, nil
	}
	return nil, lastValidationErrors, true, nil
}

func v2SemanticReviewPreflightFailureJob(
	run repository.V2PlacementRun,
	item repository.V2PlacementItem,
	evidence verifier.V2SemanticReviewEvidence,
	validationErrors []verifier.V2SemanticValidationError,
) V2SemanticReviewJob {
	return V2SemanticReviewJob{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		PlacementItemID:  item.PlacementItemID,
		MaxAttempts:      run.MaxAttempts,
		ValidationErrors: validationErrors,
		Request: verifier.V2SemanticReviewRequest{
			RequestID:      "semantic-review:" + item.PlacementItemID,
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			Evidence:       []verifier.V2SemanticReviewEvidence{evidence},
		},
	}
}

func v2SemanticReviewRetryablePreflightJob(
	run repository.V2PlacementRun,
	item repository.V2PlacementItem,
	evidence verifier.V2SemanticReviewEvidence,
	validationErrors []verifier.V2SemanticValidationError,
) V2SemanticReviewJob {
	job := v2SemanticReviewPreflightFailureJob(run, item, evidence, nil)
	job.RetryableValidationErrors = validationErrors
	return job
}

func v2ReviewSourceProposalFromProvider(proposal verifier.V2ProviderProposal) map[string]any {
	entities := make([]map[string]any, 0, len(proposal.EntityProposals))
	for _, entity := range proposal.EntityProposals {
		item := map[string]any{
			"ref":              entity.Ref,
			"name":             entity.Name,
			"entity_kind":      entity.EntityKind,
			"identity_context": entity.IdentityContext,
			"evidence":         v2ReviewSourceEvidenceSpans(entity.Evidence),
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
			"evidence":             v2ReviewSourceEvidenceSpans(relationship.Evidence),
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

func v2ReviewSourceEvidenceSpans(spans []verifier.V2ProviderEvidenceSpan) []map[string]any {
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

func nextV2SemanticReviewPlacementItem(placement *repository.V2CreateIngestResult) (repository.V2PlacementItem, repository.V2EvidenceFragment, bool) {
	if placement == nil {
		return repository.V2PlacementItem{}, repository.V2EvidenceFragment{}, false
	}
	evidenceByIndex := map[int]repository.V2EvidenceFragment{}
	for _, fragment := range placement.Evidence {
		evidenceByIndex[fragment.EvidenceIndex] = fragment
	}
	for _, item := range placement.Items {
		status := strings.TrimSpace(item.Status)
		if status != string(domain.V2PlacementRunQueued) && status != string(domain.V2PlacementRunProcessing) {
			continue
		}
		fragment, ok := evidenceByIndex[item.EvidenceIndex]
		if !ok || strings.TrimSpace(fragment.Content) == "" {
			return repository.V2PlacementItem{}, repository.V2EvidenceFragment{}, false
		}
		return item, fragment, true
	}
	return repository.V2PlacementItem{}, repository.V2EvidenceFragment{}, false
}

func v2SemanticReviewEvidence(fragment repository.V2EvidenceFragment, evidenceID string) verifier.V2SemanticReviewEvidence {
	return verifier.V2SemanticReviewEvidence{
		EvidenceID:              evidenceID,
		FragmentID:              fragment.FragmentID,
		EvidenceIndex:           fragment.EvidenceIndex,
		Content:                 fragment.Content,
		SourceID:                fragment.SourceID,
		SourceRevisionID:        fragment.SourceRevisionID,
		CurrentSourceRevisionID: fragment.SourceRevisionID,
	}
}

func v2PlacementReviewEntityHints(proposal map[string]any) map[string]v2PlacementReviewEntityHint {
	out := map[string]v2PlacementReviewEntityHint{}
	for _, raw := range v2PlacementReviewObjectArray(proposal, "entity_hints", "entities") {
		ref := v2ReviewString(raw, "ref")
		name := v2ReviewString(raw, "name")
		if ref == "" {
			ref = name
		}
		if ref == "" {
			continue
		}
		identityContext, _ := v2ReviewMap(raw["identity_context"])
		out[ref] = v2PlacementReviewEntityHint{
			Ref:             ref,
			Name:            name,
			EntityKind:      v2ReviewFirstNonEmpty(v2ReviewString(raw, "entity_kind"), string(domain.V2EntityKindOther)),
			KnownEntityID:   v2ReviewString(raw, "known_entity_id"),
			IdentityContext: identityContext,
			Evidence:        v2PlacementReviewEvidenceSpanHints(raw),
		}
	}
	return out
}

func v2PlacementReviewRelationshipSpecs(
	proposal map[string]any,
	fragment repository.V2EvidenceFragment,
	evidenceID string,
) ([]v2PlacementReviewRelationshipSpec, []verifier.V2SemanticValidationError) {
	rawRelationships := v2PlacementReviewObjectArray(proposal, "relationship_hints", "relationships")
	out := make([]v2PlacementReviewRelationshipSpec, 0, len(rawRelationships))
	var validationErrors []verifier.V2SemanticValidationError
	for i, raw := range rawRelationships {
		span, ok := v2PlacementReviewRelationshipSpan(raw, fragment)
		if !ok {
			continue
		}
		spec := v2PlacementReviewRelationshipSpec{
			Ref:                 v2ReviewFirstNonEmpty(v2ReviewString(raw, "proposal_id"), v2ReviewString(raw, "ref"), v2ReviewString(raw, "legacy_id"), fmt.Sprintf("relationship:%d", i)),
			SubjectRef:          v2ReviewFirstNonEmpty(v2ReviewString(raw, "subject_ref"), v2ReviewString(raw, "subject")),
			SubjectName:         v2ReviewFirstNonEmpty(v2ReviewString(raw, "subject_name"), v2ReviewString(raw, "subject")),
			Predicate:           v2ReviewString(raw, "predicate"),
			PredicateCandidates: v2ReviewStringArray(raw, "predicate_candidates"),
			RelationshipKind:    v2ReviewString(raw, "relationship_kind"),
			ObjectRef:           v2ReviewString(raw, "object_ref"),
			ObjectName:          v2ReviewString(raw, "object_name"),
			EvidenceID:          evidenceID,
			Quote:               span.quote,
			Start:               span.start,
			End:                 span.end,
		}
		validFrom, err := v2ReviewOptionalTime(raw, "valid_from")
		if err != nil {
			validationErrors = append(validationErrors, verifier.V2SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].valid_from", i),
				Message: err.Error(),
			})
			continue
		}
		validTo, err := v2ReviewOptionalTime(raw, "valid_to")
		if err != nil {
			validationErrors = append(validationErrors, verifier.V2SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].valid_to", i),
				Message: err.Error(),
			})
			continue
		}
		if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
			validationErrors = append(validationErrors, verifier.V2SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].valid_to", i),
				Message: "must not be before valid_from",
			})
			continue
		}
		spec.ValidFrom = validFrom
		spec.ValidTo = validTo
		if spec.ObjectRef == "" && raw["object_value"] == nil {
			spec.ObjectRef = v2ReviewString(raw, "object")
			spec.ObjectName = v2ReviewFirstNonEmpty(spec.ObjectName, v2ReviewString(raw, "object"))
		}
		if objectValue, ok := v2PlacementReviewObjectValue(raw, spec.Ref); ok {
			spec.ObjectValue = &objectValue
			spec.ObjectRef = ""
		}
		if target, ok := v2PlacementReviewCorrectionTarget(raw); ok {
			spec.CorrectionTarget = &target
		}
		if spec.SubjectRef == "" || spec.Predicate == "" || (spec.ObjectRef == "" && spec.ObjectValue == nil) {
			continue
		}
		if len(spec.PredicateCandidates) == 0 {
			validationErrors = append(validationErrors, verifier.V2SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].predicate_candidates", i),
				Message: "is required",
			})
			continue
		}
		if spec.RelationshipKind == "" {
			validationErrors = append(validationErrors, verifier.V2SemanticValidationError{
				Field:   fmt.Sprintf("relationship_hints[%d].relationship_kind", i),
				Message: "is required",
			})
			continue
		}
		out = append(out, spec)
	}
	return out, validationErrors
}

type v2PlacementReviewSpan struct {
	start int
	end   int
	quote string
}

type v2PlacementReviewEvidenceSpanHint struct {
	evidenceIndex int
	start         int
	end           int
}

func v2PlacementReviewEvidenceSpanHints(raw map[string]any) []v2PlacementReviewEvidenceSpanHint {
	spans := v2PlacementReviewObjectArray(raw, "evidence")
	out := make([]v2PlacementReviewEvidenceSpanHint, 0, len(spans))
	for _, span := range spans {
		index, hasIndex := v2ReviewInt(span, "evidence_index")
		start, hasStart := v2ReviewInt(span, "start")
		end, hasEnd := v2ReviewInt(span, "end")
		if !hasIndex || !hasStart || !hasEnd {
			continue
		}
		out = append(out, v2PlacementReviewEvidenceSpanHint{
			evidenceIndex: index,
			start:         start,
			end:           end,
		})
	}
	return out
}

func v2PlacementReviewRelationshipSpan(raw map[string]any, fragment repository.V2EvidenceFragment) (v2PlacementReviewSpan, bool) {
	spans := v2PlacementReviewObjectArray(raw, "evidence")
	for _, span := range spans {
		index, hasIndex := v2ReviewInt(span, "evidence_index")
		if hasIndex && index != fragment.EvidenceIndex {
			continue
		}
		start, hasStart := v2ReviewInt(span, "start")
		end, hasEnd := v2ReviewInt(span, "end")
		if hasStart && hasEnd {
			return v2PlacementReviewSpan{
				start: start,
				end:   end,
				quote: v2ReviewSpanQuote(fragment.Content, start, end),
			}, true
		}
		quote := v2ReviewString(span, "quote")
		if quote == "" {
			continue
		}
		if start, end, ok := v2ReviewFindSpan(fragment.Content, quote); ok {
			return v2PlacementReviewSpan{start: start, end: end, quote: quote}, true
		}
	}
	if len(spans) > 0 {
		return v2PlacementReviewSpan{}, false
	}
	return v2PlacementReviewSpan{start: 0, end: utf8.RuneCountInString(fragment.Content)}, true
}

func (s *v2SemanticPlacementReviewSource) v2PlacementReviewEntityMentions(
	ctx context.Context,
	run repository.V2PlacementRun,
	fragment repository.V2EvidenceFragment,
	entityHints map[string]v2PlacementReviewEntityHint,
	relationships []v2PlacementReviewRelationshipSpec,
	evidenceID string,
) ([]verifier.V2SemanticEntityMention, error) {
	ordered := make([]v2PlacementReviewEntityHint, 0, len(relationships)*2)
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
			hint = v2PlacementReviewEntityHint{
				Ref:        ref,
				Name:       fallbackName,
				EntityKind: string(domain.V2EntityKindOther),
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
	mentions := make([]verifier.V2SemanticEntityMention, 0, len(ordered))
	for _, hint := range ordered {
		mention := verifier.V2SemanticEntityMention{
			Ref:             hint.Ref,
			Surface:         hint.Name,
			Kind:            v2ReviewFirstNonEmpty(hint.EntityKind, string(domain.V2EntityKindOther)),
			EvidenceID:      evidenceID,
			Start:           0,
			End:             1,
			Candidates:      nil,
			IdentityContext: hint.IdentityContext,
		}
		if start, end, ok := v2PlacementReviewEntityHintSpan(fragment, hint); ok {
			mention.Start = start
			mention.End = end
			mention.Surface = v2ReviewSpanQuote(fragment.Content, start, end)
		} else if start, end, ok := v2ReviewFindSpan(fragment.Content, hint.Name); ok {
			mention.Start = start
			mention.End = end
			mention.Surface = v2ReviewSpanQuote(fragment.Content, start, end)
		} else if end := min(utf8.RuneCountInString(hint.Name), utf8.RuneCountInString(fragment.Content)); end > 0 {
			mention.End = end
			mention.Surface = v2ReviewSpanQuote(fragment.Content, mention.Start, mention.End)
		}
		candidates, err := s.catalog.ListV2SemanticReviewEntityCandidates(ctx, repository.V2SemanticReviewEntityCandidateInput{
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
			mention.Candidates = append(mention.Candidates, verifier.V2SemanticEntityCandidate{
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

func v2PlacementReviewEntityHintSpan(fragment repository.V2EvidenceFragment, hint v2PlacementReviewEntityHint) (int, int, bool) {
	for _, span := range hint.Evidence {
		if span.evidenceIndex != fragment.EvidenceIndex {
			continue
		}
		if quote := v2ReviewSpanQuote(fragment.Content, span.start, span.end); strings.TrimSpace(quote) != "" {
			return span.start, span.end, true
		}
	}
	return 0, 0, false
}

func (s *v2SemanticPlacementReviewSource) v2PlacementReviewRelationshipObservations(
	ctx context.Context,
	run repository.V2PlacementRun,
	relationships []v2PlacementReviewRelationshipSpec,
	mentions []verifier.V2SemanticEntityMention,
) ([]verifier.V2SemanticRelationshipObservation, error) {
	out := make([]verifier.V2SemanticRelationshipObservation, 0, len(relationships))
	kindByRef := map[string]string{}
	for _, mention := range mentions {
		if mention.Ref != "" && mention.Kind != "" {
			kindByRef[mention.Ref] = mention.Kind
		}
	}
	for _, relationship := range relationships {
		candidates, err := s.catalog.ListV2SemanticReviewPredicateCandidates(ctx, repository.V2SemanticReviewPredicateCandidateInput{
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			Predicate:      relationship.Predicate,
			Limit:          s.candidateLimit,
		})
		if err != nil {
			return nil, err
		}
		obs := verifier.V2SemanticRelationshipObservation{
			Ref:                 relationship.Ref,
			SubjectRef:          relationship.SubjectRef,
			OriginalPredicate:   relationship.Predicate,
			ObjectRef:           relationship.ObjectRef,
			ObjectValue:         relationship.ObjectValue,
			EvidenceID:          relationship.EvidenceID,
			Quote:               relationship.Quote,
			Start:               relationship.Start,
			End:                 relationship.End,
			ValidFrom:           relationship.ValidFrom,
			ValidTo:             relationship.ValidTo,
			CorrectionTarget:    relationship.CorrectionTarget,
			PredicateCandidates: nil,
		}
		seenCandidates := map[string]struct{}{}
		appendCandidate := func(candidate repository.V2SemanticReviewPredicateCandidate) {
			key := strings.TrimSpace(candidate.PredicateKey)
			if key == "" || candidate.Version <= 0 {
				return
			}
			dedupeKey := fmt.Sprintf("%s:%d", key, candidate.Version)
			if _, exists := seenCandidates[dedupeKey]; exists {
				return
			}
			seenCandidates[dedupeKey] = struct{}{}
			obs.PredicateCandidates = append(obs.PredicateCandidates, verifier.V2SemanticPredicateCandidate{
				PredicateKey:        candidate.PredicateKey,
				Version:             candidate.Version,
				AllowedSubjectKinds: candidate.AllowedSubjectKinds,
				AllowedObjectKinds:  candidate.AllowedObjectKinds,
				RelationshipKind:    candidate.RelationshipKind,
				CurrentCardinality:  candidate.CurrentCardinality,
				LifecycleState:      candidate.LifecycleState,
			})
		}
		for _, candidate := range candidates {
			appendCandidate(candidate)
		}
		for _, predicate := range relationship.PredicateCandidates {
			appendCandidate(v2ReviewSourceRequestLocalPredicateCandidate(predicate, relationship, kindByRef))
		}
		out = append(out, obs)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

func v2ReviewSourceRelationshipObjectKind(relationship v2PlacementReviewRelationshipSpec, kindByRef map[string]string) string {
	if relationship.ObjectValue != nil {
		return strings.TrimSpace(relationship.ObjectValue.Type)
	}
	return strings.TrimSpace(kindByRef[relationship.ObjectRef])
}

func v2PlacementReviewObjectValue(raw map[string]any, relationshipRef string) (verifier.V2SemanticValueObservation, bool) {
	objectValue, ok := v2ReviewMap(raw["object_value"])
	if !ok {
		return verifier.V2SemanticValueObservation{}, false
	}
	valueType := v2ReviewString(objectValue, "type")
	value := v2ReviewAnyString(objectValue["value"])
	if valueType == "" || value == "" {
		return verifier.V2SemanticValueObservation{}, false
	}
	return verifier.V2SemanticValueObservation{
		Ref:     v2ReviewFirstNonEmpty(v2ReviewString(objectValue, "ref"), "value:"+relationshipRef),
		Type:    valueType,
		Value:   value,
		Display: v2ReviewAnyString(objectValue["display"]),
		Unit:    v2ReviewAnyString(objectValue["unit"]),
	}, true
}

func v2PlacementReviewCorrectionTarget(raw map[string]any) (verifier.V2RelationshipCorrectionTarget, bool) {
	target, ok := v2ReviewMap(raw["correction_target"])
	if !ok {
		return verifier.V2RelationshipCorrectionTarget{}, false
	}
	relationshipID := v2ReviewString(target, "relationship_id")
	expectedVersion, ok := v2ReviewInt(target, "expected_version")
	if relationshipID == "" || !ok {
		return verifier.V2RelationshipCorrectionTarget{}, false
	}
	return verifier.V2RelationshipCorrectionTarget{
		RelationshipID:  relationshipID,
		ExpectedVersion: expectedVersion,
	}, true
}

func v2PlacementReviewObjectArray(raw map[string]any, keys ...string) []map[string]any {
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
				if fields, ok := v2ReviewMap(item); ok {
					out = append(out, fields)
				}
			}
			return out
		}
	}
	return nil
}

func v2ReviewMap(raw any) (map[string]any, bool) {
	fields, ok := raw.(map[string]any)
	return fields, ok
}

func v2ReviewString(fields map[string]any, key string) string {
	if fields == nil {
		return ""
	}
	return v2ReviewAnyString(fields[key])
}

func v2ReviewStringArray(fields map[string]any, key string) []string {
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
			add(v2ReviewAnyString(value))
		}
	case string:
		add(typed)
	}
	return out
}

func v2ReviewAnyString(raw any) string {
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

func v2ReviewFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func v2ReviewInt(fields map[string]any, key string) (int, bool) {
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

func v2ReviewOptionalTime(fields map[string]any, key string) (*time.Time, error) {
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

func v2ReviewFindSpan(content string, quote string) (int, int, bool) {
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

func v2ReviewSpanQuote(content string, start int, end int) string {
	runes := []rune(content)
	if start < 0 || end <= start || end > len(runes) {
		return ""
	}
	return string(runes[start:end])
}
