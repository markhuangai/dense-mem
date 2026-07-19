package communityservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	defaultV2CommunityMaxNodes = 5000
	defaultV2CommunityMaxEdges = 20000
	defaultV2CommunityLease    = 5 * time.Minute
)

type V2SnapshotService interface {
	Detect(ctx context.Context, req V2SnapshotRequest) (*V2SnapshotResult, error)
}

type V2SnapshotOptions struct {
	MaxNodes int
	MaxEdges int
	Lease    time.Duration
	Now      func() time.Time
}

type V2SnapshotRequest struct {
	TeamID            string
	WindowKey         string
	ConfigurationHash string
}

type V2SnapshotResult struct {
	TeamID         string
	RunID          string
	WindowKey      string
	Status         string
	Claimed        bool
	NodeCount      int
	EdgeCount      int
	CommunityCount int
}

type v2SnapshotService struct {
	repo    repository.V2CommunityRepository
	options V2SnapshotOptions
}

func NewV2SnapshotService(repo repository.V2CommunityRepository, opts V2SnapshotOptions) V2SnapshotService {
	opts = normalizeV2SnapshotOptions(opts)
	return &v2SnapshotService{repo: repo, options: opts}
}

func (s *v2SnapshotService) Detect(ctx context.Context, req V2SnapshotRequest) (*V2SnapshotResult, error) {
	if s.repo == nil {
		return nil, ErrCommunityUnavailable
	}
	req = normalizeV2SnapshotRequest(req, s.options.Now())
	run, err := s.repo.ClaimV2CommunityRun(ctx, repository.V2CommunityRunClaimInput{
		TeamID:            req.TeamID,
		WindowKey:         req.WindowKey,
		LeaseUntil:        s.options.Now().Add(s.options.Lease),
		AlgorithmKind:     repository.V2CommunityAlgorithmKind,
		AlgorithmVersion:  repository.V2CommunityAlgorithmVersion,
		ProfileVersion:    repository.V2CommunityProfileVersion,
		ConfigurationHash: strings.TrimSpace(req.ConfigurationHash),
		MaxNodes:          s.options.MaxNodes,
		MaxEdges:          s.options.MaxEdges,
	})
	if err != nil {
		return nil, err
	}
	result := v2SnapshotResultFromRun(run)
	if !run.Claimed {
		return result, nil
	}

	if _, err := s.repo.RefreshV2CommunityStaleness(ctx, repository.V2CommunityStalenessInput{
		TeamID: req.TeamID,
	}); err != nil {
		return result, s.completeFailed(ctx, run.RunID, result, err)
	}

	inputs, err := s.repo.ListV2CommunityInputs(ctx, repository.V2CommunityInputListInput{
		TeamID: req.TeamID,
		Limit:  s.options.MaxEdges + 1,
	})
	if err != nil {
		return result, s.completeFailed(ctx, run.RunID, result, err)
	}
	nodeCount := countV2CommunityNodes(inputs)
	if len(inputs) > s.options.MaxEdges || nodeCount > s.options.MaxNodes {
		result.NodeCount = nodeCount
		result.EdgeCount = len(inputs)
		result.Status = "too_large"
		completeErr := s.repo.CompleteV2CommunityRun(ctx, repository.V2CommunityRunCompleteInput{
			TeamID:    req.TeamID,
			RunID:     run.RunID,
			Status:    "too_large",
			NodeCount: nodeCount,
			EdgeCount: len(inputs),
			Error:     ErrCommunityGraphTooLarge.Error(),
		})
		if completeErr != nil {
			return result, completeErr
		}
		return result, fmt.Errorf("v2 community snapshot: nodes=%d max_nodes=%d edges=%d max_edges=%d: %w",
			nodeCount, s.options.MaxNodes, len(inputs), s.options.MaxEdges, ErrCommunityGraphTooLarge)
	}
	if len(inputs) == 0 {
		result.Status = "skipped"
		if err := s.repo.CompleteV2CommunityRun(ctx, repository.V2CommunityRunCompleteInput{
			TeamID: req.TeamID,
			RunID:  run.RunID,
			Status: "skipped",
		}); err != nil {
			return result, err
		}
		return result, nil
	}

	snapshot := buildV2CommunitySnapshot(req.TeamID, run.RunID, inputs, s.options.Now())
	result.NodeCount = snapshot.NodeCount
	result.EdgeCount = snapshot.EdgeCount
	result.CommunityCount = len(snapshot.Communities)
	if len(snapshot.Communities) == 0 {
		result.Status = "skipped"
		if err := s.repo.CompleteV2CommunityRun(ctx, repository.V2CommunityRunCompleteInput{
			TeamID: req.TeamID,
			RunID:  run.RunID,
			Status: "skipped",
		}); err != nil {
			return result, err
		}
		return result, nil
	}
	if err := s.repo.PublishV2CommunitySnapshot(ctx, repository.V2CommunitySnapshotPublishInput{
		TeamID:            req.TeamID,
		RunID:             run.RunID,
		AlgorithmKind:     repository.V2CommunityAlgorithmKind,
		AlgorithmVersion:  repository.V2CommunityAlgorithmVersion,
		ProfileVersion:    repository.V2CommunityProfileVersion,
		ConfigurationHash: strings.TrimSpace(req.ConfigurationHash),
		SourceFingerprint: snapshot.SourceFingerprint,
		SourceSnapshot:    snapshot.SourceSnapshot,
		NodeCount:         snapshot.NodeCount,
		EdgeCount:         snapshot.EdgeCount,
		Communities:       snapshot.Communities,
	}); err != nil {
		return result, s.completeFailed(ctx, run.RunID, result, err)
	}
	result.Status = "completed"
	return result, nil
}

