package recallservice

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/fulltextquery"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// RecallScopedReader is the minimal profile-scoped read interface required by
// the query-based fact/claim recall searchers.
type RecallScopedReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error)
}

type neo4jFactSearcher struct {
	reader RecallScopedReader
}

type neo4jClaimSearcher struct {
	reader RecallScopedReader
}

type neo4jCommunityExpander struct {
	reader RecallScopedReader
}

// NewFactSearcher builds the tier-1 recall searcher over the dedicated
// full-text index on Fact subject/predicate/object.
func NewFactSearcher(reader RecallScopedReader) FactSearcher {
	return &neo4jFactSearcher{reader: reader}
}

// NewClaimSearcher builds the tier-1.5 recall searcher over the dedicated
// full-text index on Claim subject/predicate/object.
func NewClaimSearcher(reader RecallScopedReader) ClaimSearcher {
	return &neo4jClaimSearcher{reader: reader}
}

// NewCommunityExpander builds the opt-in recall expander over persisted
// Community summaries and their original member nodes.
func NewCommunityExpander(reader RecallScopedReader) CommunityExpander {
	return &neo4jCommunityExpander{reader: reader}
}

func (s *neo4jFactSearcher) SearchActive(ctx context.Context, profileID string, query string, limit int) ([]FactRecallResult, error) {
	searchQuery := fulltextquery.PlainText(query)
	if searchQuery == "" {
		return []FactRecallResult{}, nil
	}

	cypher := `
	CALL db.index.fulltext.queryNodes('fact_recall_v2_idx', $searchQuery) YIELD node AS f, score
WHERE f.team_id = $profileId AND f.status = 'active'
OPTIONAL MATCH (incomingOverlay:Fact {team_id: $profileId})-[:OVERLAYS {team_id: $profileId}]->(f)
WITH f, score, count(incomingOverlay) AS incoming_overlay_count
OPTIONAL MATCH (f)-[:OVERLAYS {team_id: $profileId}]->(outgoingOverlay:Fact {team_id: $profileId})
WITH f, score, incoming_overlay_count, count(outgoingOverlay) AS outgoing_overlay_count
RETURN
    f.fact_id AS fact_id,
    f.team_id AS team_id,
    f.valid_from AS valid_from,
    f.valid_to AS valid_to,
    f.recorded_at AS recorded_at,
    f.recorded_to AS recorded_to,
    CASE
      WHEN outgoing_overlay_count > 0 THEN 'overlay'
      WHEN incoming_overlay_count > 0 THEN 'conflicted'
      ELSE 'authoritative'
    END AS authority_state,
    score
LIMIT $limit`

	_, rows, err := s.reader.ScopedRead(ctx, profileID, cypher, map[string]any{
		"searchQuery": searchQuery,
		"limit":       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("recall fact search: %w", err)
	}

	results := make([]FactRecallResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, FactRecallResult{
			FactID:         recallString(row, "fact_id"),
			ProfileID:      recallString(row, "team_id"),
			Score:          recallFloat64(row, "score"),
			ValidFrom:      recallTimePtr(row, "valid_from"),
			ValidTo:        recallTimePtr(row, "valid_to"),
			RecordedAt:     recallTime(row, "recorded_at"),
			RecordedTo:     recallTimePtr(row, "recorded_to"),
			AuthorityState: recallString(row, "authority_state"),
		})
	}

	return results, nil
}

func (s *neo4jClaimSearcher) SearchValidated(ctx context.Context, profileID string, query string, limit int) ([]ClaimRecallResult, error) {
	searchQuery := fulltextquery.PlainText(query)
	if searchQuery == "" {
		return []ClaimRecallResult{}, nil
	}

	cypher := `
	CALL db.index.fulltext.queryNodes('claim_recall_v2_idx', $searchQuery) YIELD node AS c, score
WHERE c.team_id = $profileId AND c.status = 'validated'
RETURN
    c.claim_id AS claim_id,
    c.team_id AS team_id,
    c.valid_from AS valid_from,
    c.valid_to AS valid_to,
    c.recorded_at AS recorded_at,
    c.recorded_to AS recorded_to,
    score
LIMIT $limit`

	_, rows, err := s.reader.ScopedRead(ctx, profileID, cypher, map[string]any{
		"searchQuery": searchQuery,
		"limit":       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("recall claim search: %w", err)
	}

	results := make([]ClaimRecallResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, ClaimRecallResult{
			ClaimID:    recallString(row, "claim_id"),
			ProfileID:  recallString(row, "team_id"),
			Score:      recallFloat64(row, "score"),
			ValidFrom:  recallTimePtr(row, "valid_from"),
			ValidTo:    recallTimePtr(row, "valid_to"),
			RecordedAt: recallTime(row, "recorded_at"),
			RecordedTo: recallTimePtr(row, "recorded_to"),
		})
	}

	return results, nil
}

