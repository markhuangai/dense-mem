package skillpackservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

type service struct {
	deps     Dependencies
	graphOps *graphOps
	retain   time.Duration
}

var _ Service = (*service)(nil)

func New(deps Dependencies) Service {
	retain := defaultHistoryRetention
	if deps.HistoryDays > 0 {
		retain = time.Duration(deps.HistoryDays) * 24 * time.Hour
	}
	return &service{
		deps:     deps,
		graphOps: newGraphOps(deps.Graph),
		retain:   retain,
	}
}

func (s *service) FindCandidates(ctx context.Context, profileID string, req FindCandidatesRequest) (*FindCandidatesResult, error) {
	limit := clampLimit(req.Limit, 20, maxPackItems)
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if query == "" {
		return nil, errors.New("skill pack candidates: query is required")
	}

	if candidates, err := s.graphOps.findCandidates(ctx, profileID, req.Query, limit); err != nil {
		return nil, err
	} else if len(candidates) > 0 {
		return &FindCandidatesResult{Candidates: candidates}, nil
	}

	out := &FindCandidatesResult{Candidates: []Candidate{}}
	if s.deps.FactList != nil {
		facts, _, err := s.deps.FactList.List(ctx, profileID, factservice.FactListFilters{Status: domain.FactStatusActive}, maxPackItems, "")
		if err != nil {
			return nil, err
		}
		for _, fact := range facts {
			if len(out.Candidates) >= limit {
				return out, nil
			}
			if fact == nil || !allowedPredicate(fact.Predicate) || !tripleMatches(query, fact.Subject, fact.Predicate, fact.Object) {
				continue
			}
			out.Candidates = append(out.Candidates, Candidate{
				ID:   fact.FactID,
				Type: "fact",
				Item: SkillPackItem{
					Subject:    fact.Subject,
					Predicate:  fact.Predicate,
					Object:     fact.Object,
					SourceKind: SourceKindFact,
				},
				Snippet:    fact.Object,
				RecordedAt: fact.RecordedAt,
			})
		}
	}

	if s.deps.ClaimList != nil && len(out.Candidates) < limit {
		claims, _, err := s.deps.ClaimList.List(ctx, profileID, maxPackItems, 0)
		if err != nil {
			return nil, err
		}
		for _, claim := range claims {
			if len(out.Candidates) >= limit {
				break
			}
			if claim == nil || claim.Status != domain.StatusValidated || !allowedPredicate(claim.Predicate) || !tripleMatches(query, claim.Subject, claim.Predicate, claim.Object) {
				continue
			}
			out.Candidates = append(out.Candidates, Candidate{
				ID:   claim.ClaimID,
				Type: "claim",
				Item: SkillPackItem{
					Subject:    claim.Subject,
					Predicate:  claim.Predicate,
					Object:     claim.Object,
					SourceKind: SourceKindValidatedClaim,
				},
				Snippet:    claim.Object,
				RecordedAt: claim.RecordedAt,
			})
		}
	}
	return out, nil
}

func (s *service) Export(ctx context.Context, profileID string, req ExportRequest) (*ExportResult, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("skill pack export: name is required")
	}
	pack := SkillPack{
		SchemaVersion: SchemaVersion,
		Name:          req.Name,
		Description:   req.Description,
		Items:         []SkillPackItem{},
	}

	for _, factID := range req.FactIDs {
		if s.deps.FactGet == nil {
			return nil, errors.New("skill pack export: fact get service is required")
		}
		fact, err := s.deps.FactGet.Get(ctx, profileID, factID)
		if err != nil {
			return nil, err
		}
		if !allowedPredicate(fact.Predicate) {
			return nil, fmt.Errorf("skill pack export: fact %s predicate %q is not supported", factID, fact.Predicate)
		}
		pack.Items = append(pack.Items, SkillPackItem{
			Subject:    fact.Subject,
			Predicate:  fact.Predicate,
			Object:     fact.Object,
			SourceKind: SourceKindFact,
		})
	}

	for _, claimID := range req.ClaimIDs {
		if s.deps.ClaimGet == nil {
			return nil, errors.New("skill pack export: claim get service is required")
		}
		claim, err := s.deps.ClaimGet.Get(ctx, profileID, claimID)
		if err != nil {
			return nil, err
		}
		if claim.Status != domain.StatusValidated {
			return nil, fmt.Errorf("skill pack export: claim %s must be validated", claimID)
		}
		if !allowedPredicate(claim.Predicate) {
			return nil, fmt.Errorf("skill pack export: claim %s predicate %q is not supported", claimID, claim.Predicate)
		}
		pack.Items = append(pack.Items, SkillPackItem{
			Subject:    claim.Subject,
			Predicate:  claim.Predicate,
			Object:     claim.Object,
			SourceKind: SourceKindValidatedClaim,
		})
	}

	for _, item := range req.ManualItems {
		if item.SourceKind == "" {
			item.SourceKind = SourceKindManual
		}
		pack.Items = append(pack.Items, item)
	}

	pack = normalizePack(pack)
	canonical, hash, err := canonicalArtifact(pack)
	if err != nil {
		return nil, err
	}
	return &ExportResult{
		Artifact:      pack,
		CanonicalJSON: string(canonical),
		SHA256:        hash,
		ItemCount:     len(pack.Items),
	}, nil
}

