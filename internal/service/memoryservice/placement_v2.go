package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/placementreview"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/assertionservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func (s *service) rememberV2(ctx context.Context, profileID string, req RememberRequest) (*RememberResult, error) {
	if s.deps.FragmentCreate == nil {
		return nil, errors.New("memory service: fragment create service is required")
	}
	if s.deps.PlacementStore == nil {
		return nil, errors.New("memory service: placement store is required")
	}
	if strings.TrimSpace(profileID) == "" {
		return nil, errors.New("memory service: team_id is required")
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("memory service: evidence is required")
	}

	actorProfileID := ""
	actorRole := ""
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok && actor.ProfileID != uuid.Nil {
		actorProfileID = actor.ProfileID.String()
	}
	if credential, ok := requestctx.ActorCredentialFromContext(ctx); ok {
		actorRole = credential.Role
	}

	ingestID := uuid.NewString()
	evidence, err := normalizeEvidenceInputs(ingestID, req.Evidence)
	if err != nil {
		return nil, err
	}
	trustedMemoryAuthority := requestctx.TrustedMemoryAuthority(ctx)
	if actorRole != "manager" && !trustedMemoryAuthority {
		for i := range evidence {
			if evidence[i].Authority == string(domain.AuthorityAuthoritative) {
				return nil, fmt.Errorf("memory service: evidence[%d].authority authoritative requires a manager API key", i)
			}
		}
	}
	for i := range evidence {
		if evidence[i].Authority == string(domain.AuthorityAuthoritative) {
			evidence[i].TrustedAuthority = actorRole == "manager" || trustedMemoryAuthority
		}
	}
	if err := validateProposal(evidence, req.Proposal); err != nil {
		return nil, err
	}
	if err := validateMigrationRefs(ctx, req.MigrationRefs); err != nil {
		return nil, err
	}
	if len(req.MigrationRefs) > 0 {
		if s.deps.Assertions == nil {
			return nil, errors.New("memory service: assertion service is required for legacy migration")
		}
		if err := s.deps.Assertions.CheckLegacyRefs(ctx, profileID, req.MigrationRefs); err != nil {
			return nil, err
		}
	}

	security := assessEvidenceInjection(evidence)
	if security.Quarantined {
		if s.deps.FragmentQuarantine == nil {
			return nil, errors.New("memory service: quarantined fragment create service is required")
		}
	}

	now := time.Now().UTC()
	run := domain.MemoryPlacementRun{
		IngestID:          ingestID,
		ProfileID:         strings.TrimSpace(profileID),
		ActorProfileID:    actorProfileID,
		ActorRole:         actorRole,
		Status:            domain.MemoryPlacementProcessing,
		CheckAfterSeconds: 5,
		StatusTool:        "get_memory_placement",
		PipelineVersion:   placementPipelineVersion,
		Evidence:          evidence,
		Proposal:          req.Proposal,
		Items:             initialPlacementItems(ingestID, profileID, req.Proposal.Relationships, now),
		ReviewTasks:       []domain.MemoryReviewTask{},
		Security:          security,
		MigrationRefs:     append([]domain.LegacyMemoryRef(nil), req.MigrationRefs...),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.deps.PlacementStore.CreateRun(ctx, run); err != nil {
		return nil, err
	}

	fragments := make([]FragmentOutcome, 0, len(evidence))
	fragmentIDs := make(map[int]string, len(evidence))
	for i := range evidence {
		fragment, err := s.createPlacementFragment(ctx, profileID, evidence[i], run.Security.Quarantined)
		if err != nil {
			return nil, s.failPlacement(ctx, &run, err)
		}
		status := "created"
		if fragment.Duplicate {
			status = "duplicate"
		}
		fragmentIDs[i] = fragment.Fragment.FragmentID
		fragments = append(fragments, FragmentOutcome{
			ID:          fragment.Fragment.FragmentID,
			Status:      status,
			DuplicateOf: fragment.DuplicateOf,
			CreatedAt:   fragment.Fragment.CreatedAt,
		})
	}
	attachFragmentIDs(run.Items, fragmentIDs)
	run.Status = domain.MemoryPlacementQueued
	run.UpdatedAt = time.Now().UTC()
	if err := s.deps.PlacementStore.SaveRun(ctx, run); err != nil {
		return nil, err
	}
	s.kickPlacementWorker()
	return &RememberResult{
		IngestID:          run.IngestID,
		Status:            string(run.Status),
		CheckAfterSeconds: run.CheckAfterSeconds,
		StatusTool:        run.StatusTool,
		Evidence:          fragments,
		Items:             run.Items,
	}, nil
}

