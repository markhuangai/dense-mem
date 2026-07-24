package neo4j

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	LegacyCorpusSourceKind = "neo4j"
	defaultLegacyPageSize  = 100
	maxLegacyPageSize      = 500
)

var ErrLegacyMigrationDisabled = errors.New("neo4j legacy migration adapter is disabled")

type LegacyCorpusAdapterConfig struct {
	Enabled     bool
	MaxPageSize int
}

type LegacyCorpusPageRequest struct {
	AfterSourceID string
	Limit         int
}

type LegacyCorpusPage struct {
	Items      []LegacyCorpusItem
	NextCursor string
}

type LegacyOwnerResolution struct {
	OwnerProfileID   string
	OwnerProfileName string
	CandidateCount   int
}

type LegacyCorpusItem struct {
	SourceKind       string
	SourceID         string
	SourceHash       string
	TeamID           string
	OwnerProfileID   string
	OwnerProfileName string
	OwnerResolution  string
	OwnerCandidates  int
	Content          string
	Source           string
	SourceType       string
	Authority        string
	Status           string
	Labels           []string
	Metadata         map[string]any
	Classification   map[string]any
	CreatedAt        *time.Time
	UpdatedAt        *time.Time
	Claims           []LegacyClaimHint
	Facts            []LegacyFactHint
}

type LegacyClaimHint struct {
	ClaimID   string         `json:"claim_id"`
	Subject   string         `json:"subject,omitempty"`
	Predicate string         `json:"predicate,omitempty"`
	Object    string         `json:"object,omitempty"`
	Status    string         `json:"status,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type LegacyFactHint struct {
	FactID    string         `json:"fact_id"`
	Subject   string         `json:"subject,omitempty"`
	Predicate string         `json:"predicate,omitempty"`
	Object    string         `json:"object,omitempty"`
	Status    string         `json:"status,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type LegacyCorpusReader interface {
	ReadCorpusPage(ctx context.Context, req LegacyCorpusPageRequest) (LegacyCorpusPage, error)
	ResolveUniqueTeamOwner(ctx context.Context, teamID string) (LegacyOwnerResolution, error)
	Close(ctx context.Context) error
}

type LegacyCorpusMigrationAdapter struct {
	client      Neo4jClientInterface
	maxPageSize int
}

func NewLegacyCorpusMigrationAdapter(client Neo4jClientInterface, cfg LegacyCorpusAdapterConfig) (*LegacyCorpusMigrationAdapter, error) {
	if !cfg.Enabled {
		return nil, ErrLegacyMigrationDisabled
	}
	if client == nil {
		return nil, errors.New("neo4j legacy migration adapter: client is required")
	}
	maxPage := cfg.MaxPageSize
	if maxPage <= 0 || maxPage > maxLegacyPageSize {
		maxPage = maxLegacyPageSize
	}
	return &LegacyCorpusMigrationAdapter{client: client, maxPageSize: maxPage}, nil
}

func (a *LegacyCorpusMigrationAdapter) ReadCorpusPage(ctx context.Context, req LegacyCorpusPageRequest) (LegacyCorpusPage, error) {
	if a == nil || a.client == nil {
		return LegacyCorpusPage{}, errors.New("neo4j legacy migration adapter: client is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultLegacyPageSize
	}
	if limit > a.maxPageSize {
		limit = a.maxPageSize
	}
	params := legacyCorpusPageParams(req.AfterSourceID, limit)
	raw, err := a.client.ExecuteRead(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, legacyCorpusPageCypher(), params)
		if err != nil {
			return nil, err
		}
		return res.Collect(ctx)
	})
	if err != nil {
		return LegacyCorpusPage{}, fmt.Errorf("neo4j legacy migration adapter: read corpus page: %w", err)
	}
	records, ok := raw.([]*driver.Record)
	if !ok {
		return LegacyCorpusPage{}, fmt.Errorf("neo4j legacy migration adapter: unexpected result type %T", raw)
	}
	items := make([]LegacyCorpusItem, 0, len(records))
	for _, record := range records {
		item, err := legacyCorpusItemFromRecord(record)
		if err != nil {
			return LegacyCorpusPage{}, err
		}
		items = append(items, item)
	}
	page := LegacyCorpusPage{Items: items}
	if len(items) > 0 {
		page.NextCursor = items[len(items)-1].SourceID
	}
	return page, nil
}