func (s *service) Inspect(ctx context.Context, profileID string, req InspectRequest) (*InspectResult, error) {
	pack, hash, _, sourceURL, err := s.loadArtifact(ctx, req.Artifact, req.ArtifactJSON, req.URL, req.ExpectedSHA256)
	if err != nil {
		return nil, err
	}
	return s.inspectPack(ctx, profileID, pack, hash, sourceURL)
}

func (s *service) Import(ctx context.Context, profileID string, req ImportRequest) (*ImportResult, error) {
	if req.Mode != ModeReview && req.Mode != ModeTrusted {
		return nil, errors.New("skill pack import: mode must be review or trusted")
	}
	if req.Mode == ModeTrusted && strings.TrimSpace(req.URL) != "" && strings.TrimSpace(req.ExpectedSHA256) == "" {
		return nil, errors.New("skill pack import: trusted URL imports require expected_sha256")
	}
	if s.deps.Ledger == nil {
		return nil, errors.New("skill pack import: import ledger is required")
	}
	if s.deps.FragmentCreate == nil || s.deps.ClaimCreate == nil {
		return nil, errors.New("skill pack import: fragment and claim services are required")
	}

	pack, hash, _, sourceURL, err := s.loadArtifact(ctx, req.Artifact, req.ArtifactJSON, req.URL, req.ExpectedSHA256)
	if err != nil {
		return nil, err
	}
	inspection, err := s.inspectPack(ctx, profileID, pack, hash, sourceURL)
	if err != nil {
		return nil, err
	}
	selected := selectedIndexSet(req.SelectedItems, len(pack.Items))
	decisionByIndex := conflictDecisionMap(req.ConflictDecisions)
	if req.Mode == ModeTrusted {
		missing := requiredDecisions(inspection, selected, decisionByIndex)
		if len(missing) > 0 {
			return &ImportResult{
				ArtifactHash:      hash,
				Mode:              req.Mode,
				Status:            domain.SkillPackImportStatusNeedsReview,
				DecisionsRequired: missing,
			}, nil
		}
	}

	importID := uuid.NewString()
	now := time.Now().UTC()
	record := domain.SkillPackImport{
		ImportID:           importID,
		TeamID:             profileID,
		ArtifactHash:       hash,
		SourceURL:          sourceURL,
		SchemaVersion:      pack.SchemaVersion,
		Name:               pack.Name,
		Mode:               req.Mode,
		Status:             domain.SkillPackImportStatusInspecting,
		ItemCount:          len(pack.Items),
		RetentionExpiresAt: now.Add(s.retain),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.deps.Ledger.CreateImport(ctx, record); err != nil {
		return nil, err
	}

	fragmentRes, err := s.deps.FragmentCreate.Create(ctx, profileID, &dto.CreateFragmentRequest{
		Content:        fragmentContent(pack, hash),
		SourceType:     "document",
		Source:         importSource(pack, sourceURL),
		Authority:      importAuthority(req.Mode),
		IdempotencyKey: "skill-pack:" + hash,
		Labels:         []string{"skill_pack_import"},
		Metadata: map[string]any{
			"imported":          true,
			"import_id":         importID,
			"skill_pack_hash":   hash,
			"skill_pack_schema": pack.SchemaVersion,
			"source_url":        sourceURL,
		},
		SourceQuality: sourceQuality(req.Mode),
	})
	if err != nil {
		return nil, err
	}
	fragmentID := fragmentRes.Fragment.FragmentID
	if !fragmentRes.Duplicate {
		if err := s.graphOps.tagFragment(ctx, profileID, fragmentID, importID, hash); err != nil {
			return nil, s.cleanupCreatedEntity(ctx, profileID, "fragment", fragmentID, err)
		}
		if err := s.appendChange(ctx, profileID, importID, "fragment", fragmentID, domain.SkillPackChangeActionCreated, nil, map[string]any{
			"fragment_id":  fragmentID,
			"content_hash": fragmentRes.Fragment.ContentHash,
			"import_id":    importID,
		}); err != nil {
			return nil, s.cleanupCreatedEntity(ctx, profileID, "fragment", fragmentID, err)
		}
	}

	out := &ImportResult{
		ImportID:     importID,
		ArtifactHash: hash,
		Mode:         req.Mode,
		Status:       domain.SkillPackImportStatusApplied,
		Items:        []ImportItemResult{},
	}
	for idx, item := range pack.Items {
		if !selected[idx] {
			out.SkippedCount++
			continue
		}
		result := s.importItem(ctx, profileID, importID, hash, fragmentID, req.Mode, item, inspection.Items[idx], decisionByIndex[idx])
		out.Items = append(out.Items, result)
		if result.Status == "error" {
			if result.Error == "" {
				return nil, fmt.Errorf("skill pack import: item %d failed", result.Index)
			}
			return nil, fmt.Errorf("skill pack import: item %d: %s", result.Index, result.Error)
		}
		if result.Status == "imported" || result.Status == "promoted" || result.Status == "validated" {
			out.AppliedCount++
		} else {
			out.SkippedCount++
		}
	}

	status := domain.SkillPackImportStatusApplied
	if out.AppliedCount == 0 {
		status = domain.SkillPackImportStatusNeedsReview
		out.Status = status
	}
	if err := s.deps.Ledger.UpdateImportStatus(ctx, profileID, importID, status, out.AppliedCount, out.SkippedCount, map[string]any{
		"mode":          req.Mode,
		"artifact_hash": hash,
	}); err != nil {
		// Return a recoverable result: callers need import_id to roll back
		// graph changes already covered by durable change records.
		out.Status = "status_update_failed"
		out.Error = fmt.Sprintf("skill pack import status update failed: %v", err)
		return out, nil
	}
	return out, nil
}

func (s *service) Rollback(ctx context.Context, profileID string, req RollbackRequest) (*RollbackResult, error) {
	if strings.TrimSpace(req.ImportID) == "" {
		return nil, errors.New("skill pack rollback: import_id is required")
	}
	if s.deps.Ledger == nil {
		return nil, errors.New("skill pack rollback: import ledger is required")
	}
	record, err := s.deps.Ledger.GetImport(ctx, profileID, req.ImportID)
	if err != nil {
		return nil, err
	}
	if record.Status == domain.SkillPackImportStatusRolledBack {
		return nil, errors.New("skill pack rollback: import already rolled back")
	}
	changes, err := s.deps.Ledger.ListChanges(ctx, profileID, req.ImportID)
	if err != nil {
		return nil, err
	}
	conflicts := s.rollbackConflicts(ctx, profileID, changes)
	if len(conflicts) > 0 {
		return &RollbackResult{ImportID: req.ImportID, Status: "blocked", Conflicts: conflicts}, nil
	}

	reverted := 0
	for _, change := range changes {
		switch change.Action {
		case domain.SkillPackChangeActionCreated:
			if err := s.graphOps.deleteEntity(ctx, profileID, change.EntityType, change.EntityID); err != nil {
				return nil, err
			}
			reverted++
		case domain.SkillPackChangeActionUpdated:
			if change.EntityType == "claim" {
				if err := s.graphOps.restoreClaim(ctx, profileID, change.EntityID, req.ImportID, change.BeforeState); err != nil {
					return nil, err
				}
				reverted++
			}
		case domain.SkillPackChangeActionSuperseded:
			if change.EntityType == "fact" {
				if err := s.graphOps.restoreFact(ctx, profileID, change.EntityID, req.ImportID, change.BeforeState); err != nil {
					return nil, err
				}
				reverted++
			}
		}
	}
	if err := s.deps.Ledger.MarkRolledBack(ctx, profileID, req.ImportID); err != nil {
		return nil, err
	}
	return &RollbackResult{ImportID: req.ImportID, Status: domain.SkillPackImportStatusRolledBack, RevertedCount: reverted}, nil
}

func (s *service) loadArtifact(ctx context.Context, artifact *SkillPack, artifactJSON, rawURL, expectedHash string) (SkillPack, string, string, string, error) {
	var (
		pack SkillPack
		err  error
	)
	sourceURL := strings.TrimSpace(rawURL)
	switch {
	case strings.TrimSpace(artifactJSON) != "":
		pack, err = parseArtifactJSON([]byte(artifactJSON))
	case sourceURL != "":
		var data []byte
		data, err = fetchArtifact(ctx, s.deps.HTTPClient, sourceURL)
		if err == nil {
			pack, err = parseArtifactJSON(data)
		}
	case artifact != nil:
		pack = *artifact
	default:
		err = errors.New("skill pack: artifact_json, artifact, or url is required")
	}
	if err != nil {
		return SkillPack{}, "", "", "", err
	}
	pack = normalizePack(pack)
	canonical, hash, err := canonicalArtifact(pack)
	if err != nil {
		return SkillPack{}, "", "", "", err
	}
	if err := validateExpectedHash(hash, expectedHash); err != nil {
		return SkillPack{}, "", "", "", err
	}
	return pack, hash, string(canonical), sourceURL, nil
}

func (s *service) inspectPack(ctx context.Context, profileID string, pack SkillPack, hash, sourceURL string) (*InspectResult, error) {
	out := &InspectResult{
		ArtifactHash: hash,
		Name:         pack.Name,
		Description:  pack.Description,
		ItemCount:    len(pack.Items),
		SourceURL:    sourceURL,
		Items:        make([]InspectItem, 0, len(pack.Items)),
	}
	for idx, item := range pack.Items {
		inspected, err := s.inspectItem(ctx, profileID, idx, item)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, inspected)
		if inspected.Status == "conflicts_with_fact" {
			var factIDs []string
			for _, f := range inspected.ConflictingFacts {
				factIDs = append(factIDs, f.FactID)
			}
			out.DecisionsRequired = append(out.DecisionsRequired, ConflictPrompt{
				Index:          idx,
				Reason:         "imported item conflicts with active local facts",
				FactIDs:        factIDs,
				AllowedActions: []string{DecisionImportAnyway, DecisionSkip, DecisionSupersedeLocal},
			})
		}
	}
	return out, nil
}

