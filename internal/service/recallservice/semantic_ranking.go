package recallservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type SemanticRecallFusionMode string

const (
	SemanticRecallFusionRRF            SemanticRecallFusionMode = "rrf"
	SemanticRecallFusionBranchPriority SemanticRecallFusionMode = "branch_priority"
)

type SemanticRecallRankingProfile struct {
	FusionMode            SemanticRecallFusionMode
	RRFK                  int
	BranchWeights         map[domain.SemanticRecallBranch]float64
	BranchPriority        []domain.SemanticRecallBranch
	BranchLimitMultiplier int
	BranchLimitFloor      int
	BranchLimitMax        int
}

func DefaultSemanticRecallRankingProfile() SemanticRecallRankingProfile {
	return SemanticRecallRankingProfile{
		FusionMode: SemanticRecallFusionBranchPriority,
		RRFK:       RRFConstant,
		BranchWeights: map[domain.SemanticRecallBranch]float64{
			domain.SemanticRecallBranchExact:          2,
			domain.SemanticRecallBranchEvidenceText:   1,
			domain.SemanticRecallBranchEvidenceVector: 1,
		},
		BranchPriority: []domain.SemanticRecallBranch{
			domain.SemanticRecallBranchExact,
			domain.SemanticRecallBranchEvidenceVector,
			domain.SemanticRecallBranchEvidenceText,
		},
		BranchLimitMultiplier: semanticRecallBranchLimitMultiplier,
		BranchLimitFloor:      semanticRecallBranchLimitFloor,
		BranchLimitMax:        semanticRecallBranchLimitMax,
	}
}

func NormalizeSemanticRecallRankingProfile(profile SemanticRecallRankingProfile) SemanticRecallRankingProfile {
	defaults := DefaultSemanticRecallRankingProfile()
	if profile.FusionMode == "" {
		profile.FusionMode = defaults.FusionMode
	}
	if profile.FusionMode != SemanticRecallFusionRRF && profile.FusionMode != SemanticRecallFusionBranchPriority {
		profile.FusionMode = defaults.FusionMode
	}
	if profile.RRFK <= 0 {
		profile.RRFK = defaults.RRFK
	}
	if profile.BranchLimitMultiplier <= 0 {
		profile.BranchLimitMultiplier = defaults.BranchLimitMultiplier
	}
	if profile.BranchLimitFloor <= 0 {
		profile.BranchLimitFloor = defaults.BranchLimitFloor
	}
	if profile.BranchLimitMax <= 0 {
		profile.BranchLimitMax = defaults.BranchLimitMax
	}
	if profile.BranchLimitMax < profile.BranchLimitFloor {
		profile.BranchLimitMax = profile.BranchLimitFloor
	}
	profile.BranchWeights = normalizeSemanticRecallBranchWeights(profile.BranchWeights, defaults.BranchWeights)
	profile.BranchPriority = normalizeSemanticRecallBranchPriority(profile.BranchPriority, defaults.BranchPriority)
	return profile
}

func NewSemanticRecallRankingProfile(enabledRRF bool, rrfK int, branchWeights, branchPriority string, branchLimitMultiplier, branchLimitFloor, branchLimitMax int) (SemanticRecallRankingProfile, error) {
	profile := DefaultSemanticRecallRankingProfile()
	if !enabledRRF {
		profile.FusionMode = SemanticRecallFusionBranchPriority
	}
	profile.RRFK = rrfK
	profile.BranchLimitMultiplier = branchLimitMultiplier
	profile.BranchLimitFloor = branchLimitFloor
	profile.BranchLimitMax = branchLimitMax
	if strings.TrimSpace(branchWeights) != "" {
		weights, err := parseSemanticRecallBranchWeights(branchWeights)
		if err != nil {
			return SemanticRecallRankingProfile{}, err
		}
		profile.BranchWeights = weights
	}
	if strings.TrimSpace(branchPriority) != "" {
		priority, err := parseSemanticRecallBranchPriority(branchPriority)
		if err != nil {
			return SemanticRecallRankingProfile{}, err
		}
		profile.BranchPriority = priority
	}
	return NormalizeSemanticRecallRankingProfile(profile), nil
}