func normalizeEvidenceInputs(ingestID string, input []EvidenceInput) ([]domain.MemoryEvidence, error) {
	out := make([]domain.MemoryEvidence, 0, len(input))
	defaultGroup := "conversation:" + ingestID
	for i, value := range input {
		content := value.Content
		if strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("memory service: evidence[%d].content is required", i)
		}
		sourceType := strings.ToLower(strings.TrimSpace(value.SourceType))
		if sourceType == "" {
			sourceType = string(domain.SourceTypeConversation)
		}
		if !domain.SourceType(sourceType).IsValid() {
			return nil, fmt.Errorf("memory service: evidence[%d].source_type is invalid", i)
		}
		authority := domain.Authority(strings.ToLower(strings.TrimSpace(value.Authority)))
		if authority == "" {
			authority = domain.AuthorityPrimary
		}
		if !authority.IsValid() {
			return nil, fmt.Errorf("memory service: evidence[%d].authority is invalid", i)
		}
		sourceGroup := strings.TrimSpace(value.SourceGroup)
		if sourceGroup == "" {
			if source := strings.TrimSpace(value.Source); source != "" {
				sourceGroup = "source:" + strings.ToLower(source)
			} else {
				sourceGroup = defaultGroup
			}
		}
		out = append(out, domain.MemoryEvidence{
			Index:          i,
			Content:        content,
			SourceType:     sourceType,
			Source:         strings.TrimSpace(value.Source),
			Authority:      string(authority),
			SourceGroup:    sourceGroup,
			IdempotencyKey: strings.TrimSpace(value.IdempotencyKey),
			Labels:         append([]string(nil), value.Labels...),
			Metadata:       value.Metadata,
		})
	}
	return out, nil
}

func initialPlacementItems(ingestID, profileID string, relationships []domain.MemoryRelationshipProposal, now time.Time) []domain.MemoryPlacementItem {
	items := make([]domain.MemoryPlacementItem, 0, len(relationships))
	for _, relationship := range relationships {
		indexes := relationshipEvidenceIndexes(relationship)
		firstIndex := -1
		if len(indexes) > 0 {
			firstIndex = indexes[0]
		}
		items = append(items, domain.MemoryPlacementItem{
			ItemID:               uuid.NewString(),
			IngestID:             ingestID,
			ProfileID:            profileID,
			EvidenceIndex:        firstIndex,
			EvidenceIndexes:      indexes,
			Category:             domain.MemoryPlacementAssertionCandidate,
			Status:               string(domain.MemoryPlacementQueued),
			Reason:               "atomic relationship queued for independent review",
			ProposedRelationship: relationship,
			CreatedAt:            now,
			UpdatedAt:            now,
		})
	}
	return items
}

func (s *service) createPlacementFragment(ctx context.Context, profileID string, evidence domain.MemoryEvidence, quarantined bool) (*fragmentservice.CreateResult, error) {
	req := &dto.CreateFragmentRequest{
		Content:        evidence.Content,
		SourceType:     evidence.SourceType,
		Source:         evidence.Source,
		Authority:      evidence.Authority,
		IdempotencyKey: evidence.IdempotencyKey,
		Labels:         evidence.Labels,
		Metadata:       evidence.Metadata,
		SourceQuality:  defaultSourceQuality(evidence.Authority),
		Classification: map[string]any{"placement_pipeline": placementPipelineVersion},
	}
	if quarantined {
		return s.deps.FragmentQuarantine.CreateQuarantined(ctx, profileID, req)
	}
	return s.deps.FragmentCreate.Create(ctx, profileID, req)
}

func attachFragmentIDs(items []domain.MemoryPlacementItem, fragmentIDs map[int]string) {
	for i := range items {
		ids := make([]string, 0, len(items[i].EvidenceIndexes))
		for _, index := range items[i].EvidenceIndexes {
			if id := fragmentIDs[index]; id != "" {
				ids = append(ids, id)
			}
		}
		items[i].FragmentIDs = ids
		if len(ids) > 0 {
			items[i].FragmentID = ids[0]
		}
	}
}

