// Package contract contains the trace capability's dependency-safe contracts.
package contract

import (
	"math"
	"strings"
)

const (
	defaultTraceDepth         = 1
	maxTraceDepth             = 4
	defaultTraceEdges         = 24
	maxTraceEdges             = 100
	defaultTraceEvents        = 100
	maxTraceEvents            = 500
	defaultTraceFragmentRunes = 2000
	maxTraceFragmentRunes     = 8000
)

// Input is the caller-owned trace request. The adapter derives memory-space
// scope from the authenticated relationship and never accepts it here.
type Input struct {
	TeamID                  string
	RelationshipID          string
	IncludeEvidenceContent  *bool
	IncludeVerification     *bool
	IncludeTransitions      *bool
	MaxDepth                int
	MaxEdges                int
	MaxEvents               int
	MaxFragmentContentRunes int
	PredicateKeys           []string
	Topic                   string
	MinRelevance            *float64
}

func NormalizeInput(input Input) Input {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RelationshipID = strings.TrimSpace(input.RelationshipID)
	input.Topic = strings.TrimSpace(input.Topic)
	input.MaxDepth = clampInt(input.MaxDepth, defaultTraceDepth, maxTraceDepth)
	input.MaxEdges = clampInt(input.MaxEdges, defaultTraceEdges, maxTraceEdges)
	input.MaxEvents = clampInt(input.MaxEvents, defaultTraceEvents, maxTraceEvents)
	input.MaxFragmentContentRunes = clampInt(input.MaxFragmentContentRunes, defaultTraceFragmentRunes, maxTraceFragmentRunes)
	input.PredicateKeys = normalizePredicateKeys(input.PredicateKeys)
	if input.MinRelevance != nil {
		normalized := normalizeRelevance(*input.MinRelevance)
		input.MinRelevance = &normalized
	}
	return input
}

func clampInt(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func normalizePredicateKeys(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) == 30 {
			break
		}
	}
	return out
}

func normalizeRelevance(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