func (s *service) inspectItem(ctx context.Context, profileID string, idx int, item SkillPackItem) (InspectItem, error) {
	out := InspectItem{Index: idx, Item: item, Status: "new"}
	if !allowedPredicate(item.Predicate) {
		out.Status = "unsupported_predicate"
		out.Severity = "high"
		out.Message = "predicate is not allowed for skill packs"
		return out, nil
	}
	if s.deps.FactList != nil {
		facts, _, err := s.deps.FactList.List(ctx, profileID, factservice.FactListFilters{
			Subject:   item.Subject,
			Predicate: item.Predicate,
			Status:    domain.FactStatusActive,
		}, maxPackItems, "")
		if err != nil {
			return out, err
		}
		for _, fact := range facts {
			summary := factSummary(fact)
			if fact.Object == item.Object {
				out.MatchingFacts = append(out.MatchingFacts, summary)
			} else {
				out.ConflictingFacts = append(out.ConflictingFacts, summary)
			}
		}
	}
	if s.deps.ClaimList != nil {
		claims, _, err := s.deps.ClaimList.List(ctx, profileID, maxPackItems, 0)
		if err != nil {
			return out, err
		}
		for _, claim := range claims {
			if claim.Subject == item.Subject && claim.Predicate == item.Predicate && claim.Object == item.Object {
				out.MatchingClaims = append(out.MatchingClaims, claimSummary(claim))
			}
		}
	}
	switch {
	case len(out.ConflictingFacts) > 0:
		out.Status = "conflicts_with_fact"
		out.Severity = "high"
	case len(out.MatchingFacts) > 0:
		out.Status = "duplicate_fact"
		out.Severity = "low"
	case len(out.MatchingClaims) > 0:
		out.Status = "already_claimed"
		out.Severity = "low"
	}
	return out, nil
}