func (s *service) processPlacementRunV2(ctx context.Context, run *domain.MemoryPlacementRun) error {
	if run == nil {
		return nil
	}
	ctx = placementMetricContext(ctx, run)
	if run.PipelineVersion != placementPipelineVersion {
		return s.processLegacyPlacementRun(ctx, run)
	}
	started := time.Now().UTC()
	run.Status = domain.MemoryPlacementProcessing
	run.StartedAt = &started
	run.UpdatedAt = started
	if err := s.deps.PlacementStore.SaveRun(ctx, *run); err != nil {
		return err
	}
	if run.Security.Quarantined {
		return s.processQuarantinedPlacement(ctx, run)
	}
	if s.deps.GraphReviewer == nil {
		return s.failPlacement(ctx, run, errors.New("memory service: graph reviewer is required"))
	}
	if s.deps.Verifier == nil {
		return s.failPlacement(ctx, run, errors.New("memory service: independent verifier is required"))
	}
	if s.deps.Assertions == nil {
		return s.failPlacement(ctx, run, errors.New("memory service: assertion service is required"))
	}
	if s.deps.Embedder == nil || !s.deps.Embedder.IsAvailable() {
		return s.failPlacement(ctx, run, errors.New("memory service: embedding provider is required"))
	}

	review, err := s.deps.GraphReviewer.ReviewGraph(ctx, placementreview.Request{
		ProfileID: run.ProfileID,
		Evidence:  run.Evidence,
		Proposal:  run.Proposal,
	})
	if err != nil {
		return s.failPlacement(ctx, run, fmt.Errorf("memory service: graph review: %w", err))
	}
	review, err = normalizeReviewedResult(run.Evidence, review)
	if err != nil {
		return s.failPlacement(ctx, run, err)
	}
	if len(review.Relationships) == 0 {
		return s.failPlacement(ctx, run, errors.New("memory service: reviewer returned no atomic relationships"))
	}

	entities, entityByRef, err := reviewedEntities(run.ProfileID, review.Entities, started)
	if err != nil {
		return s.failPlacement(ctx, run, err)
	}
	fragmentIDs := fragmentIDsByEvidence(run.Items)
	oldItems := placementItemsByProposalID(run.Items)
	items := make([]domain.MemoryPlacementItem, 0, len(review.Relationships))
	assertions := make([]domain.Assertion, 0, len(review.Relationships))
	statements := make([]string, 0, len(review.Relationships))
	reviewTasks := []domain.MemoryReviewTask{}
	transitions := []domain.AssertionTransitionEvent{}

	for _, relationship := range review.Relationships {
		statement, err := relationshipStatement(relationship.Proposal, entityByRef)
		if err != nil {
			return s.failPlacement(ctx, run, err)
		}
		contextJSON, err := verificationContext(run.Evidence, relationship.Proposal.Evidence)
		if err != nil {
			return s.failPlacement(ctx, run, err)
		}
		verification, err := s.deps.Verifier.Verify(ctx, verifier.Request{
			ProfileID: run.ProfileID,
			Predicate: statement,
			Context:   contextJSON,
			ValidFrom: relationship.Proposal.ValidFrom,
			ValidTo:   relationship.Proposal.ValidTo,
		})
		if err != nil {
			return s.failPlacement(ctx, run, fmt.Errorf("memory service: verify %s: %w", relationship.Proposal.ProposalID, err))
		}

		item := reviewedPlacementItem(run, relationship, oldItems, fragmentIDs, started)
		item.VerifierVerdict = verification.Verdict
		item.VerifierConfidence = verification.Confidence
		assertion, task, eventType, reason, err := s.assertionFromReview(ctx, run, relationship, verification, entityByRef, review.Model, item.ItemID, started)
		if err != nil {
			return s.failPlacement(ctx, run, err)
		}
		item.AssertionID = assertion.AssertionID
		item.RelationshipType = assertion.RelationshipType
		item.Tier = assertion.Tier
		item.AssertionStatus = assertion.Status
		item.PolicyFamily = assertion.PolicyFamily
		item.Category = placementCategory(assertion)
		item.Status = "completed"
		item.Reason = reason
		item.UpdatedAt = time.Now().UTC()
		if task != nil {
			task.PlacementItemID = item.ItemID
			item.ReviewTaskID = task.TaskID
			reviewTasks = append(reviewTasks, *task)
		}
		assertions = append(assertions, assertion)
		statements = append(statements, statement)
		items = append(items, item)
		transitions = append(transitions,
			transition(run, item, "proposed", "client_proposal", "placement"),
			transition(run, item, eventType, reasonCode(reason), "server_verifier"),
		)
	}

	if err := embedAssertionBundle(ctx, s.deps.Embedder, entities, assertions, statements); err != nil {
		return s.failPlacement(ctx, run, fmt.Errorf("memory service: embed assertion bundle: %w", err))
	}
	writeResult, err := s.deps.Assertions.WriteBundle(ctx, run.ProfileID, assertionservice.Bundle{Entities: entities, Assertions: assertions})
	if err != nil {
		return s.failPlacement(ctx, run, fmt.Errorf("memory service: write assertion bundle: %w", err))
	}
	for _, superseded := range writeResult.Superseded {
		transitions = append(transitions, transitionForSuperseded(run, superseded, started))
	}

	run.Proposal = reviewedProposal(review)
	run.Items = items
	run.ReviewTasks = reviewTasks
	run.UpdatedAt = time.Now().UTC()
	if len(reviewTasks) > 0 {
		run.Status = domain.MemoryPlacementAwaitingReview
		run.RequiresAck = false
		run.CompletedAt = nil
	} else {
		completed := run.UpdatedAt
		run.Status = domain.MemoryPlacementCompleted
		run.RequiresAck = true
		run.CompletedAt = &completed
	}
	return s.saveRunWithTransitions(ctx, *run, transitions)
}

func placementMetricContext(ctx context.Context, run *domain.MemoryPlacementRun) context.Context {
	if run == nil {
		return ctx
	}
	teamID, teamErr := uuid.Parse(run.ProfileID)
	actorID, actorErr := uuid.Parse(run.ActorProfileID)
	if teamErr == nil {
		actor := requestctx.ActorProfile{TeamID: teamID}
		if actorErr == nil {
			actor.ProfileID = actorID
		}
		ctx = requestctx.WithActorProfile(ctx, actor)
	}
	if run.ActorRole != "" {
		ctx = requestctx.WithActorCredential(ctx, requestctx.ActorCredential{Role: run.ActorRole})
	}
	return ctx
}