func parseSemanticRecallBranchWeights(value string) (map[domain.SemanticRecallBranch]float64, error) {
	out := map[domain.SemanticRecallBranch]float64{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, rawWeight, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("recall branch weight %q must be name=value", part)
		}
		branch, err := parseSemanticRecallBranchName(name)
		if err != nil {
			return nil, err
		}
		weight, err := strconv.ParseFloat(strings.TrimSpace(rawWeight), 64)
		if err != nil || weight <= 0 {
			return nil, fmt.Errorf("recall branch weight %q must be positive", part)
		}
		out[branch] = weight
	}
	if len(out) == 0 {
		return nil, errors.New("recall branch weights must include at least one branch")
	}
	return out, nil
}

func parseSemanticRecallBranchPriority(value string) ([]domain.SemanticRecallBranch, error) {
	var out []domain.SemanticRecallBranch
	seen := map[domain.SemanticRecallBranch]struct{}{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		branch, err := parseSemanticRecallBranchName(part)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[branch]; duplicate {
			return nil, fmt.Errorf("recall branch priority duplicates %q", part)
		}
		seen[branch] = struct{}{}
		out = append(out, branch)
	}
	if len(out) == 0 {
		return nil, errors.New("recall branch priority must include at least one branch")
	}
	return out, nil
}

func parseSemanticRecallBranchName(value string) (domain.SemanticRecallBranch, error) {
	switch domain.SemanticRecallBranch(strings.TrimSpace(value)) {
	case domain.SemanticRecallBranchExact:
		return domain.SemanticRecallBranchExact, nil
	case domain.SemanticRecallBranchEvidenceText:
		return domain.SemanticRecallBranchEvidenceText, nil
	case domain.SemanticRecallBranchEvidenceVector:
		return domain.SemanticRecallBranchEvidenceVector, nil
	default:
		return "", fmt.Errorf("unsupported semantic recall branch %q", strings.TrimSpace(value))
	}
}

func normalizeSemanticRecallBranchWeights(weights, defaults map[domain.SemanticRecallBranch]float64) map[domain.SemanticRecallBranch]float64 {
	out := make(map[domain.SemanticRecallBranch]float64, len(defaults)+len(weights))
	for branch, weight := range defaults {
		if semanticRecallInitialBranch(branch) && weight > 0 {
			out[branch] = weight
		}
	}
	for branch, weight := range weights {
		if semanticRecallInitialBranch(branch) && weight > 0 {
			out[branch] = weight
		}
	}
	return out
}

func normalizeSemanticRecallBranchPriority(priority, defaults []domain.SemanticRecallBranch) []domain.SemanticRecallBranch {
	seen := map[domain.SemanticRecallBranch]struct{}{}
	out := make([]domain.SemanticRecallBranch, 0, len(defaults))
	add := func(branch domain.SemanticRecallBranch) {
		if !semanticRecallInitialBranch(branch) {
			return
		}
		if _, duplicate := seen[branch]; duplicate {
			return
		}
		seen[branch] = struct{}{}
		out = append(out, branch)
	}
	for _, branch := range priority {
		add(branch)
	}
	for _, branch := range defaults {
		add(branch)
	}
	return out
}

func semanticRecallBranchPriorityMap(priority []domain.SemanticRecallBranch) map[domain.SemanticRecallBranch]int {
	out := make(map[domain.SemanticRecallBranch]int, len(priority))
	for index, branch := range priority {
		if semanticRecallInitialBranch(branch) {
			out[branch] = index
		}
	}
	return out
}

func NewSemanticRecallService(store SemanticRecallStore, embedder ...EmbeddingProvider) RecallService {
	var configured EmbeddingProvider
	if len(embedder) > 0 {
		configured = embedder[0]
	}
	return NewSemanticRecallServiceWithRanking(store, DefaultSemanticRecallRankingProfile(), configured)
}