func (s *service) importItem(ctx context.Context, profileID, importID, artifactHash, fragmentID, mode string, item SkillPackItem, inspected InspectItem, decision string) ImportItemResult {
	result := ImportItemResult{Index: inspected.Index, Item: item, Conflicts: inspected.ConflictingFacts, Decision: decision}
	if decision == DecisionSkip {
		result.Status = "skipped"
		return result
	}
	claim := &domain.Claim{
		Subject:           item.Subject,
		Predicate:         item.Predicate,
		Object:            item.Object,
		Modality:          domain.ModalityAssertion,
		Polarity:          domain.PolarityPlus,
		Speaker:           "skill_pack",
		ExtractConf:       confidenceFor(mode, item.SourceKind),
		ResolutionConf:    confidenceFor(mode, item.SourceKind),
		IdempotencyKey:    fmt.Sprintf("skill-pack:%s:%d", artifactHash, inspected.Index),
		SupportedBy:       []string{fragmentID},
		ExtractionModel:   "skill_pack_import",
		ExtractionVersion: SchemaVersion,
		PipelineRunID:     importID,
	}
	createRes, err := s.deps.ClaimCreate.Create(ctx, profileID, claim)
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		return result
	}
	result.ClaimID = createRes.Claim.ClaimID

	if mode == ModeReview {
		if !createRes.Duplicate {
			if err := s.graphOps.tagClaim(ctx, profileID, result.ClaimID, importID, artifactHash, item.SourceKind); err != nil {
				return itemError(result, s.cleanupCreatedEntity(ctx, profileID, "claim", result.ClaimID, err))
			}
			after := claimLedgerState(createRes.Claim, importID)
			if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionCreated, nil, after); err != nil {
				return itemError(result, s.cleanupCreatedEntity(ctx, profileID, "claim", result.ClaimID, err))
			}
		}
		result.Status = "imported"
		return result
	}

	promotedFactID := ""
	if item.SourceKind == SourceKindFact && createRes.Duplicate {
		var err error
		promotedFactID, err = s.graphOps.promotedFactIDForClaim(ctx, profileID, result.ClaimID)
		if err != nil {
			return itemError(result, err)
		}
		if promotedFactID != "" {
			result.FactID = promotedFactID
			result.Status = "promoted"
			return result
		}
	}

	beforeClaim := claimLedgerState(createRes.Claim, "")
	if createRes.Duplicate && s.deps.ClaimGet != nil {
		if existing, err := s.deps.ClaimGet.Get(ctx, profileID, result.ClaimID); err == nil {
			beforeClaim = claimLedgerState(existing, "")
		}
	}
	if createRes.Duplicate {
		if err := s.graphOps.trustExistingClaim(ctx, profileID, result.ClaimID); err != nil {
			return itemError(result, err)
		}
	} else {
		if err := s.graphOps.trustClaim(ctx, profileID, result.ClaimID, importID, artifactHash, item.SourceKind); err != nil {
			return itemError(result, s.cleanupCreatedEntity(ctx, profileID, "claim", result.ClaimID, err))
		}
	}
	failClaimMutation := func(err error) ImportItemResult {
		return itemError(result, s.cleanupClaimMutation(ctx, profileID, importID, result.ClaimID, createRes.Duplicate, beforeClaim, err))
	}

	if decision == DecisionSupersedeLocal {
		var factIDs []string
		for _, f := range inspected.ConflictingFacts {
			if s.deps.FactGet != nil {
				if before, err := s.deps.FactGet.Get(ctx, profileID, f.FactID); err == nil {
					if err := s.appendChange(ctx, profileID, importID, "fact", f.FactID, domain.SkillPackChangeActionSuperseded, factLedgerState(before), map[string]any{
						"fact_id": f.FactID,
						"status":  string(domain.FactStatusSuperseded),
					}); err != nil {
						return failClaimMutation(err)
					}
				}
			}
			factIDs = append(factIDs, f.FactID)
		}
		if err := s.graphOps.supersedeFacts(ctx, profileID, factIDs, result.ClaimID, importID); err != nil {
			return failClaimMutation(err)
		}
	}

	if item.SourceKind == SourceKindFact {
		if s.deps.FactPromote == nil {
			return failClaimMutation(errors.New("fact promote service is required"))
		}
		fact, err := s.deps.FactPromote.Promote(ctx, profileID, result.ClaimID)
		if err != nil {
			return failClaimMutation(err)
		}
		result.FactID = fact.FactID
		factCreated := fact.PromotedFromClaimID == result.ClaimID
		if factCreated {
			if err := s.graphOps.tagFact(ctx, profileID, fact.FactID, importID, artifactHash, item.SourceKind); err != nil {
				err = s.cleanupCreatedEntity(ctx, profileID, "fact", fact.FactID, err)
				return failClaimMutation(err)
			}
		}
		if !createRes.Duplicate {
			afterClaim := map[string]any{"claim_id": result.ClaimID, "subject": item.Subject, "predicate": item.Predicate, "object": item.Object, "status": string(domain.StatusSuperseded), "import_id": importID}
			if s.deps.ClaimGet != nil {
				if finalClaim, err := s.deps.ClaimGet.Get(ctx, profileID, result.ClaimID); err == nil {
					afterClaim = claimLedgerState(finalClaim, importID)
				}
			}
			if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionCreated, nil, afterClaim); err != nil {
				if factCreated {
					err = s.cleanupCreatedEntity(ctx, profileID, "fact", fact.FactID, err)
				}
				return failClaimMutation(err)
			}
		} else {
			afterClaim := trustedClaimAfterState(result.ClaimID, domain.StatusSuperseded)
			if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionUpdated, beforeClaim, afterClaim); err != nil {
				if factCreated {
					err = s.cleanupCreatedEntity(ctx, profileID, "fact", fact.FactID, err)
				}
				return failClaimMutation(err)
			}
		}
		if factCreated {
			if err := s.appendChange(ctx, profileID, importID, "fact", fact.FactID, domain.SkillPackChangeActionCreated, nil, factLedgerStateWithImport(fact, importID)); err != nil {
				return itemError(result, s.cleanupCreatedEntity(ctx, profileID, "fact", fact.FactID, err))
			}
		}
		result.Status = "promoted"
		return result
	}

	if !createRes.Duplicate {
		afterClaim := map[string]any{"claim_id": result.ClaimID, "subject": item.Subject, "predicate": item.Predicate, "object": item.Object, "status": string(domain.StatusValidated), "entailment_verdict": string(domain.VerdictEntailed), "import_id": importID}
		if s.deps.ClaimGet != nil {
			if finalClaim, err := s.deps.ClaimGet.Get(ctx, profileID, result.ClaimID); err == nil {
				afterClaim = claimLedgerState(finalClaim, importID)
			}
		}
		if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionCreated, nil, afterClaim); err != nil {
			return failClaimMutation(err)
		}
	} else {
		afterClaim := trustedClaimAfterState(result.ClaimID, domain.StatusValidated)
		if err := s.appendChange(ctx, profileID, importID, "claim", result.ClaimID, domain.SkillPackChangeActionUpdated, beforeClaim, afterClaim); err != nil {
			return failClaimMutation(err)
		}
	}
	result.Status = "validated"
	return result
}

