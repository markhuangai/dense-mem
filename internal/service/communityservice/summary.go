package communityservice

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const communitySummaryVersion = "community-deterministic-v1"

type communitySummaryStore interface {
	LoadSummaryInputs(ctx context.Context, profileID string) ([]communitySummaryInput, error)
	Replace(ctx context.Context, profileID string, communities []*domain.Community) error
}

type communityReadStore interface {
	Get(ctx context.Context, profileID string, communityID string) (*domain.Community, error)
	List(ctx context.Context, profileID string, limit int) ([]*domain.Community, error)
}

type communitySummaryInput struct {
	CommunityID  string
	MemberCount  int
	FactTriples  []communityTriple
	ClaimTriples []communityTriple
}

type communityTriple struct {
	Subject   string
	Predicate string
	Object    string
}

type neo4jCommunityStore struct {
	client gdsClient
}

func newNeo4jCommunityStore(client gdsClient) *neo4jCommunityStore {
	return &neo4jCommunityStore{client: client}
}

const communityCountsCypher = `
MATCH (n)
WHERE n.team_id = $profileId
  AND n.community_id IS NOT NULL
  AND ((n:SourceFragment AND coalesce(n.status, 'active') = 'active')
    OR (n:Claim AND coalesce(n.status, '') = 'validated')
    OR (n:Fact AND coalesce(n.status, '') = 'active'))
RETURN toString(n.community_id) AS community_id,
       count(n) AS member_count
ORDER BY member_count DESC, community_id ASC`

const communityFactTriplesCypher = `
MATCH (f:Fact {team_id: $profileId})
WHERE f.community_id IS NOT NULL
  AND coalesce(f.status, '') IN ['active', 'needs_revalidation']
RETURN toString(f.community_id) AS community_id,
       coalesce(f.subject, '') AS subject,
       coalesce(f.predicate, '') AS predicate,
       coalesce(f.object, '') AS object`

const communityClaimTriplesCypher = `
MATCH (c:Claim {team_id: $profileId})
WHERE c.community_id IS NOT NULL
  AND coalesce(c.status, 'candidate') <> 'rejected'
RETURN toString(c.community_id) AS community_id,
       coalesce(c.subject, '') AS subject,
       coalesce(c.predicate, '') AS predicate,
       coalesce(c.object, '') AS object`

const replaceCommunitiesDeleteCypher = `
MATCH (c:Community {team_id: $profileId})
WHERE NOT c.community_id IN $communityIDs
DETACH DELETE c`

const replaceCommunitiesUpsertCypher = `
UNWIND $communities AS community
MERGE (c:Community {team_id: $profileId, community_id: community.community_id})
SET c.level = community.level,
    c.summary = community.summary,
    c.summary_version = community.summary_version,
    c.member_count = community.member_count,
    c.top_entities = community.top_entities,
    c.top_predicates = community.top_predicates,
    c.last_summarized_at = community.last_summarized_at`

const getCommunityCypher = `
MATCH (c:Community {team_id: $profileId, community_id: $communityId})
RETURN c.community_id AS community_id,
       c.team_id AS team_id,
       c.level AS level,
       c.summary AS summary,
       c.summary_version AS summary_version,
       c.member_count AS member_count,
       c.top_entities AS top_entities,
       c.top_predicates AS top_predicates,
       c.last_summarized_at AS last_summarized_at`

const listCommunitiesCypher = `
MATCH (c:Community {team_id: $profileId})
RETURN c.community_id AS community_id,
       c.team_id AS team_id,
       c.level AS level,
       c.summary AS summary,
       c.summary_version AS summary_version,
       c.member_count AS member_count,
       c.top_entities AS top_entities,
       c.top_predicates AS top_predicates,
       c.last_summarized_at AS last_summarized_at
ORDER BY c.member_count DESC, c.community_id ASC`

func (s *neo4jCommunityStore) LoadSummaryInputs(ctx context.Context, profileID string) ([]communitySummaryInput, error) {
	countRows, err := s.readRows(ctx, communityCountsCypher, map[string]any{"profileId": profileID})
	if err != nil {
		return nil, fmt.Errorf("load community counts: %w", err)
	}
	inputsByID := make(map[string]*communitySummaryInput, len(countRows))
	for _, row := range countRows {
		communityID, _ := row["community_id"].(string)
		if communityID == "" {
			continue
		}
		inputsByID[communityID] = &communitySummaryInput{
			CommunityID: communityID,
			MemberCount: communityInt(row["member_count"]),
		}
	}

	factRows, err := s.readRows(ctx, communityFactTriplesCypher, map[string]any{"profileId": profileID})
	if err != nil {
		return nil, fmt.Errorf("load community facts: %w", err)
	}
	for _, row := range factRows {
		communityID, _ := row["community_id"].(string)
		if communityID == "" {
			continue
		}
		input := ensureCommunityInput(inputsByID, communityID)
		input.FactTriples = append(input.FactTriples, communityTriple{
			Subject:   communityString(row["subject"]),
			Predicate: communityString(row["predicate"]),
			Object:    communityString(row["object"]),
		})
	}

	claimRows, err := s.readRows(ctx, communityClaimTriplesCypher, map[string]any{"profileId": profileID})
	if err != nil {
		return nil, fmt.Errorf("load community claims: %w", err)
	}
	for _, row := range claimRows {
		communityID, _ := row["community_id"].(string)
		if communityID == "" {
			continue
		}
		input := ensureCommunityInput(inputsByID, communityID)
		input.ClaimTriples = append(input.ClaimTriples, communityTriple{
			Subject:   communityString(row["subject"]),
			Predicate: communityString(row["predicate"]),
			Object:    communityString(row["object"]),
		})
	}

	inputs := make([]communitySummaryInput, 0, len(inputsByID))
	for _, input := range inputsByID {
		inputs = append(inputs, *input)
	}
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].MemberCount == inputs[j].MemberCount {
			return inputs[i].CommunityID < inputs[j].CommunityID
		}
		return inputs[i].MemberCount > inputs[j].MemberCount
	})
	return inputs, nil
}

