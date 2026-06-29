package recallservice

import (
	"context"
	"errors"
	"sort"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func (s *recallService) enrichCommunityHits(ctx context.Context, profileID string, limit int, req RecallRequest, existing []RecallHit) []RecallHit {
	if s.communityExpander == nil || limit <= 0 {
		return nil
	}
	expansion, err := s.communityExpander.Expand(ctx, profileID, req.Query, defaultCommunityExpansionOptions())
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("recall: community expansion failed",
				observability.String("error", err.Error()),
			)
		}
		return nil
	}

	seen := recallHitKeySet(existing)
	hits := make([]RecallHit, 0, limit)
	hits = append(hits, s.communityFactHits(ctx, profileID, req, seen, expansion.Facts)...)
	hits = append(hits, s.communityClaimHits(ctx, profileID, req, seen, expansion.Claims)...)
	hits = append(hits, s.communityFragmentHits(ctx, profileID, seen, expansion.Fragments)...)
	sortRecallHits(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if s.logger != nil {
		s.logger.Info("recall: community expansion",
			observability.Int("selected_communities", expansion.SelectedCommunities),
			observability.Int("candidate_count", len(expansion.Facts)+len(expansion.Claims)+len(expansion.Fragments)),
			observability.Int("retained_count", len(hits)),
		)
	}
	return hits
}

func (s *recallService) communityFactHits(ctx context.Context, profileID string, req RecallRequest, seen map[string]struct{}, candidates []FactRecallResult) []RecallHit {
	if s.factGet == nil || len(candidates) == 0 {
		return nil
	}
	filtered := make([]FactRecallResult, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.FactID == "" || candidate.ProfileID != "" && candidate.ProfileID != profileID {
			continue
		}
		key := recallCandidateKey("fact", candidate.FactID)
		if _, ok := seen[key]; ok {
			continue
		}
		if !factCandidateMatchesRecallWindow(candidate, req.ValidAt, req.KnownAt) {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, candidate)
	}
	factsByID, batchFacts := s.batchHydrateFacts(ctx, profileID, filtered)
	hydrated := make([]hydratedFactRecallCandidate, 0, len(filtered))
	for _, candidate := range filtered {
		var f *domain.Fact
		if batchFacts {
			f = factsByID[candidate.FactID]
			if f == nil {
				s.logHydrateError(candidate.FactID, errors.New("fact not found"))
				continue
			}
		} else {
			var err error
			f, err = s.factGet.Get(ctx, profileID, candidate.FactID)
			if err != nil {
				s.logHydrateError(candidate.FactID, err)
				continue
			}
		}
		if f.ProfileID != "" && f.ProfileID != profileID {
			continue
		}
		if f.Status != domain.FactStatusActive {
			continue
		}
		if !factMatchesRecallWindow(f, req.ValidAt, req.KnownAt) {
			continue
		}
		authorityState := candidate.AuthorityState
		if authorityState == "" {
			authorityState = f.AuthorityState
		}
		if authorityState == "" {
			authorityState = "authoritative"
		}
		hydrated = append(hydrated, hydratedFactRecallCandidate{Fact: f, AuthorityState: authorityState})
	}
	var evidenceFragments map[string]*domain.Fragment
	var temporalFrame typedCurrentnessTemporalFrame
	if isCurrentnessQuery(req.Query) {
		evidenceFragments = s.batchHydrateEvidenceFragments(ctx, profileID, factEvidenceSets(hydrated))
		temporalFrame = typedCurrentnessTemporalFrameForFacts(req.Query, hydrated, evidenceFragments)
	}
	hits := make([]RecallHit, 0, len(hydrated))
	for _, candidate := range hydrated {
		f := candidate.Fact
		f.AuthorityState = candidate.AuthorityState
		tier := TierActiveFact
		if candidate.AuthorityState != "authoritative" {
			tier = TierConflict
		}
		temporalRank := typedTemporalRankTimeForRecallWithEvidence(req.Query, f.ValidFrom, f.ValidTo, f.RecordedAt, f.Evidence, evidenceFragments, temporalFrame, f.Subject, f.Predicate, f.Object)
		if !req.IncludeEvidence {
			factCopy := *f
			factCopy.Evidence = nil
			f = &factCopy
		}
		hits = append(hits, RecallHit{
			Fact:         f,
			Tier:         tier,
			Score:        f.TruthScore,
			temporalRank: temporalRank,
		})
	}
	return hits
}