func itemError(result ImportItemResult, err error) ImportItemResult {
	result.Status = "error"
	result.Error = err.Error()
	return result
}

func trustedClaimAfterState(claimID string, status domain.ClaimStatus) map[string]any {
	return map[string]any{
		"claim_id":           claimID,
		"status":             string(status),
		"entailment_verdict": string(domain.VerdictEntailed),
		"verifier_model":     "skill_pack.source_trust",
	}
}

func (s *service) appendChange(ctx context.Context, profileID, importID, entityType, entityID, action string, before, after map[string]any) error {
	if s.deps.Ledger == nil {
		return nil
	}
	return s.deps.Ledger.AppendChange(ctx, domain.SkillPackImportChange{
		ChangeID:    uuid.NewString(),
		ImportID:    importID,
		TeamID:      profileID,
		EntityType:  entityType,
		EntityID:    entityID,
		Action:      action,
		BeforeState: before,
		AfterState:  after,
		CreatedAt:   time.Now().UTC(),
	})
}

func (s *service) cleanupCreatedEntity(ctx context.Context, profileID, entityType, entityID string, cause error) error {
	if cause == nil || !s.graphOps.available() {
		return cause
	}
	if err := s.graphOps.deleteEntity(ctx, profileID, entityType, entityID); err != nil {
		return fmt.Errorf("%w; cleanup %s %s: %v", cause, entityType, entityID, err)
	}
	return cause
}

