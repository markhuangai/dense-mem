package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"

	"sort"
	"strings"

	"time"
)

type service struct {
	store   CommunityRepository
	config  AppConfig
	metrics interface{}
	now     func() time.Time
}

func New(deps Dependencies) Service {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{store: deps.Store, config: deps.AppConfig, now: now, metrics: deps.Metrics}
}

func sourceStateHash(input CommunityInput) string {
	return hashJSON([]any{input.RelationshipID, input.Version, input.SemanticGroupKey, input.EvidenceIDs, input.ObjectEntityID, input.ObjectValueID})
}
func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hashJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return hashString(string(encoded))
}

func runResultFromRecord(record *CommunityRun, status string) *RunResult {
	if record == nil {
		return nil
	}
	return &RunResult{RunID: record.RunID, TeamID: record.TeamID, WindowKey: record.WindowKey, Status: status, NodeCount: record.NodeCount, EdgeCount: record.EdgeCount, CommunityCount: record.CommunityCount, SourceFingerprint: record.SourceFingerprint, Error: publicCommunityRunError(status), StartedAt: record.StartedAt, CompletedAt: derefTime(record.CompletedAt)}
}

func publicCommunityRunError(status string) string {
	switch status {
	case "failed":
		return "community run failed"
	case "too_large":
		return "community graph exceeded the configured bound"
	case "cancelled":
		return "community run was cancelled"
	default:
		return ""
	}
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func uniqueEntityEndpoints(inputs []CommunityInput) []string {
	set := map[string]struct{}{}
	for _, input := range inputs {
		for _, id := range []string{input.SubjectEntityID, input.ObjectEntityID} {
			if id != "" {
				set[id] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func topEntitiesAndMemberships(inputs []CommunityInput) ([]string, []CommunityMembershipInput) {
	type count struct {
		id, name string
		n        int
	}
	counts := map[string]*count{}
	for _, input := range inputs {
		for _, entity := range []struct{ id, name string }{{input.SubjectEntityID, input.SubjectName}, {input.ObjectEntityID, input.ObjectName}} {
			if entity.id == "" {
				continue
			}
			item := counts[entity.id]
			if item == nil {
				item = &count{id: entity.id, name: entity.name}
				counts[entity.id] = item
			}
			item.n++
		}
	}
	items := make([]count, 0, len(counts))
	for _, item := range counts {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].n != items[j].n {
			return items[i].n > items[j].n
		}
		if items[i].name != items[j].name {
			return items[i].name < items[j].name
		}
		return items[i].id < items[j].id
	})
	memberships := make([]CommunityMembershipInput, 0, len(items))
	names := make([]string, 0, 5)
	for rank, item := range items {
		memberships = append(memberships, CommunityMembershipInput{EntityID: item.id, Rank: rank, MembershipScore: 1, SourceCount: item.n})
		if len(names) < 5 {
			names = append(names, firstNonEmpty(item.name, item.id))
		}
	}
	return names, memberships
}

func (s *service) Status(ctx context.Context, teamID string) (*StatusResult, error) {
	if s == nil || s.store == nil || s.config == nil {
		return nil, errors.New("community: repository and ontology configuration are required")
	}
	policy, err := s.config.OntologyMaintenanceRuntimeConfig(ctx)
	if err != nil {
		return nil, err
	}
	result := &StatusResult{EffectiveConfig: policy, LegacyProcessingRetired: true}
	run, err := s.store.LatestCommunityRun(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if run != nil {
		result.LatestRun = runResultFromRecord(run, run.Status)
	}
	if !policy.Enabled {
		return result, nil
	}
	store, ok := s.store.(communitycontract.TopicProjectionRepository)
	if !ok {
		return nil, errors.New("community: topic projection repository is required")
	}
	coverage, err := store.TopicProjectionCoverage(ctx, teamID)
	if err != nil {
		return nil, err
	}
	result.ProjectionCoverage = &coverage
	result.CurrentCommunityCount = coverage.CurrentTopics
	return result, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