func (s *neo4jCommunityStore) Replace(ctx context.Context, profileID string, communities []*domain.Community) error {
	communityIDs := make([]string, 0, len(communities))
	communityPayload := make([]map[string]any, 0, len(communities))
	for _, community := range communities {
		if community == nil || community.CommunityID == "" {
			continue
		}
		communityIDs = append(communityIDs, community.CommunityID)
		communityPayload = append(communityPayload, map[string]any{
			"community_id":       community.CommunityID,
			"level":              community.Level,
			"summary":            community.Summary,
			"summary_version":    community.SummaryVersion,
			"member_count":       community.MemberCount,
			"top_entities":       community.TopEntities,
			"top_predicates":     community.TopPredicates,
			"last_summarized_at": community.LastSummarizedAt,
		})
	}

	_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		deleteResult, runErr := tx.Run(ctx, replaceCommunitiesDeleteCypher, map[string]any{
			"profileId":    profileID,
			"communityIDs": communityIDs,
		})
		if runErr != nil {
			return nil, runErr
		}
		if _, consumeErr := deleteResult.Consume(ctx); consumeErr != nil {
			return nil, consumeErr
		}

		if len(communityPayload) == 0 {
			return nil, nil
		}

		upsertResult, runErr := tx.Run(ctx, replaceCommunitiesUpsertCypher, map[string]any{
			"profileId":   profileID,
			"communities": communityPayload,
		})
		if runErr != nil {
			return nil, runErr
		}
		_, consumeErr := upsertResult.Consume(ctx)
		return nil, consumeErr
	})
	if err != nil {
		return fmt.Errorf("replace communities: %w", err)
	}
	return nil
}

