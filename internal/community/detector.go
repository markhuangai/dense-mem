// Package community contains the deterministic, provider-independent community
// graph derivation used by the scheduled snapshot service.
package community

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/simple"
)

const (
	AlgorithmKind    = "louvain"
	AlgorithmVersion = "v2"
	Resolution       = 1.0
	MaxNodes         = 5000
	MaxEdges         = 20000
	DefaultSeed      = uint64(158)
)

// Input is one eligible semantic relationship. Relationships with the same
// SemanticGroupKey are one graph node; their raw relationship identity is
// retained for provenance and recall hydration.
type Input struct {
	RelationshipID   string
	SemanticGroupKey string
	SubjectEntityID  string
	ObjectEntityID   string
	ObjectValueID    string
	EvidenceIDs      []string
	PredicateKey     string
	SubjectName      string
	ObjectName       string
}

type Node struct {
	SemanticGroupKey string
	RelationshipIDs  []string
	EntityIDs        []string
	ValueIDs         []string
	EvidenceIDs      []string
}

type Edge struct {
	From   string
	To     string
	Weight float64
}

type Cluster struct {
	CommunityID uuid.UUID
	GroupKeys   []string
}

type Result struct {
	Nodes    []Node
	Edges    []Edge
	Clusters []Cluster
	TooLarge bool
}

// Detect builds the weighted semantic-group graph and applies Gonum Louvain.
// All map-derived inputs are sorted before graph construction. The supplied
// seed is part of the caller's configuration hash and must remain fixed for a
// reproducible snapshot.
func Detect(inputs []Input, seed uint64) Result {
	if seed == 0 {
		seed = DefaultSeed
	}
	byGroup := make(map[string]*Node)
	for _, input := range inputs {
		group := strings.TrimSpace(input.SemanticGroupKey)
		if group == "" {
			continue
		}
		node := byGroup[group]
		if node == nil {
			node = &Node{SemanticGroupKey: group}
			byGroup[group] = node
		}
		node.RelationshipIDs = appendUnique(node.RelationshipIDs, strings.TrimSpace(input.RelationshipID))
		node.EvidenceIDs = appendAllUnique(node.EvidenceIDs, input.EvidenceIDs)
		for _, entityID := range []string{strings.TrimSpace(input.SubjectEntityID), strings.TrimSpace(input.ObjectEntityID)} {
			if entityID != "" {
				node.EntityIDs = appendUnique(node.EntityIDs, entityID)
			}
		}
		node.ValueIDs = appendUnique(node.ValueIDs, strings.TrimSpace(input.ObjectValueID))
	}
	groups := make([]string, 0, len(byGroup))
	for group := range byGroup {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	result := Result{Nodes: make([]Node, 0, len(groups))}
	for _, group := range groups {
		node := *byGroup[group]
		sort.Strings(node.RelationshipIDs)
		sort.Strings(node.EntityIDs)
		sort.Strings(node.ValueIDs)
		sort.Strings(node.EvidenceIDs)
		result.Nodes = append(result.Nodes, node)
	}
	if len(result.Nodes) > MaxNodes {
		result.TooLarge = true
		return result
	}

	groupIndex := make(map[string]int, len(groups))
	for i, group := range groups {
		groupIndex[group] = i
	}
	weights := make(map[[2]int]float64)
	tooLarge := false
	addSharedWeights := func(values func(Node) []string, amount float64) {
		if tooLarge {
			return
		}
		owners := make(map[string][]int)
		for i, node := range result.Nodes {
			for _, value := range values(node) {
				owners[value] = append(owners[value], i)
			}
		}
		keys := make([]string, 0, len(owners))
		for key := range owners {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			members := owners[key]
			if len(members) < 2 {
				continue
			}
			contribution := amount / float64(maxInt(1, len(members)-1))
			for i := 0; i < len(members); i++ {
				for j := i + 1; j < len(members); j++ {
					left, right := members[i], members[j]
					if left > right {
						left, right = right, left
					}
					pair := [2]int{left, right}
					if _, exists := weights[pair]; !exists && len(weights) >= MaxEdges {
						tooLarge = true
						return
					}
					weights[pair] += contribution
				}
			}
		}
	}
	// Shared Evidence and shared Entity contributions intentionally use the
	// same group-degree denominator; this keeps heavily reused evidence from
	// dominating the graph.
	addSharedWeights(func(node Node) []string { return node.EvidenceIDs }, 2)
	addSharedWeights(func(node Node) []string { return node.EntityIDs }, 1)
	addSharedWeights(func(node Node) []string { return node.ValueIDs }, 1)
	if tooLarge || len(weights) > MaxEdges {
		result.TooLarge = true
		return result
	}
	keys := make([][2]int, 0, len(weights))
	for key := range weights {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	result.Edges = make([]Edge, 0, len(keys))
	graphValue := simple.NewWeightedUndirectedGraph(0, 0)
	for i, node := range result.Nodes {
		graphValue.AddNode(simple.Node(i))
		_ = node
	}
	for _, key := range keys {
		weight := weights[key]
		if weight <= 0 {
			continue
		}
		result.Edges = append(result.Edges, Edge{
			From: groups[key[0]], To: groups[key[1]], Weight: weight,
		})
		graphValue.SetWeightedEdge(graphValue.NewWeightedEdge(simple.Node(key[0]), simple.Node(key[1]), weight))
	}
	if len(result.Edges) == 0 {
		return result
	}
	reduced := community.Modularize(graphValue, Resolution, rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for _, members := range reduced.Communities() {
		if len(members) < 2 {
			continue
		}
		clusterGroups := make([]string, 0, len(members))
		for _, member := range members {
			if member == nil {
				continue
			}
			index := int(member.ID())
			if index >= 0 && index < len(groups) {
				clusterGroups = append(clusterGroups, groups[index])
			}
		}
		sort.Strings(clusterGroups)
		if len(clusterGroups) < 2 {
			continue
		}
		result.Clusters = append(result.Clusters, Cluster{
			CommunityID: stableCommunityID(clusterGroups),
			GroupKeys:   clusterGroups,
		})
	}
	sort.Slice(result.Clusters, func(i, j int) bool {
		return result.Clusters[i].GroupKeys[0] < result.Clusters[j].GroupKeys[0]
	})
	return result
}

func stableCommunityID(groups []string) uuid.UUID {
	return uuid.NewSHA1(uuid.Nil, []byte(strings.Join(groups, "\x00")))
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendAllUnique(values []string, additions []string) []string {
	for _, value := range additions {
		values = appendUnique(values, strings.TrimSpace(value))
	}
	return values
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// ConfigurationHash is persisted with each run and makes algorithm changes
// explicit without exposing provider payloads.
func ConfigurationHash(seed uint64) string {
	value := fmt.Sprintf("kind=%s;version=%s;resolution=%.6f;seed=%d;max_nodes=%d;max_edges=%d", AlgorithmKind, AlgorithmVersion, Resolution, seed, MaxNodes, MaxEdges)
	hash := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// GroupIndex returns the deterministic node index for tests and diagnostics.
func GroupIndex(result Result) map[string]int {
	out := make(map[string]int, len(result.Nodes))
	for i, node := range result.Nodes {
		out[node.SemanticGroupKey] = i
	}
	return out
}

var _ graph.WeightedUndirected = (*simple.WeightedUndirectedGraph)(nil)