func NewSemanticRecallServiceWithRanking(store SemanticRecallStore, ranking SemanticRecallRankingProfile, embedder ...EmbeddingProvider) RecallService {
	var configured EmbeddingProvider
	if len(embedder) > 0 {
		configured = embedder[0]
	}
	return &semanticRecallService{
		store:       store,
		embedder:    configured,
		vectorSlots: make(chan struct{}, semanticVectorConcurrency),
		ranking:     NormalizeSemanticRecallRankingProfile(ranking),
	}
}

func (s *semanticRecallService) Recall(ctx context.Context, profileID string, req RecallRequest) ([]RecallHit, error) {
	if s.store == nil {
		return nil, errors.New("semantic recall: store is required")
	}
	teamID := semanticRecallTeamID(ctx, profileID)
	if teamID == "" {
		return nil, errors.New("semantic recall: team id is required")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, errors.New("semantic recall: query is required")
	}
	limit := clampLimit(req.Limit)
	knownEvidenceIDs, err := normalizeUUIDList("known_evidence_ids", req.KnownEvidenceIDs, MaxKnownEvidenceIDs)
	if err != nil {
		return nil, err
	}
	expandFromEntityIDs, err := normalizeUUIDList("expand_from_entity_ids", req.ExpandFromEntityIDs, MaxExpandFromEntityIDs)
	if err != nil {
		return nil, err
	}
	knownRelationshipIDs, err := normalizeUUIDList("known_relationship_ids", req.KnownRelationshipIDs, MaxKnownRelationshipIDs)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	validAt := now
	if req.ValidAt != nil {
		validAt = req.ValidAt.UTC()
	}
	knownAt := now
	if req.KnownAt != nil {
		knownAt = req.KnownAt.UTC()
	}
	scope := domain.SemanticRecallSearchScope{
		TeamID:                   teamID,
		Features:                 parseSemanticRecallQuery(query),
		KnownEvidenceIDs:         knownEvidenceIDs,
		KnownRelationshipIDs:     knownRelationshipIDs,
		ExpandFromEntityIDs:      expandFromEntityIDs,
		ValidAt:                  validAt,
		KnownAt:                  knownAt,
		BranchLimit:              semanticRecallBranchLimit(limit, s.ranking),
		RelationshipsPerEvidence: semanticRecallRelationshipsPerEvidence,
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lexicalCh := make(chan semanticRecallBranchResult, 1)
	vectorCh := make(chan semanticRecallBranchResult, 1)

	go func() {
		batch, err := s.store.SearchRecallLexicalCandidates(ctx, scope)
		if err != nil {
			cancel()
		}
		lexicalCh <- semanticRecallBranchResult{batch: batch, err: err}
	}()
	go func() {
		result := s.searchVectorCandidates(ctx, scope, query)
		if result.err != nil {
			cancel()
		}
		vectorCh <- result
	}()

	lexical := <-lexicalCh
	vector := <-vectorCh
	if lexical.err != nil {
		return nil, lexical.err
	}
	if vector.err != nil {
		return nil, vector.err
	}
	scope.Embedding = vector.embedding
	scope.EmbeddingContractID = vector.embeddingContractID

	candidates := make([]domain.SemanticRecallCandidate, 0, len(lexical.batch.Candidates)+len(vector.batch.Candidates))
	candidates = append(candidates, lexical.batch.Candidates...)
	candidates = append(candidates, vector.batch.Candidates...)
	seeds := mergeSemanticEntitySeeds(lexical.batch.EntitySeeds, vector.batch.EntitySeeds, expandFromEntityIDs)
	var discoveryCandidates []domain.SemanticRecallCandidate
	if len(seeds) > 0 || len(expandFromEntityIDs) > 0 || len(knownRelationshipIDs) > 0 {
		adjacency, err := s.store.SearchRecallAdjacencyCandidates(ctx, scope, seeds)
		if err != nil {
			return nil, err
		}
		discoveryCandidates = selectSemanticDiscoveryCandidates(adjacency, knownEvidenceIDs, semanticRecallDiscoveryCandidateLimit)
	}

	fused := fuseSemanticRecallCandidates(candidates, scope.Features, knownEvidenceIDs, limit+semanticRecallHydrationMargin, s.ranking)
	if len(fused) == 0 && len(discoveryCandidates) == 0 {
		return nil, nil
	}
	evidenceIDs := make([]string, 0, len(fused)+len(discoveryCandidates))
	preferredRelationshipIDs := make([]string, 0, (len(fused)+len(discoveryCandidates))*semanticRecallRelationshipsPerEvidence)
	scoreByEvidenceID := make(map[string]float64, len(fused))
	rankByEvidenceID := make(map[string]int, len(fused))
	for index, item := range fused {
		evidenceIDs = append(evidenceIDs, item.evidenceID)
		scoreByEvidenceID[item.evidenceID] = item.score
		rankByEvidenceID[item.evidenceID] = index + 1
		preferredRelationshipIDs = append(preferredRelationshipIDs, item.relationshipIDs...)
	}
	for _, candidate := range discoveryCandidates {
		if strings.TrimSpace(candidate.EvidenceID) == "" {
			continue
		}
		evidenceIDs = append(evidenceIDs, candidate.EvidenceID)
		preferredRelationshipIDs = append(preferredRelationshipIDs, candidate.RelationshipIDs...)
	}
	evidenceIDs = uniqueNonEmptyStrings(evidenceIDs)
	hydrated, err := s.store.HydrateRecallEvidence(ctx, scope, evidenceIDs, uniqueNonEmptyStrings(preferredRelationshipIDs))
	if err != nil {
		return nil, err
	}
	resultsByEvidenceID := make(map[string]domain.SemanticRecallResult, len(hydrated))
	for _, result := range hydrated {
		if result.Evidence == nil || strings.TrimSpace(result.Evidence.FragmentID) == "" {
			continue
		}
		id := result.Evidence.FragmentID
		result.Score = scoreByEvidenceID[id]
		result.Rank = rankByEvidenceID[id]
		resultsByEvidenceID[id] = result
	}

	hits := make([]RecallHit, 0, limit+len(discoveryCandidates))
	answerEvidenceIDs := make(map[string]struct{}, limit)
	for _, item := range fused {
		result, ok := resultsByEvidenceID[item.evidenceID]
		if !ok || result.Evidence == nil {
			continue
		}
		answerEvidenceIDs[item.evidenceID] = struct{}{}
		hits = append(hits, RecallHit{
			Evidence:      result.Evidence,
			Relationships: append([]domain.SemanticRelationship(nil), result.Relationships...),
			Supports:      append([]domain.SemanticRelationshipSupport(nil), result.Supports...),
			Tier:          TierFragment,
			Score:         result.Score,
			FinalScore:    result.Score,
		})
		if len(hits) == limit {
			break
		}
	}
	for _, candidate := range discoveryCandidates {
		if _, alreadyResult := answerEvidenceIDs[candidate.EvidenceID]; alreadyResult {
			continue
		}
		result, ok := resultsByEvidenceID[candidate.EvidenceID]
		if !ok {
			continue
		}
		if len(result.Relationships) == 0 && len(result.Supports) == 0 {
			continue
		}
		hits = append(hits, RecallHit{
			Relationships: append([]domain.SemanticRelationship(nil), result.Relationships...),
			Supports:      append([]domain.SemanticRelationshipSupport(nil), result.Supports...),
		})
	}
	return hits, nil
}

func (s *semanticRecallService) searchVectorCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope, query string) semanticRecallBranchResult {
	if s.embedder == nil {
		return semanticRecallBranchResult{}
	}
	select {
	case s.vectorSlots <- struct{}{}:
		defer func() { <-s.vectorSlots }()
	case <-ctx.Done():
		return semanticRecallBranchResult{err: ctx.Err()}
	}
	vec, model, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return semanticRecallBranchResult{err: sanitizeEmbeddingError(err)}
	}
	scope.Embedding = vec
	scope.EmbeddingContractID = semanticRecallEmbeddingContractID(model, len(vec))
	batch, err := s.store.SearchRecallVectorCandidates(ctx, scope)
	if err != nil {
		return semanticRecallBranchResult{err: err}
	}
	return semanticRecallBranchResult{
		batch:               batch,
		embedding:           vec,
		embeddingContractID: scope.EmbeddingContractID,
	}
}