func (s *service) cleanupClaimMutation(ctx context.Context, profileID, importID, claimID string, duplicate bool, before map[string]any, cause error) error {
	if cause == nil || !s.graphOps.available() {
		return cause
	}
	if !duplicate {
		return s.cleanupCreatedEntity(ctx, profileID, "claim", claimID, cause)
	}
	if err := s.graphOps.restoreClaim(ctx, profileID, claimID, importID, before); err != nil {
		return fmt.Errorf("%w; cleanup claim %s: %v", cause, claimID, err)
	}
	return cause
}

func (s *service) rollbackConflicts(ctx context.Context, profileID string, changes []domain.SkillPackImportChange) []string {
	var conflicts []string
	for _, change := range changes {
		current, err := s.graphOps.currentState(ctx, profileID, change.EntityType, change.EntityID)
		if err != nil {
			conflicts = append(conflicts, fmt.Sprintf("%s %s read failed: %v", change.EntityType, change.EntityID, err))
			continue
		}
		if current == nil {
			conflicts = append(conflicts, fmt.Sprintf("%s %s is missing", change.EntityType, change.EntityID))
			continue
		}
		if !stateMatches(current, change.AfterState) {
			conflicts = append(conflicts, fmt.Sprintf("%s %s changed after import", change.EntityType, change.EntityID))
		}
	}
	return conflicts
}