func (s *neo4jCommunityStore) Get(ctx context.Context, profileID string, communityID string) (*domain.Community, error) {
	rows, err := s.readRows(ctx, getCommunityCypher, map[string]any{
		"profileId":   profileID,
		"communityId": communityID,
	})
	if err != nil {
		return nil, fmt.Errorf("get community: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrCommunityNotFound
	}
	return rowToCommunity(rows[0]), nil
}

func (s *neo4jCommunityStore) List(ctx context.Context, profileID string, limit int) ([]*domain.Community, error) {
	query := listCommunitiesCypher
	params := map[string]any{"profileId": profileID}
	if limit > 0 {
		query += "\nLIMIT $limit"
		params["limit"] = int64(limit)
	}

	rows, err := s.readRows(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("list communities: %w", err)
	}

	communities := make([]*domain.Community, 0, len(rows))
	for _, row := range rows {
		community := rowToCommunity(row)
		if community != nil {
			communities = append(communities, community)
		}
	}
	return communities, nil
}

func (s *neo4jCommunityStore) readRows(ctx context.Context, query string, params map[string]any) ([]map[string]any, error) {
	raw, err := s.client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, runErr := tx.Run(ctx, query, params)
		if runErr != nil {
			return nil, runErr
		}
		records, collectErr := result.Collect(ctx)
		if collectErr != nil {
			return nil, collectErr
		}
		rows := make([]map[string]any, 0, len(records))
		for _, record := range records {
			rows = append(rows, record.AsMap())
		}
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	rows, _ := raw.([]map[string]any)
	return rows, nil
}

func buildCommunitySummaries(profileID string, inputs []communitySummaryInput, summarizedAt time.Time) []*domain.Community {
	communities := make([]*domain.Community, 0, len(inputs))
	for _, input := range inputs {
		communities = append(communities, buildCommunitySummary(profileID, input, summarizedAt))
	}
	sort.Slice(communities, func(i, j int) bool {
		if communities[i].MemberCount == communities[j].MemberCount {
			return communities[i].CommunityID < communities[j].CommunityID
		}
		return communities[i].MemberCount > communities[j].MemberCount
	})
	return communities
}

func buildCommunitySummary(profileID string, input communitySummaryInput, summarizedAt time.Time) *domain.Community {
	topEntities := topCommunityTerms(input.FactTriples, 5, func(triple communityTriple) []string {
		return []string{triple.Subject, triple.Object}
	})
	topPredicates := topCommunityTerms(input.FactTriples, 5, func(triple communityTriple) []string {
		return []string{triple.Predicate}
	})
	summary := renderCommunitySummary(input.CommunityID, input.MemberCount, input.FactTriples, topEntities, topPredicates, "facts")

	if len(input.FactTriples) == 0 {
		topEntities = topCommunityTerms(input.ClaimTriples, 5, func(triple communityTriple) []string {
			return []string{triple.Subject, triple.Object}
		})
		topPredicates = topCommunityTerms(input.ClaimTriples, 5, func(triple communityTriple) []string {
			return []string{triple.Predicate}
		})
		summary = renderCommunitySummary(input.CommunityID, input.MemberCount, input.ClaimTriples, topEntities, topPredicates, "claims")
	}

	if len(input.FactTriples) == 0 && len(input.ClaimTriples) == 0 {
		summary = fmt.Sprintf("Community %s groups %d memory nodes with limited structured knowledge.", input.CommunityID, input.MemberCount)
	}

	return &domain.Community{
		CommunityID:      input.CommunityID,
		ProfileID:        profileID,
		Level:            0,
		Summary:          summary,
		SummaryVersion:   communitySummaryVersion,
		MemberCount:      input.MemberCount,
		TopEntities:      topEntities,
		TopPredicates:    topPredicates,
		LastSummarizedAt: summarizedAt,
	}
}

func renderCommunitySummary(communityID string, memberCount int, triples []communityTriple, topEntities, topPredicates []string, noun string) string {
	if len(triples) == 0 {
		return fmt.Sprintf("Community %s groups %d memory nodes with limited structured knowledge.", communityID, memberCount)
	}

	entityText := "mixed topics"
	if len(topEntities) > 0 {
		entityText = strings.Join(topEntities, ", ")
	}

	predicateText := "varied relations"
	if len(topPredicates) > 0 {
		predicateText = strings.Join(topPredicates, ", ")
	}

	highlights := topCommunityTriples(triples, 3)
	var summary string
	if noun == "claims" {
		summary = fmt.Sprintf(
			"Community %s groups %d memory nodes with no current facts yet. Claim themes center on %s. Common predicates: %s.",
			communityID,
			memberCount,
			entityText,
			predicateText,
		)
	} else {
		summary = fmt.Sprintf(
			"Community %s groups %d memory nodes around %s. It contains %d current facts. Common predicates: %s.",
			communityID,
			memberCount,
			entityText,
			len(triples),
			predicateText,
		)
	}
	if len(highlights) > 0 {
		summary += " Representative statements: " + strings.Join(highlights, "; ") + "."
	}
	return summary
}

func topCommunityTerms(triples []communityTriple, limit int, extract func(communityTriple) []string) []string {
	counts := make(map[string]int)
	for _, triple := range triples {
		for _, term := range extract(triple) {
			term = normalizeCommunityTerm(term)
			if term == "" {
				continue
			}
			counts[term]++
		}
	}
	return rankCommunityCounts(counts, limit)
}

func topCommunityTriples(triples []communityTriple, limit int) []string {
	counts := make(map[string]int)
	for _, triple := range triples {
		rendered := strings.TrimSpace(strings.Join([]string{
			normalizeCommunityTerm(triple.Subject),
			normalizeCommunityTerm(triple.Predicate),
			normalizeCommunityTerm(triple.Object),
		}, " "))
		if rendered == "" {
			continue
		}
		counts[rendered]++
	}
	return rankCommunityCounts(counts, limit)
}

func rankCommunityCounts(counts map[string]int, limit int) []string {
	type pair struct {
		value string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for value, count := range counts {
		pairs = append(pairs, pair{value: value, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count == pairs[j].count {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].count > pairs[j].count
	})
	if limit > 0 && len(pairs) > limit {
		pairs = pairs[:limit]
	}
	out := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		out = append(out, pair.value)
	}
	return out
}

func normalizeCommunityTerm(value string) string {
	return strings.TrimSpace(value)
}

func ensureCommunityInput(inputs map[string]*communitySummaryInput, communityID string) *communitySummaryInput {
	if input, ok := inputs[communityID]; ok {
		return input
	}
	input := &communitySummaryInput{CommunityID: communityID}
	inputs[communityID] = input
	return input
}

func rowToCommunity(row map[string]any) *domain.Community {
	communityID := communityString(row["community_id"])
	if communityID == "" {
		return nil
	}
	community := &domain.Community{
		CommunityID:      communityID,
		ProfileID:        communityString(row["team_id"]),
		Level:            communityInt(row["level"]),
		Summary:          communityString(row["summary"]),
		SummaryVersion:   communityString(row["summary_version"]),
		MemberCount:      communityInt(row["member_count"]),
		TopEntities:      communityStrings(row["top_entities"]),
		TopPredicates:    communityStrings(row["top_predicates"]),
		LastSummarizedAt: communityTime(row["last_summarized_at"]),
	}
	return community
}

func communityString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	}
	return ""
}

func communityStrings(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func communityInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case int32:
		return int(v)
	}
	return 0
}

func communityTime(value any) time.Time {
	t, _ := value.(time.Time)
	return t
}