type semanticRecallFusedCandidate struct {
	evidenceID              string
	score                   float64
	bestRank                int
	exactMatch              bool
	preciseMatch            bool
	phraseMatch             bool
	allHardAnchorsMatched   bool
	factSupport             bool
	independentSourceGroups int
	latestValidFrom         *time.Time
	latestRecordedAt        time.Time
	relationshipIDs         []string
	branchSeen              map[domain.SemanticRecallBranch]struct{}
	order                   int
}

func fuseSemanticRecallCandidates(candidates []domain.SemanticRecallCandidate, features domain.SemanticRecallQueryFeatures, knownEvidenceIDs []string, limit int, ranking SemanticRecallRankingProfile) []semanticRecallFusedCandidate {
	ranking = NormalizeSemanticRecallRankingProfile(ranking)
	known := stringSet(knownEvidenceIDs)
	byBranchEvidence := map[string]domain.SemanticRecallCandidate{}
	for _, candidate := range candidates {
		evidenceID := strings.TrimSpace(candidate.EvidenceID)
		if evidenceID == "" {
			continue
		}
		if _, excluded := known[evidenceID]; excluded {
			continue
		}
		if !semanticRecallInitialBranch(candidate.Branch) {
			continue
		}
		key := string(candidate.Branch) + "\x00" + evidenceID
		existing, ok := byBranchEvidence[key]
		if !ok || semanticRecallCandidateBetter(candidate, existing) {
			candidate.EvidenceID = evidenceID
			byBranchEvidence[key] = candidate
		}
	}

	if ranking.FusionMode == SemanticRecallFusionBranchPriority {
		return fuseSemanticRecallCandidatesByBranchPriority(byBranchEvidence, features, limit, ranking)
	}
	return fuseSemanticRecallCandidatesByRRF(byBranchEvidence, features, limit, ranking)
}