func stateMatches(current, expected map[string]any) bool {
	for key, expectedValue := range expected {
		if key == "" || expectedValue == nil {
			continue
		}
		currentValue, ok := current[key]
		if !ok {
			return false
		}
		if !stateValueMatches(currentValue, expectedValue) {
			return false
		}
	}
	return true
}

func stateValueMatches(currentValue, expectedValue any) bool {
	currentTime, currentIsTime := currentValue.(time.Time)
	expectedTime, expectedIsTime := expectedValue.(time.Time)
	switch {
	case currentIsTime && expectedIsTime:
		return currentTime.UTC().Equal(expectedTime.UTC())
	case currentIsTime:
		expectedString, ok := expectedValue.(string)
		if !ok {
			return false
		}
		parsed, err := time.Parse(time.RFC3339Nano, expectedString)
		if err != nil {
			return false
		}
		return currentTime.UTC().Equal(parsed.UTC())
	case expectedIsTime:
		currentString, ok := currentValue.(string)
		if !ok {
			return false
		}
		parsed, err := time.Parse(time.RFC3339Nano, currentString)
		if err != nil {
			return false
		}
		return parsed.UTC().Equal(expectedTime.UTC())
	default:
		return fmt.Sprint(currentValue) == fmt.Sprint(expectedValue)
	}
}

func requiredDecisions(inspection *InspectResult, selected map[int]bool, decisions map[int]string) []ConflictPrompt {
	var missing []ConflictPrompt
	for _, prompt := range inspection.DecisionsRequired {
		if !selected[prompt.Index] {
			continue
		}
		action := decisions[prompt.Index]
		if action == "" {
			missing = append(missing, prompt)
			continue
		}
		if action != DecisionImportAnyway && action != DecisionSkip && action != DecisionSupersedeLocal {
			prompt.Reason = "invalid conflict decision"
			missing = append(missing, prompt)
		}
	}
	return missing
}

func selectedIndexSet(indexes []int, itemCount int) map[int]bool {
	out := make(map[int]bool, itemCount)
	if len(indexes) == 0 {
		for i := 0; i < itemCount; i++ {
			out[i] = true
		}
		return out
	}
	for _, idx := range indexes {
		if idx >= 0 && idx < itemCount {
			out[idx] = true
		}
	}
	return out
}

func conflictDecisionMap(decisions []ConflictDecision) map[int]string {
	out := make(map[int]string, len(decisions))
	for _, decision := range decisions {
		out[decision.Index] = decision.Action
	}
	return out
}