const communityExpansionListCypher = `
MATCH (c:Community {team_id: $profileId})
WHERE coalesce(c.summary, '') <> ''
  AND coalesce(c.member_count, 0) > 0
RETURN c.community_id AS community_id,
       c.team_id AS team_id,
       c.level AS level,
       c.summary AS summary,
       c.summary_version AS summary_version,
       c.member_count AS member_count,
       c.top_entities AS top_entities,
       c.top_predicates AS top_predicates,
       c.last_summarized_at AS last_summarized_at
ORDER BY c.member_count DESC, c.community_id ASC
LIMIT $limit`

const communityExpansionMembersCypher = `
UNWIND $communityIds AS communityId
CALL {
  WITH communityId
  MATCH (n)
  WHERE n.team_id = $profileId
    AND toString(n.community_id) = communityId
    AND (
      (n:Fact AND coalesce(n.status, '') = 'active') OR
      (n:Claim AND coalesce(n.status, '') = 'validated') OR
      (n:SourceFragment AND coalesce(n.status, 'active') <> 'retracted')
    )
  RETURN n
  ORDER BY
    CASE
      WHEN 'Fact' IN labels(n) THEN 0
      WHEN 'Claim' IN labels(n) THEN 1
      ELSE 2
    END,
    coalesce(n.truth_score, n.extract_conf, 0.25) DESC,
    coalesce(n.fact_id, n.claim_id, n.fragment_id, '') ASC
  LIMIT $membersPerCommunity
}
RETURN toString(n.community_id) AS community_id,
       CASE
         WHEN 'Fact' IN labels(n) THEN 'fact'
         WHEN 'Claim' IN labels(n) THEN 'claim'
         ELSE 'fragment'
       END AS member_type,
       n.fact_id AS fact_id,
       n.claim_id AS claim_id,
       n.fragment_id AS fragment_id,
       n.team_id AS team_id,
       n.valid_from AS valid_from,
       n.valid_to AS valid_to,
       n.recorded_at AS recorded_at,
       n.recorded_to AS recorded_to,
       coalesce(n.truth_score, n.extract_conf, 0.25) AS score
LIMIT $maxCandidates`

type communityExpansionCandidate struct {
	community *domain.Community
	score     float64
}

func (s *neo4jCommunityExpander) Expand(ctx context.Context, profileID string, query string, opts CommunityExpansionOptions) (CommunityExpansion, error) {
	opts = normalizeCommunityExpansionOptions(opts)
	tokens := communityQueryTokens(query)
	if len(tokens) == 0 {
		return CommunityExpansion{}, nil
	}

	_, rows, err := s.reader.ScopedRead(ctx, profileID, communityExpansionListCypher, map[string]any{
		"limit": opts.ScanLimit,
	})
	if err != nil {
		return CommunityExpansion{}, fmt.Errorf("recall community list: %w", err)
	}

	candidates := make([]communityExpansionCandidate, 0, len(rows))
	for _, row := range rows {
		community := recallRowToCommunity(row)
		if community == nil || community.ProfileID != "" && community.ProfileID != profileID {
			continue
		}
		score := scoreCommunitySummary(community, tokens, query)
		if score < opts.MinScore {
			continue
		}
		candidates = append(candidates, communityExpansionCandidate{community: community, score: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].community.MemberCount != candidates[j].community.MemberCount {
			return candidates[i].community.MemberCount > candidates[j].community.MemberCount
		}
		return candidates[i].community.CommunityID < candidates[j].community.CommunityID
	})
	if len(candidates) > opts.CommunityLimit {
		candidates = candidates[:opts.CommunityLimit]
	}
	if len(candidates) == 0 {
		return CommunityExpansion{}, nil
	}

	communityIDs := make([]string, 0, len(candidates))
	communityScores := make(map[string]float64, len(candidates))
	for _, candidate := range candidates {
		communityIDs = append(communityIDs, candidate.community.CommunityID)
		communityScores[candidate.community.CommunityID] = candidate.score
	}

	_, rows, err = s.reader.ScopedRead(ctx, profileID, communityExpansionMembersCypher, map[string]any{
		"communityIds":        communityIDs,
		"membersPerCommunity": opts.MembersPerCommunity,
		"maxCandidates":       opts.MaxCandidates,
	})
	if err != nil {
		return CommunityExpansion{}, fmt.Errorf("recall community members: %w", err)
	}

	expansion := CommunityExpansion{SelectedCommunities: len(candidates)}
	for _, row := range rows {
		communityID := recallString(row, "community_id")
		score := communityMemberScore(communityScores[communityID], recallFloat64(row, "score"))
		switch recallString(row, "member_type") {
		case "fact":
			expansion.Facts = append(expansion.Facts, FactRecallResult{
				FactID:     recallString(row, "fact_id"),
				ProfileID:  recallString(row, "team_id"),
				Score:      score,
				ValidFrom:  recallTimePtr(row, "valid_from"),
				ValidTo:    recallTimePtr(row, "valid_to"),
				RecordedAt: recallTime(row, "recorded_at"),
				RecordedTo: recallTimePtr(row, "recorded_to"),
			})
		case "claim":
			expansion.Claims = append(expansion.Claims, ClaimRecallResult{
				ClaimID:    recallString(row, "claim_id"),
				ProfileID:  recallString(row, "team_id"),
				Score:      score,
				ValidFrom:  recallTimePtr(row, "valid_from"),
				ValidTo:    recallTimePtr(row, "valid_to"),
				RecordedAt: recallTime(row, "recorded_at"),
				RecordedTo: recallTimePtr(row, "recorded_to"),
			})
		case "fragment":
			expansion.Fragments = append(expansion.Fragments, CommunityFragmentRecallResult{
				FragmentID: recallString(row, "fragment_id"),
				ProfileID:  recallString(row, "team_id"),
				Score:      score,
			})
		}
	}
	return expansion, nil
}