func (s *v2SnapshotService) completeFailed(ctx context.Context, runID string, result *V2SnapshotResult, cause error) error {
	result.Status = "failed"
	err := s.repo.CompleteV2CommunityRun(ctx, repository.V2CommunityRunCompleteInput{
		TeamID:         result.TeamID,
		RunID:          runID,
		Status:         "failed",
		NodeCount:      result.NodeCount,
		EdgeCount:      result.EdgeCount,
		CommunityCount: result.CommunityCount,
		Error:          cause.Error(),
	})
	if err != nil {
		return fmt.Errorf("%w; additionally failed to complete community run: %v", cause, err)
	}
	return cause
}

type v2CommunitySnapshot struct {
	SourceFingerprint string
	SourceSnapshot    []map[string]any
	NodeCount         int
	EdgeCount         int
	Communities       []repository.V2CommunityPublishRecord
}

func buildV2CommunitySnapshot(teamID, runID string, inputs []repository.V2CommunityInput, now time.Time) v2CommunitySnapshot {
	inputs = sortedV2CommunityInputs(inputs)
	fingerprint, sourceSnapshot := v2CommunitySourceFingerprint(inputs)
	components := detectV2CommunityComponents(inputs)
	communities := make([]repository.V2CommunityPublishRecord, 0, len(components))
	for ordinal, component := range components {
		community := buildV2CommunityRecord(teamID, runID, fingerprint, ordinal, component, now)
		if community.CommunityID != "" {
			communities = append(communities, community)
		}
	}
	sort.Slice(communities, func(i, j int) bool {
		if communities[i].MemberCount == communities[j].MemberCount {
			return communities[i].CommunityID < communities[j].CommunityID
		}
		return communities[i].MemberCount > communities[j].MemberCount
	})
	for i := range communities {
		communities[i].Ordinal = i
	}
	return v2CommunitySnapshot{
		SourceFingerprint: fingerprint,
		SourceSnapshot:    sourceSnapshot,
		NodeCount:         countV2CommunityNodes(inputs),
		EdgeCount:         len(inputs),
		Communities:       communities,
	}
}

type v2CommunityComponent struct {
	EntityIDs []string
	Inputs    []repository.V2CommunityInput
}