func fuseSemanticRecallCandidatesByRRF(byBranchEvidence map[string]domain.SemanticRecallCandidate, features domain.SemanticRecallQueryFeatures, limit int, ranking SemanticRecallRankingProfile) []semanticRecallFusedCandidate {
	fusedByEvidenceID := map[string]*semanticRecallFusedCandidate{}
	for _, candidate := range byBranchEvidence {
		item := fusedByEvidenceID[candidate.EvidenceID]
		if item == nil {
			item = &semanticRecallFusedCandidate{
				evidenceID: candidate.EvidenceID,
				bestRank:   candidate.Rank,
				branchSeen: map[domain.SemanticRecallBranch]struct{}{},
				order:      len(fusedByEvidenceID),
			}
			fusedByEvidenceID[candidate.EvidenceID] = item
		}
		rank := candidate.Rank
		if rank <= 0 {
			rank = semanticRecallBranchLimitMax
		}
		item.score += semanticRecallBranchWeight(candidate.Branch, ranking) / float64(ranking.RRFK+rank)
		mergeSemanticRecallCandidateSignals(item, candidate, rank)
	}
	return orderSemanticRecallFusedCandidates(fusedByEvidenceID, features, limit)
}

func fuseSemanticRecallCandidatesByBranchPriority(byBranchEvidence map[string]domain.SemanticRecallCandidate, features domain.SemanticRecallQueryFeatures, limit int, ranking SemanticRecallRankingProfile) []semanticRecallFusedCandidate {
	candidates := make([]domain.SemanticRecallCandidate, 0, len(byBranchEvidence))
	for _, candidate := range byBranchEvidence {
		candidates = append(candidates, candidate)
	}
	priority := semanticRecallBranchPriorityMap(ranking.BranchPriority)
	sort.SliceStable(candidates, func(i, j int) bool {
		leftPriority, leftKnown := priority[candidates[i].Branch]
		rightPriority, rightKnown := priority[candidates[j].Branch]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if candidates[i].Rank != candidates[j].Rank {
			return candidates[i].Rank < candidates[j].Rank
		}
		return candidates[i].EvidenceID < candidates[j].EvidenceID
	})

	fusedByEvidenceID := map[string]*semanticRecallFusedCandidate{}
	priorityRank := 0
	for _, candidate := range candidates {
		if _, ok := priority[candidate.Branch]; !ok {
			continue
		}
		item := fusedByEvidenceID[candidate.EvidenceID]
		if item == nil {
			priorityRank++
			item = &semanticRecallFusedCandidate{
				evidenceID: candidate.EvidenceID,
				score:      1 / float64(1+priorityRank),
				bestRank:   candidate.Rank,
				branchSeen: map[domain.SemanticRecallBranch]struct{}{},
				order:      priorityRank - 1,
			}
			fusedByEvidenceID[candidate.EvidenceID] = item
		}
		rank := candidate.Rank
		if rank <= 0 {
			rank = semanticRecallBranchLimitMax
		}
		mergeSemanticRecallCandidateSignals(item, candidate, rank)
	}
	return orderSemanticRecallFusedCandidates(fusedByEvidenceID, features, limit)
}