func (a *LegacyCorpusMigrationAdapter) ResolveUniqueTeamOwner(
	ctx context.Context,
	teamID string,
) (LegacyOwnerResolution, error) {
	if a == nil || a.client == nil {
		return LegacyOwnerResolution{}, errors.New("neo4j legacy migration adapter: client is required")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return LegacyOwnerResolution{}, errors.New("neo4j legacy migration adapter: team_id is required")
	}
	raw, err := a.client.ExecuteRead(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, legacyUniqueTeamOwnerCypher(), map[string]any{"team_id": teamID})
		if err != nil {
			return nil, err
		}
		return res.Collect(ctx)
	})
	if err != nil {
		return LegacyOwnerResolution{}, fmt.Errorf("neo4j legacy migration adapter: resolve unique team owner: %w", err)
	}
	records, ok := raw.([]*driver.Record)
	if !ok {
		return LegacyOwnerResolution{}, fmt.Errorf(
			"neo4j legacy migration adapter: unexpected owner result type %T",
			raw,
		)
	}
	resolution := LegacyOwnerResolution{CandidateCount: len(records)}
	if len(records) != 1 {
		return resolution, nil
	}
	resolution.OwnerProfileID = legacyString(records[0], "owner_profile_id")
	resolution.OwnerProfileName = legacyString(records[0], "owner_profile_name")
	return resolution, nil
}

func (a *LegacyCorpusMigrationAdapter) Close(ctx context.Context) error {
	if a == nil || a.client == nil {
		return nil
	}
	return a.client.Close(ctx)
}

func legacyUniqueTeamOwnerCypher() string {
	return `
MATCH (sf:SourceFragment {team_id: $team_id})
WHERE coalesce(sf.status, 'active') <> 'retracted'
  AND coalesce(sf.status, 'active') <> 'superseded'
WITH coalesce(sf.owner_profile_id, sf.created_by_profile_id, '') AS owner_profile_id,
     max(coalesce(sf.owner_profile_name, sf.created_by_profile_name, '')) AS owner_profile_name
WHERE owner_profile_id <> ''
RETURN owner_profile_id, owner_profile_name
ORDER BY owner_profile_id ASC
LIMIT 2`
}

func legacyCorpusPageParams(afterSourceID string, limit int) map[string]any {
	return map[string]any{
		"after_source_id": strings.TrimSpace(afterSourceID),
		"limit":           int64(limit),
	}
}

func legacyCorpusPageCypher() string {
	return `
MATCH (sf:SourceFragment)
WHERE coalesce(sf.status, 'active') <> 'retracted'
  AND coalesce(sf.status, 'active') <> 'superseded'
  AND ($after_source_id = '' OR sf.fragment_id > $after_source_id)
WITH sf
ORDER BY sf.fragment_id ASC
LIMIT $limit
OPTIONAL MATCH (claim:Claim {team_id: sf.team_id})-[support:SUPPORTED_BY {team_id: sf.team_id}]->(sf)
WITH sf, collect(DISTINCT CASE
    WHEN claim IS NULL OR coalesce(claim.status, '') IN ['rejected', 'superseded'] THEN null
    ELSE {
    claim_id: claim.claim_id,
    subject: claim.subject,
    predicate: claim.predicate,
    object: claim.object,
    status: claim.status,
    support: properties(support)
} END) AS raw_claim_hints
OPTIONAL MATCH (sourceClaim:Claim {team_id: sf.team_id})-[:SUPPORTED_BY {team_id: sf.team_id}]->(sf)
OPTIONAL MATCH (sourceClaim)-[:PROMOTES_TO {team_id: sf.team_id}]->(fact:Fact {team_id: sf.team_id, status: 'active'})
WITH sf, raw_claim_hints, collect(DISTINCT CASE
    WHEN fact IS NULL OR sourceClaim IS NULL OR coalesce(sourceClaim.status, '') IN ['rejected', 'superseded'] THEN null
    ELSE {
    fact_id: fact.fact_id,
    subject: fact.subject,
    predicate: fact.predicate,
    object: fact.object,
    status: fact.status,
    promoted_from_claim_id: fact.promoted_from_claim_id
} END) AS raw_fact_hints
RETURN
    sf.fragment_id AS source_id,
    coalesce(sf.content_hash, '') AS source_hash,
    coalesce(sf.team_id, '') AS team_id,
    coalesce(sf.owner_profile_id, sf.created_by_profile_id, '') AS owner_profile_id,
    coalesce(sf.owner_profile_name, sf.created_by_profile_name, '') AS owner_profile_name,
    coalesce(sf.content, '') AS content,
    coalesce(sf.source, '') AS source,
    coalesce(sf.source_type, '') AS source_type,
    coalesce(sf.authority, '') AS authority,
    coalesce(sf.status, 'active') AS status,
    coalesce(sf.labels, []) AS labels,
    coalesce(sf.metadata_json, '{}') AS metadata_json,
    coalesce(sf.classification_json, '{}') AS classification_json,
    sf.created_at AS created_at,
    sf.updated_at AS updated_at,
    [hint IN raw_claim_hints WHERE hint IS NOT NULL] AS claim_hints,
    [hint IN raw_fact_hints WHERE hint IS NOT NULL] AS fact_hints
ORDER BY source_id ASC`
}