func (s *service) processQuarantinedPlacement(ctx context.Context, run *domain.MemoryPlacementRun) error {
	if s.deps.Assertions == nil {
		return s.failPlacement(ctx, run, errors.New("memory service: assertion service is required"))
	}
	now := time.Now().UTC()
	entities := make([]domain.Entity, 0, len(run.Proposal.Entities))
	entityByRef := make(map[string]domain.Entity, len(run.Proposal.Entities))
	for _, proposal := range run.Proposal.Entities {
		entity, err := assertionservice.NewEntity(run.ProfileID, proposal.Name, proposal.Type, proposal.Aliases)
		if err != nil {
			return s.failPlacement(ctx, run, err)
		}
		entity.ResolutionStatus = domain.EntityResolutionProvisional
		entity.ResolutionConf = 0
		entity.FirstSeenAt = now
		entity.LastSeenAt = now
		entities = append(entities, entity)
		entityByRef[proposal.Ref] = entity
	}
	assertions := make([]domain.Assertion, 0, len(run.Proposal.Relationships))
	transitions := []domain.AssertionTransitionEvent{}
	for i := range run.Items {
		item := &run.Items[i]
		proposal := item.ProposedRelationship
		assertion, err := baseAssertion(run, proposal, entityByRef, domain.AssertionTierCandidate, domain.AssertionStatusQuarantined, 0, 0, "", now)
		if err != nil {
			return s.failPlacement(ctx, run, err)
		}
		assertion.AssertionID = eventAssertionID(assertion.AssertionID, run.IngestID, proposal.ProposalID, "quarantined")
		item.AssertionID = assertion.AssertionID
		item.RelationshipType = assertion.RelationshipType
		item.Tier = assertion.Tier
		item.AssertionStatus = assertion.Status
		item.PolicyFamily = assertion.PolicyFamily
		item.Category = domain.MemoryPlacementAssertionQuarantine
		item.Status = "completed"
		item.Reason = "evidence quarantined before model execution"
		item.SecuritySignals = append([]string(nil), run.Security.Signals...)
		item.UpdatedAt = now
		assertions = append(assertions, assertion)
		transitions = append(transitions,
			transition(run, *item, "proposed", "client_proposal", "placement"),
			transition(run, *item, "quarantined", "prompt_injection", "security_filter"),
		)
	}
	if _, err := s.deps.Assertions.WriteBundle(ctx, run.ProfileID, assertionservice.Bundle{Entities: entities, Assertions: assertions}); err != nil {
		return s.failPlacement(ctx, run, err)
	}
	run.Status = domain.MemoryPlacementCompleted
	run.RequiresAck = true
	run.ReviewTasks = []domain.MemoryReviewTask{}
	run.UpdatedAt = now
	run.CompletedAt = &now
	return s.saveRunWithTransitions(ctx, *run, transitions)
}

func reviewedEntities(profileID string, input []placementreview.ReviewedEntity, now time.Time) ([]domain.Entity, map[string]domain.Entity, error) {
	entities := make([]domain.Entity, 0, len(input))
	byRef := make(map[string]domain.Entity, len(input))
	for _, reviewed := range input {
		entity, err := assertionservice.NewEntity(profileID, reviewed.Proposal.Name, reviewed.Proposal.Type, reviewed.Proposal.Aliases)
		if err != nil {
			return nil, nil, err
		}
		entity.ResolutionStatus = reviewed.ResolutionStatus
		entity.ResolutionConf = reviewed.ResolutionConf
		entity.FirstSeenAt = now
		entity.LastSeenAt = now
		entities = append(entities, entity)
		byRef[reviewed.Proposal.Ref] = entity
	}
	return entities, byRef, nil
}