func detectV2CommunityComponents(inputs []repository.V2CommunityInput) []v2CommunityComponent {
	neighbors := map[string]map[string]struct{}{}
	for _, input := range inputs {
		if input.SubjectEntityID == "" || input.ObjectEntityID == "" {
			continue
		}
		ensureV2CommunityNeighbor(neighbors, input.SubjectEntityID)[input.ObjectEntityID] = struct{}{}
		ensureV2CommunityNeighbor(neighbors, input.ObjectEntityID)[input.SubjectEntityID] = struct{}{}
	}
	nodeIDs := make([]string, 0, len(neighbors))
	for nodeID := range neighbors {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)

	visited := map[string]struct{}{}
	components := make([]v2CommunityComponent, 0)
	for _, root := range nodeIDs {
		if _, ok := visited[root]; ok {
			continue
		}
		stack := []string{root}
		componentNodes := make([]string, 0)
		visited[root] = struct{}{}
		for len(stack) > 0 {
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			componentNodes = append(componentNodes, node)
			next := make([]string, 0, len(neighbors[node]))
			for neighbor := range neighbors[node] {
				next = append(next, neighbor)
			}
			sort.Sort(sort.Reverse(sort.StringSlice(next)))
			for _, neighbor := range next {
				if _, ok := visited[neighbor]; ok {
					continue
				}
				visited[neighbor] = struct{}{}
				stack = append(stack, neighbor)
			}
		}
		sort.Strings(componentNodes)
		nodeSet := map[string]struct{}{}
		for _, nodeID := range componentNodes {
			nodeSet[nodeID] = struct{}{}
		}
		componentInputs := make([]repository.V2CommunityInput, 0)
		for _, input := range inputs {
			_, hasSubject := nodeSet[input.SubjectEntityID]
			_, hasObject := nodeSet[input.ObjectEntityID]
			if hasSubject && hasObject {
				componentInputs = append(componentInputs, input)
			}
		}
		components = append(components, v2CommunityComponent{
			EntityIDs: componentNodes,
			Inputs:    componentInputs,
		})
	}
	sort.Slice(components, func(i, j int) bool {
		if len(components[i].EntityIDs) == len(components[j].EntityIDs) {
			return strings.Join(components[i].EntityIDs, ",") < strings.Join(components[j].EntityIDs, ",")
		}
		return len(components[i].EntityIDs) > len(components[j].EntityIDs)
	})
	return components
}

func buildV2CommunityRecord(teamID, runID, fingerprint string, ordinal int, component v2CommunityComponent, now time.Time) repository.V2CommunityPublishRecord {
	if len(component.EntityIDs) == 0 || len(component.Inputs) == 0 {
		return repository.V2CommunityPublishRecord{}
	}
	communityID := deterministicV2CommunityID(teamID, fingerprint, component.EntityIDs)
	summaryInput := communitySummaryInput{
		CommunityID: communityID,
		MemberCount: len(component.EntityIDs),
	}
	sourceCounts := map[string]int{}
	sources := make([]repository.V2CommunitySourceInput, 0, len(component.Inputs))
	for i, input := range component.Inputs {
		triple := communityTriple{
			Subject:   input.SubjectName,
			Predicate: input.PredicateKey,
			Object:    input.ObjectName,
		}
		if input.Tier == "fact" {
			summaryInput.FactTriples = append(summaryInput.FactTriples, triple)
		} else {
			summaryInput.ClaimTriples = append(summaryInput.ClaimTriples, triple)
		}
		sourceCounts[input.SubjectEntityID]++
		sourceCounts[input.ObjectEntityID]++
		sources = append(sources, repository.V2CommunitySourceInput{
			RelationshipID:      input.RelationshipID,
			OwnerProfileID:      input.OwnerProfileID,
			RelationshipVersion: input.Version,
			SourceRank:          i,
		})
	}
	summary := buildCommunitySummary(teamID, summaryInput, now)
	memberships := make([]repository.V2CommunityMembershipInput, 0, len(component.EntityIDs))
	for i, entityID := range component.EntityIDs {
		memberships = append(memberships, repository.V2CommunityMembershipInput{
			EntityID:        entityID,
			Rank:            i,
			MembershipScore: 1,
			SourceCount:     sourceCounts[entityID],
		})
	}
	return repository.V2CommunityPublishRecord{
		CommunityID:       communityID,
		Ordinal:           ordinal,
		Summary:           summary.Summary,
		SummaryVersion:    summary.SummaryVersion,
		MemberCount:       len(component.EntityIDs),
		SourceCount:       len(component.Inputs),
		TopEntities:       summary.TopEntities,
		TopPredicates:     summary.TopPredicates,
		SourceFingerprint: fingerprint,
		Memberships:       memberships,
		Sources:           sources,
	}
}