func legacyCorpusItemFromRecord(record *driver.Record) (LegacyCorpusItem, error) {
	if record == nil {
		return LegacyCorpusItem{}, errors.New("neo4j legacy migration adapter: nil record")
	}
	item := LegacyCorpusItem{
		SourceKind:       LegacyCorpusSourceKind,
		SourceID:         legacyString(record, "source_id"),
		SourceHash:       legacyString(record, "source_hash"),
		TeamID:           legacyString(record, "team_id"),
		OwnerProfileID:   legacyString(record, "owner_profile_id"),
		OwnerProfileName: legacyString(record, "owner_profile_name"),
		Content:          legacyRawString(record, "content"),
		Source:           legacyString(record, "source"),
		SourceType:       legacyString(record, "source_type"),
		Authority:        legacyString(record, "authority"),
		Status:           legacyString(record, "status"),
		Labels:           legacyStringSlice(record, "labels"),
		CreatedAt:        legacyTime(record, "created_at"),
		UpdatedAt:        legacyTime(record, "updated_at"),
	}
	if item.SourceID == "" {
		return LegacyCorpusItem{}, errors.New("neo4j legacy migration adapter: source_id is required")
	}
	if item.SourceHash == "" {
		item.SourceHash = LegacyContentHash(item.Content)
	}
	var err error
	item.Metadata, err = legacyJSONObject(record, "metadata_json")
	if err != nil {
		return LegacyCorpusItem{}, err
	}
	item.Classification, err = legacyJSONObject(record, "classification_json")
	if err != nil {
		return LegacyCorpusItem{}, err
	}
	item.Claims = legacyClaimHints(record, "claim_hints")
	item.Facts = legacyFactHints(record, "fact_hints")
	return item, nil
}

func legacyString(record *driver.Record, key string) string {
	raw, ok := record.Get(key)
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func legacyRawString(record *driver.Record, key string) string {
	raw, ok := record.Get(key)
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

func legacyStringSlice(record *driver.Record, key string) []string {
	raw, ok := record.Get(key)
	if !ok || raw == nil {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func legacyJSONObject(record *driver.Record, key string) (map[string]any, error) {
	raw, ok := record.Get(key)
	if !ok || raw == nil {
		return map[string]any{}, nil
	}
	switch value := raw.(type) {
	case map[string]any:
		return value, nil
	case string:
		if strings.TrimSpace(value) == "" {
			return map[string]any{}, nil
		}
		out := map[string]any{}
		if err := json.Unmarshal([]byte(value), &out); err != nil {
			return nil, fmt.Errorf("neo4j legacy migration adapter: decode %s: %w", key, err)
		}
		return out, nil
	default:
		return map[string]any{}, nil
	}
}

func legacyTime(record *driver.Record, key string) *time.Time {
	raw, ok := record.Get(key)
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case time.Time:
		normalized := value.UTC()
		return &normalized
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil
		}
		normalized := parsed.UTC()
		return &normalized
	default:
		return nil
	}
}

func legacyClaimHints(record *driver.Record, key string) []LegacyClaimHint {
	raw := legacyHintMaps(record, key)
	out := make([]LegacyClaimHint, 0, len(raw))
	for _, item := range raw {
		claimID := strings.TrimSpace(fmt.Sprint(item["claim_id"]))
		if claimID == "" || claimID == "<nil>" {
			continue
		}
		out = append(out, LegacyClaimHint{
			ClaimID:   claimID,
			Subject:   strings.TrimSpace(fmt.Sprint(item["subject"])),
			Predicate: strings.TrimSpace(fmt.Sprint(item["predicate"])),
			Object:    strings.TrimSpace(fmt.Sprint(item["object"])),
			Status:    strings.TrimSpace(fmt.Sprint(item["status"])),
			Metadata:  legacyMapWithout(item, "claim_id", "subject", "predicate", "object", "status"),
		})
	}
	return out
}

func legacyFactHints(record *driver.Record, key string) []LegacyFactHint {
	raw := legacyHintMaps(record, key)
	out := make([]LegacyFactHint, 0, len(raw))
	for _, item := range raw {
		factID := strings.TrimSpace(fmt.Sprint(item["fact_id"]))
		if factID == "" || factID == "<nil>" {
			continue
		}
		out = append(out, LegacyFactHint{
			FactID:    factID,
			Subject:   strings.TrimSpace(fmt.Sprint(item["subject"])),
			Predicate: strings.TrimSpace(fmt.Sprint(item["predicate"])),
			Object:    strings.TrimSpace(fmt.Sprint(item["object"])),
			Status:    strings.TrimSpace(fmt.Sprint(item["status"])),
			Metadata:  legacyMapWithout(item, "fact_id", "subject", "predicate", "object", "status"),
		})
	}
	return out
}

func legacyHintMaps(record *driver.Record, key string) []map[string]any {
	raw, ok := record.Get(key)
	if !ok || raw == nil {
		return nil
	}
	switch values := raw.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(values))
		for _, item := range values {
			if len(item) > 0 {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(values))
		for _, value := range values {
			item, ok := value.(map[string]any)
			if ok && len(item) > 0 {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func legacyMapWithout(input map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	for _, key := range keys {
		delete(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LegacyContentHash returns the deterministic fallback SourceFragment hash used when Neo4j lacks one.
func LegacyContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}