func (s *service) assertionFromReview(ctx context.Context, run *domain.MemoryPlacementRun, reviewed placementreview.ReviewedRelationship, verification verifier.Response, entities map[string]domain.Entity, extractionModel, itemID string, now time.Time) (domain.Assertion, *domain.MemoryReviewTask, string, string, error) {
	status := domain.AssertionStatusActive
	tier := domain.AssertionTierCandidate
	eventType := "retained_candidate"
	reason := "verified as a weak candidate"
	ambiguous := reviewed.Ambiguous || relationshipHasAmbiguousEntity(reviewed.Proposal, entities)
	switch {
	case ambiguous:
		status = domain.AssertionStatusNeedsReview
		eventType = "review_requested"
		reason = "entity identity or relationship scope requires user review"
	case verification.Verdict == "contradicted":
		status = domain.AssertionStatusRejected
		eventType = "rejected"
		reason = "independent verifier contradicted the proposed relationship"
	case verification.Verdict == "insufficient":
		reason = "evidence is insufficient for claim validation; retained as a candidate"
	case verification.Verdict == "entailed":
		if hasTrustedAuthority(run, reviewed.Proposal.Evidence, reviewed.AuthorityExplicit) {
			tier = domain.AssertionTierFact
			eventType = "promoted"
			reason = "explicit authority and independent entailment satisfied the fact gate"
		} else if hasTwoSourceGroups(run.Evidence, reviewed.Proposal.Evidence) {
			tier = domain.AssertionTierFact
			eventType = "promoted"
			reason = "two independent evidence source groups satisfied the fact gate"
		} else {
			tier = domain.AssertionTierValidatedClaim
			eventType = "validated"
			reason = "independent verifier entailed the relationship"
		}
	default:
		return domain.Assertion{}, nil, "", "", fmt.Errorf("memory service: invalid verifier verdict %q", verification.Verdict)
	}

	assertion, err := baseAssertion(run, reviewed.Proposal, entities, tier, status, reviewed.ExtractConf, verification.Confidence, s.deps.VerifierModel, now)
	if err != nil {
		return domain.Assertion{}, nil, "", "", err
	}
	assertion.ExtractionModel = extractionModel
	if len(run.MigrationRefs) > 0 {
		status = domain.AssertionStatusNeedsReview
		assertion.Status = status
		assertion.AssertionID = eventAssertionID(assertion.AssertionID, run.IngestID, reviewed.Proposal.ProposalID, "migration_shadow")
		eventType = "review_requested"
		reason = "legacy decomposition is stored as an inactive shadow pending user confirmation"
	}
	if status == domain.AssertionStatusActive {
		existing, err := s.deps.Assertions.GetAssertion(ctx, run.ProfileID, assertion.AssertionID)
		if err != nil {
			return domain.Assertion{}, nil, "", "", err
		}
		if existing != nil && existing.Status == domain.AssertionStatusActive && verification.Verdict == "entailed" {
			assertion = mergeActiveAssertion(*existing, assertion)
			if assertion.SourceGroupCount >= 2 && assertion.Tier != domain.AssertionTierFact && verification.Verdict == "entailed" {
				assertion.Tier = domain.AssertionTierFact
				eventType = "promoted"
				reason = "two independent evidence source groups satisfied the fact gate"
			}
		} else if existing != nil && existing.Status == domain.AssertionStatusActive {
			status = domain.AssertionStatusNeedsReview
			assertion.Status = status
			assertion.AssertionID = eventAssertionID(assertion.AssertionID, run.IngestID, reviewed.Proposal.ProposalID, "insufficient_against_active")
			eventType = "review_requested"
			reason = "new evidence is insufficient to change an existing active assertion"
		}
	} else {
		assertion.AssertionID = eventAssertionID(assertion.AssertionID, run.IngestID, reviewed.Proposal.ProposalID, string(status))
	}

	var task *domain.MemoryReviewTask
	if status == domain.AssertionStatusNeedsReview {
		taskType := "confirm_relationship"
		questionPrefix := "Is this relationship correct: "
		if len(run.MigrationRefs) > 0 {
			taskType = "confirm_migration"
			questionPrefix = "Confirm this decomposed legacy relationship: "
		}
		task = &domain.MemoryReviewTask{
			TaskID:   uuid.NewString(),
			Type:     taskType,
			Question: questionPrefix + relationshipSummary(reviewed.Proposal, entities) + "?",
			Options:  []string{"accept", "reject", "correct"},
			Proposal: &reviewed.Proposal,
		}
		_ = itemID
	}
	return assertion, task, eventType, reason, nil
}