func (s *recallService) communityClaimHits(ctx context.Context, profileID string, req RecallRequest, seen map[string]struct{}, candidates []ClaimRecallResult) []RecallHit {
	if s.claimGet == nil || len(candidates) == 0 {
		return nil
	}
	filtered := make([]ClaimRecallResult, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ClaimID == "" || candidate.ProfileID != "" && candidate.ProfileID != profileID {
			continue
		}
		key := recallCandidateKey("claim", candidate.ClaimID)
		if _, ok := seen[key]; ok {
			continue
		}
		if !claimCandidateMatchesRecallWindow(candidate, req.ValidAt, req.KnownAt) {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, candidate)
	}
	claimsByID, batchClaims := s.batchHydrateClaims(ctx, profileID, filtered)
	hydrated := make([]*domain.Claim, 0, len(filtered))
	for _, candidate := range filtered {
		var c *domain.Claim
		if batchClaims {
			c = claimsByID[candidate.ClaimID]
			if c == nil {
				s.logHydrateError(candidate.ClaimID, errors.New("claim not found"))
				continue
			}
		} else {
			var err error
			c, err = s.claimGet.Get(ctx, profileID, candidate.ClaimID)
			if err != nil {
				s.logHydrateError(candidate.ClaimID, err)
				continue
			}
		}
		if c.ProfileID != "" && c.ProfileID != profileID {
			continue
		}
		if c.Status != domain.StatusValidated {
			continue
		}
		if !claimMatchesRecallWindow(c, req.ValidAt, req.KnownAt) {
			continue
		}
		hydrated = append(hydrated, c)
	}
	var evidenceFragments map[string]*domain.Fragment
	var temporalFrame typedCurrentnessTemporalFrame
	if isCurrentnessQuery(req.Query) {
		evidenceFragments = s.batchHydrateEvidenceFragments(ctx, profileID, claimEvidenceSets(hydrated))
		temporalFrame = typedCurrentnessTemporalFrameForClaims(req.Query, hydrated, evidenceFragments)
	}
	hits := make([]RecallHit, 0, len(hydrated))
	for _, c := range hydrated {
		temporalRank := typedTemporalRankTimeForRecallWithEvidence(req.Query, c.ValidFrom, c.ValidTo, c.RecordedAt, c.Evidence, evidenceFragments, temporalFrame, c.Subject, c.Predicate, c.Object)
		if !req.IncludeEvidence {
			claimCopy := *c
			claimCopy.Evidence = nil
			c = &claimCopy
		}
		hits = append(hits, RecallHit{
			Claim:        c,
			Tier:         TierValidatedClaim,
			Score:        c.ExtractConf * s.claimWeight,
			temporalRank: temporalRank,
		})
	}
	return hits
}

func (s *recallService) communityFragmentHits(ctx context.Context, profileID string, seen map[string]struct{}, candidates []CommunityFragmentRecallResult) []RecallHit {
	if s.hydrator == nil || len(candidates) == 0 {
		return nil
	}
	filtered := make([]CommunityFragmentRecallResult, 0, len(candidates))
	entries := make([]rrfEntry, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.FragmentID == "" || candidate.ProfileID != "" && candidate.ProfileID != profileID {
			continue
		}
		key := recallCandidateKey("fragment", candidate.FragmentID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, candidate)
		entries = append(entries, rrfEntry{id: candidate.FragmentID})
	}
	fragmentsByID, batchFragments := s.batchHydrateFragments(ctx, profileID, entries)
	hits := make([]RecallHit, 0, len(filtered))
	for _, candidate := range filtered {
		var frag *domain.Fragment
		if batchFragments {
			frag = fragmentsByID[candidate.FragmentID]
			if frag == nil {
				s.logHydrateError(candidate.FragmentID, errors.New("fragment not found"))
				continue
			}
		} else {
			var err error
			frag, err = s.hydrator.GetByID(ctx, profileID, candidate.FragmentID)
			if err != nil {
				s.logHydrateError(candidate.FragmentID, err)
				continue
			}
		}
		if frag.ProfileID != "" && frag.ProfileID != profileID {
			continue
		}
		if frag.Status == domain.FragmentStatusRetracted {
			continue
		}
		hits = append(hits, RecallHit{
			Fragment:   frag,
			Tier:       TierFragment,
			Score:      candidate.Score,
			FinalScore: candidate.Score,
		})
	}
	return hits
}

func defaultCommunityExpansionOptions() CommunityExpansionOptions {
	return CommunityExpansionOptions{
		CommunityLimit:      DefaultCommunityExpansionCommunityLimit,
		MembersPerCommunity: DefaultCommunityExpansionMembersPerCommunity,
		ScanLimit:           DefaultCommunityExpansionScanLimit,
		MaxCandidates:       DefaultCommunityExpansionCommunityLimit * DefaultCommunityExpansionMembersPerCommunity,
		MinScore:            DefaultCommunityExpansionMinScore,
	}
}

func sortRecallHits(hits []RecallHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		leftTier := recallSortTier(hits[i])
		rightTier := recallSortTier(hits[j])
		if leftTier != rightTier {
			return leftTier < rightTier
		}
		if !hits[i].temporalRank.IsZero() || !hits[j].temporalRank.IsZero() {
			switch {
			case hits[i].temporalRank.IsZero():
				return false
			case hits[j].temporalRank.IsZero():
				return true
			case !hits[i].temporalRank.Equal(hits[j].temporalRank):
				return hits[i].temporalRank.After(hits[j].temporalRank)
			}
		}
		return hits[i].Score > hits[j].Score
	})
}

func recallSortTier(hit RecallHit) string {
	if hit.sortTier != "" {
		return hit.sortTier
	}
	return hit.Tier
}

func recallHitKeySet(hits []RecallHit) map[string]struct{} {
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		if key, ok := recallHitKey(hit); ok {
			seen[key] = struct{}{}
		}
	}
	return seen
}

func recallHitKey(hit RecallHit) (string, bool) {
	switch {
	case hit.Fact != nil && hit.Fact.FactID != "":
		return recallCandidateKey("fact", hit.Fact.FactID), true
	case hit.Claim != nil && hit.Claim.ClaimID != "":
		return recallCandidateKey("claim", hit.Claim.ClaimID), true
	case hit.Fragment != nil && hit.Fragment.FragmentID != "":
		return recallCandidateKey("fragment", hit.Fragment.FragmentID), true
	default:
		return "", false
	}
}

func recallCandidateKey(kind, id string) string {
	return kind + ":" + id
}