func mergeSemanticRecallCandidateSignals(item *semanticRecallFusedCandidate, candidate domain.SemanticRecallCandidate, rank int) {
	if item.bestRank <= 0 || rank < item.bestRank {
		item.bestRank = rank
	}
	item.exactMatch = item.exactMatch || candidate.ExactMatch
	item.preciseMatch = item.preciseMatch || candidate.PreciseMatch
	item.phraseMatch = item.phraseMatch || candidate.PhraseMatch
	item.allHardAnchorsMatched = item.allHardAnchorsMatched || candidate.AllHardAnchorsMatched
	item.factSupport = item.factSupport || candidate.FactSupport
	if candidate.IndependentSourceGroups > item.independentSourceGroups {
		item.independentSourceGroups = candidate.IndependentSourceGroups
	}
	if candidate.LatestValidFrom != nil && (item.latestValidFrom == nil || candidate.LatestValidFrom.After(*item.latestValidFrom)) {
		t := candidate.LatestValidFrom.UTC()
		item.latestValidFrom = &t
	}
	if candidate.LatestRecordedAt.After(item.latestRecordedAt) {
		item.latestRecordedAt = candidate.LatestRecordedAt.UTC()
	}
	item.relationshipIDs = append(item.relationshipIDs, candidate.RelationshipIDs...)
	item.branchSeen[candidate.Branch] = struct{}{}
}

func orderSemanticRecallFusedCandidates(fusedByEvidenceID map[string]*semanticRecallFusedCandidate, features domain.SemanticRecallQueryFeatures, limit int) []semanticRecallFusedCandidate {
	ordered := make([]semanticRecallFusedCandidate, 0, len(fusedByEvidenceID))
	hasHardAnchorMatch := false
	for _, item := range fusedByEvidenceID {
		item.relationshipIDs = uniqueNonEmptyStrings(item.relationshipIDs)
		item.score = semanticRecallDeterministicScore(item, features)
		if item.allHardAnchorsMatched {
			hasHardAnchorMatch = true
		}
		ordered = append(ordered, *item)
	}
	if len(features.HardAnchors) > 0 && hasHardAnchorMatch {
		filtered := ordered[:0]
		for _, item := range ordered {
			if item.allHardAnchorsMatched {
				filtered = append(filtered, item)
			}
		}
		ordered = filtered
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		if ordered[i].bestRank != ordered[j].bestRank {
			return ordered[i].bestRank < ordered[j].bestRank
		}
		return ordered[i].evidenceID < ordered[j].evidenceID
	})
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

func semanticRecallCandidateBetter(next, current domain.SemanticRecallCandidate) bool {
	if next.Rank != current.Rank {
		if current.Rank <= 0 {
			return true
		}
		return next.Rank > 0 && next.Rank < current.Rank
	}
	if next.RawScore != current.RawScore {
		return next.RawScore > current.RawScore
	}
	return strings.Join(next.RelationshipIDs, "\x00") < strings.Join(current.RelationshipIDs, "\x00")
}

func semanticRecallBranchWeight(branch domain.SemanticRecallBranch, ranking SemanticRecallRankingProfile) float64 {
	if weight, ok := ranking.BranchWeights[branch]; ok && weight > 0 {
		return weight
	}
	return 0
}