func claimLedgerState(claim *domain.Claim, importID string) map[string]any {
	if claim == nil {
		return nil
	}
	state := map[string]any{
		"claim_id":           claim.ClaimID,
		"subject":            claim.Subject,
		"predicate":          claim.Predicate,
		"object":             claim.Object,
		"status":             string(claim.Status),
		"entailment_verdict": string(claim.EntailmentVerdict),
	}
	if claim.VerifierModel != "" {
		state["verifier_model"] = claim.VerifierModel
	}
	if claim.LastVerifierResponse != "" {
		state["last_verifier_response"] = claim.LastVerifierResponse
	}
	if claim.VerifiedAt != nil {
		state["verified_at"] = claim.VerifiedAt.UTC().Format(time.RFC3339Nano)
	}
	if importID != "" {
		state["import_id"] = importID
	}
	return state
}

func factLedgerState(fact *domain.Fact) map[string]any {
	return factLedgerStateWithImport(fact, "")
}

func factLedgerStateWithImport(fact *domain.Fact, importID string) map[string]any {
	if fact == nil {
		return nil
	}
	state := map[string]any{
		"fact_id":   fact.FactID,
		"subject":   fact.Subject,
		"predicate": fact.Predicate,
		"object":    fact.Object,
		"status":    string(fact.Status),
	}
	if fact.ValidTo != nil {
		state["valid_to"] = fact.ValidTo.UTC().Format(time.RFC3339Nano)
	}
	if fact.RecordedTo != nil {
		state["recorded_to"] = fact.RecordedTo.UTC().Format(time.RFC3339Nano)
	}
	if fact.RetractedAt != nil {
		state["retracted_at"] = fact.RetractedAt.UTC().Format(time.RFC3339Nano)
	}
	if fact.LastConfirmedAt != nil {
		state["last_confirmed_at"] = fact.LastConfirmedAt.UTC().Format(time.RFC3339Nano)
	}
	if importID != "" {
		state["import_id"] = importID
	}
	return state
}

func factSummary(fact *domain.Fact) FactSummary {
	if fact == nil {
		return FactSummary{}
	}
	return FactSummary{
		FactID:    fact.FactID,
		Subject:   fact.Subject,
		Predicate: fact.Predicate,
		Object:    fact.Object,
		Status:    string(fact.Status),
	}
}

func claimSummary(claim *domain.Claim) ClaimSummary {
	if claim == nil {
		return ClaimSummary{}
	}
	return ClaimSummary{
		ClaimID:   claim.ClaimID,
		Subject:   claim.Subject,
		Predicate: claim.Predicate,
		Object:    claim.Object,
		Status:    string(claim.Status),
	}
}

func tripleMatches(query, subject, predicate, object string) bool {
	text := strings.ToLower(subject + " " + predicate + " " + object)
	for _, part := range strings.Fields(query) {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

func clampLimit(value, fallback, maxValue int) int {
	if value <= 0 {
		return fallback
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func confidenceFor(mode, sourceKind string) float64 {
	if mode == ModeTrusted {
		if sourceKind == SourceKindFact {
			return 0.98
		}
		return 0.9
	}
	return 0.8
}

func sourceQuality(mode string) float64 {
	if mode == ModeTrusted {
		return 1.0
	}
	return 0.8
}

func importAuthority(mode string) string {
	if mode == ModeTrusted {
		return "authoritative"
	}
	return "secondary"
}

func importSource(pack SkillPack, sourceURL string) string {
	if sourceURL != "" {
		return sourceURL
	}
	return "skill_pack:" + pack.Name
}

func fragmentContent(pack SkillPack, hash string) string {
	var b strings.Builder
	b.WriteString("Skill pack import: ")
	b.WriteString(pack.Name)
	b.WriteString("\nSHA-256: ")
	b.WriteString(hash)
	if pack.Description != "" {
		b.WriteString("\nDescription: ")
		b.WriteString(pack.Description)
	}
	for _, item := range pack.Items {
		b.WriteString("\n- ")
		b.WriteString(item.Subject)
		b.WriteString(" ")
		b.WriteString(item.Predicate)
		b.WriteString(" ")
		b.WriteString(item.Object)
		b.WriteString(" [")
		b.WriteString(item.SourceKind)
		b.WriteString("]")
	}
	content := b.String()
	if len(content) > 8192 {
		return content[:8192]
	}
	return content
}