func baseAssertion(run *domain.MemoryPlacementRun, proposal domain.MemoryRelationshipProposal, entities map[string]domain.Entity, tier domain.AssertionTier, status domain.AssertionStatus, extractConf, verifierConfidence float64, verifierModel string, now time.Time) (domain.Assertion, error) {
	subject, ok := entities[proposal.SubjectRef]
	if !ok {
		return domain.Assertion{}, errors.New("memory service: reviewed subject is missing")
	}
	objectKey := ""
	objectEntityID := ""
	var objectValue *domain.TypedValue
	resolutionConf := subject.ResolutionConf
	if proposal.ObjectValue != nil {
		value, err := assertionservice.NewValue(run.ProfileID, proposal.ObjectValue.Type, proposal.ObjectValue.Value, proposal.ObjectValue.Display, proposal.ObjectValue.Unit)
		if err != nil {
			return domain.Assertion{}, err
		}
		objectValue = &value
		objectKey = "value:" + value.ValueID
	} else {
		object, exists := entities[proposal.ObjectRef]
		if !exists {
			return domain.Assertion{}, errors.New("memory service: reviewed object is missing")
		}
		objectEntityID = object.EntityID
		objectKey = "entity:" + object.EntityID
		if object.ResolutionConf < resolutionConf {
			resolutionConf = object.ResolutionConf
		}
	}
	predicate := assertionservice.NormalizePredicate(proposal.Predicate)
	evidence, sourceQuality, err := assertionEvidence(run, proposal.Evidence)
	if err != nil {
		return domain.Assertion{}, err
	}
	assertion := domain.Assertion{
		AssertionID:       assertionservice.AssertionID(run.ProfileID, subject.EntityID, predicate, objectKey, proposal.Polarity, proposal.ValidFrom, proposal.ValidTo),
		ProfileID:         run.ProfileID,
		OwnerProfileID:    run.ActorProfileID,
		SubjectEntityID:   subject.EntityID,
		PredicateKey:      predicate,
		RelationshipType:  assertionservice.RelationshipType(predicate),
		ObjectEntityID:    objectEntityID,
		ObjectValue:       objectValue,
		Tier:              tier,
		Status:            status,
		PolicyFamily:      proposal.PolicyFamily,
		Polarity:          proposal.Polarity,
		Modality:          proposal.Modality,
		ValidFrom:         proposal.ValidFrom,
		ValidTo:           proposal.ValidTo,
		RecordedAt:        now,
		ExtractConf:       extractConf,
		ResolutionConf:    resolutionConf,
		SourceQuality:     sourceQuality,
		SupportCount:      len(evidence),
		SourceGroupCount:  distinctSourceGroups(evidence),
		Evidence:          evidence,
		ExtractionModel:   "",
		ExtractionVersion: placementPipelineVersion,
		VerifierModel:     verifierModel,
		PipelineRunID:     run.IngestID,
		ProjectionVersion: assertionservice.ProjectionVersion,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	return assertion, assertion.Validate()
}

func assertionEvidence(run *domain.MemoryPlacementRun, refs []domain.MemoryEvidenceRef) ([]domain.EvidenceSpan, float64, error) {
	fragmentIDs := fragmentIDsByEvidence(run.Items)
	out := make([]domain.EvidenceSpan, 0, len(refs))
	quality := 0.0
	for _, ref := range refs {
		evidence := evidenceForIndex(run.Evidence, ref.EvidenceIndex)
		fragmentID := fragmentIDs[ref.EvidenceIndex]
		if evidence.Content == "" || fragmentID == "" {
			return nil, 0, fmt.Errorf("memory service: evidence %d has no stored fragment", ref.EvidenceIndex)
		}
		out = append(out, domain.EvidenceSpan{FragmentID: fragmentID, Start: ref.Start, End: ref.End, SourceGroup: evidence.SourceGroup})
		quality += defaultSourceQuality(evidence.Authority)
	}
	if len(out) > 0 {
		quality /= float64(len(out))
	}
	return out, quality, nil
}

func mergeActiveAssertion(existing, incoming domain.Assertion) domain.Assertion {
	oldSupport := existing.SupportCount
	newSupport := incoming.SupportCount
	combined := make([]domain.EvidenceSpan, 0, len(existing.Evidence)+len(incoming.Evidence))
	seen := map[string]struct{}{}
	for _, evidence := range append(append([]domain.EvidenceSpan(nil), existing.Evidence...), incoming.Evidence...) {
		key := fmt.Sprintf("%s:%d:%d:%s", evidence.FragmentID, evidence.Start, evidence.End, evidence.SourceGroup)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		combined = append(combined, evidence)
	}
	incoming.Evidence = combined
	incoming.SupportCount = len(combined)
	incoming.SourceGroupCount = distinctSourceGroups(combined)
	if incoming.SupportCount > 0 {
		incoming.SourceQuality = (existing.SourceQuality*float64(oldSupport) + incoming.SourceQuality*float64(newSupport)) / float64(oldSupport+newSupport)
	}
	if tierRank(existing.Tier) > tierRank(incoming.Tier) {
		incoming.Tier = existing.Tier
	}
	incoming.RecordedAt = existing.RecordedAt
	incoming.CreatedAt = existing.CreatedAt
	return incoming
}

func embedAssertionBundle(ctx context.Context, embedder embedding.EmbeddingProviderInterface, entities []domain.Entity, assertions []domain.Assertion, statements []string) error {
	texts := make([]string, 0, len(entities)+len(assertions))
	for _, entity := range entities {
		texts = append(texts, entity.CanonicalName+" "+entity.EntityType)
	}
	texts = append(texts, statements...)
	vectors, model, err := embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return err
	}
	if len(vectors) != len(texts) {
		return fmt.Errorf("embedding provider returned %d vectors for %d texts", len(vectors), len(texts))
	}
	for i := range entities {
		entities[i].Embedding = vectors[i]
		entities[i].EmbeddingModel = model
	}
	for i := range assertions {
		assertions[i].Embedding = vectors[len(entities)+i]
		assertions[i].EmbeddingModel = model
	}
	return nil
}