func semanticRecallInitialBranch(branch domain.SemanticRecallBranch) bool {
	switch branch {
	case domain.SemanticRecallBranchExact,
		domain.SemanticRecallBranchEvidenceText,
		domain.SemanticRecallBranchEvidenceVector:
		return true
	default:
		return false
	}
}

func selectSemanticDiscoveryCandidates(candidates []domain.SemanticRecallCandidate, knownEvidenceIDs []string, limit int) []domain.SemanticRecallCandidate {
	if limit <= 0 || len(candidates) == 0 {
		return nil
	}
	known := stringSet(knownEvidenceIDs)
	seenEvidence := map[string]struct{}{}
	out := make([]domain.SemanticRecallCandidate, 0, limit)
	for _, candidate := range candidates {
		evidenceID := strings.TrimSpace(candidate.EvidenceID)
		if evidenceID == "" {
			continue
		}
		if _, skip := known[evidenceID]; skip {
			continue
		}
		if _, duplicate := seenEvidence[evidenceID]; duplicate {
			continue
		}
		seenEvidence[evidenceID] = struct{}{}
		candidate.EvidenceID = evidenceID
		candidate.RelationshipIDs = uniqueNonEmptyStrings(candidate.RelationshipIDs)
		out = append(out, candidate)
		if len(out) == limit {
			break
		}
	}
	return out
}

func semanticRecallDeterministicScore(item *semanticRecallFusedCandidate, features domain.SemanticRecallQueryFeatures) float64 {
	score := item.score
	if item.exactMatch {
		score *= 1.18
	}
	if item.allHardAnchorsMatched {
		score *= 1.15
	}
	if item.preciseMatch {
		score *= 1.08
	}
	if item.phraseMatch {
		score *= 1.05
	}
	if item.factSupport {
		score *= 1.03
	}
	if item.independentSourceGroups > 1 {
		bonus := 1 + minFloat64(0.06, 0.02*float64(item.independentSourceGroups-1))
		score *= bonus
	}
	if features.CurrentnessIntent && !item.latestRecordedAt.IsZero() {
		age := time.Since(item.latestRecordedAt)
		switch {
		case age <= 30*24*time.Hour:
			score *= 1.04
		case age <= 180*24*time.Hour:
			score *= 1.02
		}
	}
	if features.TemporalIntent && item.latestValidFrom != nil {
		score *= 1.02
	}
	return score
}

func mergeSemanticEntitySeeds(first, second []domain.SemanticRecallEntitySeed, explicitEntityIDs []string) []domain.SemanticRecallEntitySeed {
	byID := map[string]domain.SemanticRecallEntitySeed{}
	add := func(seed domain.SemanticRecallEntitySeed) {
		seed.EntityID = strings.TrimSpace(seed.EntityID)
		if seed.EntityID == "" {
			return
		}
		existing, ok := byID[seed.EntityID]
		if !ok {
			byID[seed.EntityID] = seed
			return
		}
		if seed.Rank > 0 && (existing.Rank <= 0 || seed.Rank < existing.Rank) {
			existing.Rank = seed.Rank
		}
		existing.Exact = existing.Exact || seed.Exact
		existing.HardAnchor = existing.HardAnchor || seed.HardAnchor
		existing.Explicit = existing.Explicit || seed.Explicit
		if seed.Score > existing.Score {
			existing.Score = seed.Score
		}
		byID[seed.EntityID] = existing
	}
	for _, seed := range first {
		add(seed)
	}
	for _, seed := range second {
		add(seed)
	}
	for index, id := range explicitEntityIDs {
		add(domain.SemanticRecallEntitySeed{
			EntityID: id,
			Rank:     index + 1,
			Exact:    true,
			Explicit: true,
			Score:    1,
		})
	}
	out := make([]domain.SemanticRecallEntitySeed, 0, len(byID))
	for _, seed := range byID {
		out = append(out, seed)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Exact != out[j].Exact {
			return out[i].Exact
		}
		if out[i].Explicit != out[j].Explicit {
			return out[i].Explicit
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].EntityID < out[j].EntityID
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}