func normalizeCommunityExpansionOptions(opts CommunityExpansionOptions) CommunityExpansionOptions {
	if opts.CommunityLimit <= 0 {
		opts.CommunityLimit = DefaultCommunityExpansionCommunityLimit
	}
	if opts.CommunityLimit > 10 {
		opts.CommunityLimit = 10
	}
	if opts.MembersPerCommunity <= 0 {
		opts.MembersPerCommunity = DefaultCommunityExpansionMembersPerCommunity
	}
	if opts.MembersPerCommunity > 25 {
		opts.MembersPerCommunity = 25
	}
	if opts.ScanLimit <= 0 {
		opts.ScanLimit = DefaultCommunityExpansionScanLimit
	}
	if opts.ScanLimit > 100 {
		opts.ScanLimit = 100
	}
	if opts.ScanLimit < opts.CommunityLimit {
		opts.ScanLimit = opts.CommunityLimit
	}
	if opts.MaxCandidates <= 0 {
		opts.MaxCandidates = opts.CommunityLimit * opts.MembersPerCommunity
	}
	if opts.MaxCandidates > 100 {
		opts.MaxCandidates = 100
	}
	if opts.MinScore <= 0 {
		opts.MinScore = DefaultCommunityExpansionMinScore
	}
	return opts
}

func recallRowToCommunity(row map[string]any) *domain.Community {
	communityID := recallString(row, "community_id")
	if communityID == "" {
		return nil
	}
	return &domain.Community{
		CommunityID:      communityID,
		ProfileID:        recallString(row, "team_id"),
		Level:            recallInt(row, "level"),
		Summary:          recallString(row, "summary"),
		SummaryVersion:   recallString(row, "summary_version"),
		MemberCount:      recallInt(row, "member_count"),
		TopEntities:      recallStringSlice(row, "top_entities"),
		TopPredicates:    recallStringSlice(row, "top_predicates"),
		LastSummarizedAt: recallTime(row, "last_summarized_at"),
	}
}

func scoreCommunitySummary(community *domain.Community, tokens []string, query string) float64 {
	if community == nil || strings.TrimSpace(community.Summary) == "" || community.MemberCount <= 0 {
		return 0
	}
	parts := make([]string, 0, 1+len(community.TopEntities)+len(community.TopPredicates))
	parts = append(parts, community.Summary)
	parts = append(parts, community.TopEntities...)
	parts = append(parts, community.TopPredicates...)
	text := strings.ToLower(strings.Join(parts, " "))
	matched := 0
	for _, token := range tokens {
		if strings.Contains(text, token) {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	score := float64(matched) / float64(len(tokens))
	phrase := strings.ToLower(strings.TrimSpace(query))
	if phrase != "" && strings.Contains(text, phrase) {
		score += 0.25
	}
	if score > 1 {
		return 1
	}
	return score
}

func communityQueryTokens(query string) []string {
	seen := map[string]struct{}{}
	tokens := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" || communityStopWords[token] {
			continue
		}
		if len(token) < 2 {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

var communityStopWords = map[string]bool{
	"about": true,
	"and":   true,
	"are":   true,
	"can":   true,
	"for":   true,
	"from":  true,
	"how":   true,
	"that":  true,
	"the":   true,
	"this":  true,
	"what":  true,
	"when":  true,
	"where": true,
	"will":  true,
	"with":  true,
}

func communityMemberScore(communityScore, memberScore float64) float64 {
	if communityScore <= 0 {
		return memberScore
	}
	if memberScore <= 0 {
		return communityScore
	}
	return communityScore * memberScore
}

func recallString(row map[string]any, key string) string {
	if v, ok := row[key].(string); ok {
		return v
	}
	return ""
}

func recallInt(row map[string]any, key string) int {
	switch v := row[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case int32:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	default:
		return 0
	}
}

func recallFloat64(row map[string]any, key string) float64 {
	switch v := row[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	default:
		return 0
	}
}

func recallStringSlice(row map[string]any, key string) []string {
	switch v := row[key].(type) {
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func recallTimePtr(row map[string]any, key string) *time.Time {
	if v, ok := row[key].(time.Time); ok {
		return &v
	}
	return nil
}

func recallTime(row map[string]any, key string) time.Time {
	if v, ok := row[key].(time.Time); ok {
		return v
	}
	return time.Time{}
}