func verificationContext(evidence []domain.MemoryEvidence, refs []domain.MemoryEvidenceRef) (string, error) {
	type contextItem struct {
		EvidenceIndex int    `json:"evidence_index"`
		SourceGroup   string `json:"source_group"`
		Authority     string `json:"authority"`
		Excerpt       string `json:"excerpt"`
	}
	items := make([]contextItem, 0, len(refs))
	for _, ref := range refs {
		item := evidenceForIndex(evidence, ref.EvidenceIndex)
		runes := []rune(item.Content)
		if ref.Start < 0 || ref.End <= ref.Start || ref.End > len(runes) {
			return "", errors.New("memory service: invalid evidence span for verification")
		}
		items = append(items, contextItem{EvidenceIndex: ref.EvidenceIndex, SourceGroup: item.SourceGroup, Authority: item.Authority, Excerpt: string(runes[ref.Start:ref.End])})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func relationshipStatement(proposal domain.MemoryRelationshipProposal, entities map[string]domain.Entity) (string, error) {
	subject, ok := entities[proposal.SubjectRef]
	if !ok {
		return "", errors.New("memory service: relationship subject is missing")
	}
	object := ""
	if proposal.ObjectValue != nil {
		object = proposal.ObjectValue.Value
	} else if entity, exists := entities[proposal.ObjectRef]; exists {
		object = entity.CanonicalName
	} else {
		return "", errors.New("memory service: relationship object is missing")
	}
	polarity := ""
	if proposal.Polarity == domain.PolarityMinus {
		polarity = "not "
	}
	return strings.TrimSpace(subject.CanonicalName + " " + polarity + strings.ReplaceAll(assertionservice.NormalizePredicate(proposal.Predicate), "_", " ") + " " + object), nil
}

func relationshipSummary(proposal domain.MemoryRelationshipProposal, entities map[string]domain.Entity) string {
	statement, err := relationshipStatement(proposal, entities)
	if err != nil {
		return proposal.ProposalID
	}
	return statement
}

func reviewedPlacementItem(run *domain.MemoryPlacementRun, reviewed placementreview.ReviewedRelationship, old map[string]domain.MemoryPlacementItem, fragments map[int]string, now time.Time) domain.MemoryPlacementItem {
	item, exists := old[reviewed.Proposal.ProposalID]
	if !exists {
		item = domain.MemoryPlacementItem{ItemID: uuid.NewString(), IngestID: run.IngestID, ProfileID: run.ProfileID, CreatedAt: now}
	}
	item.EvidenceIndexes = relationshipEvidenceIndexes(reviewed.Proposal)
	item.EvidenceIndex = -1
	if len(item.EvidenceIndexes) > 0 {
		item.EvidenceIndex = item.EvidenceIndexes[0]
	}
	item.FragmentIDs = make([]string, 0, len(item.EvidenceIndexes))
	for _, index := range item.EvidenceIndexes {
		if fragmentID := fragments[index]; fragmentID != "" {
			item.FragmentIDs = append(item.FragmentIDs, fragmentID)
		}
	}
	item.FragmentID = ""
	if len(item.FragmentIDs) > 0 {
		item.FragmentID = item.FragmentIDs[0]
	}
	item.ReviewedRelationship = reviewed.Proposal
	item.Status = string(domain.MemoryPlacementProcessing)
	item.UpdatedAt = now
	return item
}

func relationshipEvidenceIndexes(proposal domain.MemoryRelationshipProposal) []int {
	seen := map[int]struct{}{}
	indexes := make([]int, 0, len(proposal.Evidence))
	for _, evidence := range proposal.Evidence {
		if _, exists := seen[evidence.EvidenceIndex]; exists {
			continue
		}
		seen[evidence.EvidenceIndex] = struct{}{}
		indexes = append(indexes, evidence.EvidenceIndex)
	}
	sort.Ints(indexes)
	return indexes
}

func fragmentIDsByEvidence(items []domain.MemoryPlacementItem) map[int]string {
	out := map[int]string{}
	for _, item := range items {
		for i, index := range item.EvidenceIndexes {
			if i < len(item.FragmentIDs) && item.FragmentIDs[i] != "" {
				out[index] = item.FragmentIDs[i]
			}
		}
		if item.EvidenceIndex >= 0 && item.FragmentID != "" {
			out[item.EvidenceIndex] = item.FragmentID
		}
	}
	return out
}

func placementItemsByProposalID(items []domain.MemoryPlacementItem) map[string]domain.MemoryPlacementItem {
	out := make(map[string]domain.MemoryPlacementItem, len(items))
	for _, item := range items {
		if id := item.ProposedRelationship.ProposalID; id != "" {
			out[id] = item
		}
	}
	return out
}

func reviewedProposal(review placementreview.Result) domain.MemoryProposal {
	proposal := domain.MemoryProposal{Entities: make([]domain.MemoryEntityProposal, 0, len(review.Entities)), Relationships: make([]domain.MemoryRelationshipProposal, 0, len(review.Relationships))}
	for _, entity := range review.Entities {
		proposal.Entities = append(proposal.Entities, entity.Proposal)
	}
	for _, relationship := range review.Relationships {
		proposal.Relationships = append(proposal.Relationships, relationship.Proposal)
	}
	return proposal
}

func relationshipHasAmbiguousEntity(proposal domain.MemoryRelationshipProposal, entities map[string]domain.Entity) bool {
	if entities[proposal.SubjectRef].ResolutionStatus == domain.EntityResolutionAmbiguous {
		return true
	}
	return proposal.ObjectRef != "" && entities[proposal.ObjectRef].ResolutionStatus == domain.EntityResolutionAmbiguous
}

func hasTrustedAuthority(run *domain.MemoryPlacementRun, refs []domain.MemoryEvidenceRef, authorityExplicit bool) bool {
	if run.ActorRole == "manager" && authorityExplicit {
		return true
	}
	for _, ref := range refs {
		evidence := evidenceForIndex(run.Evidence, ref.EvidenceIndex)
		if evidence.Authority == string(domain.AuthorityAuthoritative) && (run.ActorRole == "manager" || evidence.TrustedAuthority) {
			return true
		}
	}
	return false
}

func hasTwoSourceGroups(evidence []domain.MemoryEvidence, refs []domain.MemoryEvidenceRef) bool {
	groups := map[string]struct{}{}
	indexes := map[int]struct{}{}
	for _, ref := range refs {
		if _, seen := indexes[ref.EvidenceIndex]; seen {
			continue
		}
		indexes[ref.EvidenceIndex] = struct{}{}
		group := evidenceForIndex(evidence, ref.EvidenceIndex).SourceGroup
		if group != "" {
			groups[group] = struct{}{}
		}
	}
	return len(groups) >= 2
}

func distinctSourceGroups(evidence []domain.EvidenceSpan) int {
	groups := map[string]struct{}{}
	for _, item := range evidence {
		groups[item.SourceGroup] = struct{}{}
	}
	return len(groups)
}

func placementCategory(assertion domain.Assertion) domain.MemoryPlacementCategory {
	switch assertion.Status {
	case domain.AssertionStatusQuarantined:
		return domain.MemoryPlacementAssertionQuarantine
	case domain.AssertionStatusNeedsReview:
		return domain.MemoryPlacementAssertionReview
	case domain.AssertionStatusRejected:
		return domain.MemoryPlacementAssertionRejected
	}
	switch assertion.Tier {
	case domain.AssertionTierFact:
		return domain.MemoryPlacementAssertionFact
	case domain.AssertionTierValidatedClaim:
		return domain.MemoryPlacementAssertionValidated
	default:
		return domain.MemoryPlacementAssertionCandidate
	}
}

func tierRank(tier domain.AssertionTier) int {
	switch tier {
	case domain.AssertionTierFact:
		return 3
	case domain.AssertionTierValidatedClaim:
		return 2
	case domain.AssertionTierCandidate:
		return 1
	default:
		return 0
	}
}

func eventAssertionID(base, ingestID, proposalID, status string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join([]string{base, ingestID, proposalID, status}, ":"))).String()
}