func v2CommunitySourceFingerprint(inputs []repository.V2CommunityInput) (string, []map[string]any) {
	parts := make([]string, 0, len(inputs))
	snapshot := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		parts = append(parts, fmt.Sprintf("%s:%d:%s:%s:%d:%s",
			input.RelationshipID,
			input.Version,
			input.SubjectEntityID,
			input.PredicateKey,
			input.PredicateVersion,
			input.ObjectEntityID,
		))
		snapshot = append(snapshot, map[string]any{
			"relationship_id":   input.RelationshipID,
			"owner_profile_id":  input.OwnerProfileID,
			"version":           input.Version,
			"subject_entity_id": input.SubjectEntityID,
			"predicate_key":     input.PredicateKey,
			"predicate_version": input.PredicateVersion,
			"object_entity_id":  input.ObjectEntityID,
			"tier":              input.Tier,
		})
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), snapshot
}

func deterministicV2CommunityID(teamID, fingerprint string, entityIDs []string) string {
	namespace := uuid.NewSHA1(uuid.NameSpaceURL, []byte("dense-mem:v2-community:"+teamID))
	return uuid.NewSHA1(namespace, []byte(fingerprint+":"+strings.Join(entityIDs, ","))).String()
}

func countV2CommunityNodes(inputs []repository.V2CommunityInput) int {
	nodes := map[string]struct{}{}
	for _, input := range inputs {
		if input.SubjectEntityID != "" {
			nodes[input.SubjectEntityID] = struct{}{}
		}
		if input.ObjectEntityID != "" {
			nodes[input.ObjectEntityID] = struct{}{}
		}
	}
	return len(nodes)
}

func sortedV2CommunityInputs(inputs []repository.V2CommunityInput) []repository.V2CommunityInput {
	out := append([]repository.V2CommunityInput(nil), inputs...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelationshipID < out[j].RelationshipID
	})
	return out
}

func ensureV2CommunityNeighbor(neighbors map[string]map[string]struct{}, entityID string) map[string]struct{} {
	edgeSet := neighbors[entityID]
	if edgeSet == nil {
		edgeSet = map[string]struct{}{}
		neighbors[entityID] = edgeSet
	}
	return edgeSet
}

func normalizeV2SnapshotOptions(opts V2SnapshotOptions) V2SnapshotOptions {
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = defaultV2CommunityMaxNodes
	}
	if opts.MaxEdges <= 0 {
		opts.MaxEdges = defaultV2CommunityMaxEdges
	}
	if opts.Lease <= 0 {
		opts.Lease = defaultV2CommunityLease
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return opts
}

func normalizeV2SnapshotRequest(req V2SnapshotRequest, now time.Time) V2SnapshotRequest {
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.WindowKey = strings.TrimSpace(req.WindowKey)
	req.ConfigurationHash = strings.TrimSpace(req.ConfigurationHash)
	if req.WindowKey == "" {
		req.WindowKey = now.UTC().Format("2006-01-02")
	}
	return req
}

func v2SnapshotResultFromRun(run *repository.V2CommunityRun) *V2SnapshotResult {
	return &V2SnapshotResult{
		TeamID:         run.TeamID,
		RunID:          run.RunID,
		WindowKey:      run.WindowKey,
		Status:         run.Status,
		Claimed:        run.Claimed,
		NodeCount:      run.NodeCount,
		EdgeCount:      run.EdgeCount,
		CommunityCount: run.CommunityCount,
	}
}