func (s *service) processLegacyPlacementRun(ctx context.Context, run *domain.MemoryPlacementRun) error {
	if run == nil {
		return nil
	}
	now := time.Now().UTC()
	run.Status = domain.MemoryPlacementProcessing
	run.StartedAt = &now
	run.UpdatedAt = now
	if err := s.deps.PlacementStore.SaveRun(ctx, *run); err != nil {
		return err
	}
	for i := range run.Items {
		item := &run.Items[i]
		evidence := evidenceForIndex(run.Evidence, item.EvidenceIndex)
		if strings.TrimSpace(evidence.Content) == "" {
			item.Category = domain.MemoryPlacementNeedsEvidence
			item.Status = "completed"
			item.Reason = "Dense-Mem verifier could not find evidence content for this placement item."
			item.UpdatedAt = time.Now().UTC()
			continue
		}
		if looksFalse(evidence.Content) {
			item.Category = domain.MemoryPlacementRejectedFalse
			item.Status = "completed"
			item.Reason = "Dense-Mem verifier classified the evidence as a contradiction or false-memory correction."
			item.UpdatedAt = time.Now().UTC()
			continue
		}
		claim, ok := extractServerClaim(evidence, item.FragmentID)
		if !ok {
			item.Category = domain.MemoryPlacementFragmentOnly
			item.Status = "completed"
			item.Reason = "Evidence was stored as a fragment; the verifier found no supported personal-memory claim to extract."
			item.UpdatedAt = time.Now().UTC()
			continue
		}
		outcome, _ := s.processClaim(ctx, run.ProfileID, item.FragmentID, claim, true)
		item.ClaimID = outcome.ClaimID
		if outcome.Fact != nil {
			item.FactID = outcome.Fact.FactID
		}
		item.Category, item.Reason = placementFromClaimOutcome(outcome)
		item.Status = "completed"
		item.Error = outcome.Error
		item.UpdatedAt = time.Now().UTC()
	}
	completed := time.Now().UTC()
	run.Status = domain.MemoryPlacementCompleted
	run.CompletedAt = &completed
	run.UpdatedAt = completed
	return s.deps.PlacementStore.SaveRun(ctx, *run)
}
